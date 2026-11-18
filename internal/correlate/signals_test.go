package correlate

import (
	"testing"

	"golang.org/x/sys/unix"

	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// kill feeds a kill whose signal_generate record (scope, result) fires
// inside the call, as the kernel's does.
func (f feeder) kill(target uint32, sig int32, scope uint32, result int32) {
	f.handle(&types.NullEvent{EventType: types.ENTER_NULL_EVENT, TraceId: types.SYS_ENTER_KILL,
		Time: f.tick(), Pid: f.pid, Tid: f.tid})
	f.handle(&types.SignalEvent{EventType: types.SIGNAL_EVENT, Time: f.tick(), Pid: f.pid, Tid: f.tid,
		TargetPid: target, TargetTid: target, Sig: sig, Result: result, Group: 1, Scope: scope})
	f.exit(types.SYS_EXIT_KILL, 0)
}

const bothEnds = types.SIGNAL_SCOPE_SENDER | types.SIGNAL_SCOPE_TARGET

// TestKillBetweenTracedProcesses: the kill names target and signal, the
// receiver gets a signal event naming its sender, and a signaled edge
// links the two.
func TestKillBetweenTracedProcesses(t *testing.T) {
	c, sink := newTest(t)
	f := feeder{t, c, 10, 11, new(uint64)}
	f.kill(20, int32(unix.SIGTERM), bothEnds, signalDelivered)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	k := byKind(sink.events, schema.KindKill)[0]
	if k.FD != schema.NoFD || *k.Args.TargetPID != 20 || *k.Args.Signal != int32(unix.SIGTERM) {
		t.Fatalf("kill fd %d args %+v", k.FD, k.Args)
	}
	s := byKind(sink.events, schema.KindSignal)[0]
	if s.PID != 20 || s.TID != 20 || *s.Args.SenderPID != 10 || *s.Args.Signal != int32(unix.SIGTERM) {
		t.Fatalf("signal pid %d tid %d args %+v", s.PID, s.TID, s.Args)
	}
	if !hasEdge(c.Graph(), schema.EdgeSignaled, schema.ProcessNodeID("h1", 10, 1010),
		schema.ProcessNodeID("h1", 20, 1020), "") {
		t.Error("no signaled edge 10 → 20")
	}
}

// TestSignalFromUntracedSender: a supervisor's signal reaches a traced
// receiver as an event naming the sender, without an edge (no node).
func TestSignalFromUntracedSender(t *testing.T) {
	c, sink := newTest(t)
	if err := c.Handle(&types.SignalEvent{EventType: types.SIGNAL_EVENT, Time: 50, Pid: 1, Tid: 1,
		TargetPid: 20, TargetTid: 21, Sig: int32(unix.SIGTERM), Scope: types.SIGNAL_SCOPE_TARGET}); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	s := byKind(sink.events, schema.KindSignal)
	if len(s) != 1 || *s[0].Args.SenderPID != 1 || s[0].TID != 21 {
		t.Fatalf("signal events %+v", s)
	}
	for _, e := range c.Graph().Edges {
		if e.Type == schema.EdgeSignaled {
			t.Fatalf("edge %+v from an untraced sender", e)
		}
	}
}

// TestIgnoredSignalNotDelivered: an ignored signal is no delivery, but the
// kill still names what it sent; a kill to an untraced process has no
// signal event either.
func TestIgnoredSignalNotDelivered(t *testing.T) {
	c, sink := newTest(t)
	f := feeder{t, c, 10, 11, new(uint64)}
	f.kill(20, int32(unix.SIGCHLD), bothEnds, 1)
	f.kill(30, int32(unix.SIGTERM), types.SIGNAL_SCOPE_SENDER, signalDelivered)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if s := byKind(sink.events, schema.KindSignal); len(s) != 0 {
		t.Fatalf("signal events %+v, want none", s)
	}
	if k := byKind(sink.events, schema.KindKill); len(k) != 2 || *k[0].Args.Signal != int32(unix.SIGCHLD) ||
		*k[1].Args.TargetPID != 30 {
		t.Fatalf("kills %+v", k)
	}
}

// TestKillWithoutSignal: kill(pid, 0) or one that failed generates no
// record, so the kill carries no target or signal.
func TestKillWithoutSignal(t *testing.T) {
	c, sink := newTest(t)
	f := feeder{t, c, 10, 11, new(uint64)}
	f.handle(&types.NullEvent{EventType: types.ENTER_NULL_EVENT, TraceId: types.SYS_ENTER_KILL,
		Time: f.tick(), Pid: 10, Tid: 11})
	f.exit(types.SYS_EXIT_KILL, -int64(unix.ESRCH))
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	k := byKind(sink.events, schema.KindKill)[0]
	if k.Args.Signal != nil || k.Args.TargetPID != nil || k.Err != "ESRCH" {
		t.Fatalf("kill %+v args %+v", k, k.Args)
	}
}
