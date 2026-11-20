package render

import (
	"slices"
	"strconv"

	"iormesh/internal/viewmodel"
)

// Sequence layout: one lifeline per thread of a service, in the
// conversation's own order (Conversation.Lifelines, first appearance) —
// "gateway thread X → orders thread Y" — one row per step. A lifeline's
// head names its service and, below, its thread; a virtual thread's
// I/O on its carrier is a lifeline of its own, drawn dashed and marked
// "(virtual)", never the carrier's platform lifeline. Causal arrows are
// solid, time-ordered ones dashed and unordered ones dotted with a "?"
// — the diagram shows how each step's place is known, as the model says
// it (docs/visualization.md "Sequence"). A file step is a note on its
// lifeline. A model without lifelines (built before task 0f4) is drawn
// with one lifeline per service.
const (
	laneWidth  = 220
	laneLeft   = 120
	headerY    = 30
	headHeight = 32
	firstRow   = 90
	stepHeight = 30
	selfLoop   = 40
)

// sequenceStyle styles the sequence diagrams.
const sequenceStyle = `rect{fill:#fff;stroke:#333}rect.note{fill:#fdf6d8;stroke:#c9b458}` +
	`rect.carrier{fill:#f4f4ff;stroke:#66c;stroke-dasharray:4 3}` +
	`line.life{stroke:#bbb;stroke-dasharray:2 2}line.step{stroke:#222}` +
	`line.time-ordered{stroke:#666;stroke-dasharray:6 4}` +
	`line.unordered{stroke:#c33;stroke-dasharray:2 3}text.title{font-weight:bold}`

// Sequence renders one conversation as an SVG sequence diagram.
func Sequence(c viewmodel.Conversation) []byte {
	lanes, from, to := lanesOf(c)
	x := func(lane int) int { return laneLeft + max(lane, 0)*laneWidth }
	rows := len(c.Steps) + btoi(c.MoreSteps > 0)
	height := firstRow + rows*stepHeight + 20
	var s svg
	s.open(laneLeft*2+max(len(lanes), 1)*laneWidth, height, sequenceStyle)
	s.text("title", 10, 16, "start", "conversation "+c.ID+": "+short(c.Entry)+" "+short(c.Label))
	for i, l := range lanes {
		drawHead(&s, l, x(i), height)
	}
	for i, st := range c.Steps {
		drawStep(&s, st, firstRow+i*stepHeight, x(from(i)), x(to(i)), to(i) >= 0)
	}
	if c.MoreSteps > 0 {
		s.text("label", 10, firstRow+len(c.Steps)*stepHeight, "start",
			"truncated: "+strconv.Itoa(c.MoreSteps)+" more steps")
	}
	return s.close()
}

// lanesOf is the conversation's lifelines and, per step, the index of
// its own and its peer's (-1 for none). A step whose index falls outside
// the list (a hostile model) is drawn on the first lifeline rather than
// trusted. Without lifelines, each service is one.
func lanesOf(c viewmodel.Conversation) ([]viewmodel.Lifeline, func(int) int, func(int) int) {
	if len(c.Lifelines) == 0 {
		lanes := make([]viewmodel.Lifeline, 0, len(c.Services))
		for _, sv := range c.Services {
			lanes = append(lanes, viewmodel.Lifeline{Service: sv})
		}
		idx := func(sv string) int { return slices.Index(c.Services, sv) }
		return lanes, func(i int) int { return max(idx(c.Steps[i].Service), 0) },
			func(i int) int { return peerIndex(c.Steps[i], idx(c.Steps[i].To)) }
	}
	valid := func(n int) int {
		if n < 0 || n >= len(c.Lifelines) {
			return 0
		}
		return n
	}
	return c.Lifelines, func(i int) int { return valid(c.Steps[i].Lane) },
		func(i int) int { return peerIndex(c.Steps[i], valid(c.Steps[i].ToLane)) }
}

// peerIndex is n for an arrow, -1 for a note (a file step or a step
// without a peer).
func peerIndex(st viewmodel.Step, n int) int {
	if st.Kind == viewmodel.StepFile || st.To == "" || st.ToLane < 0 {
		return -1
	}
	return max(n, 0)
}

// drawHead draws a lifeline's head — service, and thread below it — and
// its line down the diagram.
func drawHead(s *svg, l viewmodel.Lifeline, x, height int) {
	class, thread := "lane", short(l.Thread)
	if l.Carrier {
		class, thread = "carrier", short(l.Thread+" (virtual)")
	}
	s.rect(class, x-boxWidth/4-20, headerY, boxWidth/2+40, headHeight)
	s.text("label", x, headerY+13, "middle", short(l.Service))
	if thread != "" {
		s.text("label", x, headerY+27, "middle", thread)
	}
	s.line("life", x, headerY+headHeight, x, height-10, false)
}

// drawStep draws one step at row y from lifeline x from to x to: an
// arrow (a loop when both are one), or a note on the step's own
// lifeline when it has no peer.
func drawStep(s *svg, st viewmodel.Step, y, from, to int, arrow bool) {
	label := short(st.Label)
	if st.Ordering == viewmodel.OrderNone {
		label = "? " + label
	}
	if !arrow {
		s.rect("note", from+6, y-12, boxWidth, boxHeight-4)
		s.text("label", from+10, y, "start", label)
		return
	}
	class := "step"
	if st.Ordering != viewmodel.OrderCausal {
		class = st.Ordering
	}
	if to == from {
		s.line(class, from, y, from+selfLoop, y, false)
		s.line(class, from+selfLoop, y, from+selfLoop, y+8, false)
		s.line(class, from+selfLoop, y+8, from, y+8, true)
		s.text("label", from+selfLoop+4, y, "start", label)
		return
	}
	s.line(class, from, y, to, y, true)
	s.text("label", (from+to)/2, y-4, "middle", label)
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
