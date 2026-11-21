package federate

import (
	"bufio"
	"os"
	"path/filepath"
	"testing"

	"iormesh/internal/schema"
)

// The pingPong of TestClockBoundRecoversSkew bounds θ to exactly
// [θ−2500, θ+2000]: the upper side by the handshake (client→server data
// ties at best), the lower side by the server→client data alone. So a
// direction whose data messages are dropped shows in Messages and, for
// server→client, in HasLower.
const (
	pingMsgs    = 7 // handshake + 3 requests + 3 responses
	theta       = int64(sec)
	clientFirst = t0 // the client's connect enter (newConv)
)

// drop appends a drop of reason ("" for ring-buffer loss) at wall: a
// host-level one (pid 0) or one naming pid.
func (t *trace) drop(pid uint32, reason string, wall uint64) {
	args := schema.Args{Lost: ptr(uint64(3))}
	if reason != "" {
		args.DropReason = ptr(reason)
	}
	t.event(pid, schema.KindDrop, schema.NoFD, 0, wall, "", args)
	if pid == 0 {
		ev := &t.events[len(t.events)-1]
		ev.PIDInc, ev.Comm = 0, ""
	}
}

// counters appends a tcp_state record of conv c's client (FIN_WAIT1) or
// server (CLOSE_WAIT) end at true time at, carrying tcp_sock counters
// that rebuild c's key.
func (c *conv) counters(client bool, at, acked, received uint64) {
	k := c.k
	if client {
		c.a.event(100, schema.KindTCPState, 7, 0, on(at, c.offA), c.conn+"-c", withSeq(
			tuple("10.0.1.1:45678", "10.0.1.254:8080"), "FIN_WAIT1",
			seqFields{SndUna: k.Client + uint32(acked), RcvNxt: k.Server + uint32(received) + 1,
				BytesAcked: acked, BytesReceived: received}))
		return
	}
	c.b.event(200, schema.KindTCPState, 9, 0, on(at, c.offB), c.conn+"-s", withSeq(
		tuple("10.0.2.2:8080", "10.0.2.254:40000"), "CLOSE_WAIT",
		seqFields{SndUna: k.Server + uint32(acked) + 1, RcvNxt: k.Client + uint32(received) + 1,
			BytesAcked: acked, BytesReceived: received}))
}

// pingConv runs the pingPong between hostA (client, clock offset 0) and
// hostB (server, offset θ).
func pingConv() (*trace, *trace, *conv) {
	a, b := newTrace("hostA", "shop"), newTrace("hostB", "billing")
	c := newConv(a, b, 0, theta, "x", ISNPair{0x10, 0x20}, t0)
	c.pingPong(t0 + 10*tus)
	return a, b, c
}

// wantBound checks the one bound left: msgs messages, and the lower
// side (server→client data) present or not.
func wantBound(t *testing.T, r Result, msgs int, lower bool) {
	t.Helper()
	if len(r.Clocks) != 1 {
		t.Fatalf("%d bounds, want 1: %v", len(r.Clocks), r.Clocks)
	}
	c := r.Clocks[0]
	if c.Messages != msgs || c.HasLower != lower || !c.HasUpper || c.Upper != theta+2000 ||
		(lower && c.Lower != theta-2500) {
		t.Fatalf("got %s, want %d messages, lower side %v", clockLine(c), msgs, lower)
	}
}

// TestClockDropScope: a drop takes the data messages of a direction only
// when it may have hidden one of the sender's sends — on the sending host,
// at or after the socket opened there — never those of the other
// direction, and never when it came before the connection.
func TestClockDropScope(t *testing.T) {
	before, inside, after := clientFirst-sec, t0+100*tus, t0+30*sec
	cases := []struct {
		name  string
		add   func(a, b *trace)
		msgs  int
		lower bool
	}{
		{"no drop", func(a, b *trace) {}, pingMsgs, true},
		{"ring loss on the server before its accept", func(a, b *trace) {
			b.drop(0, "", on(before, theta))
		}, pingMsgs, true},
		{"ring loss on the server inside the connection", func(a, b *trace) {
			b.drop(0, "", on(inside, theta))
		}, 4, false}, // server→client only
		{"ring loss on the server after the connection", func(a, b *trace) {
			b.drop(0, "", on(after, theta))
		}, 4, false}, // reported later, maybe lost earlier
		{"attach_gap on the client before its connect", func(a, b *trace) {
			a.drop(100, schema.DropAttachGap, before)
		}, pingMsgs, true},
		{"attach_gap on the client after its connect", func(a, b *trace) {
			a.drop(100, schema.DropAttachGap, inside)
		}, 4, true}, // client→server only
		{"ring loss on the server at its accept's very instant", func(a, b *trace) {
			b.drop(0, "", on(t0, theta)+2000) // trace.server's accept4 enter
		}, 4, false}, // the lost record may be the connection's
		{"skipped runs on the server before its accept", func(a, b *trace) {
			b.drop(0, schema.DropSkippedRuns, on(before, theta))
		}, pingMsgs, true},
		{"skipped runs on the client before its connect", func(a, b *trace) {
			a.drop(0, schema.DropSkippedRuns, before)
		}, 4, true}, // the socket may predate connect
		{"unattributed on the server before its accept", func(a, b *trace) {
			b.drop(200, schema.DropUnattributed, on(before, theta))
		}, 4, false}, // its calls run on past its instant
		{"drops on both hosts inside", func(a, b *trace) {
			a.drop(0, "", inside)
			b.drop(0, "", on(inside, theta))
		}, 1, false}, // handshake only
		{"another project's session on the server host", func(a, b *trace) {
			b.drop(0, "", on(inside, theta))
			b.events[len(b.events)-1].Project = "other"
		}, pingMsgs, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, b, _ := pingConv()
			tc.add(a, b)
			wantBound(t, mergeTraces(t, Options{}, a, b), tc.msgs, tc.lower)
		})
	}
}

