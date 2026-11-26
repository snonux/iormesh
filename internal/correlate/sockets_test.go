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

// TestInitialSnapshotFusion (decision #35, task cm4): a loopback
// connection both of whose ends were open before the session — no
// connect, no accept, only the tracer's two initial snapshots — fuses
// into one conn id, which the ends' later reads and writes carry; each
// snapshot is a tcp_state event marked with its role, bound to the fd
// it was found at as an fd that predates the trace (gen 0), and the
// graph has a fused connected edge per process. A snapshot whose reads
// tore carries none of the four fields, and a write on an fd no record
// bound names no connection.
func TestInitialSnapshotFusion(t *testing.T) {
	c, sink := newTest(t)
	cnow, snow := uint64(100), uint64(100)
	client := feeder{t, c, 100, 101, &cnow}
	server := feeder{t, c, 200, 201, &snow}
	ci := tcp(client.tick(), 0xA, 100, 6, types.TCP_STATE_INITIAL_CLIENT, tcpEstablished, cli, srv)
	ci.SndUna, ci.RcvNxt, ci.BytesAcked, ci.BytesReceived = 0x1006, 0xF0000008, 6, 7
	client.handle(ci)
	snow = cnow
	si := tcp(server.tick(), 0xB, 200, 7, types.TCP_STATE_INITIAL_SERVER, tcpEstablished, srv, cli)
	si.SeqStatus = types.TCP_SEQ_TORN
	server.handle(si)
	client.write(6, 5)
	server.read(7, 5)
	server.write(9, 5) // an fd no record bound
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	states := byKind(sink.events, schema.KindTCPState)
	if len(states) != 2 {
		t.Fatalf("%d tcp_state events, want the two initial snapshots", len(states))
	}
	id := states[0].ConnID
	for i, want := range []string{schema.SockSnapshotInitialClient, schema.SockSnapshotInitialServer} {
		e := states[i]
		if e.Args.SockSnapshot == nil || *e.Args.SockSnapshot != want || e.ConnID == "" || e.ConnID != id ||
			e.FDGen != 0 {
			t.Errorf("snapshot %d: mark %v conn %q gen %d, want %s on one conn, gen 0", i,
				e.Args.SockSnapshot, e.ConnID, e.FDGen, want)
		}
		if err := e.Validate(); err != nil {
			t.Errorf("snapshot %d invalid: %v", i, err)
		}
	}
	if states[0].Args.SndUna == nil || *states[0].Args.BytesAcked != 6 || states[1].Args.SndUna != nil {
		t.Errorf("fields: client %+v, torn server %+v; want the client's only", states[0].Args, states[1].Args)
	}
	for _, e := range append(byKind(sink.events, schema.KindWrite), byKind(sink.events, schema.KindRead)...) {
		want := id
		if e.FD == 9 {
			want = ""
		}
		if e.ConnID != want {
			t.Errorf("%s on fd %d: conn %q, want %q", e.Event, e.FD, e.ConnID, want)
		}
	}
	g := c.Graph()
	for _, pid := range []uint32{100, 200} {
		if !hasEdge(g, schema.EdgeConnected, schema.ProcessNodeID("h1", pid, 1000+uint64(pid)),
			schema.SocketNodeID(id), schema.StateFused) {
			t.Errorf("no fused connected edge for pid %d: %+v", pid, g.Edges)
		}
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

// TestLostCloseThenAddressReuse: a server end's TCP_CLOSE record is lost
// (a skipped run), and the next child the kernel allocates at the same
// struct sock address is accepted on the same fd number. Its 4-tuple
// contradicts the old socket's, so it is a new socket with a conn id of
// its own, not a second connection under the old one (task qf4: M4-9's
// inventory end on Rocky 9.9, whose mixed conn the merge could only call
// a key conflict). A record of the old life with the old tuple stays on
// the old conn.
func TestLostCloseThenAddressReuse(t *testing.T) {
	const peerA, peerB, local = "10.0.0.2:41000", "10.0.0.3:42000", "10.0.0.1:8080"
	c, sink := newTest(t)
	now := uint64(100)
	server := feeder{t, c, 200, 201, &now}
	server.handle(tcp(server.tick(), 0xB, 200, 7, types.TCP_STATE_SNAPSHOT, tcpEstablished, local, peerA))
	server.accept4(0, 7)
	server.read(7, 5)
	server.handle(tcp(server.tick(), 0xB, 200, 7, tcpEstablished, 8, local, peerA)) // CLOSE_WAIT
	server.close(7)
	server.handle(tcp(server.tick(), 0xB, 200, 7, 8, 9, local, peerA)) // LAST_ACK; its CLOSE is lost
	server.handle(tcp(server.tick(), 0xB, 200, 7, types.TCP_STATE_SNAPSHOT, tcpEstablished, local, peerB))
	server.accept4(0, 7)
	server.read(7, 6)
	server.handle(tcp(server.tick(), 0xB, 200, 7, tcpEstablished, tcpClose, local, peerB))
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	states, reads := byKind(sink.events, schema.KindTCPState), byKind(sink.events, schema.KindRead)
	if len(states) != 5 || len(reads) != 2 {
		t.Fatalf("got %d tcp_state and %d read events, want 5 and 2", len(states), len(reads))
	}
	first, second := states[0].ConnID, states[3].ConnID
	if first == "" || second == "" || first == second {
		t.Fatalf("conn ids %q and %q, want two distinct ones", first, second)
	}
	for i, want := range []string{first, first, first, second, second} {
		if states[i].ConnID != want {
			t.Errorf("tcp_state %d (%s) on %q, want %q", i, *states[i].Args.SockState, states[i].ConnID, want)
		}
	}
	if reads[0].ConnID != first || reads[1].ConnID != second {
		t.Errorf("reads on %q and %q, want %q and %q", reads[0].ConnID, reads[1].ConnID, first, second)
	}
	if states[3].FDGen != 2 || states[4].FDGen != 2 {
		t.Errorf("new life's records on fd gens %d and %d, want the second accept's 2",
			states[3].FDGen, states[4].FDGen)
	}
}

// TestStaleRecordOfNewLifeKeepsFdBinding: the new life's first record is a
// state change that went out under the stale owner (sock.c's residual
// risk: SYN_RECV -> ESTABLISHED before accept). It starts a new socket but
// binds nothing and is not emitted, since its pid and fd are the old
// owner's: the old fd keeps its connection, and the accept snapshot that
// follows on another fd joins the new socket.
func TestStaleRecordOfNewLifeKeepsFdBinding(t *testing.T) {
	const peerA, peerB, local = "10.0.0.2:41000", "10.0.0.3:42000", "10.0.0.1:8080"
	c, sink := newTest(t)
	now := uint64(100)
	server := feeder{t, c, 200, 201, &now}
	server.handle(tcp(server.tick(), 0xB, 200, 7, types.TCP_STATE_SNAPSHOT, tcpEstablished, local, peerA))
	server.accept4(0, 7)
	server.handle(tcp(server.tick(), 0xB, 200, 7, tcpSynRecv, tcpEstablished, local, peerB))
	server.read(7, 5)
	server.handle(tcp(server.tick(), 0xB, 200, 8, types.TCP_STATE_SNAPSHOT, tcpEstablished, local, peerB))
	server.accept4(0, 8)
	server.read(8, 6)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	states, reads := byKind(sink.events, schema.KindTCPState), byKind(sink.events, schema.KindRead)
	if len(states) != 2 || len(reads) != 2 {
		t.Fatalf("got %d tcp_state and %d read events, want 2 (the stale one dropped) and 2",
			len(states), len(reads))
	}
	if states[1].ConnID == states[0].ConnID || states[1].FD != 8 {
		t.Errorf("second snapshot %+v on the old conn %q, want a new conn on fd 8", states[1], states[0].ConnID)
	}
	if reads[0].ConnID != states[0].ConnID || reads[1].ConnID != states[1].ConnID {
		t.Errorf("reads on %q and %q, want the old conn %q and the new %q",
			reads[0].ConnID, reads[1].ConnID, states[0].ConnID, states[1].ConnID)
	}
}

// TestDualStackBoundConnectSameLife: a dual-stack AF_INET6 socket bound
// to [::]:P (the JVM's bind(null)) that connects to an IPv4 peer reports
// [::]:P at SYN_SENT and its real address from ESTABLISHED on. The
// unspecified address is no complete tuple, so it stays one socket and
// one conn (review B1 of task qf4).
func TestDualStackBoundConnectSameLife(t *testing.T) {
	const local, peer = "10.0.0.1:40000", "10.0.0.2:80"
	c, sink := newTest(t)
	now := uint64(100)
	client := feeder{t, c, 100, 101, &now}
	client.handle(&types.FdEvent{EventType: types.ENTER_FD_EVENT, TraceId: types.SYS_ENTER_CONNECT,
		Time: client.tick(), Pid: 100, Tid: 101, Fd: 6})
	syn := tcp6(client.tick(), 0xA, 100, 6, tcpClose, synSent, local, peer)
	syn.Saddr = [16]byte{} // [::]:40000, as the kernel reports it
	client.handle(syn)
	client.handle(tcp6(client.tick(), 0xA, 100, 6, synSent, tcpEstablished, local, peer))
	client.exit(types.SYS_EXIT_CONNECT, 0)
	client.write(6, 5)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	conn := byKind(sink.events, schema.KindConnect)[0]
	states, writes := byKind(sink.events, schema.KindTCPState), byKind(sink.events, schema.KindWrite)
	if len(states) != 2 || len(writes) != 1 {
		t.Fatalf("got %d tcp_state and %d write events, want 2 and 1", len(states), len(writes))
	}
	for _, e := range append(states, writes[0]) {
		if e.ConnID == "" || e.ConnID != conn.ConnID {
			t.Errorf("%s on %q, want the connect's conn %q", e.Event, e.ConnID, conn.ConnID)
		}
	}
}

// TestLocalAddressContradictsSplits: with the remote end the same, a
// complete local address that differs (a server bound to the wildcard
// accepting on two of its addresses at one reused struct sock, the first
// child's CLOSE lost) is another life all the same.
func TestLocalAddressContradictsSplits(t *testing.T) {
	const peer, localA, localB = "10.0.0.2:41000", "10.0.0.1:8080", "10.0.1.1:8080"
	c, sink := newTest(t)
	now := uint64(100)
	server := feeder{t, c, 200, 201, &now}
	server.handle(tcp(server.tick(), 0xB, 200, 7, types.TCP_STATE_SNAPSHOT, tcpEstablished, localA, peer))
	server.accept4(0, 7)
	server.close(7)
	server.handle(tcp(server.tick(), 0xB, 200, 7, types.TCP_STATE_SNAPSHOT, tcpEstablished, localB, peer))
	server.accept4(0, 7)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	states := byKind(sink.events, schema.KindTCPState)
	if len(states) != 2 || states[0].ConnID == states[1].ConnID {
		t.Fatalf("tcp_state events %+v, want two on two conns", states)
	}
}

// TestTransitionOutOfListenStartsLife: a transition out of LISTEN (an
// accepted child's LISTEN -> SYN_RECV) begins a socket's life even when
// its tuple matches the old socket's, so it binds its own fd.
func TestTransitionOutOfListenStartsLife(t *testing.T) {
	const peer, local = "10.0.0.2:41000", "10.0.0.1:8080"
	c, sink := newTest(t)
	now := uint64(100)
	server := feeder{t, c, 200, 201, &now}
	server.handle(tcp(server.tick(), 0xB, 200, 7, types.TCP_STATE_SNAPSHOT, tcpEstablished, local, peer))
	server.accept4(0, 7)
	server.handle(tcp(server.tick(), 0xB, 200, 9, tcpListen, tcpSynRecv, local, peer))
	server.read(9, 4)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	states, reads := byKind(sink.events, schema.KindTCPState), byKind(sink.events, schema.KindRead)
	if len(states) != 2 || len(reads) != 1 {
		t.Fatalf("got %d tcp_state and %d read events, want 2 and 1", len(states), len(reads))
	}
	if states[1].ConnID == states[0].ConnID || reads[0].ConnID != states[1].ConnID {
		t.Errorf("new life on %q, its read on %q, old conn %q: want the new life and its read on one new conn",
			states[1].ConnID, reads[0].ConnID, states[0].ConnID)
	}
}

// TestClientReconnectAfterLostClose:a client socket's CLOSE is lost and
// the kernel reuses its address for the next connect on another fd. That
// connect's CLOSE -> SYN_SENT begins a life (as the tracer's owner check
// knows), so it starts a new socket and binds the new fd even before the
// tuple is complete: each fd's data stays on its own connection (review
// N2 of task qf4).
func TestClientReconnectAfterLostClose(t *testing.T) {
	const cliA, cliB, srvA, srvB = "10.0.0.1:40000", "10.0.0.1:40001", "10.0.0.2:80", "10.0.0.3:80"
	c, sink := newTest(t)
	now := uint64(100)
	client := feeder{t, c, 100, 101, &now}
	connect := func(fd int32, local, remote string) {
		client.handle(&types.FdEvent{EventType: types.ENTER_FD_EVENT, TraceId: types.SYS_ENTER_CONNECT,
			Time: client.tick(), Pid: 100, Tid: 101, Fd: fd})
		client.handle(tcp(client.tick(), 0xA, 100, fd, tcpClose, synSent, "10.0.0.1:0", remote))
		client.handle(tcp(client.tick(), 0xA, 100, fd, synSent, tcpEstablished, local, remote))
		client.exit(types.SYS_EXIT_CONNECT, 0)
	}
	connect(6, cliA, srvA)
	client.write(6, 5) // its CLOSE is lost
	connect(8, cliB, srvB)
	client.write(8, 6)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	conns, writes := byKind(sink.events, schema.KindConnect), byKind(sink.events, schema.KindWrite)
	if len(conns) != 2 || len(writes) != 2 {
		t.Fatalf("got %d connects and %d writes, want 2 and 2", len(conns), len(writes))
	}
	if conns[0].ConnID == conns[1].ConnID {
		t.Fatalf("both connects on conn %q, want two", conns[0].ConnID)
	}
	for i := range 2 {
		if writes[i].ConnID != conns[i].ConnID {
			t.Errorf("write on fd %d on %q, want its connect's %q", writes[i].FD, writes[i].ConnID, conns[i].ConnID)
		}
	}
	for _, e := range byKind(sink.events, schema.KindTCPState) {
		want := conns[0].ConnID
		if e.FD == 8 {
			want = conns[1].ConnID
		}
		if e.ConnID != want {
			t.Errorf("%s on fd %d on %q, want %q", *e.Args.SockState, e.FD, e.ConnID, want)
		}
	}
}

// TestIncompleteTupleSameLife: a record without a remote port (a
// listener's, or one read before the peer was set) proves no other life,
// and neither does a complete one after it: all stay on the socket's conn.
func TestIncompleteTupleSameLife(t *testing.T) {
	const peer, local, unset = "10.0.0.2:41000", "10.0.0.1:8080", "0.0.0.0:0"
	c, sink := newTest(t)
	now := uint64(100)
	server := feeder{t, c, 200, 201, &now}
	server.handle(tcp(server.tick(), 0xB, 200, 7, types.TCP_STATE_SNAPSHOT, tcpEstablished, local, peer))
	server.accept4(0, 7)
	server.handle(tcp(server.tick(), 0xB, 200, 7, tcpEstablished, 8, local, unset))
	server.handle(tcp(server.tick(), 0xB, 200, 7, 8, tcpClose, local, peer))
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	states := byKind(sink.events, schema.KindTCPState)
	if len(states) != 3 || states[0].ConnID == "" || states[1].ConnID != states[0].ConnID ||
		states[2].ConnID != states[0].ConnID {
		t.Fatalf("tcp_state events %+v, want three on one conn", states)
	}
}
