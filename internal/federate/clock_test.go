package federate

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"iormesh/internal/schema"
)

// Synthetic traffic runs on one true timeline; each host stamps it with
// its own clock, true time plus the host's offset, so the offset
// clock(B) − clock(A) the merge must recover is known exactly.
const (
	t0  = wall0 + 10*sec // true time of the first connection
	tus = uint64(time.Microsecond)
)

// conv is one fused connection between a client on host a and a server
// on host b, each stamping events with its own clock offset.
type conv struct {
	a, b       *trace
	offA, offB int64
	conn       string
	k          ISNPair
	cOff, sOff uint64 // stream offset of each direction's next byte
}

// on stamps true instant t with a clock offset.
func on(t uint64, off int64) uint64 { return uint64(int64(t) + off) }

// data appends one data record carrying its sequence range.
func (t *trace) data(pid uint32, kind string, fd int32, enter, lat uint64, conn string,
	seq uint32, n uint64) {
	t.event(pid, kind, fd, int64(n), enter, conn, schema.Args{TCPSeq: ptr(seq)})
	ev := &t.events[len(t.events)-1]
	ev.Bytes, ev.LatencyNS = n, lat
}

// newConv opens a connection at true time at: the client's connect and
// SYN_SENT at at, the server's accept 2 µs later (trace.client/server).
func newConv(a, b *trace, offA, offB int64, conn string, k ISNPair, at uint64) *conv {
	a.client(100, conn+"-c", "10.0.1.1:45678", "10.0.1.254:8080", k, on(at, offA))
	b.server(200, conn+"-s", "10.0.2.2:8080", "10.0.2.254:40000", k, on(at, offB))
	return &conv{a: a, b: b, offA: offA, offB: offB, conn: conn, k: k}
}

// request sends n bytes client→server at true time at; the server,
// already blocked in read since at, returns lat later.
func (c *conv) request(at, lat, n uint64) {
	seq := c.k.Client + 1 + uint32(c.cOff)
	c.a.data(100, schema.KindWrite, 7, on(at, c.offA), 1*tus, c.conn+"-c", seq, n)
	c.b.data(200, schema.KindRead, 9, on(at, c.offB), lat, c.conn+"-s", seq, n)
	c.cOff += n
}

// response sends n bytes server→client the same way.
func (c *conv) response(at, lat, n uint64) {
	seq := c.k.Server + 1 + uint32(c.sOff)
	c.b.data(200, schema.KindWrite, 9, on(at, c.offB), 1*tus, c.conn+"-s", seq, n)
	c.a.data(100, schema.KindRead, 7, on(at, c.offA), lat, c.conn+"-c", seq, n)
	c.sOff += n
}

// pingPong runs three request/response rounds from true time at. The
// smallest one-way gaps are 2 µs (the handshake) client→server and
// 2.5 µs server→client, so the bound is exactly [θ−2500, θ+2000].
func (c *conv) pingPong(at uint64) {
	for i, lat := range [][2]uint64{{5, 6}, {3, 2}, {4, 7}} {
		r := at + uint64(i)*100*tus
		c.request(r, lat[0]*tus, 20)
		c.response(r+50*tus, lat[1]*tus+tus/2, 300)
	}
}