// TestClockDropWithoutOpeningCall: an end whose socket no successful
// connect or accept dates has no instant a drop could precede, so any
// drop of its host takes its direction.
func TestClockDropWithoutOpeningCall(t *testing.T) {
	a, b, _ := pingConv()
	for i := range b.events {
		if b.events[i].Event == schema.KindAccept4 {
			b.events[i].Ret, b.events[i].Err = -11, "EAGAIN"
		}
	}
	b.drop(0, "", on(clientFirst-sec, theta))
	wantBound(t, mergeTraces(t, Options{}, a, b), 4, false)
}

// TestClockCounterProof: tcp_state counters taken after a message's send
// and receive prove it whatever the drops say; a record before the send
// or the receive proves nothing, nor do counters past the bounds.
func TestClockCounterProof(t *testing.T) {
	late := t0 + sec
	cases := []struct {
		name  string
		add   func(c *conv)
		msgs  int
		lower bool
	}{
		{"counters after the data on both ends", func(c *conv) {
			c.counters(true, late, 61, 900)
			c.counters(false, late, 900, 61)
		}, pingMsgs, true}, // drops overruled
		{"client counters only", func(c *conv) {
			c.counters(true, late, 61, 900)
		}, 1, false}, // no receiver proof for client→server, no sender proof for server→client
		{"counters before the last round", func(c *conv) {
			c.counters(true, t0+200*tus, 41, 600)
			c.counters(false, t0+200*tus, 600, 41)
		}, 5, true}, // the first two rounds proven, the last not
		{"bytes_acked at the proof's cap", func(c *conv) {
			c.counters(true, late, ackedProofMax, 900)
			c.counters(false, late, ackedProofMax, 61)
		}, 1, false}, // a queue that large could hide a lap
		{"bytes_received at 4 GiB", func(c *conv) {
			c.counters(true, late, 61, wrap4G)
			c.counters(false, late, 900, wrap4G)
		}, 1, false}, // offsets past 2^32
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, b, c := pingConv()
			// Drops inside the connection on both hosts: the stream check
			// fails both ways, so only the counter proof can keep data.
			a.drop(0, "", t0+100*tus)
			b.drop(0, "", on(t0+100*tus, theta))
			tc.add(c)
			wantBound(t, mergeTraces(t, Options{}, a, b), tc.msgs, tc.lower)
		})
	}
}

// TestClockFoldedBytesCount: a folded write (no tcp_seq) still counts
// toward the sums that guard the stream check, so 4 GiB of it leaves the
// direction to the counter proof.
func TestClockFoldedBytesCount(t *testing.T) {
	a, b, _ := pingConv()
	a.event(100, schema.KindWrite, 7, 1<<20, t0+500*tus, "x-c", schema.Args{Count: ptr(uint64(4096))})
	a.events[len(a.events)-1].Bytes = wrap4G
	wantBound(t, mergeTraces(t, Options{}, a, b), 1, false)
}

// TestClockUnfoldedBytesCount: so does a single write without tcp_seq
// (another thread interleaved with it) and without args.count.
func TestClockUnfoldedBytesCount(t *testing.T) {
	a, b, _ := pingConv()
	a.event(100, schema.KindWrite, 7, 1<<20, t0+500*tus, "x-c", schema.Args{})
	a.events[len(a.events)-1].Bytes = wrap4G
	wantBound(t, mergeTraces(t, Options{}, a, b), 1, false)
}

// TestClockSumsCover: a recorded offset past the sender's byte sum proves
// bytes no record counted — an untraced sendmsg, or a holder the trace
// cannot tie to the connection — so the sums prove nothing for that
// direction; the other direction keeps its messages.
func TestClockSumsCover(t *testing.T) {
	for _, tc := range []struct {
		name string
		add  func(a, b *trace, c *conv)
	}{
		{"a receive past the sender's sum", func(a, b *trace, c *conv) {
			b.data(200, schema.KindRead, 9, on(t0+400*tus, theta), tus, "x-s", c.k.Client+1+1000, 10)
		}},
		{"a send past its own sum", func(a, b *trace, c *conv) {
			a.data(100, schema.KindWrite, 7, t0+400*tus, tus, "x-c", c.k.Client+1+1000, 10)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b, c := pingConv()
			tc.add(a, b, c)
			wantBound(t, mergeTraces(t, Options{}, a, b), 4, true)
		})
	}
	if !sumsCover(&timing{sent: 10, sends: []streamRec{{seq: 1, n: 10}}}, &timing{}, 0) {
		t.Fatal("a send ending exactly at the sum is covered")
	}
}

