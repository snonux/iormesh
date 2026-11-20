package normalize

import (
	"testing"

	"iormesh/internal/schema"
)

// ipcEvent is a minimal valid event of pid 4242 for the step 7 tests.
func ipcEvent(seq uint64, kind string, fd int32, gen uint32) schema.Event {
	return schema.Event{SchemaVersion: schema.Version, Project: "p", Seq: seq, HostID: "h",
		PID: 4242, PIDInc: 7, TID: 4243, Event: kind, FD: fd, FDGen: gen,
		Attribution: schema.AttributionNone}
}

// TestNormalizeSecondFDAndPids: args.fd2 is renumbered as an fd instance
// of the event's process — the same number its own events get — and a
// kill's target and a signal's sender take the PID ordinal their process
// has, so the check can bind a pipe's end through pipe2 and compare runs.
func TestNormalizeSecondFDAndPids(t *testing.T) {
	pipe := ipcEvent(1, schema.KindPipe2, 9, 1)
	fd2, gen2 := int32(10), uint32(1)
	pipe.Args.FD2, pipe.Args.FD2Gen = &fd2, &gen2
	write := ipcEvent(2, schema.KindWrite, 10, 1)
	kill := ipcEvent(3, schema.KindKill, schema.NoFD, 0)
	target, sig := uint32(4242), int32(15)
	kill.Args.TargetPID, kill.Args.Signal = &target, &sig
	got, err := Normalize(Trace{Events: []schema.Event{pipe, write, kill},
		Graph: schema.Graph{SchemaVersion: schema.Version}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	p, w, k := got.Events[0], got.Events[1], got.Events[2]
	if *p.Args.FD2 != w.FD || *p.Args.FD2Gen != w.FDGen || p.FD == w.FD {
		t.Errorf("pipe2 fd %d fd2 %d/%d, write fd %d/%d: want fd2 = the write's instance",
			p.FD, *p.Args.FD2, *p.Args.FD2Gen, w.FD, w.FDGen)
	}
	if *k.Args.TargetPID != k.PID {
		t.Errorf("kill target %d, want its process's ordinal %d", *k.Args.TargetPID, k.PID)
	}
	if fd2 != 10 || target != 4242 {
		t.Error("Normalize wrote through the input's pointers")
	}

	x, err := ParseExpectation([]byte(`{"scenario": "s", "schema_version": 1, "events": [
	  {"thread": "$t1", "event": "pipe2", "fd": "$fd1", "args": {"fd2": "$fd2"}},
	  {"thread": "$t1", "event": "write", "fd": "$fd2"},
	  {"thread": "$t1", "event": "kill", "args": {"signal": 15}}],
	  "edges": [], "must_not_appear": []}`))
	if err != nil {
		t.Fatal(err)
	}
	if vs := Check(got, x); len(vs) != 0 {
		t.Fatalf("violations %v", vs)
	}
	x.Events[1].FD = x.Events[0].FD // the write on the read end
	if vs := Check(got, x); len(vs) != 1 {
		t.Fatalf("write bound to fd2's other end passes: %v", vs)
	}
}

// TestExpectationFD2MustBeFDSymbol: args.fd2 binds fd instances only.
func TestExpectationFD2MustBeFDSymbol(t *testing.T) {
	_, err := ParseExpectation([]byte(`{"scenario": "s", "schema_version": 1, "events": [
	  {"thread": "$t1", "event": "pipe2", "args": {"fd2": "$p1"}}],
	  "edges": [], "must_not_appear": []}`))
	if err == nil {
		t.Fatal("fd2 bound to a process symbol was accepted")
	}
}

// TestNormalizeForkParent (task ze4): a fork's parent_pid and
// parent_incarnation take the ordinals the parent's own events have, and
// the input's pointers stay untouched.
func TestNormalizeForkParent(t *testing.T) {
	parent := ipcEvent(1, schema.KindWrite, 1, 0)
	fork := ipcEvent(2, schema.KindFork, schema.NoFD, 0)
	fork.PID, fork.PIDInc, fork.TID = 5000, 9, 5000
	ppid, pinc := uint32(4242), uint64(7)
	fork.Args.ParentPID, fork.Args.ParentInc = &ppid, &pinc
	got, err := Normalize(Trace{Events: []schema.Event{parent, fork},
		Graph: schema.Graph{SchemaVersion: schema.Version}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	p, f := got.Events[0], got.Events[1]
	if *f.Args.ParentPID != p.PID || *f.Args.ParentInc != p.PIDInc || f.PID == p.PID {
		t.Errorf("fork %d@%d parent %d@%d, want the parent's ordinals %d@%d",
			f.PID, f.PIDInc, *f.Args.ParentPID, *f.Args.ParentInc, p.PID, p.PIDInc)
	}
	if ppid != 4242 || pinc != 7 {
		t.Error("Normalize wrote through the input's pointers")
	}
}
