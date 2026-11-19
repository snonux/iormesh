package correlate

import (
	"errors"
	"maps"
	"reflect"
	"sort"
	"strings"
	"testing"

	"iormesh/internal/agentspool"
	"iormesh/internal/schema"
)

// The join tests run on a fixed clock offset: mono = boot − testOffset.
const (
	testOffset = 1_000_000
	jvmPID     = 77
	jvmStart   = 5000
	mainTID    = 78
)

// fakeRing hands out queued records on each Drain, like a ring the agent
// keeps writing to.
type fakeRing struct {
	hdr    agentspool.Header
	queue  []agentspool.Record
	err    error
	closed bool
}

func (r *fakeRing) Header() agentspool.Header { return r.hdr }

func (r *fakeRing) Drain(fn func(agentspool.Record)) (int, error) {
	n := len(r.queue)
	for _, rec := range r.queue {
		fn(rec)
	}
	r.queue = nil
	return n, r.err
}

func (r *fakeRing) Losses() agentspool.Losses { return agentspool.Losses{DroppedBrackets: 1} }
func (r *fakeRing) Close() error              { r.closed = true; return nil }

// memTables collects the method and thread tables, validating each row.
type memTables struct {
	t       *testing.T
	methods []schema.Method
	threads []schema.Thread
}

func (m *memTables) Method(row schema.Method) error {
	if err := row.Validate(); err != nil {
		m.t.Errorf("method %+v: %v", row, err)
	}
	m.methods = append(m.methods, row)
	return nil
}

func (m *memTables) Thread(row schema.Thread) error {
	if err := row.Validate(); err != nil {
		m.t.Errorf("thread %+v: %v", row, err)
	}
	m.threads = append(m.threads, row)
	return nil
}

// joinTest is a JavaJoin on a fake spool, clock and sink.
type joinTest struct {
	t      *testing.T
	j      *JavaJoin
	sink   *memSink
	tables *memTables
	now    uint64 // boot clock
	spool  []AgentRing
	seq    uint64
}

func newJoinTest(t *testing.T) *joinTest {
	jt := &joinTest{t: t, sink: &memSink{t: t}, tables: &memTables{t: t}, now: 1_000_000_000}
	jt.j = NewJavaJoin(JavaJoinConfig{Next: jt.sink, Tables: jt.tables,
		Scan: func() ([]AgentRing, error) {
			r := jt.spool
			jt.spool = nil
			return r, nil
		},
		ProcInfo:   func(pid uint32) (string, uint64) { return "java", jvmStart },
		Clock:      func() uint64 { return jt.now },
		MonoOffset: func() (int64, error) { return testOffset, nil }})
	return jt
}

// The natives of the fixture's hooks (ring), as their uprobes name them.
const (
	symWriteBytes = "Java_java_io_FileOutputStream_writeBytes"
	symStat0      = "Java_sun_nio_fs_UnixNativeDispatcher_stat0"
	symAccept     = "Java_sun_nio_ch_Net_accept"
	symReadBytes  = "Java_java_io_FileInputStream_readBytes"
	symRAFRead    = "Java_java_io_RandomAccessFile_readBytes0"
)

// kindNative is the native the fixture's hooks issue each kind from:
// event marks its events with it.
var kindNative = map[string]string{
	"write": symWriteBytes, "newstat": symStat0, "accept4": symAccept, "read": symReadBytes,
}

// markLead is how long before a syscall the fixture's natives are entered.
const markLead = 10