// merge writes the traces and merges them.
func mergeTraces(t *testing.T, opt Options, traces ...*trace) Result {
	t.Helper()
	dir := t.TempDir()
	var srcs []Source
	for i, tr := range traces {
		srcs = append(srcs, tr.write(t, filepath.Join(dir, string(rune('a'+i)))))
	}
	r, err := Merge(context.Background(), srcs, opt)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestClockBoundRecoversSkew: S2's injected skews, both signs, come back
// inside a bound exactly as wide as the tightest gap each way.
func TestClockBoundRecoversSkew(t *testing.T) {
	for _, theta := range []int64{1_234_567_891, -987_654_321, 0} {
		a, b := newTrace("hostA", "shop"), newTrace("hostB", "billing")
		const offA = int64(100 * time.Millisecond)
		newConv(a, b, offA, offA+theta, "x", ISNPair{0xfffffff0, 0x1000}, t0).pingPong(t0 + 10*tus)
		r := mergeTraces(t, Options{}, a, b)
		if len(r.Clocks) != 1 {
			t.Fatalf("θ=%d: %d bounds, want 1: %+v", theta, len(r.Clocks), r.Clocks)
		}
		c := r.Clocks[0]
		w, ok := c.Width()
		// 1 ppm over the 1-minute window pads each side by 60 µs.
		if c.Lower != theta-2500 || c.Upper != theta+2000 || c.Drift != 60_000 ||
			!ok || w != 124_500 || c.Messages != 7 {
			t.Fatalf("θ=%d: got %s", theta, clockLine(c))
		}
	}
}

// TestClockBoundDrift: a clock that moves 10 ms between two connections a
// minute apart gives two windows, each holding its own offset; one window
// over both is empty — reported with its witnesses, never clamped.
func TestClockBoundDrift(t *testing.T) {
	build := func() (*trace, *trace) {
		a, b := newTrace("hostA", "shop"), newTrace("hostB", "billing")
		newConv(a, b, 0, int64(sec), "x", ISNPair{1, 2}, t0).pingPong(t0 + 10*tus)
		later := t0 + 60*sec
		newConv(a, b, 0, int64(sec)+int64(10*time.Millisecond), "y", ISNPair{3, 4}, later).
			pingPong(later + 10*tus)
		return a, b
	}
	a, b := build()
	r := mergeTraces(t, Options{}, a, b)
	if len(r.Clocks) != 2 {
		t.Fatalf("%d windows, want 2", len(r.Clocks))
	}
	for i, theta := range []int64{int64(sec), int64(sec) + int64(10*time.Millisecond)} {
		if c := r.Clocks[i]; c.Empty() || c.Lower != theta-2500 || c.Upper != theta+2000 {
			t.Fatalf("window %d: want [θ−2500, θ+2000] around θ=%d: %s", i, theta, clockLine(c))
		}
	}
	a, b = build()
	r = mergeTraces(t, Options{ClockWindow: time.Hour}, a, b)
	if len(r.Clocks) != 1 || !r.Clocks[0].Empty() {
		t.Fatalf("one hour window: got %v, want one empty bound", r.Clocks)
	}
	c := r.Clocks[0]
	if c.LowerConn == c.UpperConn || c.Lower <= c.Upper {
		t.Fatalf("empty bound must keep both sides and name both witnesses: %s", clockLine(c))
	}
	if _, ok := c.Width(); ok {
		t.Fatal("an empty bound has no width")
	}
}

// TestClockBoundNegatives: no fused connection, no estimate; skew beyond
// the fusion window, no fusion and so no estimate; the handshake alone,
// a one-sided bound without a width.
func TestClockBoundNegatives(t *testing.T) {
	t.Run("no fused connection", func(t *testing.T) {
		a, b := newTrace("hostA", "shop"), newTrace("hostB", "billing")
		newConv(a, b, 0, 0, "x", ISNPair{1, 2}, t0)
		b.events = b.events[:0] // the server end was never traced
		b.server(200, "other-s", "10.0.2.2:8080", "10.0.2.254:40000", ISNPair{5, 6}, t0)
		if r := mergeTraces(t, Options{}, a, b); len(r.Conns) != 0 || len(r.Clocks) != 0 {
			t.Fatalf("got %d conns and bounds %v, want none", len(r.Conns), r.Clocks)
		}
	})
	for _, tc := range []struct {
		name  string
		theta time.Duration
		fused bool
	}{{"skew 90 s, inside the window", 90 * time.Second, true},
		{"skew 3 min, beyond the window", 3 * time.Minute, false}} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := newTrace("hostA", "shop"), newTrace("hostB", "billing")
			newConv(a, b, 0, int64(tc.theta), "x", ISNPair{1, 2}, t0).pingPong(t0 + 10*tus)
			r := mergeTraces(t, Options{}, a, b)
			if got := len(r.Conns) == 1 && len(r.Clocks) == 1; got != tc.fused {
				t.Fatalf("fused and bounded %v, want %v: %+v", got, tc.fused, r.Unresolved)
			}
			if tc.fused && r.Clocks[0].Lower != int64(tc.theta)-2500 {
				t.Fatalf("got %s", clockLine(r.Clocks[0]))
			}
		})
	}
	t.Run("handshake only: one-sided", func(t *testing.T) {
		a, b := newTrace("hostA", "shop"), newTrace("hostB", "billing")
		newConv(a, b, 0, 7*int64(time.Millisecond), "x", ISNPair{1, 2}, t0)
		c := mergeTraces(t, Options{}, a, b).Clocks
		if len(c) != 1 || c[0].HasLower || !c[0].HasUpper || c[0].Upper != 7_002_000 {
			t.Fatalf("got %v, want an upper bound of θ+2 µs only", c)
		}
		if _, ok := c[0].Width(); ok {
			t.Fatal("a one-sided bound has no width")
		}
	})
}

