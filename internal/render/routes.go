package render

import (
	"strconv"

	"iormesh/internal/routemap"
	"iormesh/internal/viewmodel"
)

// The routed edges in graph.svg (PLAN.md M7 "Ingress route mapping":
// the ingress scenario "renders as one routed edge — app → FQDN →
// Traefik → deployment"). The merged graph has no node for an FQDN, an
// ingress, a Service or a workload — no tracer ran there — so the routed
// edges do not join the columns above: they are a band below them, one
// row per edge, its hops as boxes left to right joined by arrows, then
// its basis and connections. An edge the route map completed (basis
// "service" or "route") is drawn dashed in its own colour; only a fused
// one (both ends traced) is drawn solid, so a completed edge is never
// read as a traced fact. The route flags follow, one line each: a flag
// is never dropped from the picture.
const (
	// maxRouteRows caps each list of the band, as maxColumnNodes caps a
	// column; the rest is counted.
	maxRouteRows = maxColumnNodes
	// maxFlagLabel caps a flag line's runes: its detail is prose.
	maxFlagLabel = 120
	// maxNote caps the note after a routed edge's last hop.
	maxNote = 60
	// flagWidth is the width a flag line needs at the drawing's 11px
	// monospace font (~6.6px a rune).
	flagWidth = maxFlagLabel*7 + 20
)

// routeStyle is added to graphStyle only when the band is drawn, so a
// graph without routes keeps its bytes.
const routeStyle = `rect.fqdn{fill:#f0e8f8}rect.ingress{fill:#f8e8f0}rect.service{fill:#e8f8f4}` +
	`rect.workload{fill:#e8f4f8}rect.pod{fill:#f4f8e8}rect.flag{fill:#c33;stroke:#c33}` +
	`line.routed{stroke:#939;stroke-width:1.5;stroke-dasharray:6 3}text.flag{fill:#c33}`

// hopClasses are the box classes of a route's hops, by field.
var hopClasses = []string{"app", "fqdn", "ingress", "service", "workload", "pod"}

// routeBand is the band's content and its top edge.
type routeBand struct {
	routes []viewmodel.Route
	flags  []viewmodel.RouteFlag
	top    int
}

func newRouteBand(rs []viewmodel.Route, fs []viewmodel.RouteFlag, top int) routeBand {
	return routeBand{routes: rs, flags: fs, top: top}
}

func (b routeBand) empty() bool { return len(b.routes) == 0 && len(b.flags) == 0 }

// rows is how many rows a list of n takes: its title, its rows up to the
// cap, and the count of the rest.
func rows(n int) int {
	if n == 0 {
		return 0
	}
	return 1 + min(n, maxRouteRows) + btoi(n > maxRouteRows)
}

// bottom is the drawing's height: the band's end, or the columns' when
// there is no band.
func (b routeBand) bottom() int {
	if b.empty() {
		return b.top
	}
	return b.top + (rows(len(b.routes))+rows(len(b.flags))+1)*rowHeight
}

// width is what the band needs across: the longest chain of hops and its
// note, or a flag line.
func (b routeBand) width() int {
	w := 0
	for _, r := range b.routes[:min(len(b.routes), maxRouteRows)] {
		w = max(w, leftMargin*2+len(routeCells(r))*colWidth+maxNote*7)
	}
	if len(b.flags) > 0 {
		w = max(w, leftMargin*2+flagWidth)
	}
	return w
}

// draw draws the routed edges, then the flags.
func (b routeBand) draw(s *svg) {
	if b.empty() {
		return
	}
	y := b.top
	if len(b.routes) > 0 {
		s.text("title", leftMargin, y+14, "start", "routed edges (route map; dashed: not traced)")
		y += rowHeight
		for _, r := range b.routes[:min(len(b.routes), maxRouteRows)] {
			drawRoute(s, r, y)
			y += rowHeight
		}
		y = moreLine(s, "label", len(b.routes)-maxRouteRows, "routed edges", y)
	}
	if len(b.flags) > 0 {
		s.text("title", leftMargin, y+14, "start", "route flags (where the map and the traces disagree)")
		y += rowHeight
		for _, f := range b.flags[:min(len(b.flags), maxRouteRows)] {
			s.rect("flag", leftMargin, y+6, 8, 8)
			s.text("flag", leftMargin+14, y+14, "start", cut(viewmodel.FlagText(f), maxFlagLabel))
			y += rowHeight
		}
		moreLine(s, "flag", len(b.flags)-maxRouteRows, "route flags", y)
	}
}

// moreLine counts what a list's cap left out and returns the next row.
func moreLine(s *svg, class string, n int, what string, y int) int {
	if n <= 0 {
		return y
	}
	s.text(class, leftMargin, y+14, "start", "+"+strconv.Itoa(n)+" more "+what)
	return y + rowHeight
}

// hopCell is one drawn hop: its box class and its label.
type hopCell struct{ class, label string }

// routeCells are r's hops with their classes, the empty ones left out.
func routeCells(r viewmodel.Route) []hopCell {
	var out []hopCell
	for i, h := range []string{r.From, r.FQDN, r.Ingress, r.Service, r.Workload, r.Pod} {
		if h != "" || i == 0 {
			out = append(out, hopCell{hopClasses[i], h})
		}
	}
	return out
}

// drawRoute draws one routed edge at row y: a box per hop, an arrow
// between each two, the note after the last.
func drawRoute(s *svg, r viewmodel.Route, y int) {
	line := "routed"
	if r.Basis == routemap.BasisFused {
		line = "fused"
	}
	cells := routeCells(r)
	for i, c := range cells {
		x := leftMargin + i*colWidth
		if i > 0 {
			s.line(line, x-(colWidth-boxWidth), y+boxHeight/2, x, y+boxHeight/2, true)
		}
		s.rect(c.class, x, y, boxWidth, boxHeight)
		s.text("label", x+6, y+14, "start", short(c.label))
	}
	x := leftMargin + len(cells)*colWidth - (colWidth - boxWidth) + 16 // past a full label's overhang
	s.text("label", x, y+14, "start", cut(viewmodel.RouteNote(r), maxNote))
}
