package federate

import (
	"cmp"
	"slices"
)

// Relations Timeline.Order finds between two instants.
const (
	// Unordered: neither instant provably came first — they are causally
	// unrelated as far as the traces show, or no bound covers them. Raw
	// timestamps never break this tie.
	Unordered Relation = iota
	Before             // the first instant came first
	After              // the second instant came first
)

// Relation is how two instants on the hosts' clocks are ordered.
type Relation int

// Stamp is an instant on one host's wall clock, ns.
type Stamp struct {
	Host string
	Wall uint64
}

// Timeline orders instants across hosts by the merge's clock bounds,
// never by comparing raw timestamps of two clocks (PLAN.md M4 "Clock
// reconciliation"): x on host X precedes y on host Y only when y − x
// strictly exceeds every offset the bound's Interval allows. Hosts
// without traffic of their own are related through a chain of hosts that
// have some, the intervals adding up along it.
//
// The order is sound under the drift assumption (Options.DriftPPM), not
// complete: what it cannot prove it leaves Unordered. In particular the
// message that set a bound sits exactly on it, so its own send and
// receive compare as Unordered — the bound alone does not know they are
// one message — and the drift allowance leaves stamps closer than it
// unordered too.
type Timeline struct {
	// pairs holds the usable bounds — two-sided and not empty — per host
	// pair in Start order. A one-sided bound proves no finite offset and
	// an empty one proves nothing at all.
	pairs map[[2]string][]ClockBound
	peers map[string][]string // hosts sharing a usable bound, sorted
}

// NewTimeline indexes the merge's bounds (Result.Clocks).
func NewTimeline(bounds []ClockBound) Timeline {
	tl := Timeline{pairs: map[[2]string][]ClockBound{}, peers: map[string][]string{}}
	for _, b := range bounds {
		if _, ok := b.Width(); !ok {
			continue
		}
		k := [2]string{b.HostA, b.HostB}
		if len(tl.pairs[k]) == 0 {
			tl.peers[b.HostA] = append(tl.peers[b.HostA], b.HostB)
			tl.peers[b.HostB] = append(tl.peers[b.HostB], b.HostA)
		}
		tl.pairs[k] = append(tl.pairs[k], b)
	}
	for k, bs := range tl.pairs {
		slices.SortFunc(bs, func(x, y ClockBound) int { return cmp.Compare(x.Start, y.Start) })
		tl.pairs[k] = bs
	}
	for h := range tl.peers {
		slices.Sort(tl.peers[h])
	}
	return tl
}

// String names the relation.
func (r Relation) String() string {
	switch r {
	case Before:
		return "before"
	case After:
		return "after"
	}
	return "unordered"
}

// Order relates x and y. On one host both stamps come from one clock and
// compare directly; equal stamps stay Unordered.
func (tl Timeline) Order(x, y Stamp) Relation {
	diff := int64(y.Wall) - int64(x.Wall)
	lo, hi := int64(0), int64(0)
	if x.Host != y.Host {
		var ok bool
		if lo, hi, ok = tl.Offset(x.Host, y.Host, x.Wall); !ok {
			return Unordered
		}
	}
	// True time between them: (y − o_Y) − (x − o_X) = diff − offset.
	switch {
	case diff-hi > 0:
		return Before
	case diff-lo < 0:
		return After
	}
	return Unordered
}

// Offset bounds clock(to) − clock(from) around instant at on from's clock:
// along the shortest chain of hosts with usable bounds, adding each hop's
// Interval in the window that holds the instant. The instant is carried
// from clock to clock as an interval, since each hop knows the offset
// only that well, and a hop counts only when that whole interval lies in
// one window — a window's interval says nothing outside its own time.
// ok is false when no chain exists or a hop has no such window: an
// offset is never extrapolated across time, since drift moves it.
func (tl Timeline) Offset(from, to string, at uint64) (lo, hi int64, ok bool) {
	path := tl.path(from, to)
	if path == nil {
		return 0, 0, false
	}
	tlo, thi := int64(at), int64(at) // the instant on the current clock
	for i := 1; i < len(path); i++ {
		l, h, found := tl.hop(path[i-1], path[i], tlo, thi)
		if !found {
			return 0, 0, false
		}
		lo, hi = lo+l, hi+h
		tlo, thi = tlo+l, thi+h
	}
	return lo, hi, true
}

// path finds the hosts from from to to by breadth-first search over
// sorted peers, so the same bounds always give the same chain: fewest
// hops, the first by name on a tie. The choice is blind to time — two
// hosts are peers if any window bounds them — so the chosen chain may
// lack a window at the instant asked about while another chain has one;
// Offset then says not ok. Sound, not complete.
func (tl Timeline) path(from, to string) []string {
	prev := map[string]string{from: ""}
	queue := []string{from}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == to {
			var path []string
			for h := to; h != ""; h = prev[h] {
				path = append(path, h)
			}
			slices.Reverse(path)
			return path
		}
		for _, p := range tl.peers[cur] {
			if _, seen := prev[p]; !seen {
				prev[p] = cur
				queue = append(queue, p)
			}
		}
	}
	return nil
}

// hop bounds clock(next) − clock(cur) for an instant known to lie in
// [tlo, thi] on cur's clock. A window is on its HostA's clock: from
// HostA the interval is checked against it directly, from HostB it is
// first carried over by the window's own offset interval.
func (tl Timeline) hop(cur, next string, tlo, thi int64) (lo, hi int64, ok bool) {
	if cur < next {
		for _, b := range tl.pairs[[2]string{cur, next}] {
			if inWindow(b, tlo, thi) {
				lo, hi := b.Interval()
				return lo, hi, true
			}
		}
		return 0, 0, false
	}
	for _, b := range tl.pairs[[2]string{next, cur}] {
		l, h := b.Interval()
		if inWindow(b, tlo-h, thi-l) {
			return -h, -l, true
		}
	}
	return 0, 0, false
}

// inWindow reports whether [lo, hi] on HostA's clock lies in b's window.
func inWindow(b ClockBound, lo, hi int64) bool {
	return int64(b.Start) <= lo && hi < int64(b.End)
}