// TestClockCounterInsideCall: a counter record taken while the call ran
// proves nothing — after a send's enter but before its exit (its start
// is read in the enter hook, after the enter's timestamp), or before a
// receive's exit (its bytes may not all be counted yet).
func TestClockCounterInsideCall(t *testing.T) {
	at, late := t0+10*tus, t0+sec
	for _, tc := range []struct {
		name             string
		sendRec, recvRec uint64 // true times of the two counter records
		msgs             int
	}{
		{"both after the calls", late, late, 2},
		{"inside the send", at + tus/2, late, 1},
		{"inside the receive", late, at + 5*tus, 1},
	} {
		a, b := newTrace("hostA", "shop"), newTrace("hostB", "billing")
		c := newConv(a, b, 0, theta, "x", ISNPair{0x10, 0x20}, t0)
		c.request(at, 10*tus, 20) // the client's write takes 1 µs, the read 10 µs
		a.drop(0, "", at)
		b.drop(0, "", on(at, theta))
		c.counters(true, tc.sendRec, 21, 0)
		c.counters(false, tc.recvRec, 0, 21)
		cl := mergeTraces(t, Options{}, a, b).Clocks
		if len(cl) != 1 || cl[0].Messages != tc.msgs {
			t.Fatalf("%s: got %v, want %d messages", tc.name, cl, tc.msgs)
		}
	}
}

// TestClockLB4Run2 replays the M4-10 failure of task lb4's second
// two-host run (Rocky 9.9, 2026-11-21): the tcp-two-host connection
// between m4shop on iom1 and m4billing on iom2, trimmed from the
// controller's store (only the connection's records, the drops and the
// attach events; host ids renamed). Both hosts dropped: an attach_gap
// before the connection on each, skipped runs after it on each. Every
// drop counted against the whole host left the handshake alone
// ("1 windows (1 one-sided, 0 empty)"). The skipped runs still block the
// stream check — they may have hidden sends — but the connection's own
// tcp_state counters prove both data messages: two-sided.
func TestClockLB4Run2(t *testing.T) {
	events := readNDJSON(t, "m4shop.ndjson", "m4billing.ndjson")
	got := ClockBounds(events, Options{})
	if len(got) != 1 {
		t.Fatalf("%d bounds, want 1: %v", len(got), got)
	}
	c := got[0]
	// θ = clock(iom2) − clock(iom1): above billing's write enter −
	// shop's read exit, below billing's read exit − shop's write enter.
	if c.HostA != "iom1" || c.HostB != "iom2" || c.Messages != 3 || c.Empty() ||
		!c.HasLower || c.Lower != -567_047 || !c.HasUpper || c.Upper != -94_740 {
		t.Fatalf("got %s, want [-567047, -94740] from 3 messages", clockLine(c))
	}
	// The stream check alone, as run 2 had it, fails both ways: the
	// skipped runs came after each socket opened, so they may have hidden
	// sends. The drops still count wherever they could have.
	col := newCollector()
	for i := range events {
		col.add(&events[i])
	}
	conns := Fuse(col.halves(), DefaultWindow).Conns
	if len(conns) != 1 {
		t.Fatalf("%d fused conns, want 1", len(conns))
	}
	cl := end{host: "iom1", acc: col.acc[LocalConn{"iom1", conns[0].Client.ConnID}], client: true}
	sv := end{host: "iom2", acc: col.acc[LocalConn{"iom2", conns[0].Server.ConnID}]}
	k := conns[0].Key
	if col.streamProof(cl, sv, k.Client) || col.streamProof(sv, cl, k.Server) {
		t.Fatal("the stream check must not survive skipped runs after the socket opened")
	}
	// Without its skipped runs, billing's sends are complete: its
	// attach_gap came before the accept.
	col.drops[hostProject{"iom2", "m4billing"}] = col.drops[hostProject{"iom2", "m4billing"}][:1]
	if !col.streamProof(sv, cl, k.Server) {
		t.Fatal("an attach_gap before the accept must not block the stream check")
	}
}

// readNDJSON decodes testdata/lb4-run2's files strictly.
func readNDJSON(t *testing.T, names ...string) []schema.Event {
	t.Helper()
	var out []schema.Event
	for _, name := range names {
		f, err := os.Open(filepath.Join("testdata", "lb4-run2", name))
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			ev, err := schema.DecodeEvent(sc.Bytes())
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			out = append(out, ev)
		}
		if err := sc.Err(); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	return out
}
