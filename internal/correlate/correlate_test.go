package correlate

import (
	"errors"
	"fmt"
	"testing"

	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// memSink collects events and validates each, so every test also proves
// the correlator emits schema-valid events in gap-free seq order.
type memSink struct {
	t      *testing.T
	events []schema.Event
	fail   error
	closed bool
}

func (s *memSink) Event(ev *schema.Event) error {
	if s.fail != nil {
		return s.fail
	}
	if err := ev.Validate(); err != nil {
		s.t.Errorf("invalid event %+v: %v", ev, err)
	}
	if want := uint64(len(s.events) + 1); ev.Seq != want {
		s.t.Errorf("seq %d, want %d (gap-free, in order)", ev.Seq, want)
	}
	// A deep enough copy: the Java join lends its java and otel objects
	// for the call only (schema.Sink).
	e := *ev
	if e.Java != nil {
		j := *e.Java
		e.Java = &j
	}
	if e.Otel != nil {
		o := *e.Otel
		e.Otel = &o
	}
	s.events = append(s.events, e)
	return nil
}

func (s *memSink) Close() error { s.closed = true; return nil }

func newTest(t *testing.T) (*Correlator, *memSink) {
	sink := &memSink{t: t}
	c := New(Config{Host: Host{ID: "h1", Project: "p"}, Sink: sink,
		ProcInfo: func(pid uint32) (string, uint64) { return fmt.Sprintf("proc%d", pid), 1000 + uint64(pid) }})
	return c, sink
}

// testIdentity is what newTest's ProcInfo reports for pid, for the
// callers that seed the correlator themselves (Seed, Incarnation).
func testIdentity(pid uint32) Identity {
	return Identity{Comm: fmt.Sprintf("proc%d", pid), StartNS: 1000 + uint64(pid)}
}

// feeder builds records on one thread with a monotonic clock.
type feeder struct {
	t        *testing.T
	c        *Correlator
	pid, tid uint32
	now      *uint64
}

func (f feeder) tick() uint64 { *f.now += 10; return *f.now }

func (f feeder) handle(rec any) {
	f.t.Helper()
	if err := f.c.Handle(rec); err != nil {
		f.t.Fatal(err)
	}
}

func (f feeder) open(path string, flags int32, ret int64) {
	ev := &types.OpenEvent{EventType: types.ENTER_OPEN_EVENT, TraceId: types.SYS_ENTER_OPENAT,
		Time: f.tick(), Pid: f.pid, Tid: f.tid, Flags: flags}
	copy(ev.Filename[:], path)
	f.handle(ev)
	f.exit(types.SYS_EXIT_OPENAT, ret)
}

func (f feeder) fdCall(enterID, exitID types.TraceId, fd int32, ret int64) {
	f.handle(&types.FdEvent{EventType: types.ENTER_FD_EVENT, TraceId: enterID,
		Time: f.tick(), Pid: f.pid, Tid: f.tid, Fd: fd})
	f.exit(exitID, ret)
}

func (f feeder) read(fd int32, ret int64) {
	f.fdCall(types.SYS_ENTER_READ, types.SYS_EXIT_READ, fd, ret)
}
func (f feeder) write(fd int32, ret int64) {
	f.fdCall(types.SYS_ENTER_WRITE, types.SYS_EXIT_WRITE, fd, ret)
}
func (f feeder) close(fd int32) { f.fdCall(types.SYS_ENTER_CLOSE, types.SYS_EXIT_CLOSE, fd, 0) }

func (f feeder) exit(id types.TraceId, ret int64) {
	f.handle(&types.RetEvent{EventType: types.EXIT_RET_EVENT, TraceId: id,
		Time: f.tick(), Pid: f.pid, Tid: f.tid, Ret: ret})
}

func byKind(events []schema.Event, kind string) []schema.Event {
	var out []schema.Event
	for _, e := range events {
		if e.Event == kind {
			out = append(out, e)
		}
	}
	return out
}

func TestFileFlow(t *testing.T) {
	c, sink := newTest(t)
	now := uint64(0)
	f := feeder{t, c, 10, 11, &now}
	f.open("/tmp/a.dat", 577, 5)
	f.write(5, 4096)
	f.close(5)
	f.open("/tmp/a.dat", 0, 5)
	f.read(5, 4096)
	f.close(5)
	if err := c.Close(); err != nil || !sink.closed {
		t.Fatalf("close: %v, sink closed %v", err, sink.closed)
	}
	opens := byKind(sink.events, schema.KindOpenat)
	if len(opens) != 2 || opens[0].FDGen != 1 || opens[1].FDGen != 2 || *opens[0].Args.Path != "/tmp/a.dat" {
		t.Fatalf("opens %+v: want two opens of /tmp/a.dat, fd_gen 1 then 2", opens)
	}
	if *opens[0].Args.Flags != 577 || opens[0].FD != 5 || opens[0].Comm != "proc10" || opens[0].PIDInc != 1010 {
		t.Fatalf("open envelope %+v", opens[0])
	}
	w, r := byKind(sink.events, schema.KindWrite)[0], byKind(sink.events, schema.KindRead)[0]
	if w.Bytes != 4096 || w.FDGen != 1 || r.Bytes != 4096 || r.FDGen != 2 || w.LatencyNS != 10 {
		t.Fatalf("write %+v read %+v: want 4096 bytes on gen 1 and 2", w, r)
	}
	g := c.Graph()
	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}
	if !hasEdge(g, schema.EdgeOpened, schema.ProcessNodeID("h1", 10, 1010), schema.FileNodeID("h1", "/tmp/a.dat"), "") {
		t.Fatalf("graph lacks the opened edge: %+v", g.Edges)
	}
}

