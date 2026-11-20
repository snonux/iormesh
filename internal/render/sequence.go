package render

import (
	"slices"
	"strconv"

	"iormesh/internal/viewmodel"
)

// Sequence layout: one lifeline per service in the conversation's own
// order (Conversation.Services, first appearance), one row per step.
// Causal arrows are solid, time-ordered ones dashed and unordered ones
// dotted with a "?" — the diagram shows how each step's place is known,
// as the model says it (docs/visualization.md "Sequence"). A file step
// is a note on its lifeline.
const (
	laneWidth  = 220
	laneLeft   = 120
	headerY    = 30
	firstRow   = 70
	stepHeight = 30
	selfLoop   = 40
)

// sequenceStyle styles the sequence diagrams.
const sequenceStyle = `rect{fill:#fff;stroke:#333}rect.note{fill:#fdf6d8;stroke:#c9b458}` +
	`line.life{stroke:#bbb;stroke-dasharray:2 2}line.step{stroke:#222}` +
	`line.time-ordered{stroke:#666;stroke-dasharray:6 4}` +
	`line.unordered{stroke:#c33;stroke-dasharray:2 3}text.title{font-weight:bold}`

// Sequence renders one conversation as an SVG sequence diagram.
func Sequence(c viewmodel.Conversation) []byte {
	lanes := c.Services
	x := func(service string) int {
		return laneLeft + max(slices.Index(lanes, service), 0)*laneWidth
	}
	rows := len(c.Steps) + btoi(c.MoreSteps > 0)
	height := firstRow + rows*stepHeight + 20
	var s svg
	s.open(laneLeft*2+max(len(lanes), 1)*laneWidth, height, sequenceStyle)
	s.text("title", 10, 16, "start", "conversation "+c.ID+": "+short(c.Entry)+" "+short(c.Label))
	for _, l := range lanes {
		s.rect("lane", x(l)-boxWidth/4-20, headerY, boxWidth/2+40, boxHeight)
		s.text("label", x(l), headerY+14, "middle", short(l))
		s.line("life", x(l), headerY+boxHeight, x(l), height-10, false)
	}
	for i, st := range c.Steps {
		drawStep(&s, st, firstRow+i*stepHeight, x)
	}
	if c.MoreSteps > 0 {
		s.text("label", 10, firstRow+len(c.Steps)*stepHeight, "start",
			"truncated: "+strconv.Itoa(c.MoreSteps)+" more steps")
	}
	return s.close()
}

// drawStep draws one step at row y: an arrow between two lifelines (a
// loop when both are one), or a note on the step's own lifeline.
func drawStep(s *svg, st viewmodel.Step, y int, x func(string) int) {
	from := x(st.Service)
	label := short(st.Label)
	if st.Thread != "" {
		label = short(st.Thread) + ": " + label
	}
	if st.Ordering == viewmodel.OrderNone {
		label = "? " + label
	}
	if st.Kind == viewmodel.StepFile || st.To == "" {
		s.rect("note", from+6, y-12, boxWidth, boxHeight-4)
		s.text("label", from+10, y, "start", label)
		return
	}
	class := "step"
	if st.Ordering != viewmodel.OrderCausal {
		class = st.Ordering
	}
	to := x(st.To)
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