// ring puts a new ring for the JVM into the spool, already holding the
// hook table (hook 1: write, hook 2: stat, hook 3: accept, hook 4: read)
// and the main thread.
func (jt *joinTest) ring(recs ...agentspool.Record) *fakeRing {
	r := &fakeRing{hdr: agentspool.Header{Version: agentspool.Version, PID: jvmPID,
		CreatedMono: jt.mono(jt.now)}}
	r.queue = append([]agentspool.Record{
		agentspool.Hook{ID: 1, Kind: "write", Class: "java/io/FileOutputStream", Method: "write",
			Native: "writeBytes", Syscalls: []string{"write"}},
		agentspool.Hook{ID: 2, Kind: "stat", Class: "sun/nio/fs/UnixNativeDispatcher",
			Method: "stat", Native: "stat0", Syscalls: []string{"stat", "statx"}},
		agentspool.Hook{ID: 3, Kind: "accept", Class: "sun/nio/ch/NioSocketImpl", Method: "accept",
			Native: "sun/nio/ch/Net.accept", Syscalls: []string{"accept", "accept4"}},
		agentspool.Hook{ID: 4, Kind: "read", Class: "java/io/FileInputStream", Method: "read",
			Native: "readBytes", Syscalls: []string{"read"}},
		agentspool.Thread{ID: 1, KTID: mainTID, Name: "main"},
		agentspool.Stack{ID: 10, Frames: []string{"java.io.FileOutputStream#writeBytes(FileOutputStream.java:-2)",
			"app.Main#save(Main.java:12)"}},
	}, recs...)
	jt.spool = append(jt.spool, r)
	return r
}

func (jt *joinTest) mono(boot uint64) uint64 { return boot - testOffset }

// event sends a syscall event of tid starting at boot time at, marked by
// the kernel as issued from the native kindNative names for its kind.
func (jt *joinTest) event(pid, tid uint32, kind string, at uint64) {
	jt.t.Helper()
	if sym, ok := kindNative[kind]; ok {
		jt.mark(pid, tid, at, sym, at-markLead)
	}
	jt.unmarked(pid, tid, kind, at)
}

// mark sends the kernel's mark for the syscall tid enters at boot time
// at: issued from inside native sym ("" for an unknown one, cookie 0),
// entered at boot time entry.
func (jt *joinTest) mark(pid, tid uint32, at uint64, sym string, entry uint64) {
	cookie := uint64(0)
	if sym != "" {
		cookie = jt.j.cookie(sym)
	}
	jt.j.Mark(NativeMark{PID: pid, TID: tid, EnterBoot: at, Cookie: cookie, EntryMono: jt.mono(entry)})
}

// unmarked sends a syscall event without a mark: one issued outside every
// hooked native.
func (jt *joinTest) unmarked(pid, tid uint32, kind string, at uint64) {
	jt.t.Helper()
	jt.seq++
	ev := schema.Event{SchemaVersion: schema.Version, Project: "p", Seq: jt.seq, BootNS: at,
		HostID: "h", PID: pid, PIDInc: jvmStart, TID: tid, Comm: "java", Event: kind,
		FD: 3, LatencyNS: 100, Attribution: schema.AttributionNone}
	if err := jt.j.Event(&ev); err != nil {
		jt.t.Fatal(err)
	}
}

func (jt *joinTest) close() {
	jt.t.Helper()
	if err := jt.j.Close(); err != nil {
		jt.t.Fatal(err)
	}
}

// bracket is a bracket of hook on tid around [enter, exit] (boot times).
func (jt *joinTest) bracket(hook int32, tid int32, enter, exit, stack, thread uint64) agentspool.Bracket {
	return agentspool.Bracket{Hook: hook, KTID: tid, EnterMono: jt.mono(enter),
		ExitMono: jt.mono(exit), StackID: stack, ThreadID: thread}
}

func (jt *joinTest) watermark(boot uint64) agentspool.Watermark {
	return agentspool.Watermark{Mono: jt.mono(boot)}
}

// TestJavaJoinAttributes: a write inside a write bracket gets the stack
// and thread; the method table gets each frame once, the thread table
// the THREAD record.
func TestJavaJoinAttributes(t *testing.T) {
	jt := newJoinTest(t)
	at := jt.now + 1000
	jt.ring(jt.bracket(1, mainTID, at-50, at+500, 10, 1), jt.watermark(at+20_000_000))
	jt.event(jvmPID, mainTID, "write", at)
	jt.close()
	ev := jt.sink.events[0]
	want := &schema.Java{Thread: "main", Stack: []uint64{1, 2}}
	if ev.Attribution != schema.AttributionJava || !reflect.DeepEqual(ev.Java, want) {
		t.Fatalf("event %s %+v, want java %+v", ev.Attribution, ev.Java, want)
	}
	if len(jt.tables.methods) != 2 || jt.tables.methods[1].Method != "save" ||
		len(jt.tables.threads) != 1 || jt.tables.threads[0].Name != "main" {
		t.Errorf("tables %+v %+v", jt.tables.methods, jt.tables.threads)
	}
	if s := jt.j.Stats(); s.Attributed != 1 || s.Rings != 1 || s.Lost.DroppedBrackets != 1 {
		t.Errorf("stats %+v", s)
	}
}

