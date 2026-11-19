package correlate

import (
	"testing"

	"golang.org/x/sys/unix"

	"iormesh/internal/schema"
	"iormesh/internal/types"
)

func (f feeder) fcntl(fd int32, cmd uint32, arg uint64, ret int64) {
	f.handle(&types.FcntlEvent{EventType: types.ENTER_FCNTL_EVENT, TraceId: types.SYS_ENTER_FCNTL,
		Time: f.tick(), Pid: f.pid, Tid: f.tid, Fd: uint32(fd), Cmd: cmd, Arg: arg})
	f.exit(types.SYS_EXIT_FCNTL, ret)
}

func (f feeder) dup3(oldfd int32, flags int32, ret int64) {
	f.handle(&types.Dup3Event{EventType: types.ENTER_DUP3_EVENT, TraceId: types.SYS_ENTER_DUP3,
		Time: f.tick(), Pid: f.pid, Tid: f.tid, Fd: oldfd, Flags: flags})
	f.exit(types.SYS_EXIT_DUP3, ret)
}

func (f feeder) exec() {
	f.handle(&types.ProcessExecEvent{EventType: types.PROCESS_EXEC_EVENT, Time: f.tick(),
		Pid: f.pid, Tid: f.tid})
}

// forkClock is the earliest time fork stamps: newTest's ProcInfo says a
// pid started at 1000+pid, and a child never starts after its fork
// record (newTask's plausibility check, childIdentity).
const forkClock = 100_000

func (f feeder) fork(child uint32) {
	*f.now = max(*f.now, forkClock)
	f.handle(&types.TaskNewtaskEvent{EventType: types.TASK_NEWTASK_EVENT, Time: f.tick(),
		Pid: child, Tid: child, CreatorPid: f.pid})
}

// isOpen reports whether the correlator holds fd open in pid.
func isOpen(c *Correlator, pid uint32, fd int32) bool {
	_, ok := c.procs[pid].fds[fd]
	return ok
}

