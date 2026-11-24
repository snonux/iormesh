package correlate

import (
	"errors"
	"fmt"

	"iormesh/internal/agentspool"
	"iormesh/internal/redact"
	"iormesh/internal/schema"
)

// JavaJoin attributes the syscall events of JVMs running the iormesh agent
// to the Java code that caused them (M3 steps 5–6, docs/milestones.md "Join to
// kernel events"). It is a schema.Sink between the Correlator and the trace's
// sink: every event passes through it in seq order, and the events of a
// JVM wait until the agent's ring (decision #27, internal/agentspool)
// says every bracket that could contain them has been read.
//
// The join (S1, docs/spikes/s1-java-join.md; task td4): the kernel marks
// every syscall a thread issues from inside a hooked JDK native with that
// native and the native's entry time on CLOCK_MONOTONIC (the Java mark,
// internal/c/javamark.c, sent ahead of the enter as a NativeMark). An
// event belongs to the innermost agent bracket on its kernel tid whose
// window contains the native's entry — the same clock as the agent's
// System.nanoTime, no conversion — when the bracket's hook reaches that
// very native and allows the syscall's kind. A syscall without a mark was
// not issued by a hooked native: JVM work inside the bracket (class
// loading reading a JAR, say), never the method's I/O, whatever its kind;
// the allowlist stays as a second guard. Everything else stays attribution
// none, never guessed: no mark, another native, an unbracketed native
// entry, another kind, two brackets that overlap without nesting, a
// folded read/write (one event for many calls), a bracket whose stack the
// agent never delivered.
//
// Release timing still converts the event's boot-clock end to
// CLOCK_MONOTONIC with a midpoint offset sampled in user space (S1
// experiment 2; the narrowest of offsetSamples triples per poll, task
// se4), to compare it with the ring's watermark.
//
// I/O of a virtual thread is joined the same way (the agent records the
// carrier it ran on) and flagged java.carrier, naming the carrier thread
// (docs/milestones.md, "Platform threads only in v1").
//
// The agent's HTTP hooks add events of their own: each request becomes an
// http event, merged into the stream by time (javahttp.go). Because it
// inserts events, the joiner numbers what it emits itself: seq stays
// gap-free and in emission order.
//
// One goroutine owns a JavaJoin, like the Correlator feeding it: Event,
// Poll and Close must not run concurrently.
type JavaJoin struct {
	cfg  JavaJoinConfig
	held []schema.Event // in seq order, waiting for their JVM's ring
	head int            // held[:head] are released (compact)
	jvms map[uint32]*jvm
	// waiting are rings of processes not traced (yet), held unread
	// (javatrust.go, task yi4); traced lists, per pid, the incarnations
	// the stream has named, and lastPID and lastInc the last one noted,
	// which spares the lookup for a run of one process's events.
	waiting []*jvm
	traced  map[uint32][]uint64
	lastPID uint32
	lastInc uint64
	// exiting counts the rings (accepted or waiting) whose process
	// exited and that retire has not closed yet (javaretire.go); it may
	// count one a later ring replaced, which retire's next pass drops.
	exiting int
	offset  int64 // boot − mono, ns, from the last poll
	// scanned is the boot time just before the last poll, failed spool
	// scan or not: the join's clock for its scan cadence, timeouts and
	// retention. found is the same for the last scan that succeeded: a
	// syscall that ended before it whose process had no ring then never
	// will have a bracket (the agent maps its ring before its first one).
	// A failed scan cannot vouch for that (task 3j4).
	scanned uint64
	found   uint64
	// scanErr is the last scan's error, nil once one succeeds: a failed
	// scan is warned about once per failing streak and retried at the
	// next poll, never the end of the trace; Close reports it.
	scanErr error
	methods map[methodKey]uint64    // frame of a class → method id
	classes map[schema.Class]uint64 // class row (id 0) → class id
	// cookies numbers the natives the hook tables reach, by JNI symbol:
	// the id a native's uprobe carries in its marks, from 1 (0 is the
	// mark's "innermost native unknown"), shared by every JVM.
	cookies map[string]uint64
	// marks are the kernel's Java marks waiting for their syscall's event,
	// per thread in enter order (javamark.go).
	marks map[uint32]*markQueue
	stats JavaStats
	// java and otel are the joined event's objects (setJava): the event
	// is decided and emitted at once, and the sink borrows it only for
	// that call, so one pair serves every event instead of an
	// allocation per joined event (task ee4). A released held event may
	// still point here; nothing reads it after its emit.
	java schema.Java
	otel schema.Otel
	// The http events (javahttp.go): records waiting for their turn by
	// time, the socket index, the policy that redacts them again, and
	// what they borrow from the stream — envelope, wall-clock offset and
	// the latest completion time seen.
	https   []pendingHTTP
	conns   *connIndex
	policy  redact.Policy
	project string
	hostID  string
	hostPod *schema.Pod // the pod alone, from the stream's pid-0 events (task sh4)
	wallOff int64
	maxEnd  uint64
	seq     uint64 // the last seq emitted
	// tick is the clock Event reads to see whether a scan is due: cfg.Clock
	// when the caller set one, otherwise a bootClock — a once-per-event
	// syscall otherwise (task ud4). poll, Poll and Close read cfg.Clock.
	tick func() uint64
}