// TestJavaJoinStaysNone: everything the join cannot vouch for stays none,
// each counted under its reason.
func TestJavaJoinStaysNone(t *testing.T) {
	for name, c := range staysNoneCases() {
		t.Run(name, func(t *testing.T) { c.run(t) })
	}
}

// noneCase is one TestJavaJoinStaysNone case: the event's kind, whether
// it is folded, the native the kernel marked it with (noMark: none,
// unknownNative: cookie 0), the ring's records, and the counter that must
// say why.
type noneCase struct {
	kind    string
	folded  bool
	native  string
	records func(jt *joinTest, at uint64) []agentspool.Record
	want    func(s JavaStats) uint64
}

// Marks of noneCase other than a native's symbol.
const (
	noMark        = "-"
	unknownNative = ""
)

func staysNoneCases() map[string]noneCase {
	write := func(jt *joinTest, at uint64) []agentspool.Record {
		return []agentspool.Record{jt.bracket(1, mainTID, at-50, at+500, 10, 1)}
	}
	cases := map[string]noneCase{
		"unbracketed": {"write", false, symWriteBytes, func(jt *joinTest, at uint64) []agentspool.Record {
			return []agentspool.Record{jt.bracket(1, mainTID, at+10, at+500, 10, 1)}
		}, func(s JavaStats) uint64 { return s.Unbracketed }},
		"other kind": {"read", false, symWriteBytes, write, func(s JavaStats) uint64 { return s.OtherKind }},
		"other thread": {"write", false, symWriteBytes, func(jt *joinTest, at uint64) []agentspool.Record {
			return []agentspool.Record{jt.bracket(1, mainTID+1, at-50, at+500, 10, 1)}
		}, func(s JavaStats) uint64 { return s.Unbracketed }},
		"overlap": {"write", false, symWriteBytes, func(jt *joinTest, at uint64) []agentspool.Record {
			return []agentspool.Record{jt.bracket(1, mainTID, at-50, at+500, 10, 1),
				jt.bracket(1, mainTID, at-20, at+600, 10, 1)}
		}, func(s JavaStats) uint64 { return s.Ambiguous }},
		"folded": {"write", true, symWriteBytes, write, func(s JavaStats) uint64 { return s.Ambiguous }},
		"no stack walked": {"write", false, symWriteBytes, func(jt *joinTest, at uint64) []agentspool.Record {
			return []agentspool.Record{jt.bracket(1, mainTID, at-50, at+500, 0, 1)}
		}, func(s JavaStats) uint64 { return s.NoStack }},
		"stack never delivered": {"write", false, symWriteBytes, func(jt *joinTest, at uint64) []agentspool.Record {
			return []agentspool.Record{jt.bracket(1, mainTID, at-50, at+500, 99, 1)}
		}, func(s JavaStats) uint64 { return s.NoStack }},
		"unknown hook": {"write", false, symWriteBytes, func(jt *joinTest, at uint64) []agentspool.Record {
			return []agentspool.Record{jt.bracket(7, mainTID, at-50, at+500, 10, 1)}
		}, func(s JavaStats) uint64 { return s.OtherNative }},
	}
	maps.Copy(cases, jvmWorkCases())
	return cases
}

