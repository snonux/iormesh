package federate

import (
	"testing"
	"time"
)

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
	// A..C composes to [θ−5000, θ+4000] with θ = offC − offA.
	lo, hi, ok := tl.Offset("hostA", "hostC", on(t0, offA))
	if theta := offC - offA; !ok || lo != theta-5000 || hi != theta+4000 {
		t.Fatalf("A..C offset [%d,%d] ok=%v, want [θ−5000, θ+4000] around %d", lo, hi, ok, theta)
	}
	x := Stamp{"hostA", on(t0, offA)}
	cases := []struct {
		name string
		x, y Stamp
		want Relation
	}{
		{"20 µs later on C: provably after", x, Stamp{"hostC", on(t0+20*tus, offC)}, Before},
		{"seen from C", Stamp{"hostC", on(t0+20*tus, offC)}, x, After},
		{"3 µs later: inside the uncertainty", x, Stamp{"hostC", on(t0+3*tus, offC)}, Unordered},
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
