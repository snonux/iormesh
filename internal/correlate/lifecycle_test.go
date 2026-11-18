package correlate

import (
	"errors"
	"slices"
	"testing"

	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// incarnations counts the graph's incarnation edges from app.
func incarnations(g schema.Graph, app string) []string {
	var to []string
	for _, e := range g.Edges {
		if e.Type == schema.EdgeIncarnation && e.From == app {
			to = append(to, e.To)
		}
	}
	return to
}

// TestIncarnationLinksRestarts: two incarnations of one app under two
// PIDs hang off one app node, and the graph validates.
func TestIncarnationLinksRestarts(t *testing.T) {
	c, _ := newTest(t)
	app := schema.AppNodeID("p", "*RestartApp*")
	for _, pid := range []uint32{40, 41} {
		if err := c.Incarnation(app, pid, 0); err != nil {
			t.Fatal(err)
		}
	}
	g := c.Graph()
	if err := g.Validate(); err != nil {
		t.Fatalf("graph invalid: %v", err)
	}
	got := incarnations(g, app)
	want := []string{schema.ProcessNodeID("h1", 40, 1040), schema.ProcessNodeID("h1", 41, 1041)}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("incarnation edges to %v, want %v", got, want)
	}
}

// TestIncarnationGapDrop: a non-zero attach gap becomes one drop event
// naming the process, ahead of its first traced event; a zero gap emits
// nothing.
func TestIncarnationGapDrop(t *testing.T) {
	c, sink := newTest(t)
	app := schema.AppNodeID("p", "*x*")
	if err := c.Incarnation(app, 40, 0); err != nil {
		t.Fatal(err)
	}
	if err := c.Incarnation(app, 41, 6); err != nil {
		t.Fatal(err)
	}
	now := uint64(100)
	feeder{t, c, 41, 41, &now}.open("/tmp/a", 0, 3)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	drops := byKind(sink.events, schema.KindDrop)
	if len(drops) != 1 {
		t.Fatalf("drops %+v, want exactly the one of pid 41", drops)
	}
	d := drops[0]
	if d.PID != 41 || d.PIDInc != 1041 || *d.Args.Lost != 6 || *d.Args.DropReason != schema.DropAttachGap {
		t.Fatalf("drop %+v (args %+v), want pid 41 lost 6 attach_gap", d, d.Args)
	}
	if sink.events[0].Event != schema.KindDrop || sink.events[1].Event != schema.KindOpenat {
		t.Fatalf("events %v: the drop must precede the process's first event", sink.events)
	}
}

// TestNewTaskRegistersChild: a forked child is registered at its
// task_newtask record (incarnation read then) with a forked edge from its
// creator, a new thread is not a process, and a PID reused by a new child
// is a new incarnation.
func TestNewTaskRegistersChild(t *testing.T) {
	c, _ := newTest(t)
	info := map[uint32]uint64{50: 7}
	c.cfg.ProcInfo = func(pid uint32) (string, uint64) { return "child", info[pid] }
	handle := func(rec any) {
		if err := c.Handle(rec); err != nil {
			t.Fatal(err)
		}
	}
	handle(&types.TaskNewtaskEvent{EventType: types.TASK_NEWTASK_EVENT, Time: 1, Pid: 50, Tid: 50, CreatorPid: 1})
	handle(&types.TaskNewtaskEvent{EventType: types.TASK_NEWTASK_EVENT, Time: 2, Pid: 50, Tid: 51,
		CreatorPid: 50, CloneFlags: cloneThread})
	handle(&types.ProcessExitEvent{EventType: types.PROCESS_EXIT_EVENT, Time: 3, Pid: 50, Tid: 50, GroupDead: 1})
	info[50] = 9 // the PID is reused by a later child
	handle(&types.TaskNewtaskEvent{EventType: types.TASK_NEWTASK_EVENT, Time: 4, Pid: 50, Tid: 50, CreatorPid: 1})
	var procs []string
	for _, n := range c.Graph().Nodes {
		if n.Type == schema.NodeProcess {
			procs = append(procs, n.ID)
		}
	}
	parent := schema.ProcessNodeID("h1", 1, 0)
	want := []string{parent, schema.ProcessNodeID("h1", 50, 7), schema.ProcessNodeID("h1", 50, 9)}
	if !slices.Equal(procs, want) {
		t.Fatalf("process nodes %v, want %v", procs, want)
	}
	for _, child := range want[1:] {
		if !hasEdge(c.Graph(), schema.EdgeForked, parent, child, "") {
			t.Errorf("no forked edge %s → %s", parent, child)
		}
	}
}

// TestExitDropsPendingEnter: a thread that dies inside a syscall leaves
// no enter behind for a later thread reusing its tid to pair with.
func TestExitDropsPendingEnter(t *testing.T) {
	c, sink := newTest(t)
	now := uint64(0)
	f := feeder{t, c, 60, 61, &now}
	f.handle(&types.FdEvent{EventType: types.ENTER_FD_EVENT, TraceId: types.SYS_ENTER_READ,
		Time: f.tick(), Pid: 60, Tid: 61, Fd: 3})
	f.handle(&types.ProcessExitEvent{EventType: types.PROCESS_EXIT_EVENT, Time: f.tick(), Pid: 60, Tid: 61})
	f.exit(types.SYS_EXIT_READ, 8)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if len(byKind(sink.events, schema.KindRead)) != 0 || c.Unpaired() != 1 {
		t.Fatalf("events %v, unpaired %d: the dead thread's enter paired", sink.events, c.Unpaired())
	}
}

func TestIncarnationAfterEnd(t *testing.T) {
	c, _ := newTest(t)
	if err := c.End(schema.EndStop); err != nil {
		t.Fatal(err)
	}
	if err := c.Incarnation(schema.AppNodeID("p", "*x*"), 1, 0); !errors.Is(err, ErrEnded) {
		t.Fatalf("Incarnation after End: %v, want ErrEnded", err)
	}
}
