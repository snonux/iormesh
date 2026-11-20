package federate

import (
	"cmp"
	"slices"
	"time"
)

// DefaultClockWindow is how much of a host pair's traffic one clock
// bound covers. The bound assumes one constant offset per window, and
// NTP-disciplined clocks drift by about a ppm: 60 µs over a minute, in
// the range of the bound's own width (S2: ≈10–25 µs on veth, 82–104 µs
// for single exchanges), while a whole 4 h session drifts by ~14 ms and
// its single interval could come out empty. A minute keeps drift visible
// as bounds that move from window to window.
const DefaultClockWindow = time.Minute

// ClockBound is what the fused connections between two hosts prove about
// their clocks in one window: Offset = clock(HostB) − clock(HostA), in
// ns, lies in [Lower, Upper]. Every message of the window adds one
// inequality — a send precedes its receive — so the bound is never
// wrong, only wide; its width says how far the two hosts' timestamps can
// be compared. A side no message bounded is absent (HasLower, HasUpper
// false): the handshake alone bounds one side only.
type ClockBound struct {
	HostA, HostB string // HostA < HostB
	// Start and End delimit the window [Start, End) on HostA's wall
	// clock: a message falls in the window of its HostA-side instant.
	Start, End uint64
	Lower      int64
	Upper      int64
	HasLower   bool
	HasUpper   bool
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

// Empty reports contradictory inequalities: no offset satisfies them
// all. It is reported, never clamped — a stepped clock, a suspend, drift
// within the window or a wrong record, and either way nothing in the
// window is ordered by it.
func (b ClockBound) Empty() bool { return b.HasLower && b.HasUpper && b.Lower > b.Upper }

// Width is Upper − Lower, the bound's uncertainty; ok is false for a
// one-sided or empty bound, which has no finite width.
func (b ClockBound) Width() (int64, bool) {
	if !b.HasLower || !b.HasUpper || b.Empty() {
		return 0, false
	}
	return b.Upper - b.Lower, true
}

// clockBounds intersects the messages per host pair and window, sorted
// by (HostA, HostB, Start). A message within one host bounds nothing.
func clockBounds(msgs []message, window time.Duration) []ClockBound {
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
			cb = &ClockBound{HostA: a, HostB: b, Start: k.start, End: k.start + w}
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