// AgentRing is what the joiner reads of one agent ring; *agentspool.Ring
// implements it, tests fake it.
type AgentRing interface {
	Header() agentspool.Header
	DrainTo(v agentspool.Visitor) (int, error)
	Losses() agentspool.Losses
	Close() error
	Remove() error
}

// JavaTables receives the method, thread and class tables
// (methods.ndjson, threads.ndjson, classes.ndjson). An error ends the
// trace, as a sink error does.
type JavaTables interface {
	Method(schema.Method) error
	Thread(schema.Thread) error
	Class(schema.Class) error
}

// JavaJoinConfig configures a JavaJoin. Next, Tables and Scan are
// required; the rest default.
type JavaJoinConfig struct {
	Next   schema.Sink
	Tables JavaTables
	// Scan returns the rings that appeared in the spool directory since
	// the last call, with an error for what it could not read. The
	// rings it returns are taken even then; the error is warned about
	// (Warn) and the scan retried at the next poll, and only Close
	// returns it, should the last scan fail too (task 3j4).
	Scan func() ([]AgentRing, error)
	// ProcInfo gives a JVM's start time, to tell its ring from a stale
	// one of an earlier process with the same pid; defaults to
	// ReadProcInfo.
	ProcInfo ProcInfo
	// Clock is the boot clock (default BootNow; Event then reads a
	// cheaper estimate of it, bootClock); MonoOffset samples boot − mono
	// (MonoOffset).
	Clock      Clock
	MonoOffset func() (int64, error)
	// ScanEvery is how often Event polls the spool (ns, default 50 ms);
	// the run loop's ticker polls an idle stream too.
	ScanEvery uint64
	// Timeout releases a JVM's event whose ring's watermark has not
	// passed it after this long (ns, default 2 s): an agent that stopped
	// writing watermarks must not hold the trace up.
	Timeout uint64
	// Grace is how far the watermark must be past a syscall's end before
	// the event is joined (ns, default 5 ms): a bracket is written when
	// its method returns, a little after its last syscall.
	Grace uint64
	// Retain is how long a bracket is kept after the watermark passed
	// its exit (ns, default 10 s): the correlator may hold a socket
	// event back for its fuse window (5 s) before it gets here. A mark
	// is kept that long past its syscall's enter, plus Timeout.
	Retain uint64
	// UpstreamHold is how long the correlator may hold the whole stream
	// back behind one socket event (ns): its fuse window, which a
	// connection whose other end is on another host waits out in full.
	// The default is the correlator's default window, and a caller that
	// sets Config.FuseWindowNS must set this to the same value. The hold
	// is clock-bounded because the run loop ticks the correlator
	// (Correlator.Tick) on an idle stream; a record backlog delays it
	// further, which Timeout's 2 s on top absorbs. An http record's
	// Timeout counts from the end of the hold (httpReady), or it would go
	// out ahead of its own connection's connect or accept, still held
	// upstream, and lose its conn_id (task 1f4).
	UpstreamHold uint64
	// AttachMarks attaches the Java mark's uprobes to one JVM's natives
	// (bpf.Tracer.AttachJavaMarks), once its ring delivered the hook
	// table; it returns the natives no library defines. nil: the tracer
	// has no mark, so no event is attributed.
	AttachMarks func(pid uint32, natives []MarkedNative) (missing []string, err error)
	// Warn reports what keeps a JVM's syscalls from being marked (an
	// attach that failed, natives not found); nil drops it.
	Warn func(error)
}

