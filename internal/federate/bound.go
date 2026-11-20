package federate

import (
	"cmp"
	"math"
	"slices"
	"time"
)

// Clock-bound defaults.
const (
	// DefaultClockWindow is how much of a host pair's traffic one clock
	// bound covers. Clocks drift, so the offset is not constant: a minute
	// keeps the drift allowance (DefaultDriftPPM) small next to the bound
	// itself, and drift over a long session shows up as bounds that move
	// from window to window instead of one interval coming out empty.
	DefaultClockWindow = time.Minute
	// DefaultDriftPPM is the assumed worst rate, in parts per million, at
	// which two NTP-disciplined hosts' clocks move apart (S2 "Limits":
	// about a ppm). A clock NTP is stepping or slewing hard breaks the
	// assumption; raise Options.DriftPPM for such hosts.
	DefaultDriftPPM = 1.0
)

// ClockBound is what the fused connections between two hosts prove about
// their clocks in one window. Offset = clock(HostB) − clock(HostA), in ns.
// Every message adds one inequality — a send precedes its receive — that
// held at the message's own instant: Lower and Upper are the tightest of
// them. The offset moves within the window by drift, so at an arbitrary
// instant of the window it lies in Interval(), the raw bound widened on
// each side by Drift (the drift rate times the window's length). That
// interval is right as long as the drift assumption holds; its width
// says how far the two hosts' timestamps can be compared. A side no
// message bounded is absent (HasLower, HasUpper false): the handshake
// alone bounds one side only.
type ClockBound struct {
	HostA, HostB string // HostA < HostB
	// Start and End delimit the window [Start, End) on HostA's wall
	// clock: a message falls in the window of its HostA-side instant.
	Start, End uint64
	Lower      int64
	Upper      int64
	HasLower   bool
	HasUpper   bool
	// Drift is the allowance, ns, that Interval adds to each side: how
	// far the offset may move within the window at the assumed drift
	// rate (Options.DriftPPM).
	Drift int64
	// LowerConn and UpperConn name the fused connection whose message set
	// each side (the smallest id on a tie): where to look when a bound is
	// empty.
	LowerConn, UpperConn string
	Messages             int // inequalities intersected
}

// windowKey names one host pair's window.
type windowKey struct {
	a, b  string
	start uint64
}

// Interval is where the offset lies at any instant of the window: the
// raw bound widened by Drift on each side. Meaningful for a side only
// when that side is present.
func (b ClockBound) Interval() (lo, hi int64) { return b.Lower - b.Drift, b.Upper + b.Drift }

// Empty reports inequalities no offset drifting within the allowance
// satisfies. It is reported, never clamped — a stepped clock, a suspend,
// drift beyond the assumed rate or a wrong record, and either way
// nothing in the window is ordered by it.
func (b ClockBound) Empty() bool {
	lo, hi := b.Interval()
	return b.HasLower && b.HasUpper && lo > hi
}

// Width is the width of Interval, the bound's uncertainty; ok is false
// for a one-sided or empty bound, which has no finite width.
func (b ClockBound) Width() (int64, bool) {
	if !b.HasLower || !b.HasUpper || b.Empty() {
		return 0, false
	}
	lo, hi := b.Interval()
	return hi - lo, true
}

// driftAllowance is how far two clocks drifting apart at ppm parts per
// million move within one window, rounded up so it never understates.
func driftAllowance(window time.Duration, ppm float64) int64 {
	return int64(math.Ceil(float64(window) * ppm / 1e6))
}

// clockBounds intersects the messages per host pair and window, sorted
// by (HostA, HostB, Start). A message within one host bounds nothing.
func clockBounds(msgs []message, window time.Duration, drift int64) []ClockBound {
	w := uint64(window)
	acc := map[windowKey]*ClockBound{}
	for _, m := range msgs {
		if m.from == m.to {
			continue
		}
		a, b := min(m.from, m.to), max(m.from, m.to)
		at := m.recv // the HostA-side instant
		if m.from == a {
			at = m.send
		}
		k := windowKey{a, b, at / w * w}
		cb := acc[k]
		if cb == nil {
			cb = &ClockBound{HostA: a, HostB: b, Start: k.start, End: k.start + w, Drift: drift}
			acc[k] = cb
		}
		tighten(cb, m)
	}
	out := make([]ClockBound, 0, len(acc))
	for _, cb := range acc {
		out = append(out, *cb)
	}
	slices.SortFunc(out, func(x, y ClockBound) int {
		return cmp.Or(cmp.Compare(x.HostA, y.HostA), cmp.Compare(x.HostB, y.HostB),
			cmp.Compare(x.Start, y.Start))
	})
	return out
}

// tighten intersects one message's inequality: o_to − o_from ≤ recv − send.
// From HostA it caps Offset from above; from HostB it is −Offset that is
// capped, so Offset is bounded from below.
func tighten(b *ClockBound, m message) {
	b.Messages++
	d := int64(m.recv) - int64(m.send)
	if m.from == b.HostA {
		if !b.HasUpper || d < b.Upper || (d == b.Upper && m.conn < b.UpperConn) {
			b.Upper, b.UpperConn, b.HasUpper = d, m.conn, true
		}
		return
	}
	if !b.HasLower || -d > b.Lower || (-d == b.Lower && m.conn < b.LowerConn) {
		b.Lower, b.LowerConn, b.HasLower = -d, m.conn, true
	}
}