// jvmWorkCases are the negative cases of the Java mark (task td4): JVM
// work of a kind the hook allows, inside its bracket, which the
// allowlist alone attributed. A read inside a FileInputStream.read
// bracket (hook 4) that the JVM issued itself - loading a class from a
// JAR, say - stays none, whether it came from no hooked native, from
// another one (RandomAccessFile's, which ZipFile reads JARs through), from
// one nested too deep to name, or carries a stale mark of a bracket that
// closed before it.
func jvmWorkCases() map[string]noneCase {
	read := func(jt *joinTest, at uint64) []agentspool.Record {
		return []agentspool.Record{jt.bracket(4, mainTID, at-50, at+500, 10, 1)}
	}
	return map[string]noneCase{
		"jvm work, no native": {"read", false, noMark, read,
			func(s JavaStats) uint64 { return s.Unmarked }},
		"jvm work, another native": {"read", false, symRAFRead, read,
			func(s JavaStats) uint64 { return s.OtherNative }},
		"jvm work, native unknown": {"read", false, unknownNative, read,
			func(s JavaStats) uint64 { return s.OtherNative }},
		"stale mark": {"read", false, symReadBytes, func(jt *joinTest, at uint64) []agentspool.Record {
			// The mark's native (entered at at-10) sits in a bracket that
			// closed before the syscall; the syscall's own bracket began
			// after that entry.
			return []agentspool.Record{jt.bracket(4, mainTID, at-30, at-5, 10, 1),
				jt.bracket(4, mainTID, at-2, at+500, 10, 1)}
		}, func(s JavaStats) uint64 { return s.Unbracketed }},
	}
}

func (c noneCase) run(t *testing.T) {
	jt := newJoinTest(t)
	at := jt.now + 1000
	jt.ring(append(c.records(jt, at), jt.watermark(at+20_000_000))...)
	jt.seq++
	ev := schema.Event{SchemaVersion: schema.Version, Project: "p", Seq: jt.seq,
		BootNS: at, HostID: "h", PID: jvmPID, PIDInc: jvmStart, TID: mainTID,
		Comm: "java", Event: c.kind, FD: 3, Attribution: schema.AttributionNone}
	if c.folded {
		n := uint64(2)
		ev.Args.Count = &n
	}
	if c.native != noMark {
		jt.mark(jvmPID, mainTID, at, c.native, at-markLead)
	}
	if err := jt.j.Event(&ev); err != nil {
		t.Fatal(err)
	}
	jt.close()
	if got := jt.sink.events[0]; got.Attribution != schema.AttributionNone || got.Java != nil {
		t.Errorf("event %s %+v, want none", got.Attribution, got.Java)
	}
	if s := jt.j.Stats(); c.want(s) != 1 || s.Attributed != 0 {
		t.Errorf("stats %+v", s)
	}
	if jt.j.pendingMarks() != 0 {
		t.Errorf("%d marks left after their event", jt.j.pendingMarks())
	}
}

// TestJavaJoinInnermost: of nested brackets the innermost wins, and a
// stat alias (stat → newstat) matches.
func TestJavaJoinInnermost(t *testing.T) {
	jt := newJoinTest(t)
	at := jt.now + 1000
	jt.ring(agentspool.Stack{ID: 11, Frames: []string{"app.Main#check(Main.java:30)"}},
		jt.bracket(2, mainTID, at-20, at+200, 11, 1),  // inner, exits first
		jt.bracket(1, mainTID, at+300, at+400, 10, 1), // a later sibling inside the outer
		jt.bracket(2, mainTID, at-90, at+900, 10, 1),  // outer
		jt.watermark(at+20_000_000))
	jt.event(jvmPID, mainTID, "newstat", at)
	jt.close()
	ev := jt.sink.events[0]
	if ev.Java == nil || len(ev.Java.Stack) != 1 || jt.tables.methods[ev.Java.Stack[0]-1].Method != "check" {
		t.Fatalf("event %+v, want the inner bracket's stack", ev.Java)
	}
}

// TestJavaJoinCarrier: a virtual thread's I/O is joined, flagged carrier
// and named after the platform thread whose tid it ran on.
func TestJavaJoinCarrier(t *testing.T) {
	jt := newJoinTest(t)
	at := jt.now + 1000
	const carrier = 90
	jt.ring(agentspool.Thread{ID: 2, KTID: carrier, Name: "ForkJoinPool-1-worker-1"},
		agentspool.Thread{ID: 3, KTID: carrier, Name: "", Virtual: true},
		jt.bracket(1, carrier, at-50, at+500, 10, 3), jt.watermark(at+20_000_000))
	jt.event(jvmPID, carrier, "write", at)
	jt.close()
	want := &schema.Java{Thread: "ForkJoinPool-1-worker-1", Stack: []uint64{1, 2}, Carrier: true}
	if ev := jt.sink.events[0]; !reflect.DeepEqual(ev.Java, want) {
		t.Fatalf("java %+v, want %+v", ev.Java, want)
	}
	if !jt.tables.threads[2].Virtual || jt.j.Stats().Carrier != 1 {
		t.Errorf("threads %+v, stats %+v", jt.tables.threads, jt.j.Stats())
	}
}

