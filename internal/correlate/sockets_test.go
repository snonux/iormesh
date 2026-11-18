package correlate

import (
	"net/netip"
	"testing"

	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// tcp builds a tcp_state record for an IPv4 socket.
func tcp(at, sk uint64, pid uint32, fd, old, new int32, local, remote string) *types.TcpStateEvent {
	r := &types.TcpStateEvent{EventType: types.TCP_STATE_EVENT, Time: at, Sk: sk, Pid: pid, Fd: fd,
		Oldstate: old, Newstate: new, Family: afInet}
	l, rm := netip.MustParseAddrPort(local), netip.MustParseAddrPort(remote)
	a4, b4 := l.Addr().As4(), rm.Addr().As4()
	copy(r.Saddr[:], a4[:])
	copy(r.Daddr[:], b4[:])
	r.Sport, r.Dport = uint32(l.Port()), uint32(rm.Port())
	return r
}

// tcp6 builds the record a dual-stack (JVM) socket reports: AF_INET6 with
// IPv4-mapped addresses (::ffff:a.b.c.d) for IPv4 peers.
func tcp6(at, sk uint64, pid uint32, fd, old, new int32, local, remote string) *types.TcpStateEvent {
	r := tcp(at, sk, pid, fd, old, new, local, remote)
	l, rm := netip.MustParseAddrPort(local), netip.MustParseAddrPort(remote)
	r.Family = afInet6
	r.Saddr = netip.AddrFrom16(l.Addr().As16()).As16()
	r.Daddr = netip.AddrFrom16(rm.Addr().As16()).As16()
	return r
}

const (
	synSent = 2
	cli     = "127.0.0.1:40000"
	srv     = "127.0.0.1:8080"
)

// loopback feeds a client connect (pid 100, fd 6) and a server accept
// (pid 200, fd 7) whose sockets mirror each other, established serverAt
// ns after the client, then one write on each side.
func loopback(t *testing.T, c *Correlator, serverAt uint64) {
	cnow, snow := uint64(100), uint64(100)
	client := feeder{t, c, 100, 101, &cnow}
	server := feeder{t, c, 200, 201, &snow}
	client.handle(&types.FdEvent{EventType: types.ENTER_FD_EVENT, TraceId: types.SYS_ENTER_CONNECT,
		Time: client.tick(), Pid: 100, Tid: 101, Fd: 6})
	client.handle(tcp(client.tick(), 0xA, 100, 6, tcpClose, synSent, "127.0.0.1:0", srv))
	client.handle(tcp(client.tick(), 0xA, 100, 6, synSent, tcpEstablished, cli, srv))
	client.exit(types.SYS_EXIT_CONNECT, 0)
	snow = cnow + serverAt
	server.handle(&types.AcceptEvent{EventType: types.ENTER_ACCEPT_EVENT, TraceId: types.SYS_ENTER_ACCEPT,
		Time: server.tick(), Pid: 200, Tid: 201, Fd: 3})
	server.handle(tcp(server.tick(), 0xB, 200, 7, types.TCP_STATE_SNAPSHOT, tcpEstablished, srv, cli))
	server.handle(&types.AcceptEvent{EventType: types.EXIT_ACCEPT_EVENT, TraceId: types.SYS_EXIT_ACCEPT,
		Time: server.tick(), Pid: 200, Tid: 201, Fd: -1, Ret: 7})
	client.write(6, 5)
	server.write(7, 5)
}

func TestLoopbackFusion(t *testing.T) {
	c, sink := newTest(t)
	loopback(t, c, 1000)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	conn := byKind(sink.events, schema.KindConnect)[0]
	acc := byKind(sink.events, schema.KindAccept)[0]
	if conn.ConnID == "" || conn.ConnID != acc.ConnID {
		t.Fatalf("connect conn_id %q, accept %q: want one fused id", conn.ConnID, acc.ConnID)
	}
	if *conn.Args.RemotePort != 8080 || *acc.Args.LocalPort != 8080 || *acc.Args.RemotePort != 40000 {
		t.Fatalf("connect args %+v accept args %+v", conn.Args, acc.Args)
	}
	if acc.FD != 7 || acc.FDGen != 1 {
		t.Fatalf("accept fd %d gen %d, want 7 gen 1", acc.FD, acc.FDGen)
	}
	for _, e := range byKind(sink.events, schema.KindWrite) {
		if e.ConnID != conn.ConnID {
			t.Fatalf("write %+v lacks the conn id", e)
		}
	}
	g := c.Graph()
	sock := schema.SocketNodeID(conn.ConnID)
	for _, pid := range []uint32{100, 200} {
		if !hasEdge(g, schema.EdgeConnected, schema.ProcessNodeID("h1", pid, 1000+uint64(pid)), sock, schema.StateFused) {
			t.Fatalf("no fused connected edge for pid %d: %+v", pid, g.Edges)
		}
	}
	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestLoopbackHeldUntilFused: the client's socket events are held until
// the server's end arrives, so they are emitted with the fused id rather
// than an early guess.
func TestLoopbackHeldUntilFused(t *testing.T) {
	c, sink := newTest(t)
	now := uint64(100)
	client := feeder{t, c, 100, 101, &now}
	client.handle(&types.FdEvent{EventType: types.ENTER_FD_EVENT, TraceId: types.SYS_ENTER_CONNECT,
		Time: client.tick(), Pid: 100, Tid: 101, Fd: 6})
	client.handle(tcp(client.tick(), 0xA, 100, 6, synSent, tcpEstablished, cli, srv))
	client.exit(types.SYS_EXIT_CONNECT, 0)
	client.open("/tmp/x", 0, 9) // not a socket event, but queued behind one
	if len(sink.events) != 0 {
		t.Fatalf("emitted %d events before the peer arrived; want all held", len(sink.events))
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if len(sink.events) != 3 {
		t.Fatalf("%d events after Close, want 3", len(sink.events))
	}
}

// TestUnfusedBeyondWindow: ends established further apart than the window
// are two unresolved connections, never one guessed fusion.
func TestUnfusedBeyondWindow(t *testing.T) {
	c, sink := newTest(t)
	loopback(t, c, 2*defaultFuseWindowNS)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	conn := byKind(sink.events, schema.KindConnect)[0]
	acc := byKind(sink.events, schema.KindAccept)[0]
	if conn.ConnID == acc.ConnID || conn.ConnID == "" || acc.ConnID == "" {
		t.Fatalf("connect %q accept %q: want two distinct unresolved ids", conn.ConnID, acc.ConnID)
	}
	if !hasEdge(c.Graph(), schema.EdgeConnected, schema.ProcessNodeID("h1", 100, 1100),
		schema.SocketNodeID(conn.ConnID), schema.StateUnresolved) {
		t.Fatalf("client edge not unresolved: %+v", c.Graph().Edges)
	}
}

// TestWindowReleasesWithoutClose: a later record past the deadline
// releases an unfused socket's events; nothing waits for Close.
func TestWindowReleasesWithoutClose(t *testing.T) {
	c, sink := newTest(t)
	now := uint64(100)
	client := feeder{t, c, 100, 101, &now}
	client.handle(tcp(client.tick(), 0xA, 100, 6, synSent, tcpEstablished, cli, "10.0.0.9:443"))
	now += defaultFuseWindowNS + 1
	client.read(3, 1)
	if len(sink.events) != 2 || sink.events[0].ConnID == "" {
		t.Fatalf("events %+v: want the tcp_state released unresolved and the read", sink.events)
	}
}

// TestCloseThenReuse: after close(2) the fd number is reused by an open.
// The late TCP_CLOSE record must not re-bind it, and the open's fd is no
// socket.
func TestCloseThenReuse(t *testing.T) {
	c, sink := newTest(t)
	loopback(t, c, 1000)
	now := uint64(1_000_000)
	client := feeder{t, c, 100, 101, &now}
	client.close(6)
	client.open("/tmp/after", 0, 6)
	client.handle(tcp(client.tick(), 0xA, 100, 6, tcpEstablished, tcpClose, cli, srv))
	client.read(6, 3)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	reads := byKind(sink.events, schema.KindRead)
	last := reads[len(reads)-1]
	// fd 6 came from an untraced socket() (gen 0); the open reusing it
	// is gen 1.
	if last.ConnID != "" || last.FDGen != 1 {
		t.Fatalf("read after reuse %+v: want a file read on gen 1, no conn id", last)
	}
	states := byKind(sink.events, schema.KindTCPState)
	if closeEv := states[len(states)-1]; *closeEv.Args.SockState != "CLOSE" || closeEv.FDGen != 0 {
		t.Fatalf("late TCP_CLOSE %+v: want reported on the closed socket's instance (gen 0)", closeEv)
	}
}

// TestDualStackLoopback: both ends as the JVM reports them (AF_INET6,
// ::ffff:127.0.0.1) fuse, and args carry the unmapped address.
func TestDualStackLoopback(t *testing.T) {
	c, sink := newTest(t)
	cnow, snow := uint64(100), uint64(200)
	client := feeder{t, c, 100, 101, &cnow}
	server := feeder{t, c, 200, 201, &snow}
	client.handle(&types.FdEvent{EventType: types.ENTER_FD_EVENT, TraceId: types.SYS_ENTER_CONNECT,
		Time: client.tick(), Pid: 100, Tid: 101, Fd: 6})
	client.handle(tcp6(client.tick(), 0xA, 100, 6, synSent, tcpEstablished, cli, srv))
	client.exit(types.SYS_EXIT_CONNECT, 0)
	server.handle(tcp6(server.tick(), 0xB, 200, 7, types.TCP_STATE_SNAPSHOT, tcpEstablished, srv, cli))
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	conn := byKind(sink.events, schema.KindConnect)[0]
	states := byKind(sink.events, schema.KindTCPState)
	if conn.ConnID == "" || conn.ConnID != states[len(states)-1].ConnID {
		t.Fatalf("dual-stack ends not fused: connect %q server %q", conn.ConnID, states[len(states)-1].ConnID)
	}
	if *conn.Args.RemoteAddr != "127.0.0.1" {
		t.Fatalf("remote addr %q, want the unmapped 127.0.0.1", *conn.Args.RemoteAddr)
	}
}

// TestResetKeepsConnID: after an RST (ESTABLISHED -> CLOSE while the fd is
// open) the fd's remaining events still carry the connection's id.
func TestResetKeepsConnID(t *testing.T) {
	c, sink := newTest(t)
	loopback(t, c, 1000)
	now := uint64(1_000_000)
	client := feeder{t, c, 100, 101, &now}
	client.handle(tcp(client.tick(), 0xA, 100, 6, tcpEstablished, tcpClose, cli, srv))
	client.read(6, -104) // ECONNRESET
	client.close(6)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	want := byKind(sink.events, schema.KindConnect)[0].ConnID
	for _, kind := range []string{schema.KindRead, schema.KindClose} {
		evs := byKind(sink.events, kind)
		if got := evs[len(evs)-1]; got.ConnID != want {
			t.Fatalf("%s after RST has conn_id %q, want %q", kind, got.ConnID, want)
		}
	}
}

// TestSnapshotCloseWaitFuses: a server end whose peer already sent FIN is
// snapshotted in CLOSE_WAIT; that still counts as established.
func TestSnapshotCloseWaitFuses(t *testing.T) {
	c, sink := newTest(t)
	cnow, snow := uint64(100), uint64(200)
	client := feeder{t, c, 100, 101, &cnow}
	server := feeder{t, c, 200, 201, &snow}
	client.handle(tcp(client.tick(), 0xA, 100, 6, synSent, tcpEstablished, cli, srv))
	server.handle(tcp(server.tick(), 0xB, 200, 7, types.TCP_STATE_SNAPSHOT, 8, srv, cli)) // CLOSE_WAIT
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if sink.events[0].ConnID != sink.events[1].ConnID {
		t.Fatalf("CLOSE_WAIT snapshot not fused: %q vs %q", sink.events[0].ConnID, sink.events[1].ConnID)
	}
}

// TestRefusedConnectsStayApart: two refused connects on one tuple never
// established; their ids must still differ, and resolving the first must
// not evict the second from the join index.
func TestRefusedConnectsStayApart(t *testing.T) {
	c, sink := newTest(t)
	now := uint64(100)
	f := feeder{t, c, 100, 101, &now}
	f.handle(tcp(f.tick(), 0xA, 100, 6, tcpClose, synSent, cli, srv))
	f.handle(tcp(f.tick(), 0xA, 100, 6, synSent, tcpClose, cli, srv))
	now += defaultFuseWindowNS
	f.handle(tcp(f.tick(), 0xC, 100, 6, tcpClose, synSent, cli, srv))
	f.handle(tcp(f.tick(), 0xC, 100, 6, synSent, tcpClose, cli, srv))
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if sink.events[0].ConnID == sink.events[2].ConnID {
		t.Fatalf("two refused connects share conn_id %q", sink.events[0].ConnID)
	}
}