// JavaStats counts what the join did, for the run's summary line.
type JavaStats struct {
	Rings      int    // agent rings read
	Corrupt    int    // rings that failed (ErrCorrupt): read up to the failure
	Brackets   uint64 // brackets read
	Attributed uint64 // events given attribution java
	Carrier    uint64 // of those, I/O of a virtual thread (java.carrier)
	// Events of JVMs with a ring that stayed none, by why.
	Unmarked    uint64 // issued outside every hooked native: JVM work
	OtherNative uint64 // issued from a native the bracket's hook does not reach
	// Unbracketed: no bracket contains the native's entry (or, Stale, the
	// one that does closed before the syscall).
	Unbracketed uint64
	// Stale counts, of Unbracketed, the events whose containing bracket
	// was found but closed before the syscall started on the converted
	// clock: a stale mark — or a clock offset off by more than the ~100 ns
	// between a native's last syscall and its bracket's close (task se4).
	Stale     uint64
	OtherKind uint64 // inside a bracket whose hook does not issue this kind
	Ambiguous uint64 // overlapping brackets, or a folded read/write
	NoStack   uint64 // the bracket's stack was not delivered (or not walked)
	// TimedOut counts events released by Timeout before the watermark
	// passed, joined or not (they are also counted above).
	TimedOut uint64
	// HTTP counts the http events emitted, HTTPNoConn those of them whose
	// connection was not found (no conn_id, no req_seq).
	HTTP, HTTPNoConn uint64
	// DNS counts the dns events emitted (task wd4).
	DNS uint64
	// MarkSkipped counts marks passed over unclaimed because an event of
	// their thread entered later: their own event never asked (not
	// joinable, no ring) or never came (a lost exit). The join assumes a
	// thread's events arrive in enter order; were that broken, marks
	// would be skipped here before their events came, so a rise in this
	// count beside falling attribution is the sign to look for.
	MarkSkipped uint64
	// MarkFailed counts JVMs whose natives could not be probed: their
	// syscalls carry no mark and stay none.
	MarkFailed int
	// Lost is the agents' loss counters, summed over the rings.
	Lost agentspool.Losses
}

// The defaults of JavaJoinConfig.
const (
	defaultScanEvery = 50_000_000
	defaultTimeout   = 2_000_000_000
	defaultGrace     = 5_000_000
	defaultRetain    = 10_000_000_000
)

// NewJavaJoin returns a JavaJoin feeding cfg.Next.
func NewJavaJoin(cfg JavaJoinConfig) *JavaJoin {
	if cfg.ProcInfo == nil {
		cfg.ProcInfo = ReadProcInfo
	}
	tick := cfg.Clock
	if cfg.Clock == nil {
		cfg.Clock = BootNow
		tick = newBootClock(BootNow).now
	}
	if cfg.MonoOffset == nil {
		cfg.MonoOffset = MonoOffset
	}
	cfg.ScanEvery = orDefault(cfg.ScanEvery, defaultScanEvery)
	cfg.Timeout = orDefault(cfg.Timeout, defaultTimeout)
	cfg.Grace = orDefault(cfg.Grace, defaultGrace)
	cfg.Retain = orDefault(cfg.Retain, defaultRetain)
	cfg.UpstreamHold = orDefault(cfg.UpstreamHold, defaultFuseWindowNS)
	return &JavaJoin{cfg: cfg, jvms: map[uint32]*jvm{}, traced: map[uint32][]uint64{},
		methods: map[methodKey]uint64{},
		classes: map[schema.Class]uint64{},
		conns:   newConnIndex(), policy: redact.Default(),
		cookies: map[string]uint64{}, marks: map[uint32]*markQueue{}, tick: tick}
}

var _ schema.Sink = (*JavaJoin)(nil)

func orDefault(v, def uint64) uint64 {
	if v == 0 {
		return def
	}
	return v
}

// Event implements schema.Sink: ev waits behind the events held before
// it and is released, joined, as soon as it is decidable. The queue keeps
// the one copy the Sink contract requires; from there the event is handed
// on by pointer. The copy is shallow: the join relies on its upstream (the
// correlator) not reusing the objects an event points to, though the
// widened schema.Sink contract would allow it (task ee4 review).
func (j *JavaJoin) Event(ev *schema.Event) error {
	j.seen(ev)
	j.trace(ev)
	j.held = append(j.held, *ev)
	if now := j.tick(); now >= j.scanned+j.cfg.ScanEvery {
		if err := j.poll(now); err != nil {
			return err
		}
	}
	return j.release(false)
}