// TestJavaJoinWaitsForWatermark: a JVM's event waits until the watermark
// is Grace past its end, and holds the events behind it (seq order);
// another process's event behind it leaves only after a later scan.
func TestJavaJoinWaitsForWatermark(t *testing.T) {
	jt := newJoinTest(t)
	at := jt.now + 1000
	r := jt.ring(jt.watermark(at))
	jt.event(jvmPID, mainTID, "write", at)
	jt.event(999, 1000, "write", at+10)
	if len(jt.sink.events) != 0 {
		t.Fatalf("released %d events before the watermark", len(jt.sink.events))
	}
	r.queue = []agentspool.Record{jt.bracket(1, mainTID, at-50, at+500, 10, 1),
		jt.watermark(at + 6_000_000)}
	jt.now += defaultScanEvery
	if err := jt.j.Poll(); err != nil {
		t.Fatal(err)
	}
	if len(jt.sink.events) != 2 || jt.sink.events[0].Attribution != schema.AttributionJava ||
		jt.sink.events[1].Attribution != schema.AttributionNone {
		t.Fatalf("events %+v", jt.sink.events)
	}
	jt.close()
}

// TestJavaJoinTimeout: a ring whose watermark stalls releases its events
// Timeout after them, with what is known.
func TestJavaJoinTimeout(t *testing.T) {
	jt := newJoinTest(t)
	at := jt.now + 1000
	jt.ring(jt.bracket(1, mainTID, at-50, at+500, 10, 1))
	jt.event(jvmPID, mainTID, "write", at)
	jt.now += defaultTimeout / 2
	if err := jt.j.Poll(); err != nil || len(jt.sink.events) != 0 {
		t.Fatalf("released early: %v, %d", err, len(jt.sink.events))
	}
	jt.now += defaultTimeout
	if err := jt.j.Poll(); err != nil || len(jt.sink.events) != 1 {
		t.Fatalf("not released: %v, %d", err, len(jt.sink.events))
	}
	if s := jt.j.Stats(); s.TimedOut != 1 || s.Attributed != 1 {
		t.Errorf("stats %+v", s)
	}
	jt.close()
}

// TestJavaJoinStaleRing: a ring of an earlier process with the same pid
// does not attribute the new process's events.
func TestJavaJoinStaleRing(t *testing.T) {
	jt := newJoinTest(t)
	at := jt.now + 1000
	jt.ring(jt.bracket(1, mainTID, at-50, at+500, 10, 1), jt.watermark(at+20_000_000))
	jt.seq++
	ev := schema.Event{SchemaVersion: schema.Version, Project: "p", Seq: jt.seq, BootNS: at,
		HostID: "h", PID: jvmPID, PIDInc: jvmStart + 1, TID: mainTID, Comm: "java",
		Event: "write", FD: 3, Attribution: schema.AttributionNone}
	if err := jt.j.Event(&ev); err != nil {
		t.Fatal(err)
	}
	jt.close()
	if jt.sink.events[0].Attribution != schema.AttributionNone {
		t.Error("stale ring attributed another process's event")
	}
}

// TestJavaJoinCorruptRing: a ring that fails is read up to the failure;
// its events are decided at once with what was read.
func TestJavaJoinCorruptRing(t *testing.T) {
	jt := newJoinTest(t)
	at := jt.now + 1000
	r := jt.ring(jt.bracket(1, mainTID, at-50, at+500, 10, 1))
	r.err = agentspool.ErrCorrupt
	jt.event(jvmPID, mainTID, "write", at)
	if len(jt.sink.events) != 1 || jt.sink.events[0].Attribution != schema.AttributionJava {
		t.Fatalf("events %+v", jt.sink.events)
	}
	jt.close()
	if s := jt.j.Stats(); s.Corrupt != 1 || !r.closed {
		t.Errorf("stats %+v, closed %v", s, r.closed)
	}
}

