package federate

import (
	"slices"
	"testing"
	"time"
)

// TestTimelineDriftNeverMisorders: an offset that drifts within the
// assumed rate (1 ppm: up to 60 µs over the window) after the messages
// that bounded it never turns into a false order, for any gap. Without
// the drift allowance it would: the bound was tight at t0 only.
func TestTimelineDriftNeverMisorders(t *testing.T) {
	theta0 := int64(sec)
	a, b := newTrace("hostA", "shop"), newTrace("hostB", "billing")
	newConv(a, b, 0, theta0, "x", ISNPair{1, 2}, t0).pingPong(t0 + 10*tus)
	bounds := mergeTraces(t, Options{}, a, b).Clocks
	tl := NewTimeline(bounds)
	at := t0 + 25*sec // still the window of t0, 25 s later
	for drift := int64(-60_000); drift <= 60_000; drift += 15_000 {
		for g := int64(-150_000); g <= 150_000; g += 1000 {
			x := Stamp{"hostA", at}
			y := Stamp{"hostB", uint64(int64(at) + g + theta0 + drift)}
			got := tl.Order(x, y)
			if (got == Before && g <= 0) || (got == After && g >= 0) {
				t.Fatalf("drift %d ns, true gap %d ns: %v", drift, g, got)
			}
		}
	}
	// The hazard the allowance removes: unpadded, 25 µs of drift makes a
	// y that came 10 µs before x look after it.
	raw := slices.Clone(bounds)
	raw[0].Drift = 0
	x := Stamp{"hostA", at}
	y := Stamp{"hostB", uint64(int64(at) - 10_000 + theta0 + 25_000)}
	if got := NewTimeline(raw).Order(x, y); got != Before {
		t.Fatalf("unpadded bound: %v, want the false before this test guards against", got)
	}
	if got := tl.Order(x, y); got != Unordered {
		t.Fatalf("padded bound: %v, want unordered", got)
	}
}

// TestTimelineMessageOnItsBound: the message that set a bound sits
// exactly on it, so its own send and receive stay Unordered — the
// equality case — even without any drift allowance.
func TestTimelineMessageOnItsBound(t *testing.T) {
	const theta, send = int64(700), uint64(1_000_000)
	b := ClockBound{HostA: "hostA", HostB: "hostB", End: 2 * t0,
		Lower: theta - 2500, Upper: theta + 2000, HasLower: true, HasUpper: true}
	recv := Stamp{"hostB", uint64(int64(send) + theta + 2000)}
	if got := NewTimeline([]ClockBound{b}).Order(Stamp{"hostA", send}, recv); got != Unordered {
		t.Fatalf("send vs its own receive: %v, want unordered", got)
	}
	recv.Wall++
	if got := NewTimeline([]ClockBound{b}).Order(Stamp{"hostA", send}, recv); got != Before {
		t.Fatalf("1 ns past the bound: %v, want before", got)
	}
}

// TestTimelineThreeHosts: A talks to B and B to C, never A to C. A and C
// get no bound of their own, but the timeline orders their instants
// through B — and only when the summed uncertainty allows it.
func TestTimelineThreeHosts(t *testing.T) {
	offA, offB, offC := int64(0), int64(500*time.Millisecond), int64(-300*time.Millisecond)
	a, b, c := newTrace("hostA", "shop"), newTrace("hostB", "billing"), newTrace("hostC", "ledger")
	newConv(a, b, offA, offB, "ab", ISNPair{1, 2}, t0).pingPong(t0 + 10*tus)
	newConv(b, c, offB, offC, "bc", ISNPair{3, 4}, t0).pingPong(t0 + 10*tus)
	r := mergeTraces(t, Options{}, a, b, c)
	if len(r.Clocks) != 2 || r.Clocks[0].HostB != "hostB" || r.Clocks[1].HostA != "hostB" {
		t.Fatalf("want bounds for A..B and B..C only, got %v", r.Clocks)
	}
	tl := NewTimeline(r.Clocks)
	// A..C composes to [θ−5000, θ+4000] with θ = offC − offA, padded by
	// each hop's 60 µs drift allowance on both sides.
	lo, hi, ok := tl.Offset("hostA", "hostC", on(t0, offA))
	if theta := offC - offA; !ok || lo != theta-125_000 || hi != theta+124_000 {
		t.Fatalf("A..C offset [%d,%d] ok=%v, want [θ−125000, θ+124000] around %d",
			lo, hi, ok, theta)
	}
	x := Stamp{"hostA", on(t0, offA)}
	cases := []struct {
		name string
		x, y Stamp
		want Relation
	}{
		{"200 µs later on C: provably after", x, Stamp{"hostC", on(t0+200*tus, offC)}, Before},
		{"seen from C", Stamp{"hostC", on(t0+200*tus, offC)}, x, After},
		{"100 µs later: inside the uncertainty", x, Stamp{"hostC", on(t0+100*tus, offC)}, Unordered},
		{"same host compares directly", x, Stamp{"hostA", x.Wall + 1}, Before},
		{"an hour later: no window, no extrapolation", Stamp{"hostA", on(t0+3600*sec, offA)},
			Stamp{"hostC", on(t0+3601*sec, offC)}, Unordered},
	}
	for _, tc := range cases {
		if got := tl.Order(tc.x, tc.y); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestTimelineUnusableBounds: one-sided, empty or missing bounds order
// nothing across hosts — raw timestamps never decide.
func TestTimelineUnusableBounds(t *testing.T) {
	x, y := Stamp{"hostA", t0}, Stamp{"hostB", t0 + sec}
	for name, bounds := range map[string][]ClockBound{
		"none":       nil,
		"one-sided":  {{HostA: "hostA", HostB: "hostB", Start: 0, End: 2 * t0, Upper: 5, HasUpper: true}},
		"contradict": {{HostA: "hostA", HostB: "hostB", End: 2 * t0, Lower: 9, Upper: 5, HasLower: true, HasUpper: true}},
	} {
		if got := NewTimeline(bounds).Order(x, y); got != Unordered {
			t.Errorf("%s: %v, want unordered", name, got)
		}
	}
	if Before.String() != "before" || After.String() != "after" || Unordered.String() != "unordered" {
		t.Fatal("relation names")
	}
}
