package correlate

import (
	"net/netip"
	"testing"

	"golang.org/x/sys/unix"

	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// pipe2 feeds a pipe2 returning (r, w) with flags.
func (f feeder) pipe2(flags, r, w int32) {
	f.handle(&types.PipeEvent{EventType: types.ENTER_PIPE_EVENT, TraceId: types.SYS_ENTER_PIPE2,
		Time: f.tick(), Pid: f.pid, Tid: f.tid, Flags: flags, Fd0: -1, Fd1: -1})
	f.handle(&types.PipeEvent{EventType: types.EXIT_PIPE_EVENT, TraceId: types.SYS_EXIT_PIPE2,
		Time: f.tick(), Pid: f.pid, Tid: f.tid, Flags: flags, Fd0: r, Fd1: w})
}

// addrCall feeds an fd call with its sock_addr record before the exit.
func (f feeder) addrCall(enterID, exitID types.TraceId, fd int32, a *types.SockAddrEvent, ret int64) {
	f.handle(&types.FdEvent{EventType: types.ENTER_FD_EVENT, TraceId: enterID,
		Time: f.tick(), Pid: f.pid, Tid: f.tid, Fd: fd})
	a.EventType, a.TraceId, a.Time, a.Pid, a.Tid = types.SOCK_ADDR_EVENT, exitID, f.tick(), f.pid, f.tid
	f.handle(a)
	f.exit(exitID, ret)
}

// udpAddr is a UDP socket's record: its local address, and arg the
// datagram's peer.
func udpAddr(local, arg string) *types.SockAddrEvent {
	l, a := netip.MustParseAddrPort(local), netip.MustParseAddrPort(arg)
	r := &types.SockAddrEvent{Family: afInet, SockType: sockDgram, ArgFamily: afInet,
		LocalPort: uint32(l.Port()), ArgPort: uint32(a.Port())}
	la, aa := l.Addr().As4(), a.Addr().As4()
	copy(r.LocalAddr[:], la[:])
	copy(r.ArgAddr[:], aa[:])
	return r
}

// unixAddr is an AF_UNIX stream socket's record.
func unixAddr(path string, sk, peer uint64) *types.SockAddrEvent {
	r := &types.SockAddrEvent{Family: afUnix, SockType: sockStream, Sk: sk, Peer: peer}
	if path != "" {
		r.PathKind = types.SOCK_ADDR_PATH_NAMED
		copy(r.Path[:], path)
	}
	return r
}

// TestPipeEndsAreOneConnection: pipe2's two ends are new instances that
// name one fused connection; a forked child's dup2 of an end onto its
// stdio joins it, so parent and child both get a connected edge to it.
func TestPipeEndsAreOneConnection(t *testing.T) {
	c, sink := newTest(t)
	parent := feeder{t, c, 10, 11, new(uint64)}
	parent.pipe2(unix.O_CLOEXEC, 5, 6)
	parent.fork(20)
	child := feeder{t, c, 20, 20, parent.now}
	child.fdCall(types.SYS_ENTER_DUP2, types.SYS_EXIT_DUP2, 5, 0)
	child.read(0, 3)
	parent.write(6, 3)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	p := byKind(sink.events, schema.KindPipe2)[0]
	if p.FD != 5 || p.FDGen != 1 || p.Args.FD2 == nil || *p.Args.FD2 != 6 || *p.Args.FD2Gen != 1 {
		t.Fatalf("pipe2 fd %d gen %d args %+v, want 5 and 6, both gen 1", p.FD, p.FDGen, p.Args)
	}
	r, w := byKind(sink.events, schema.KindRead)[0], byKind(sink.events, schema.KindWrite)[0]
	if p.ConnID == "" || r.ConnID != p.ConnID || w.ConnID != p.ConnID {
		t.Fatalf("conn ids pipe2 %q read %q write %q, want one", p.ConnID, r.ConnID, w.ConnID)
	}
	g := c.Graph()
	for _, pid := range []uint32{10, 20} {
		from := schema.ProcessNodeID("h1", pid, 1000+uint64(pid))
		if !hasEdge(g, schema.EdgeConnected, from, schema.SocketNodeID(p.ConnID), schema.StateFused) {
			t.Errorf("no fused connected edge from pid %d", pid)
		}
	}
	if !c.procs[10].cloexec[5] || !c.procs[10].cloexec[6] {
		t.Error("pipe2(O_CLOEXEC) ends not close-on-exec")
	}
}

// TestUnixConnectionFuses: the connecting end and the accepted end point
// at each other (sk, peer), so they share a fused conn id; bind and
// connect name the socket file.
func TestUnixConnectionFuses(t *testing.T) {
	c, sink := newTest(t)
	server := feeder{t, c, 10, 11, new(uint64)}
	client := feeder{t, c, 20, 21, server.now}
	server.addrCall(types.SYS_ENTER_BIND, types.SYS_EXIT_BIND, 4, unixAddr("/t/s.sock", 0x1, 0), 0)
	client.addrCall(types.SYS_ENTER_CONNECT, types.SYS_EXIT_CONNECT, 5, unixAddr("/t/s.sock", 0xC, 0xA), 0)
	server.handle(&types.AcceptEvent{EventType: types.ENTER_ACCEPT_EVENT, TraceId: types.SYS_ENTER_ACCEPT,
		Time: server.tick(), Pid: 10, Tid: 11, Fd: 4})
	acc := unixAddr("", 0xA, 0xC)
	acc.EventType, acc.TraceId, acc.Time, acc.Pid, acc.Tid, acc.Fd =
		types.SOCK_ADDR_EVENT, types.SYS_EXIT_ACCEPT, server.tick(), 10, 11, 7
	server.handle(acc)
	server.handle(&types.AcceptEvent{EventType: types.EXIT_ACCEPT_EVENT, TraceId: types.SYS_EXIT_ACCEPT,
		Time: server.tick(), Pid: 10, Tid: 11, Fd: -1, Ret: 7})
	client.write(5, 5)
	server.read(7, 5)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	b, conn := byKind(sink.events, schema.KindBind)[0], byKind(sink.events, schema.KindConnect)[0]
	if b.Args.Path == nil || *b.Args.Path != "/t/s.sock" || conn.Args.Path == nil || *conn.Args.Path != "/t/s.sock" {
		t.Fatalf("bind args %+v connect args %+v, want the socket path", b.Args, conn.Args)
	}
	a := byKind(sink.events, schema.KindAccept)[0]
	r, w := byKind(sink.events, schema.KindRead)[0], byKind(sink.events, schema.KindWrite)[0]
	if conn.ConnID == "" || a.ConnID != conn.ConnID || r.ConnID != conn.ConnID || w.ConnID != conn.ConnID {
		t.Fatalf("conn ids connect %q accept %q read %q write %q, want one", conn.ConnID, a.ConnID, r.ConnID, w.ConnID)
	}
	if !hasEdge(c.Graph(), schema.EdgeConnected, schema.ProcessNodeID("h1", 10, 1010),
		schema.SocketNodeID(conn.ConnID), schema.StateFused) {
		t.Error("server has no fused connected edge")
	}
}

// TestUnixEndAloneResolves: an end whose other end is never traced gets a
// conn id of its own once the window passed, unresolved.
func TestUnixEndAloneResolves(t *testing.T) {
	c, sink := newTest(t)
	client := feeder{t, c, 20, 21, new(uint64)}
	client.addrCall(types.SYS_ENTER_CONNECT, types.SYS_EXIT_CONNECT, 5, unixAddr("/t/s.sock", 0xC, 0xA), 0)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	conn := byKind(sink.events, schema.KindConnect)[0]
	if conn.ConnID == "" || !hasEdge(c.Graph(), schema.EdgeConnected, schema.ProcessNodeID("h1", 20, 1020),
		schema.SocketNodeID(conn.ConnID), schema.StateUnresolved) {
		t.Fatalf("lone unix end: conn %q, want an unresolved edge", conn.ConnID)
	}
}

// TestUDPLoopbackFlowFuses: a datagram exchange between a socket bound to
// 127.0.0.1 and one bound to the wildcard address carries both addresses
// per datagram (the wildcard as no address) and fuses into one flow.
func TestUDPLoopbackFlowFuses(t *testing.T) {
	c, sink := newTest(t)
	server := feeder{t, c, 10, 11, new(uint64)}
	client := feeder{t, c, 20, 21, server.now}
	client.addrCall(types.SYS_ENTER_SENDTO, types.SYS_EXIT_SENDTO, 5,
		udpAddr("0.0.0.0:40000", "127.0.0.1:9000"), 5)
	server.addrCall(types.SYS_ENTER_RECVFROM, types.SYS_EXIT_RECVFROM, 4,
		udpAddr("127.0.0.1:9000", "127.0.0.1:40000"), 5)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	send, recv := byKind(sink.events, schema.KindSendto)[0], byKind(sink.events, schema.KindRecvfrom)[0]
	if send.Bytes != 5 || send.Args.LocalAddr != nil || *send.Args.LocalPort != 40000 ||
		*send.Args.RemoteAddr != "127.0.0.1" || *send.Args.RemotePort != 9000 {
		t.Fatalf("sendto args %+v bytes %d", send.Args, send.Bytes)
	}
	if *recv.Args.LocalAddr != "127.0.0.1" || *recv.Args.RemotePort != 40000 {
		t.Fatalf("recvfrom args %+v", recv.Args)
	}
	if send.ConnID == "" || send.ConnID != recv.ConnID {
		t.Fatalf("flow ids sendto %q recvfrom %q, want one", send.ConnID, recv.ConnID)
	}
	if !hasEdge(c.Graph(), schema.EdgeConnected, schema.ProcessNodeID("h1", 20, 1020),
		schema.SocketNodeID(send.ConnID), schema.StateFused) {
		t.Error("client has no fused edge to the flow")
	}
}

// TestUDPFlowToElsewhereStaysUnresolved: a wildcard socket sending off
// host cannot be keyed by its real source, so its flow never fuses.
func TestUDPFlowToElsewhereStaysUnresolved(t *testing.T) {
	c, sink := newTest(t)
	client := feeder{t, c, 20, 21, new(uint64)}
	client.addrCall(types.SYS_ENTER_SENDTO, types.SYS_EXIT_SENDTO, 5,
		udpAddr("0.0.0.0:40000", "192.0.2.1:53"), 5)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	send := byKind(sink.events, schema.KindSendto)[0]
	if !hasEdge(c.Graph(), schema.EdgeConnected, schema.ProcessNodeID("h1", 20, 1020),
		schema.SocketNodeID(send.ConnID), schema.StateUnresolved) {
		t.Fatalf("off-host flow %q: want an unresolved edge", send.ConnID)
	}
}

// TestInetBindReportsLocalAddress: an inet bind names the address the
// socket ended up with (the kernel's port for port 0).
func TestInetBindReportsLocalAddress(t *testing.T) {
	c, sink := newTest(t)
	f := feeder{t, c, 10, 11, new(uint64)}
	a := udpAddr("127.0.0.1:41000", "127.0.0.1:0")
	f.addrCall(types.SYS_ENTER_BIND, types.SYS_EXIT_BIND, 4, a, 0)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	b := byKind(sink.events, schema.KindBind)[0]
	if b.Args.LocalPort == nil || *b.Args.LocalPort != 41000 || *b.Args.LocalAddr != "127.0.0.1" {
		t.Fatalf("bind args %+v, want 127.0.0.1:41000", b.Args)
	}
}

// TestSockAddrForAnotherCallIgnored: a record whose call is not the
// pending one changes nothing.
func TestSockAddrForAnotherCallIgnored(t *testing.T) {
	c, sink := newTest(t)
	f := feeder{t, c, 10, 11, new(uint64)}
	f.handle(&types.FdEvent{EventType: types.ENTER_FD_EVENT, TraceId: types.SYS_ENTER_BIND,
		Time: f.tick(), Pid: 10, Tid: 11, Fd: 4})
	a := unixAddr("/x", 1, 0)
	a.EventType, a.TraceId, a.Time, a.Pid, a.Tid = types.SOCK_ADDR_EVENT, types.SYS_EXIT_CONNECT, f.tick(), 10, 11
	f.handle(a)
	f.exit(types.SYS_EXIT_BIND, 0)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if b := byKind(sink.events, schema.KindBind)[0]; b.Args.Path != nil {
		t.Fatalf("bind took a connect's address: %+v", b.Args)
	}
}

// TestEpollCalls: epoll_create1 creates a close-on-exec fd, epoll_ctl
// names the epoll fd, the registered one and the op, and a wait names the
// epoll fd while poll names none.
func TestEpollCalls(t *testing.T) {
	c, sink := newTest(t)
	f := feeder{t, c, 10, 11, new(uint64)}
	f.handle(&types.EventfdEvent{EventType: types.ENTER_EVENTFD_EVENT, TraceId: types.SYS_ENTER_EPOLL_CREATE1,
		Time: f.tick(), Pid: 10, Tid: 11, Flags: unix.EPOLL_CLOEXEC, Ret: -1, Fd: -1})
	f.handle(&types.EventfdEvent{EventType: types.EXIT_EVENTFD_EVENT, TraceId: types.SYS_EXIT_EPOLL_CREATE1,
		Time: f.tick(), Pid: 10, Tid: 11, Flags: unix.EPOLL_CLOEXEC, Ret: 8, Fd: -1})
	f.handle(&types.EpollCtlEvent{EventType: types.ENTER_EPOLL_CTL_EVENT, TraceId: types.SYS_ENTER_EPOLL_CTL,
		Time: f.tick(), Pid: 10, Tid: 11, Epfd: 8, Op: unix.EPOLL_CTL_ADD, Fd: 4})
	f.exit(types.SYS_EXIT_EPOLL_CTL, 0)
	f.handle(&types.PollEvent{EventType: types.ENTER_POLL_EVENT, TraceId: types.SYS_ENTER_EPOLL_WAIT,
		Time: f.tick(), Pid: 10, Tid: 11, Fd: 8})
	f.exit(types.SYS_EXIT_EPOLL_WAIT, 1)
	f.handle(&types.PollEvent{EventType: types.ENTER_POLL_EVENT, TraceId: types.SYS_ENTER_POLL,
		Time: f.tick(), Pid: 10, Tid: 11, Fd: -1})
	f.exit(types.SYS_EXIT_POLL, 0)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	cr := byKind(sink.events, schema.KindEpollCreate1)[0]
	ctl := byKind(sink.events, schema.KindEpollCtl)[0]
	wait, poll := byKind(sink.events, schema.KindEpollWait)[0], byKind(sink.events, schema.KindPoll)[0]
	if cr.FD != 8 || cr.FDGen != 1 || !c.procs[10].cloexec[8] {
		t.Errorf("epoll_create1 fd %d gen %d, want 8 gen 1, close-on-exec", cr.FD, cr.FDGen)
	}
	if ctl.FD != 8 || ctl.FDGen != 1 || *ctl.Args.FD2 != 4 || *ctl.Args.Cmd != unix.EPOLL_CTL_ADD {
		t.Errorf("epoll_ctl fd %d gen %d args %+v", ctl.FD, ctl.FDGen, ctl.Args)
	}
	if wait.FD != 8 || wait.Ret != 1 || poll.FD != schema.NoFD {
		t.Errorf("epoll_wait fd %d ret %d, poll fd %d", wait.FD, wait.Ret, poll.FD)
	}
}

// TestZeroCopyBytes: a zero-copy call reports what moved on its first fd.
func TestZeroCopyBytes(t *testing.T) {
	c, sink := newTest(t)
	f := feeder{t, c, 10, 11, new(uint64)}
	f.fdCall(types.SYS_ENTER_SENDFILE64, types.SYS_EXIT_SENDFILE64, 6, 4096)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if e := byKind(sink.events, schema.KindSendfile64)[0]; e.FD != 6 || e.Bytes != 4096 {
		t.Fatalf("sendfile64 fd %d bytes %d", e.FD, e.Bytes)
	}
}