// TestJavaJoinErrors: scan, offset and table errors end the trace.
func TestJavaJoinErrors(t *testing.T) {
	boom := errors.New("boom")
	jt := newJoinTest(t)
	jt.j.cfg.Scan = func() ([]AgentRing, error) { return nil, boom }
	if err := jt.j.Poll(); !errors.Is(err, boom) {
		t.Errorf("scan error: %v", err)
	}
	jt.j.cfg.MonoOffset = func() (int64, error) { return 0, boom }
	if err := jt.j.Close(); !errors.Is(err, boom) || !jt.sink.closed {
		t.Errorf("close: %v, sink closed %v", err, jt.sink.closed)
	}
}

// TestInnermostPrunes: brackets Retain behind the watermark are dropped.
func TestInnermostPrunes(t *testing.T) {
	v := newJVM(&fakeRing{}, 1, 1, 0, 1)
	v.addBracket(agentspool.Bracket{Hook: 1, KTID: 5, EnterMono: 10, ExitMono: 20})
	v.addBracket(agentspool.Bracket{Hook: 1, KTID: 5, EnterMono: 1000, ExitMono: 2000})
	v.addBracket(agentspool.Bracket{Hook: 1, KTID: 6, EnterMono: 1, ExitMono: 2})
	v.watermark = 1500
	v.prune(1000)
	if v.brackets[5] == nil || len(v.brackets[5].list) != 1 || v.brackets[6] != nil {
		t.Errorf("brackets %+v", v.brackets)
	}
	if _, found, _ := v.innermost(5, 1500); !found {
		t.Error("kept bracket not found")
	}
}

// TestJavaJoinBeforeRing: a syscall that ended before the ring existed
// (JVM startup) is decided at once, without waiting for a watermark.
func TestJavaJoinBeforeRing(t *testing.T) {
	jt := newJoinTest(t)
	at := jt.now - 5000 // before the ring's creation (jt.now)
	jt.ring()
	jt.event(jvmPID, mainTID, "write", at)
	if len(jt.sink.events) != 1 || jt.sink.events[0].Attribution != schema.AttributionNone {
		t.Fatalf("events %+v", jt.sink.events)
	}
	jt.close()
	if s := jt.j.Stats(); s.Unbracketed != 1 || s.TimedOut != 0 {
		t.Errorf("stats %+v", s)
	}
}

// TestJavaJoinCompacts: events released in parts, with new ones arriving
// behind a waiting head, leave in seq order and the queue's room is
// reused rather than grown.
func TestJavaJoinCompacts(t *testing.T) {
	jt := newJoinTest(t)
	r := jt.ring()
	for i := range 200 {
		at := jt.now + 1000
		jt.event(jvmPID, mainTID, "write", at)
		if i%3 == 0 { // the watermark passes every third event
			r.queue = append(r.queue, jt.watermark(at+6_000_000))
		}
		jt.now += defaultScanEvery
	}
	if c := cap(jt.j.held); c > 16 {
		t.Errorf("queue grew to %d for at most a few waiting events", c)
	}
	jt.close()
	if len(jt.sink.events) != 200 { // memSink checks the seq order
		t.Fatalf("%d events", len(jt.sink.events))
	}
}

// TestJavaJoinParkingHook: a hook that can park (its native is another
// class's) joins on a platform thread, but never on a virtual thread or
// one whose THREAD record is missing: the carrier may have run another
// virtual thread inside that window, whose own bracket was lost.
func TestJavaJoinParkingHook(t *testing.T) {
	const carrier = 90
	cases := map[string]struct {
		thread uint64
		tid    int32
		joined bool
	}{
		"platform thread": {1, mainTID, true},
		"virtual thread":  {3, carrier, false},
		"unknown thread":  {9, mainTID, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			jt := newJoinTest(t)
			at := jt.now + 1000
			jt.ring(agentspool.Thread{ID: 3, KTID: carrier, Virtual: true},
				jt.bracket(3, c.tid, at-50, at+500, 10, c.thread), jt.watermark(at+20_000_000))
			jt.event(jvmPID, uint32(c.tid), "accept4", at)
			jt.close()
			joined := jt.sink.events[0].Attribution == schema.AttributionJava
			if joined != c.joined || (!joined && jt.j.Stats().Ambiguous != 1) {
				t.Errorf("joined %v, want %v; stats %+v", joined, c.joined, jt.j.Stats())
			}
		})
	}
}