func hasEdge(g schema.Graph, typ, from, to, state string) bool {
	for _, e := range g.Edges {
		if e.Type == typ && e.From == from && e.To == to && e.State == state {
			return true
		}
	}
	return false
}

func TestMarkerAndFailedOpen(t *testing.T) {
	c, sink := newTest(t)
	now := uint64(0)
	f := feeder{t, c, 10, 11, &now}
	f.open("/iormesh-marker/file-basic/begin", 0, -2)
	f.open("/nonexistent", 0, -13)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	m := sink.events[0]
	if m.Event != schema.KindMarker || *m.Args.Marker != "file-basic/begin" || m.FD != schema.NoFD || m.Err != "ENOENT" {
		t.Fatalf("marker %+v", m)
	}
	o := sink.events[1]
	if o.Event != schema.KindOpenat || o.FD != schema.NoFD || o.FDGen != 0 || o.Err != "EACCES" {
		t.Fatalf("failed open %+v: want fd -1, gen 0, EACCES", o)
	}
	if len(c.Graph().Edges) != 1 { // runs_on only: a failed open opened nothing
		t.Fatalf("edges %+v, want only runs_on", c.Graph().Edges)
	}
}

// TestUnknownAndReusedFds: an fd never seen predates the trace (gen 0); an
// fd closed and then used again without a traced creation was recreated
// by an untraced syscall (socket, dup, …), so it is a new instance.
func TestUnknownAndReusedFds(t *testing.T) {
	c, sink := newTest(t)
	now := uint64(0)
	f := feeder{t, c, 10, 11, &now}
	f.read(9, 1)
	f.close(9)
	f.read(9, 1)
	f.write(9, 1)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	gens := []uint32{}
	for _, e := range sink.events {
		gens = append(gens, e.FDGen)
	}
	if fmt.Sprint(gens) != "[0 0 1 1]" {
		t.Fatalf("fd_gens %v, want [0 0 1 1]", gens)
	}
}

func TestUnpairedExits(t *testing.T) {
	c, sink := newTest(t)
	now := uint64(0)
	f := feeder{t, c, 10, 11, &now}
	f.exit(types.SYS_EXIT_READ, 4) // enter lost
	f.handle(&types.FdEvent{EventType: types.ENTER_FD_EVENT, TraceId: types.SYS_ENTER_READ,
		Time: f.tick(), Pid: 10, Tid: 11, Fd: 3})
	f.exit(types.SYS_EXIT_WRITE, 4) // a different syscall's exit
	f.exit(types.SYS_EXIT_READ, 4)  // its enter was consumed by the mismatch
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if len(sink.events) != 0 || c.Unpaired() != 3 {
		t.Fatalf("events %d unpaired %d; want none emitted, 3 unpaired", len(sink.events), c.Unpaired())
	}
}

func TestSinkErrorPropagates(t *testing.T) {
	c, sink := newTest(t)
	sink.fail = errors.New("disk full")
	now := uint64(0)
	f := feeder{t, c, 10, 11, &now}
	f.handle(&types.FdEvent{EventType: types.ENTER_FD_EVENT, TraceId: types.SYS_ENTER_READ,
		Time: f.tick(), Pid: 10, Tid: 11, Fd: 3})
	err := c.Handle(&types.RetEvent{EventType: types.EXIT_RET_EVENT, TraceId: types.SYS_EXIT_READ,
		Time: f.tick(), Pid: 10, Tid: 11, Ret: 1})
	if !errors.Is(err, sink.fail) {
		t.Fatalf("Handle error %v, want the sink's", err)
	}
}

func TestBootToWallOffset(t *testing.T) {
	off, err := BootToWallOffset()
	if err != nil {
		t.Fatal(err)
	}
	// The wall clock is past 2020 and the host booted after 1970, so the
	// offset is a positive epoch-scale number.
	if off < 1_577_836_800_000_000_000 {
		t.Fatalf("offset %d is not epoch-scale", off)
	}
}

// TestPathFixupAndUnknownPath: a faulted enter read recovered at exit
// (OPEN_NAME_FIXUP_EVENT) gives the open its path; an unrecovered one
// emits no event but still tracks the fd; the bare marker root is a plain
// open, never a marker with an empty name.
func TestPathFixupAndUnknownPath(t *testing.T) {
	c, sink := newTest(t)
	now := uint64(0)
	f := feeder{t, c, 10, 11, &now}
	f.handle(&types.OpenEvent{EventType: types.ENTER_OPEN_EVENT, TraceId: types.SYS_ENTER_OPENAT,
		Time: f.tick(), Pid: 10, Tid: 11})
	fix := &types.OpenNameFixupEvent{EventType: types.OPEN_NAME_FIXUP_EVENT,
		TraceId: types.SYS_ENTER_OPENAT, Tid: 11, Slot: types.OPEN_NAME_FIXUP_SLOT_FIRST}
	copy(fix.Filename[:], "/tmp/late")
	f.handle(fix)
	f.exit(types.SYS_EXIT_OPENAT, 4)
	f.open("", 0, 5) // never recovered
	f.read(5, 1)
	f.open("/iormesh-marker/", 0, -21)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if *sink.events[0].Args.Path != "/tmp/late" {
		t.Fatalf("fixed-up open %+v", sink.events[0])
	}
	if c.NoPath() != 1 || sink.events[1].Event != schema.KindRead || sink.events[1].FDGen != 1 {
		t.Fatalf("unknown path: noPath %d next event %+v; want 1 and a read on gen 1",
			c.NoPath(), sink.events[1])
	}
	if last := sink.events[2]; last.Event != schema.KindOpenat || *last.Args.Path != "/iormesh-marker/" {
		t.Fatalf("bare marker root %+v, want a plain openat", last)
	}
}