// TestCopiesNameTheSameFile: every copy form (dup, dup2, dup3,
// F_DUPFD, F_DUPFD_CLOEXEC) makes a new instance of its number that names
// the old fd's path; dup2 onto itself creates nothing; dup3 reports its
// flags and fcntl its command.
func TestCopiesNameTheSameFile(t *testing.T) {
	c, sink := newTest(t)
	f := feeder{t, c, 10, 11, new(uint64)}
	f.open("/a", 0, 4)
	f.fdCall(types.SYS_ENTER_DUP, types.SYS_EXIT_DUP, 4, 5)
	f.fdCall(types.SYS_ENTER_DUP2, types.SYS_EXIT_DUP2, 4, 6)
	f.dup3(4, unix.O_CLOEXEC, 7)
	f.fcntl(4, unix.F_DUPFD, 0, 8)
	f.fcntl(4, unix.F_DUPFD_CLOEXEC, 0, 9)
	f.fdCall(types.SYS_ENTER_DUP2, types.SYS_EXIT_DUP2, 4, 4)
	for fd := int32(4); fd <= 9; fd++ {
		f.ftruncate(fd)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	for i, e := range byKind(sink.events, schema.KindFtruncate) {
		if e.FDGen != 1 || e.Args.Path == nil || *e.Args.Path != "/a" {
			t.Errorf("ftruncate %d: fd %d gen %d path %v, want gen 1 naming /a",
				i, e.FD, e.FDGen, e.Args.Path)
		}
	}
	if d := byKind(sink.events, schema.KindDup3)[0]; d.Args.Flags == nil || *d.Args.Flags != unix.O_CLOEXEC {
		t.Errorf("dup3 args %+v, want flags O_CLOEXEC", d.Args)
	}
	if fc := byKind(sink.events, schema.KindFcntl)[1]; fc.Args.Cmd == nil || *fc.Args.Cmd != unix.F_DUPFD_CLOEXEC || fc.Ret != 9 {
		t.Errorf("fcntl %+v (args %+v), want cmd F_DUPFD_CLOEXEC returning 9", fc, fc.Args)
	}
}

// TestCopyOntoOpenFdIsNewInstance: dup2 onto an open number replaces that
// instance (a new generation), and a copy of a socket fd carries the
// connection.
func TestCopyOntoOpenFdIsNewInstance(t *testing.T) {
	c, sink := newTest(t)
	loopback(t, c, 1000)
	client := feeder{t, c, 100, 101, new(uint64)}
	*client.now = 10_000
	client.open("/b", 0, 9)
	client.fdCall(types.SYS_ENTER_DUP2, types.SYS_EXIT_DUP2, 6, 9)
	client.write(9, 3)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	conn := byKind(sink.events, schema.KindConnect)[0]
	w := byKind(sink.events, schema.KindWrite)
	last := w[len(w)-1]
	if last.FD != 9 || last.FDGen != 2 || last.ConnID == "" || last.ConnID != conn.ConnID {
		t.Fatalf("write on the copy: fd %d gen %d conn %q, want fd 9 gen 2 on conn %q",
			last.FD, last.FDGen, last.ConnID, conn.ConnID)
	}
}

// TestForkInheritsTable: a child starts with its parent's fds — same
// instances, same paths — continues their generations, and its changes do
// not reach the parent.
func TestForkInheritsTable(t *testing.T) {
	c, sink := newTest(t)
	parent := feeder{t, c, 10, 11, new(uint64)}
	parent.open("/a", 0, 4)
	parent.open("/x", 0, 5)
	parent.close(5)
	parent.fork(20)
	child := feeder{t, c, 20, 20, parent.now}
	child.ftruncate(4)
	child.open("/y", 0, 5)
	child.close(4)
	parent.ftruncate(4)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	ft := byKind(sink.events, schema.KindFtruncate)
	if ft[0].PID != 20 || ft[0].FDGen != 1 || *ft[0].Args.Path != "/a" {
		t.Errorf("child ftruncate %+v: want the inherited fd 4 (gen 1) naming /a", ft[0])
	}
	if ft[1].PID != 10 || ft[1].FDGen != 1 || ft[1].Args.Path == nil || *ft[1].Args.Path != "/a" {
		t.Errorf("parent ftruncate %+v: the child's close must not reach the parent", ft[1])
	}
	if o := byKind(sink.events, schema.KindOpenat)[2]; o.PID != 20 || o.FDGen != 2 {
		t.Errorf("child open %+v: want gen 2, after the parent's gen 1 of fd 5", o)
	}
	g := c.Graph()
	if !hasEdge(g, schema.EdgeForked, schema.ProcessNodeID("h1", 10, 1010),
		schema.ProcessNodeID("h1", 20, 1020), "") {
		t.Errorf("no forked edge 10 → 20: %+v", g.Edges)
	}
	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestExecClosesCloexecFds: an exec closes exactly the fds whose
// close-on-exec state is set or unknown, except unknown stdio; the state
// comes from open, accept4, dup3 and fcntl F_SETFD/F_GETFD, and a fork
// passes it on.
func TestExecClosesCloexecFds(t *testing.T) {
	cases := []struct {
		name    string
		fd      int32
		set     func(f feeder, fd int32)
		survive bool
	}{
		{"open O_CLOEXEC", 4, func(f feeder, fd int32) { f.open("/a", unix.O_CLOEXEC, int64(fd)) }, false},
		{"open", 4, func(f feeder, fd int32) { f.open("/a", 0, int64(fd)) }, true},
		{"F_SETFD FD_CLOEXEC", 4, func(f feeder, fd int32) {
			f.open("/a", 0, int64(fd))
			f.fcntl(fd, unix.F_SETFD, unix.FD_CLOEXEC, 0)
		}, false},
		{"F_GETFD set", 4, func(f feeder, fd int32) { f.fcntl(fd, unix.F_GETFD, 0, unix.FD_CLOEXEC) }, false},
		{"F_GETFD clear", 4, func(f feeder, fd int32) { f.fcntl(fd, unix.F_GETFD, 0, 0) }, true},
		{"dup2 clears", 4, func(f feeder, fd int32) {
			f.open("/a", unix.O_CLOEXEC, 3)
			f.fdCall(types.SYS_ENTER_DUP2, types.SYS_EXIT_DUP2, 3, int64(fd))
		}, true},
		{"dup3 O_CLOEXEC", 4, func(f feeder, fd int32) { f.dup3(3, unix.O_CLOEXEC, int64(fd)) }, false},
		{"accept4 SOCK_CLOEXEC", 4, func(f feeder, fd int32) { f.accept4(unix.SOCK_CLOEXEC, int64(fd)) }, false},
		{"unknown", 4, func(f feeder, fd int32) { f.read(fd, 1) }, false},
		{"unknown stdio", 1, func(f feeder, fd int32) { f.write(fd, 1) }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTest(t)
			parent := feeder{t, c, 10, 11, new(uint64)}
			tc.set(parent, tc.fd)
			parent.fork(20)
			child := feeder{t, c, 20, 20, parent.now}
			child.exec()
			if got := isOpen(c, 20, tc.fd); got != tc.survive {
				t.Fatalf("fd %d open after exec: %v, want %v", tc.fd, got, tc.survive)
			}
			if !isOpen(c, 10, tc.fd) {
				t.Fatalf("the child's exec closed the parent's fd %d", tc.fd)
			}
		})
	}
}

func (f feeder) accept4(flags int32, ret int64) {
	f.handle(&types.AcceptEvent{EventType: types.ENTER_ACCEPT_EVENT, TraceId: types.SYS_ENTER_ACCEPT4,
		Time: f.tick(), Pid: f.pid, Tid: f.tid, Fd: 3, Flags: flags})
	f.handle(&types.AcceptEvent{EventType: types.EXIT_ACCEPT_EVENT, TraceId: types.SYS_EXIT_ACCEPT4,
		Time: f.tick(), Pid: f.pid, Tid: f.tid, Fd: -1, Ret: ret})
}

// TestExecAfterCloseReuse: an fd the exec closed is a new instance when
// the new program's untraced syscall reuses its number, so it does not
// inherit the old program's path.
func TestExecAfterCloseReuse(t *testing.T) {
	c, sink := newTest(t)
	f := feeder{t, c, 10, 11, new(uint64)}
	f.open("/a", unix.O_CLOEXEC, 4)
	f.exec()
	f.ftruncate(4)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	ft := byKind(sink.events, schema.KindFtruncate)[0]
	if ft.FDGen != 2 || ft.Args.Path != nil {
		t.Fatalf("ftruncate after exec %+v (path %v): want gen 2 and no path", ft, ft.Args.Path)
	}
}