// TestStreamMessages covers the byte matching: by stream offset across the
// ISN's wrap, duplicates folded, and every case where an offset would
// name no single send giving nothing rather than a guess.
func TestStreamMessages(t *testing.T) {
	const iss = uint32(0xfffffff0)
	rec := func(off uint64, n, at uint64) streamRec {
		return streamRec{seq: iss + 1 + uint32(off), n: n, enter: at, exit: at + 10}
	}
	cases := []struct {
		name  string
		sends []streamRec
		recv  streamRec
		want  int // matched messages
	}{
		{"offsets past the ISN's wrap", []streamRec{rec(0, 10, 1), rec(10, 30, 2)}, rec(5, 20, 9), 1},
		{"duplicate send from two sessions", []streamRec{rec(0, 10, 1), rec(0, 10, 1)}, rec(0, 10, 9), 1},
		{"last byte never sent in the trace", []streamRec{rec(0, 10, 1)}, rec(10, 5, 9), 0},
		{"overlapping sends", []streamRec{rec(0, 10, 1), rec(5, 10, 2)}, rec(0, 3, 9), 0},
		{"send across 4 GiB", []streamRec{rec(wrap4G-5, 10, 1)}, rec(0, 3, 9), 0},
	}
	for _, tc := range cases {
		got := streamMessages("hostA", "hostB", "c", tc.sends, []streamRec{tc.recv}, iss)
		if len(got) != tc.want {
			t.Errorf("%s: %d messages, want %d", tc.name, len(got), tc.want)
		}
	}
	m := streamMessages("hostA", "hostB", "c", cases[0].sends, []streamRec{cases[0].recv}, iss)
	if m[0].send != 2 || m[0].recv != 19 { // last byte 24 sits in the second send
		t.Fatalf("paired with %+v, want the send holding the last byte", m[0])
	}
	if !long(&timing{sent: wrap4G}) || !long(&timing{maxCount: wrap4G}) || long(&timing{}) {
		t.Fatal("long misjudges a 4 GiB stream")
	}
}

// TestClockBoundStreamGuards runs whole merges: a stream past 4 GiB or a
// host that dropped records leaves the handshake as the only message,
// and a receive spanning two sends pairs with the later one.
func TestClockBoundStreamGuards(t *testing.T) {
	const theta = int64(sec)
	k := ISNPair{0x10, 0x20}
	t.Run("stream past 4 GiB", func(t *testing.T) {
		a, b := newTrace("hostA", "shop"), newTrace("hostB", "billing")
		newConv(a, b, 0, theta, "x", k, t0).pingPong(t0 + 10*tus)
		// A later record whose counters, mod 2^32, rebuild the same key.
		big := uint64(1)<<32 + 23
		a.event(100, schema.KindTCPState, 7, 0, t0+sec, "x-c", withSeq(
			tuple("10.0.1.1:45678", "10.0.1.254:8080"), "FIN_WAIT1",
			seqFields{SndUna: k.Client + 23, RcvNxt: k.Server + 302, BytesAcked: big,
				BytesReceived: 301}))
		assertHandshakeOnly(t, mergeTraces(t, Options{}, a, b), theta)
	})
	t.Run("host dropped records", func(t *testing.T) {
		a, b := newTrace("hostA", "shop"), newTrace("hostB", "billing")
		newConv(a, b, 0, theta, "x", k, t0).pingPong(t0 + 10*tus)
		b.event(0, schema.KindDrop, schema.NoFD, 0, on(t0+sec, theta), "",
			schema.Args{Lost: ptr(uint64(3))})
		drop := &b.events[len(b.events)-1]
		drop.PIDInc, drop.Comm = 0, ""
		assertHandshakeOnly(t, mergeTraces(t, Options{}, a, b), theta)
	})
	t.Run("receive spanning two sends", func(t *testing.T) {
		a, b := newTrace("hostA", "shop"), newTrace("hostB", "billing")
		c := newConv(a, b, 0, theta, "x", k, t0)
		at := t0 + 10*tus
		a.data(100, schema.KindWrite, 7, at, tus, "x-c", k.Client+1, 10)
		a.data(100, schema.KindWrite, 7, at+tus, tus, "x-c", k.Client+11, 10)
		b.data(200, schema.KindRead, 9, on(at, c.offB), 2*tus+tus/2, "x-s", k.Client+1, 20)
		// Paired with the second send (enter at+1 µs): θ + 1.5 µs, under
		// the handshake's θ + 2 µs; the first send would give θ + 2.5 µs.
		cl := mergeTraces(t, Options{}, a, b).Clocks
		if len(cl) != 1 || cl[0].Upper != theta+1500 || cl[0].Messages != 2 {
			t.Fatalf("got %v, want upper θ+1500 from 2 messages", cl)
		}
	})
}

// assertHandshakeOnly: the one bound left is the SYN's upper side.
func assertHandshakeOnly(t *testing.T, r Result, theta int64) {
	t.Helper()
	if len(r.Clocks) != 1 || r.Clocks[0].Messages != 1 || r.Clocks[0].HasLower ||
		r.Clocks[0].Upper != theta+2000 {
		t.Fatalf("got %v, want the handshake's upper bound only", r.Clocks)
	}
}