// TestJavaJoinAttachesMarks: a JVM's natives are probed once, after its
// ring delivered the hook table, each distinct native once with the
// cookie its marks are then checked against; a failed attach and natives
// no library defines are reported, and the failure counted.
func TestJavaJoinAttachesMarks(t *testing.T) {
	jt := newJoinTest(t)
	var calls [][]MarkedNative
	var warned []error
	jt.j.cfg.Warn = func(err error) { warned = append(warned, err) }
	jt.j.cfg.AttachMarks = func(pid uint32, natives []MarkedNative) ([]string, error) {
		if pid != jvmPID {
			t.Errorf("attach for pid %d", pid)
		}
		calls = append(calls, natives)
		return []string{symAccept}, nil
	}
	r := jt.ring()
	if err := jt.j.Poll(); err != nil {
		t.Fatal(err)
	}
	r.queue = []agentspool.Record{jt.watermark(jt.now)}
	if err := jt.j.Poll(); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || len(calls[0]) != 4 {
		t.Fatalf("attach calls %+v, want one with the four natives", calls)
	}
	for _, n := range calls[0] {
		if n.Cookie == 0 || jt.j.cookie(n.Symbol) != n.Cookie {
			t.Errorf("native %+v: cookie not the join's", n)
		}
	}
	if len(warned) != 1 || !strings.Contains(warned[0].Error(), symAccept) {
		t.Errorf("warnings %v", warned)
	}
	jt.j.cfg.AttachMarks = func(uint32, []MarkedNative) ([]string, error) {
		return nil, errors.New("no uprobe_multi")
	}
	jt.ring()
	if err := jt.j.Poll(); err != nil {
		t.Fatal(err)
	}
	if s := jt.j.Stats(); s.MarkFailed != 1 || len(warned) != 2 {
		t.Errorf("stats %+v, warnings %v", s, warned)
	}
	jt.close()
}

// TestJavaJoinMarkQueue: a thread's marks pair with its events in enter
// order while several wait at once — a lost exit's mark (no event) is
// skipped, an event without a mark of its own takes no other's, and an
// event of another thread takes none of this one's.
func TestJavaJoinMarkQueue(t *testing.T) {
	jt := newJoinTest(t)
	at := jt.now + 1000
	var recs []agentspool.Record
	for i := range uint64(4) {
		recs = append(recs, jt.bracket(1, mainTID, at+i*1000-50, at+i*1000+500, 10, 1))
	}
	jt.ring(append(recs, jt.watermark(at+20_000_000))...)
	jt.mark(jvmPID, mainTID, at, symWriteBytes, at-markLead) // its exit lost
	jt.event(jvmPID, mainTID, "write", at+1000)              // marked
	jt.unmarked(jvmPID, mainTID, "write", at+2000)           // JVM work
	jt.unmarked(jvmPID, mainTID+1, "write", at+3000)         // another thread
	jt.event(jvmPID, mainTID, "write", at+3000)              // marked
	jt.close()
	var got []string
	for _, ev := range jt.sink.events {
		got = append(got, ev.Attribution)
	}
	want := []string{schema.AttributionJava, schema.AttributionNone, schema.AttributionNone,
		schema.AttributionJava}
	if !reflect.DeepEqual(got, want) || jt.j.pendingMarks() != 0 {
		t.Fatalf("attributions %v, want %v; %d marks left", got, want, jt.j.pendingMarks())
	}
	if n := jt.j.Stats().MarkSkipped; n != 1 {
		t.Errorf("MarkSkipped %d, want 1 (the lost exit's mark)", n)
	}
}

