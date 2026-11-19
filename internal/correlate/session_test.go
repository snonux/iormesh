package correlate

import (
	"errors"
	"testing"

	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// testSession is a complete session header payload.
var testSession = schema.Session{BootID: "boot-1", FQDN: "h1.example.org", Kernel: "6.12.0", Arch: "x86_64"}

// newClockTest is newTest with a fixed clock, so host events have known
// timestamps.
func newClockTest(t *testing.T, at uint64) (*Correlator, *memSink) {
	c, sink := newTest(t)
	c.cfg.Clock = func() uint64 { return at }
	return c, sink
}

func TestSessionIsFirstEvent(t *testing.T) {
	c, sink := newClockTest(t, 500)
	if err := c.Session(testSession); err != nil {
		t.Fatal(err)
	}
	now := uint64(0)
	feeder{t, c, 10, 11, &now}.open("/tmp/a", 0, 3)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	s := sink.events[0]
	if s.Event != schema.KindSession || s.Seq != 1 || *s.Session != testSession {
		t.Fatalf("first event %+v, want the session header at seq 1", s)
	}
	if s.PID != 0 || s.TID != 0 || s.PIDInc != 0 || s.FD != schema.NoFD || s.BootNS != 500 {
		t.Fatalf("session envelope %+v: want a host-level event stamped by the clock", s)
	}
}

// TestSessionNotFirstRejected: a header after other events, or a second
// one, is refused and emits nothing.
func TestSessionNotFirstRejected(t *testing.T) {
	c, sink := newTest(t)
	if err := c.Session(testSession); err != nil {
		t.Fatal(err)
	}
	if err := c.Session(testSession); !errors.Is(err, ErrSessionNotFirst) {
		t.Fatalf("second Session: %v, want ErrSessionNotFirst", err)
	}
	c2, sink2 := newTest(t)
	now := uint64(0)
	feeder{t, c2, 10, 11, &now}.open("/tmp/a", 0, 3)
	if err := c2.Session(testSession); !errors.Is(err, ErrSessionNotFirst) {
		t.Fatalf("Session after an event: %v, want ErrSessionNotFirst", err)
	}
	if n, n2 := len(byKind(sink.events, schema.KindSession)), len(byKind(sink2.events, schema.KindSession)); n != 1 || n2 != 0 {
		t.Fatalf("session events emitted: %d and %d, want 1 and 0", n, n2)
	}
}

// TestRecordLosses: only growth of the cumulative counter becomes a drop
// event, carrying the delta; zero and repeated totals emit nothing.
func TestRecordLosses(t *testing.T) {
	c, sink := newTest(t)
	for _, total := range []uint64{0, 3, 3, 5, 4} {
		if err := c.RecordLosses(total); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	drops := byKind(sink.events, schema.KindDrop)
	if len(drops) != 2 || *drops[0].Args.Lost != 3 || *drops[1].Args.Lost != 2 {
		t.Fatalf("drops %+v: want lost 3 then 2", drops)
	}
	if len(sink.events) != 2 {
		t.Fatalf("%d events, want only the two drops", len(sink.events))
	}
}

// TestRecordSkipsApartFromLosses: skipped program runs (task vd4) become
// their own host-level skipped_runs drops, tracked apart from the lost
// records: neither counter's total moves the other's delta, and a plain
// loss drop carries no reason.
func TestRecordSkipsApartFromLosses(t *testing.T) {
	c, sink := newTest(t)
	steps := []struct{ lost, skipped uint64 }{{0, 0}, {0, 691}, {2, 691}, {2, 700}, {3, 650}}
	for _, s := range steps {
		if err := c.RecordLosses(s.lost); err != nil {
			t.Fatal(err)
		}
		if err := c.RecordSkips(s.skipped); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	type drop struct {
		lost   uint64
		reason string
	}
	want := []drop{{691, schema.DropSkippedRuns}, {2, ""}, {9, schema.DropSkippedRuns}, {1, ""}}
	drops := byKind(sink.events, schema.KindDrop)
	if len(drops) != len(want) {
		t.Fatalf("%d drops, want %d: %+v", len(drops), len(want), drops)
	}
	for i, d := range drops {
		got := drop{lost: *d.Args.Lost}
		if d.Args.DropReason != nil {
			got.reason = *d.Args.DropReason
		}
		if got != want[i] || d.PID != 0 {
			t.Errorf("drop %d: %+v pid %d, want %+v pid 0", i, got, d.PID, want[i])
		}
	}
}

// TestRecordSkipsAfterEnd: like every host event, a skip report after the
// end event is refused, so the end stays last.
func TestRecordSkipsAfterEnd(t *testing.T) {
	c, _ := newTest(t)
	if err := c.End(schema.EndStop); err != nil {
		t.Fatal(err)
	}
	if err := c.RecordSkips(5); !errors.Is(err, ErrEnded) {
		t.Fatalf("RecordSkips after End: %v, want ErrEnded", err)
	}
}

// TestDropsInterleavedWithHeldSockets: a drop reported while socket events
// wait for their conn id is numbered behind them and emitted in seq order
// (memSink fails on any gap or reorder).
func TestDropsInterleavedWithHeldSockets(t *testing.T) {
	c, sink := newTest(t)
	now := uint64(100)
	client := feeder{t, c, 100, 101, &now}
	client.handle(&types.FdEvent{EventType: types.ENTER_FD_EVENT, TraceId: types.SYS_ENTER_CONNECT,
		Time: client.tick(), Pid: 100, Tid: 101, Fd: 6})
	client.handle(tcp(client.tick(), 0xA, 100, 6, synSent, tcpEstablished, cli, srv))
	client.exit(types.SYS_EXIT_CONNECT, 0)
	if err := c.RecordLosses(7); err != nil {
		t.Fatal(err)
	}
	client.open("/tmp/x", 0, 9)
	if err := c.RecordLosses(8); err != nil {
		t.Fatal(err)
	}
	if len(sink.events) != 0 {
		t.Fatalf("%d events emitted ahead of the held connect", len(sink.events))
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range sink.events {
		kinds = append(kinds, e.Event)
	}
	want := []string{schema.KindTCPState, schema.KindConnect, schema.KindDrop, schema.KindOpenat, schema.KindDrop}
	if len(kinds) != len(want) {
		t.Fatalf("kinds %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds %v, want %v", kinds, want)
		}
	}
}

// TestEndIsLast: the end event follows every held event, and nothing is
// accepted after it.
func TestEndIsLast(t *testing.T) {
	c, sink := newClockTest(t, 900)
	now := uint64(100)
	client := feeder{t, c, 100, 101, &now}
	client.handle(&types.FdEvent{EventType: types.ENTER_FD_EVENT, TraceId: types.SYS_ENTER_CONNECT,
		Time: client.tick(), Pid: 100, Tid: 101, Fd: 6})
	client.handle(tcp(client.tick(), 0xA, 100, 6, synSent, tcpEstablished, cli, srv))
	client.exit(types.SYS_EXIT_CONNECT, 0)
	if err := c.End(schema.EndTargetsExited); err != nil {
		t.Fatal(err)
	}
	late := []struct {
		name string
		call func() error
	}{
		{"Handle", func() error { return c.Handle(&types.RetEvent{EventType: types.EXIT_RET_EVENT}) }},
		{"RecordLosses", func() error { return c.RecordLosses(1) }},
		{"Session", func() error { return c.Session(testSession) }},
		{"End", func() error { return c.End(schema.EndStop) }},
	}
	for _, l := range late {
		if err := l.call(); !errors.Is(err, ErrEnded) {
			t.Errorf("%s after End: %v, want ErrEnded", l.name, err)
		}
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	last := sink.events[len(sink.events)-1]
	if last.Event != schema.KindEnd || *last.Args.EndReason != schema.EndTargetsExited || last.BootNS != 900 {
		t.Fatalf("last event %+v, want end/targets_exited", last)
	}
	if n := len(byKind(sink.events, schema.KindEnd)); n != 1 {
		t.Fatalf("%d end events, want 1", n)
	}
}

// TestEndRejectsUnknownReason: an invalid reason is refused before it is
// numbered, so a valid End can still follow and seq stays gap-free.
func TestEndRejectsUnknownReason(t *testing.T) {
	c, sink := newTest(t)
	for _, bad := range []string{"", "signal", "Duration"} {
		if err := c.End(bad); !errors.Is(err, schema.ErrInvalid) {
			t.Fatalf("End(%q): %v, want ErrInvalid", bad, err)
		}
	}
	if err := c.End(schema.EndDuration); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if len(sink.events) != 1 || sink.events[0].Seq != 1 || *sink.events[0].Args.EndReason != schema.EndDuration {
		t.Fatalf("events %+v: want one end event at seq 1", sink.events)
	}
}
