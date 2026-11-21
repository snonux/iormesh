package internal

import (
	"io"
	"os"
	"testing"
	"time"

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
