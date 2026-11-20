package internal

import (
	"io"
	"os"
	"testing"
	"time"

	"iormesh/internal/types"
)

// The M4 tcp_sock fields of tcp_state_event (snd_una, rcv_nxt,
// bytes_acked, bytes_received; decision #26), read from the real kernel:
// a loopback connection of this process exchanges a known number of bytes
// each way, and every record of both ends must hold the counters that
// exchange implies and rebuild one ISN pair (the S2 formulas,
// docs/spikes/s2-cross-host.md). Root only, like sock_root_test.go, whose
// tracer this reuses.

// Bytes each end writes; distinct, so a swapped field cannot pass.
const (
	seqRequest  = 5
	seqResponse = 7
	tcpFinWait1 = 4
)

// isnPair is a connection's (client ISS, server ISS).
type isnPair struct{ client, server uint32 }

// seqRecords are the records of one loopback connection, by end.
type seqRecords struct{ client, server []*types.TcpStateEvent }

// TestSockSeqFields: the client's ESTABLISHED record counts only its own
// SYN as acked, the accept snapshot nothing yet, the client's FIN_WAIT1
// (taken after the exchange) the request plus the SYN acked and the
// response received — and every record from ESTABLISHED on, CLOSE
// included, rebuilds the same ISN pair on both ends.
func TestSockSeqFields(t *testing.T) {
	requireRoot(t)
	tr := newSockTracer(t)
	tr.setTarget(t, uint32(os.Getpid()), true)

	p := dialLoopback(t)
	exchange(t, p)
	p.close()
	recs := tr.collectUntilClosed(t, p)
	checkCounts(t, recs)
	want := clientKey(t, recs.client)
	for _, ev := range recs.client {
		checkKey(t, "client", ev, want, false)
	}
	for _, ev := range recs.server {
		checkKey(t, "server", ev, want, true)
	}
}

// exchange sends the request and the response and waits for each to be
// read, so the client's close follows a fully acknowledged exchange.
func exchange(t *testing.T, p *loopbackPair) {
	t.Helper()
	buf := make([]byte, seqResponse)
	if _, err := p.client.Write(buf[:seqRequest]); err != nil {
		t.Fatalf("client write: %v", err)
	}
	if _, err := io.ReadFull(p.server, buf[:seqRequest]); err != nil {
		t.Fatalf("server read: %v", err)
	}
	if _, err := p.server.Write(buf); err != nil {
		t.Fatalf("server write: %v", err)
	}
	if _, err := io.ReadFull(p.client, buf); err != nil {
		t.Fatalf("client read: %v", err)
	}
}

// collectUntilClosed gathers every record of the pair's two sockets until
// both reported TCP_CLOSE. The sockets are told apart by their ports:
// the client's local port is the client port.
func (tr *sockTracer) collectUntilClosed(t *testing.T, p *loopbackPair) seqRecords {
	t.Helper()
	var recs seqRecords
	closed := map[uint64]bool{}
	tr.waitTCPState(5*time.Second, func(ev *types.TcpStateEvent) bool {
		switch {
		case ev.Sport == p.clientPort && ev.Dport == p.serverPort:
			recs.client = append(recs.client, ev)
		case ev.Sport == p.serverPort && ev.Dport == p.clientPort:
			recs.server = append(recs.server, ev)
		default:
			return false
		}
		if ev.Newstate == tcpClose {
			closed[ev.Sk] = true
		}
		return len(closed) == 2
	})
	if len(closed) != 2 {
		t.Fatalf("saw TCP_CLOSE of %d of the two sockets; records %+v", len(closed), recs)
	}
	return recs
}

// checkCounts pins the byte counters at the three records whose values
// the exchange fixes. A CLOSE -> SYN_SENT record carries no local port
// yet (it precedes the port choice), so it is not among the client's.
func checkCounts(t *testing.T, recs seqRecords) {
	t.Helper()
	want := []struct {
		side            string
		evs             []*types.TcpStateEvent
		old, new        int32
		acked, received uint64
	}{
		{"client ESTABLISHED", recs.client, tcpSynSent, tcpEstablished, 1, 0},
		{"server accept snapshot", recs.server, types.TCP_STATE_SNAPSHOT, tcpEstablished, 0, 0},
		{"client FIN_WAIT1", recs.client, tcpEstablished, tcpFinWait1, 1 + seqRequest, seqResponse},
	}
	for _, w := range want {
		ev := findState(w.evs, w.old, w.new)
		if ev == nil {
			t.Errorf("%s: no %d -> %d record", w.side, w.old, w.new)
			continue
		}
		if ev.SchemaVersion != types.TCP_STATE_EVENT_SCHEMA_VERSION {
			t.Errorf("%s: schema version %d", w.side, ev.SchemaVersion)
		}
		if ev.BytesAcked != w.acked || ev.BytesReceived != w.received {
			t.Errorf("%s: bytes_acked %d bytes_received %d, want %d and %d",
				w.side, ev.BytesAcked, ev.BytesReceived, w.acked, w.received)
		}
	}
}

func findState(evs []*types.TcpStateEvent, old, new int32) *types.TcpStateEvent {
	for _, ev := range evs {
		if ev.Oldstate == old && ev.Newstate == new {
			return ev
		}
	}
	return nil
}

// clientKey rebuilds the pair from the client's ESTABLISHED record.
func clientKey(t *testing.T, evs []*types.TcpStateEvent) isnPair {
	t.Helper()
	ev := findState(evs, tcpSynSent, tcpEstablished)
	if ev == nil {
		t.Fatal("no client SYN_SENT -> ESTABLISHED record")
	}
	return rebuild(ev, false)
}

// rebuild is S2's role-aware formula, mod 2^32: the connecting side's
// bytes_acked already counts its SYN, the accepted child's does not,
// and bytes_received never counts the peer's SYN. internal/federate
// (key.go) is the production copy; the integration test drives that one.
func rebuild(ev *types.TcpStateEvent, server bool) isnPair {
	own := ev.SndUna - uint32(ev.BytesAcked)
	peer := ev.RcvNxt - uint32(ev.BytesReceived) - 1
	if server {
		return isnPair{client: peer, server: own - 1}
	}
	return isnPair{client: own, server: peer}
}

// checkKey requires ev to rebuild want. The records before the handshake
// completes (the client's SYN_SENT one) hold no peer ISN and are skipped,
// as the correlator leaves their fields out of the trace.
func checkKey(t *testing.T, side string, ev *types.TcpStateEvent, want isnPair, server bool) {
	t.Helper()
	if ev.Newstate == tcpSynSent {
		return
	}
	if got := rebuild(ev, server); got != want {
		t.Errorf("%s %d -> %d: snd_una %#x rcv_nxt %#x acked %d received %d rebuild %+v, want %+v",
			side, ev.Oldstate, ev.Newstate, ev.SndUna, ev.RcvNxt, ev.BytesAcked,
			ev.BytesReceived, got, want)
	}
}
