package correlate

import (
	"testing"

	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// groupDead hands the correlator the exit record of pid's last thread.
func (f feeder) groupDead(pid uint32) {
	f.handle(&types.ProcessExitEvent{EventType: types.PROCESS_EXIT_EVENT, Time: f.tick(),
		Pid: pid, Tid: pid, GroupDead: 1})
}

// openedEdges counts the graph's opened edges to path.
func openedEdges(g schema.Graph, path string) int {
	n := 0
	for _, e := range g.Edges {
		if e.Type == schema.EdgeOpened && e.To == schema.FileNodeID("h1", path) {
			n++
		}
	}
	return n
}

// pastFuseWindow moves f's clock beyond the fuse window with a record of
// f's own process.
func pastFuseWindow(f feeder) {
	*f.now += defaultFuseWindowNS + 1
	f.read(0, 1)
}

// TestDeadChildrenLeaveNoState (task xi4): a parent holding many fds
// forks children that inherit its table and exit. A dead child is
// skipped by renames from its group-dead exit on — a rename right after
// names the parent alone, no dead child — and its table and whole state
// go once the fuse window has passed, so the correlator holds the live
// parent only.
func TestDeadChildrenLeaveNoState(t *testing.T) {
	c, _ := newTest(t)
	now := uint64(0)
	f := feeder{t, c, 10, 10, &now}
	for fd := int64(3); fd < 43; fd++ {
		f.open("/data/f", 0, fd)
	}
	f.open("/var/log/app.log", 0, 50)
	for child := uint32(2000); child < 2200; child++ {
		f.fork(child)
		f.groupDead(child)
	}
	f.rename("/var/log/app.log", "/var/log/app.log.1")
	if n := openedEdges(c.Graph(), "/var/log/app.log.1"); n != 1 {
		t.Fatalf("%d opened edges to the rotated log, want 1: dead children do not hold it", n)
	}
	pastFuseWindow(f)
	if len(c.procs) != 1 || c.procs[10] == nil {
		t.Fatalf("correlator holds %d processes after the children died, want only the parent", len(c.procs))
	}
}

// TestLateTCPStateOfDeadProcess (task xi4): the kernel reports a dead
// process's socket transitions after its exit, owned by the dead pid —
// well past the fuse window for a FIN_WAIT2 tail. They still name that
// incarnation (inc, comm) and the socket's fd generation, even when /proc
// shows another process under the pid by then; once the socket is closed
// and the window passed, the state goes.
func TestLateTCPStateOfDeadProcess(t *testing.T) {
	c, sink := newTest(t)
	now := uint64(0)
	f := feeder{t, c, 10, 10, &now}
	f.fork(60)
	child := feeder{t, c, 60, 60, &now}
	child.open("/tmp/x", 0, 6)
	child.close(6) // fd 6's next instance is generation 2
	child.handle(&types.FdEvent{EventType: types.ENTER_FD_EVENT, TraceId: types.SYS_ENTER_CONNECT,
		Time: child.tick(), Pid: 60, Tid: 60, Fd: 6})
	child.handle(tcp(child.tick(), 0xA, 60, 6, tcpClose, synSent, "10.0.0.1:0", "10.0.0.2:80"))
	child.handle(tcp(child.tick(), 0xA, 60, 6, synSent, tcpEstablished, "10.0.0.1:4000", "10.0.0.2:80"))
	child.exit(types.SYS_EXIT_CONNECT, 0)
	f.groupDead(60)
	c.cfg.ProcInfo = func(uint32) (string, uint64) { return "squatter", 9999 }
	pastFuseWindow(f)
	child.handle(tcp(child.tick(), 0xA, 60, 6, tcpEstablished, 4, "10.0.0.1:4000", "10.0.0.2:80"))
	pastFuseWindow(f)
	child.handle(tcp(child.tick(), 0xA, 60, 6, 4, tcpClose, "10.0.0.1:4000", "10.0.0.2:80"))
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	states := byKind(sink.events, schema.KindTCPState)
	if len(states) != 4 {
		t.Fatalf("%d tcp_state events, want 4", len(states))
	}
	for _, e := range states[2:] {
		if e.PIDInc != 1060 || e.Comm != "proc60" || e.FDGen != 2 || e.ConnID != states[0].ConnID {
			t.Errorf("late %s: inc %d comm %q gen %d conn %q, want the dead child's (1060 proc60 gen 2 %q)",
				*e.Args.SockState, e.PIDInc, e.Comm, e.FDGen, e.ConnID, states[0].ConnID)
		}
	}
	pastFuseWindow(f)
	if _, ok := c.procs[60]; ok {
		t.Fatal("the dead child's state outlived its last socket and the fuse window")
	}
}

// TestSiblingFlushAfterGroupDead (task xi4 review): two exiting threads
// can both report group_dead (el9's signal->live fallback, exec.c), and
// a sibling's folded write, flushed from its own exit, can trail the
// first such record. It still names the fd instance it wrote to.
func TestSiblingFlushAfterGroupDead(t *testing.T) {
	c, sink := newTest(t)
	now := uint64(0)
	f := feeder{t, c, 10, 10, &now}
	f.fork(60)
	sib := feeder{t, c, 60, 61, &now}
	sib.open("/tmp/a", 0, 5)
	sib.close(5)
	sib.open("/tmp/a", 0, 5) // generation 2
	first := sib.tick()
	f.groupDead(60)
	sib.folded(types.SYS_ENTER_WRITE, 5, 3, 30, first, sib.tick())
	sib.handle(&types.ProcessExitEvent{EventType: types.PROCESS_EXIT_EVENT, Time: sib.tick(),
		Pid: 60, Tid: 61, GroupDead: 1})
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	writes := byKind(sink.events, schema.KindWrite)
	if len(writes) != 1 || writes[0].FDGen != 2 {
		t.Fatalf("writes %+v, want the sibling's folded write on fd 5 generation 2", writes)
	}
}

// TestRequeuedDeadProcessWaitsLatestWindow (task xi4 review): a dead
// process queued at its exit, which then owns a socket from a late first
// record, waits out the window after that socket's close, not the one
// after its exit.
func TestRequeuedDeadProcessWaitsLatestWindow(t *testing.T) {
	c, _ := newTest(t)
	now := uint64(0)
	f := feeder{t, c, 10, 10, &now}
	f.fork(60)
	f.groupDead(60)
	*f.now += defaultFuseWindowNS / 2
	f.handle(tcp(f.tick(), 0xA, 60, 6, tcpClose, synSent, "10.0.0.1:0", "10.0.0.2:80"))
	f.handle(tcp(f.tick(), 0xA, 60, 6, synSent, tcpClose, "10.0.0.1:4000", "10.0.0.2:80"))
	*f.now += defaultFuseWindowNS/2 + 100
	f.read(0, 1)
	if _, ok := c.procs[60]; !ok {
		t.Fatal("the dead process was reaped inside the window after its socket's close")
	}
	pastFuseWindow(f)
	if _, ok := c.procs[60]; ok {
		t.Fatal("the dead process outlived the window after its socket's close")
	}
}

// TestPIDReusedBeforeReap (task xi4): a pid reused by a new traced child
// before its predecessor's fuse window passed keeps the new state when
// the window does.
func TestPIDReusedBeforeReap(t *testing.T) {
	c, _ := newTest(t)
	now := uint64(0)
	f := feeder{t, c, 10, 10, &now}
	f.fork(60)
	f.groupDead(60)
	c.cfg.ProcInfo = func(uint32) (string, uint64) { return "next", 100_025 }
	f.fork(60)
	pastFuseWindow(f)
	p, ok := c.procs[60]
	if !ok || p.inc != 100_025 {
		t.Fatalf("pid 60's state %+v after the window, want the live successor (inc 100025)", p)
	}
}
