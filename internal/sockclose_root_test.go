package internal

import (
	"io"
	"net"
	"net/netip"
	"os"
	"syscall"
	"testing"
	"time"

	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// The record federate falls back on when a connection's ESTABLISHED
// record is lost (task qf4): a skipped inet_sock_set_state run —
// trace_call_bpf's bpf_prog_active guard drops a softirq transition
// while another tracing program runs on the CPU — leaves a client half
// with only SYN_SENT and a later record, often its CLOSE. After
// ESTABLISHED, snd_una − bytes_acked and rcv_nxt − bytes_received stay
// constant (tcp_snd_una_update and tcp_rcv_nxt_update move each pair
// together, FIN included), so every later record must rebuild the same
// ISN pair — also on an RST abort, where snd_una need not be final, and
// on the end that receives the RST (tcp_reset's CLOSE, in softirq).
// TestSockSeqFields covers the FIN close; this file covers the aborts.
// Every client record from ESTABLISHED on must also count its own SYN
// as acked (bytes_acked ≥ 1), the evidence federate requires before a
// client's CLOSE may key a half that has no other keyed record.

// abortCase is one way a connection ends in an RST.
type abortCase struct {
	name  string
	abort func(t *testing.T, p *loopbackPair)
}

// TestSockSeqAbortClose: the client's and the server's every record from
// ESTABLISHED on, CLOSE included, rebuilds the client's ESTABLISHED key
// when the connection is reset: by the client after a complete exchange,
// by the client with data still queued against a full receive window,
// and by the server, whose RST the client's CLOSE then answers.
func TestSockSeqAbortClose(t *testing.T) {
	requireRoot(t)
	tr := newSockTracer(t)
	tr.setTarget(t, uint32(os.Getpid()), true)
	cases := []abortCase{
		{"client RST after exchange", func(t *testing.T, p *loopbackPair) {
			exchange(t, p)
			lingerZero(t, p.client)
			p.close()
		}},
		{"client RST with data queued", func(t *testing.T, p *loopbackPair) {
			exchange(t, p)
			fillWindow(t, p)
			lingerZero(t, p.client)
			p.close()
		}},
		{"server RST", func(t *testing.T, p *loopbackPair) {
			exchange(t, p)
			lingerZero(t, p.server)
			_ = p.server.Close()
			// The client reads the RST before its own close.
			_ = p.client.SetReadDeadline(time.Now().Add(2 * time.Second))
			_, _ = io.ReadAll(p.client)
			_ = p.client.Close()
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := dialLoopback(t)
			c.abort(t, p)
			recs := tr.collectUntilClosed(t, p)
			checkAbortRecords(t, recs)
		})
	}
}

// checkAbortRecords requires a CLOSE record on each end, every record
// from ESTABLISHED on rebuilding the client's ESTABLISHED key, and every
// such client record counting the client's SYN as acked.
func checkAbortRecords(t *testing.T, recs seqRecords) {
	t.Helper()
	want := clientKey(t, recs.client)
	for _, side := range []struct {
		name   string
		evs    []*types.TcpStateEvent
		server bool
	}{{"client", recs.client, false}, {"server", recs.server, true}} {
		closed := false
		for _, ev := range side.evs {
			checkKey(t, side.name, ev, want, side.server)
			if ev.Newstate == tcpClose {
				closed = true
				t.Logf("%s %d -> CLOSE: acked %d received %d rebuilds %+v",
					side.name, ev.Oldstate, ev.BytesAcked, ev.BytesReceived,
					rebuild(ev, side.server))
			}
			if !side.server && ev.Newstate != tcpSynSent && ev.BytesAcked < 1 {
				t.Errorf("client %d -> %d: bytes_acked %d, want the SYN counted",
					ev.Oldstate, ev.Newstate, ev.BytesAcked)
			}
		}
		if !closed {
			t.Errorf("%s: no CLOSE record", side.name)
		}
	}
}

// lingerZero makes c's close send an RST instead of a FIN.
func lingerZero(t *testing.T, c interface{ SetLinger(int) error }) {
	t.Helper()
	if err := c.SetLinger(0); err != nil {
		t.Fatalf("set linger: %v", err)
	}
}

// fillWindow writes from the client until the server, which reads
// nothing, has its receive buffer full and the client's send queue
// holds data the peer has not taken: the abort then happens with
// snd_una short of write_seq.
func fillWindow(t *testing.T, p *loopbackPair) {
	t.Helper()
	if err := p.server.SetReadBuffer(4096); err != nil {
		t.Fatalf("set read buffer: %v", err)
	}
	buf := make([]byte, 1<<16)
	_ = p.client.SetWriteDeadline(time.Now().Add(300 * time.Millisecond))
	total := 0
	for {
		n, err := p.client.Write(buf)
		total += n
		if err != nil {
			break
		}
	}
	if total == 0 {
		t.Fatal("client wrote nothing before the window closed")
	}
}

// TestSockDualStackBoundConnect: a dual-stack AF_INET6 socket bound to
// [::]:0 (the JVM's bind(null)) that connects to an IPv4 listener reports
// its tuple from the real kernel as it is before and after the address is
// mapped, and the correlator keeps every record of it on one connection,
// fused with the accepted end (task qf4, review B1: a record whose tuple
// merely completes the socket's is no other life).
func TestSockDualStackBoundConnect(t *testing.T) {
	requireRoot(t)
	tr := newSockTracer(t)
	tr.setTarget(t, uint32(os.Getpid()), true)
	ln, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	accepted := make(chan *net.TCPConn, 1)
	go func() {
		c, _ := ln.AcceptTCP()
		accepted <- c
	}()
	sport := uint32(ln.Addr().(*net.TCPAddr).Port)
	cport := dualStackConnect(t, sport)
	if s := <-accepted; s != nil {
		_ = s.Close()
	}
	recs := tr.collectPair(t, cport, sport)
	sawUnspecified := false
	for _, r := range recs {
		t.Logf("%d -> %d family %d saddr %v:%d daddr %v:%d", r.Oldstate, r.Newstate,
			r.Family, r.Saddr, r.Sport, r.Daddr, r.Dport)
		sawUnspecified = sawUnspecified || (r.Sport == cport && netip.AddrFrom16(r.Saddr).Unmap().IsUnspecified())
	}
	t.Logf("unspecified local address reported: %v", sawUnspecified)
	conns := map[string]bool{}
	for _, e := range correlateRecords(t, recs) {
		if e.Event == schema.KindTCPState {
			conns[e.ConnID] = true
		}
	}
	if len(conns) != 1 || conns[""] {
		t.Errorf("tcp_state events on conns %v, want one fused conn", conns)
	}
}

// collectPair gathers the records of both ends of the connection between
// ports a and b until both reported TCP_CLOSE.
func (tr *sockTracer) collectPair(t *testing.T, a, b uint32) []*types.TcpStateEvent {
	t.Helper()
	var recs []*types.TcpStateEvent
	closed := 0
	tr.waitTCPState(5*time.Second, func(ev *types.TcpStateEvent) bool {
		if (ev.Sport != a || ev.Dport != b) && (ev.Sport != b || ev.Dport != a) {
			return false
		}
		recs = append(recs, ev)
		if ev.Newstate == tcpClose {
			closed++
		}
		return closed == 2
	})
	if closed != 2 {
		t.Fatalf("saw %d of the two CLOSE records; records %+v", closed, recs)
	}
	return recs
}

// dualStackConnect opens an AF_INET6 socket, binds it to [::]:0, connects
// it to ::ffff:127.0.0.1:port, closes it, and returns its local port.
func dualStackConnect(t *testing.T, port uint32) uint32 {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_INET6, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("socket: %v", err)
	}
	defer func() { _ = syscall.Close(fd) }()
	if err := syscall.SetsockoptInt(fd, syscall.IPPROTO_IPV6, syscall.IPV6_V6ONLY, 0); err != nil {
		t.Fatalf("clear IPV6_V6ONLY: %v", err)
	}
	if err := syscall.Bind(fd, &syscall.SockaddrInet6{}); err != nil {
		t.Fatalf("bind [::]:0: %v", err)
	}
	sa, err := syscall.Getsockname(fd)
	if err != nil {
		t.Fatalf("getsockname: %v", err)
	}
	local := uint32(sa.(*syscall.SockaddrInet6).Port)
	peer := &syscall.SockaddrInet6{Port: int(port), Addr: [16]byte{10: 0xff, 11: 0xff, 12: 127, 15: 1}}
	if err := syscall.Connect(fd, peer); err != nil {
		t.Fatalf("connect to ::ffff:127.0.0.1:%d: %v", port, err)
	}
	return local
}