// Poll reads the spool and the rings and releases what became decidable;
// the run loop calls it on its ticker, so an idle stream's last events
// still leave.
func (j *JavaJoin) Poll() error {
	if err := j.poll(j.cfg.Clock()); err != nil {
		return err
	}
	return j.release(false)
}

// Close reads the rings one last time, releases every held event with
// what is known by then (a bracket not yet written leaves its syscalls
// none), closes the rings and then the next sink. The release happens
// even when that last poll fails, and a last scan that failed is
// reported: the trace's tail and its end event are never dropped for an
// error that only costs attribution (task 3j4). The files stay: their
// JVMs still run — or exited too recently to be retired, and the next
// session's scan removes those.
func (j *JavaJoin) Close() error {
	err := j.poll(j.cfg.Clock())
	err = errors.Join(err, j.scanErr, j.release(true))
	for _, v := range j.jvms {
		j.stats.Lost = addLosses(j.stats.Lost, v.ring.Losses())
		err = errors.Join(err, v.ring.Close())
	}
	// A ring never traced is closed uncounted: its loss counters are
	// whatever its writer put there.
	for _, v := range j.waiting {
		err = errors.Join(err, v.ring.Close())
	}
	return errors.Join(err, j.cfg.Next.Close())
}

// Stats returns the join's counters; complete after Close.
func (j *JavaJoin) Stats() JavaStats {
	return j.stats
}

// poll samples the clock offset, opens new rings and drains every ring.
// now is taken before the scan, so "no ring at scan time" is never
// claimed for a moment the scan did not cover. A failed scan does not
// stop the poll: the rings already open are drained as ever.
func (j *JavaJoin) poll(now uint64) error {
	off, err := j.cfg.MonoOffset()
	if err != nil {
		return err
	}
	j.offset = off
	// Event polls with bootClock's estimate, which can lag the precise
	// reading a Poll took before it, so now may be behind scanned. Never
	// step back: an earlier scan's claim still holds, and a smaller
	// scanned would only delay decisions and timeouts (task ud4 review).
	// scanned moves on after a failed scan too, or Event would poll — and
	// fail — on every event while the spool stays unreadable.
	if j.scan() == nil {
		j.found = max(j.found, now)
	}
	j.scanned = max(j.scanned, now)
	for _, v := range j.jvms {
		if err := j.drain(v); err != nil {
			return err
		}
		j.attachMarks(v)
		j.markCheck(v)
	}
	j.conns.purge(j.maxEnd, j.cfg.Retain)
	j.pruneMarks()
	return nil
}

// scan opens the rings the spool scan found — those of the directories
// that could be read, should another fail — and returns its error. A
// failed scan (EMFILE, a spool directory removed) is warned about at the
// first failure of a streak only, so a directory gone for good does not
// flood stderr; the next poll retries it.
func (j *JavaJoin) scan() error {
	rings, err := j.cfg.Scan()
	for _, r := range rings {
		j.addRing(r)
	}
	if err != nil && j.scanErr == nil {
		j.warn(fmt.Errorf("agent spool scan (retried at each poll): %w", err))
	}
	j.scanErr = err
	return err
}

// addRing takes in a ring the spool scan found. It is read only once its
// process is traced (task yi4, javatrust.go): until then it waits unread
// — the spool takes a ring from any process whose owner matches the
// file's, and only the stream knows which of them the session traces.
// The process's start time is read now, while the scan's owner check
// still holds, to tell its incarnation from a later one under the pid.
func (j *JavaJoin) addRing(r AgentRing) {
	h := r.Header()
	pid := uint32(h.PID)
	comm, start := j.cfg.ProcInfo(pid)
	v := newJVM(r, pid, start, h.CreatedMono, j.boot(h.CreatedMono))
	// The comm from /proc until a joined event brings the traced one: an
	// http event may come before any of its JVM's syscall events.
	v.comm = comm
	if j.isTraced(v) {
		j.accept(v)
		return
	}
	j.hold(v)
}

// accept registers a traced process's ring under its JVM's pid, to be
// drained from the next poll on. A ring of the same pid accepted later (a
// reused pid, a restarted JVM; for two found in one scan, the later in
// name order, see hold) replaces the earlier, which is closed and counted
// at once (task 2j4): no event looks at it any more — pending asks the
// pid's current ring, which no longer owns the old process's events —
// and its process, another incarnation, is dead, so its file goes too.
func (j *JavaJoin) accept(v *jvm) {
	if old := j.jvms[v.pid]; old != nil {
		j.closeRing(old, old.start != v.start)
	}
	j.jvms[v.pid] = v
	j.stats.Rings++
}