// TestJavaJoinPrunesMarks: a mark whose event never came is forgotten
// once no event can still claim it.
func TestJavaJoinPrunesMarks(t *testing.T) {
	jt := newJoinTest(t)
	jt.mark(jvmPID, mainTID, jt.now, symWriteBytes, jt.now-markLead)
	jt.now += defaultRetain
	if err := jt.j.Poll(); err != nil || jt.j.pendingMarks() != 1 {
		t.Fatalf("pruned early: %v, %d marks", err, jt.j.pendingMarks())
	}
	jt.now += defaultTimeout + 1
	if err := jt.j.Poll(); err != nil || jt.j.pendingMarks() != 0 {
		t.Fatalf("not pruned: %v, %d marks", err, jt.j.pendingMarks())
	}
	jt.close()
}

// TestJavaJoinNoNativeFound: a JDK whose libraries define none of the
// natives (no Java_* symbols) leaves the JVM unmarked, counted and
// reported like a failed attach.
func TestJavaJoinNoNativeFound(t *testing.T) {
	jt := newJoinTest(t)
	var warned []error
	jt.j.cfg.Warn = func(err error) { warned = append(warned, err) }
	jt.j.cfg.AttachMarks = func(_ uint32, natives []MarkedNative) ([]string, error) {
		var all []string
		for _, n := range natives {
			all = append(all, n.Symbol)
		}
		return all, nil
	}
	jt.ring()
	if err := jt.j.Poll(); err != nil {
		t.Fatal(err)
	}
	if s := jt.j.Stats(); s.MarkFailed != 1 || len(warned) != 1 ||
		!strings.Contains(warned[0].Error(), "none of the 4 natives") {
		t.Errorf("stats %+v, warnings %v", s, warned)
	}
	jt.close()
}

// TestJavaJoinMarksDead: a probed JVM whose threads keep closing brackets
// without sending one mark (a kernel whose uprobe_multi filters by thread)
// is reported once and counted unmarked; a thread that sends marks keeps
// its brackets out of the count.
func TestJavaJoinMarksDead(t *testing.T) {
	jt := newJoinTest(t)
	var warned []error
	jt.j.cfg.Warn = func(err error) { warned = append(warned, err) }
	jt.j.cfg.AttachMarks = func(uint32, []MarkedNative) ([]string, error) { return nil, nil }
	r := jt.ring()
	if err := jt.j.Poll(); err != nil {
		t.Fatal(err)
	}
	const worker = mainTID + 1
	jt.mark(jvmPID, mainTID, jt.now+1, symWriteBytes, jt.now)
	for i := range noMarkBrackets {
		at := jt.now + 1000 + uint64(i)*1000
		r.queue = append(r.queue, jt.bracket(1, mainTID, at, at+100, 10, 1))
		if i < noMarkBrackets-1 {
			r.queue = append(r.queue, jt.bracket(1, worker, at, at+100, 10, 1))
		}
	}
	if err := jt.j.Poll(); err != nil || len(warned) != 0 {
		t.Fatalf("warned below the threshold: %v, %v", err, warned)
	}
	r.queue = []agentspool.Record{jt.bracket(1, worker, jt.now+90_000, jt.now+91_000, 10, 1)}
	for range 2 {
		if err := jt.j.Poll(); err != nil {
			t.Fatal(err)
		}
	}
	if s := jt.j.Stats(); s.MarkFailed != 1 || len(warned) != 1 ||
		!strings.Contains(warned[0].Error(), "46ba0e49") {
		t.Errorf("stats %+v, warnings %v", s, warned)
	}
	jt.close()
}

// TestExitSearchMatchesSearch holds exitSearch to sort.Search on lists
// with repeated exit times, for every probe time around them and every
// hint, in range or not.
func TestExitSearchMatchesSearch(t *testing.T) {
	for n := range 40 {
		list := make([]agentspool.Bracket, n)
		for i := range list {
			list[i].ExitMono = uint64(10 + 2*(i/3)) // runs of three equal exits
		}
		for probe := uint64(0); probe < uint64(2*n+14); probe++ {
			want := sort.Search(n, func(i int) bool { return list[i].ExitMono >= probe })
			for hint := -1; hint <= n+1; hint++ {
				if got := exitSearch(list, probe, hint); got != want {
					t.Fatalf("n %d, t %d, hint %d: exitSearch %d, sort.Search %d",
						n, probe, hint, got, want)
				}
			}
		}
	}
}
