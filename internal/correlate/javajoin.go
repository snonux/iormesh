package correlate

import (
	"errors"

	"iormesh/internal/agentspool"
	"iormesh/internal/schema"
)

// JavaJoin attributes the syscall events of JVMs running the iormesh agent
// to the Java code that caused them (M3 steps 5–6, PLAN.md "Join to kernel
// events"). It is a schema.Sink between the Correlator and the trace's
// sink: every event passes through it in seq order, and the events of a
// JVM wait until the agent's ring (decision #27, internal/agentspool)
// says every bracket that could contain them has been read.
//
// The join (S1, docs/spikes/s1-java-join.md): an event belongs to the
// innermost agent bracket on its kernel tid whose window contains the
// syscall's start, compared in CLOCK_MONOTONIC — System.nanoTime's clock
// — after converting the event's boot-clock stamp with a midpoint offset
// sampled in user space (S1 experiment 2: 0 of 120 000 syscalls outside
// their bracket). The bracket's hook must allow the syscall's kind: any
// other kind inside the bracket is JVM work (class loading, say), not the
// method's I/O. Everything else stays attribution none, never guessed: an
// unbracketed syscall, another kind, two brackets that overlap without
// nesting, a folded read/write (one event for many calls), a bracket
// whose stack the agent never delivered.
//
// The BPF uprobe mark S1 also used (which native issued the syscall) is
// not built yet; until it is, the hook's allowlist is the only guard
// against JVM work of an allowed kind inside a bracket (docs/schema.md,
// "Java attribution").
//
// I/O of a virtual thread is joined the same way (the agent records the
// carrier it ran on) and flagged java.carrier, naming the carrier thread
// (PLAN.md, "Platform threads only in v1").
//
// One goroutine owns a JavaJoin, like the Correlator feeding it: Event,
// Poll and Close must not run concurrently.
type JavaJoin struct {
	cfg    JavaJoinConfig
	held   []schema.Event // in seq order, waiting for their JVM's ring
	head   int            // held[:head] are released (compact)
	jvms   map[uint32]*jvm
	dead   []*jvm // rings replaced by a newer JVM of the same pid
	offset int64  // boot − mono, ns, from the last poll
	// scanned is the boot time just before the last spool scan: a
	// syscall that ended before it whose process had no ring then never
	// will have a bracket (the agent maps its ring before its first one).
	scanned uint64
	methods map[string]uint64 // frame text → method id
	stats   JavaStats
}

// AgentRing is what the joiner reads of one agent ring; *agentspool.Ring
// implements it, tests fake it.
type AgentRing interface {
	Header() agentspool.Header
	Drain(fn func(agentspool.Record)) (int, error)
	Losses() agentspool.Losses
	Close() error
}

// JavaTables receives the method and thread tables (methods.ndjson,
// threads.ndjson). An error ends the trace, as a sink error does.
type JavaTables interface {
	Method(schema.Method) error
	Thread(schema.Thread) error
}

// JavaJoinConfig configures a JavaJoin. Next, Tables and Scan are
// required; the rest default.
type JavaJoinConfig struct {
	Next   schema.Sink
	Tables JavaTables
	// Scan returns the rings that appeared in the spool directory since
	// the last call.
	Scan func() ([]AgentRing, error)
	// ProcInfo gives a JVM's start time, to tell its ring from a stale
	// one of an earlier process with the same pid; defaults to
	// ReadProcInfo.
	ProcInfo ProcInfo
	// Clock is the boot clock (BootNow); MonoOffset samples boot − mono
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
	// event back for its fuse window (5 s) before it gets here.
	Retain uint64
}

// JavaStats counts what the join did, for the run's summary line.
type JavaStats struct {
	Rings      int    // agent rings read
	Corrupt    int    // rings that failed (ErrCorrupt): read up to the failure
	Brackets   uint64 // brackets read
	Attributed uint64 // events given attribution java
	Carrier    uint64 // of those, I/O of a virtual thread (java.carrier)
	// Events of JVMs with a ring that stayed none, by why.
	Unbracketed uint64 // no bracket contains the syscall
	OtherKind   uint64 // inside a bracket whose hook does not issue this kind
	Ambiguous   uint64 // overlapping brackets, or a folded read/write
	NoStack     uint64 // the bracket's stack was not delivered (or not walked)
	TimedOut    uint64 // released by Timeout before the watermark passed
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
	if cfg.Clock == nil {
		cfg.Clock = BootNow
	}
	if cfg.MonoOffset == nil {
		cfg.MonoOffset = MonoOffset
	}
	cfg.ScanEvery = orDefault(cfg.ScanEvery, defaultScanEvery)
	cfg.Timeout = orDefault(cfg.Timeout, defaultTimeout)
	cfg.Grace = orDefault(cfg.Grace, defaultGrace)
	cfg.Retain = orDefault(cfg.Retain, defaultRetain)
	return &JavaJoin{cfg: cfg, jvms: map[uint32]*jvm{}, methods: map[string]uint64{}}
}

var _ schema.Sink = (*JavaJoin)(nil)

func orDefault(v, def uint64) uint64 {
	if v == 0 {
		return def
	}
	return v
}

// Event implements schema.Sink: ev waits behind the events held before
// it and is released, joined, as soon as it is decidable.
func (j *JavaJoin) Event(ev schema.Event) error {
	j.held = append(j.held, ev)
	if now := j.cfg.Clock(); now >= j.scanned+j.cfg.ScanEvery {
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
// none), closes the rings and then the next sink.
func (j *JavaJoin) Close() error {
	err := j.poll(j.cfg.Clock())
	if err == nil {
		err = j.release(true)
	}
	for _, v := range j.allJVMs() {
		j.stats.Lost = addLosses(j.stats.Lost, v.ring.Losses())
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
// claimed for a moment the scan did not cover.
func (j *JavaJoin) poll(now uint64) error {
	off, err := j.cfg.MonoOffset()
	if err != nil {
		return err
	}
	j.offset = off
	rings, err := j.cfg.Scan()
	if err != nil {
		return err
	}
	for _, r := range rings {
		j.addRing(r)
	}
	j.scanned = now
	for _, v := range j.jvms {
		if err := j.drain(v); err != nil {
			return err
		}
	}
	return nil
}

// addRing registers a ring under its JVM's pid. A newer ring for the same
// pid (a reused pid, a restarted JVM) replaces the older, which is kept
// only to be closed and counted.
func (j *JavaJoin) addRing(r AgentRing) {
	h := r.Header()
	pid := uint32(h.PID)
	_, start := j.cfg.ProcInfo(pid)
	if old := j.jvms[pid]; old != nil {
		j.dead = append(j.dead, old)
	}
	j.jvms[pid] = newJVM(r, pid, start, h.CreatedMono, j.boot(h.CreatedMono))
	j.stats.Rings++
}

func (j *JavaJoin) allJVMs() []*jvm {
	all := append([]*jvm(nil), j.dead...)
	for _, v := range j.jvms {
		all = append(all, v)
	}
	return all
}

// release emits held events from the head while each is decidable;
// force decides everything with what is known now.
func (j *JavaJoin) release(force bool) error {
	for ; j.head < len(j.held); j.head++ {
		ev := &j.held[j.head]
		v, wait := j.pending(ev)
		if wait && !force {
			break
		}
		if v != nil {
			if err := j.attribute(ev, v); err != nil {
				return err
			}
		}
		if err := j.cfg.Next.Event(*ev); err != nil {
			j.head++
			return err
		}
	}
	j.compact()
	return nil
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
		// the syscall found none.
		return nil, j.scanned <= end
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
		schema.KindExec, schema.KindSignal:
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