// release emits held events from the head while each is decidable, with
// the http events that completed before each merged in ahead of it;
// force decides everything with what is known now. Then the rings of
// exited JVMs that nothing needs any more are retired (javaretire.go);
// Close closes the rest itself.
func (j *JavaJoin) release(force bool) error {
	err := j.releaseHeld(force)
	j.compact()
	if err == nil && !force {
		err = j.retire()
	}
	return err
}

func (j *JavaJoin) releaseHeld(force bool) error {
	for {
		var ev *schema.Event
		if j.head < len(j.held) {
			ev = &j.held[j.head]
		}
		if j.httpDue(ev) {
			if !force && !j.httpReady() {
				return nil
			}
			if err := j.emitHTTP(); err != nil {
				return err
			}
			continue
		}
		if ev == nil {
			return nil
		}
		v, wait := j.pending(ev)
		if wait && !force {
			return nil
		}
		j.head++
		if err := j.decide(ev, v); err != nil {
			return err
		}
	}
}

// decide joins a released event to its JVM's brackets (v nil: not
// joinable) and emits it.
func (j *JavaJoin) decide(ev *schema.Event, v *jvm) error {
	if v != nil {
		v.comm, v.inc, v.pod = ev.Comm, ev.PIDInc, ev.Pod
		if err := j.attribute(ev, v); err != nil {
			return err
		}
	}
	return j.emit(ev)
}

// emit numbers ev and hands it on.
func (j *JavaJoin) emit(ev *schema.Event) error {
	j.seq++
	ev.Seq = j.seq
	return j.cfg.Next.Event(ev)
}

// compact reclaims the released events' room. A JVM's events usually
// wait a few milliseconds behind its watermark while new ones arrive, so
// the queue is rarely empty: slicing the head off would shrink its
// capacity from the front and make every append reallocate and copy the
// whole queue (a fifth of the join's CPU on the S3 burst, task 2b4).
// Instead the head is an index, rewound when the queue empties, and the
// live part moves down only once the released part is the larger.
func (j *JavaJoin) compact() {
	switch {
	case j.head == len(j.held):
		clear(j.held)
		j.held, j.head = j.held[:0], 0
	case j.head > len(j.held)/2:
		n := copy(j.held, j.held[j.head:])
		clear(j.held[n:])
		j.held, j.head = j.held[:n], 0
	}
}

// pending says whether ev must still wait, and the JVM whose brackets
// may contain it (nil: the event cannot be joined — another process, a
// synthetic kind — and stays none).
func (j *JavaJoin) pending(ev *schema.Event) (v *jvm, wait bool) {
	if !joinable(ev) {
		return nil, false
	}
	end := ev.BootNS + ev.LatencyNS
	v = j.jvms[ev.PID]
	if v == nil || !v.owns(ev) {
		// No ring for this process (yet): decidable once a scan after
		// the syscall found none — or, while the spool cannot be read,
		// Timeout after it, with no ring to join it to (task 3j4).
		return nil, j.found <= end && j.scanned < end+j.cfg.Timeout
	}
	// Decidable: the ring failed, the watermark passed, or the syscall
	// ended before the ring existed (JVM startup: no bracket can hold it).
	if mono := j.mono(end); v.failed || v.watermark >= mono+j.cfg.Grace || mono < v.createdMono {
		return v, false
	}
	if j.scanned >= end+j.cfg.Timeout {
		j.stats.TimedOut++
		return v, false
	}
	return v, true
}

// joinable: a syscall event of a thread, one call. Synthetic events have
// no bracket to sit in; a folded read/write stands for many calls, which
// one bracket cannot vouch for.
func joinable(ev *schema.Event) bool {
	switch ev.Event {
	case schema.KindSession, schema.KindEnd, schema.KindDrop, schema.KindTCPState,
		schema.KindExec, schema.KindSignal, schema.KindFork, schema.KindAttach:
		return false
	}
	return ev.TID != 0
}

// boot converts a CLOCK_MONOTONIC time to the boot clock, the inverse of
// mono.
func (j *JavaJoin) boot(mono uint64) uint64 {
	return uint64(int64(mono) + j.offset)
}

// mono converts a boot-clock time to CLOCK_MONOTONIC with the last
// sampled offset; 0 for a time before the monotonic epoch.
func (j *JavaJoin) mono(boot uint64) uint64 {
	m := int64(boot) - j.offset
	if m < 0 {
		return 0
	}
	return uint64(m)
}
