// Package render draws the deterministic SVG exports of a build
// (PLAN.md M5 "Deterministic SVG export", "Sequence view";
// docs/implementation.md M5 "Determinism rules"): graph.svg from the
// merged graph, labeled with the view model's names (names.go), and one
// sequence diagram per conversation of the view model. Same input, same
// bytes:
//
//   - input is a normalized (sorted) graph or the sorted view model, and
//     only sorted slices are iterated — never a map;
//   - layout is a pure function of that order: columns and rows, no force
//     simulation, no randomness, no clock;
//   - coordinates are integers, formatted with strconv.Itoa;
//   - nothing of the rendering machine — no time, hostname or version —
//     goes into the output;
//   - every text passes through one function, esc.
//
// It is cgo-free, like everything iormeshctl links.
package render

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"iormesh/internal/termsafe"
)

// maxLabel caps a label's runes: a long path would run into the next
// column. The cut is marked, never silent.
const maxLabel = 40

// classes are the CSS classes a drawing may carry. Some come from the
// data — a node's type, an edge's type or state, a step's ordering — and
// a hostile graph or model must not write its own attribute text, so
// every class passes cls: one of these, else "other" (task mb4 review).
var classes = map[string]bool{
	"host": true, "process": true, "file": true, "socket": true, "app": true,
	"runs_on": true, "opened": true, "connected": true, "incarnation": true, "forked": true,
	"signaled": true, "fused": true, "unresolved": true, "more": true, "title": true, "label": true,
	"lane": true, "carrier": true, "life": true, "note": true, "step": true, "time-ordered": true, "unordered": true,
}

// cls is c when it is a known class, else "other".
func cls(c string) string {
	if classes[c] {
		return c
	}
	return "other"
}

// svg accumulates one document.
type svg struct{ b strings.Builder }

// open starts a document of the given size with the shared styles.
func (s *svg) open(w, h int, style string) {
	s.b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" width="` + strconv.Itoa(w) +
		`" height="` + strconv.Itoa(h) + `" viewBox="0 0 ` + strconv.Itoa(w) + " " + strconv.Itoa(h) +
		`" font-family="monospace" font-size="11">` + "\n")
	s.b.WriteString("<style>" + style + "</style>\n")
	s.b.WriteString(`<defs><marker id="arrow" viewBox="0 0 10 10" refX="10" refY="5" ` +
		`markerWidth="7" markerHeight="7" orient="auto-start-reverse">` +
		`<path d="M0,0 L10,5 L0,10 z"/></marker></defs>` + "\n")
}

// close ends the document and returns it.
func (s *svg) close() []byte {
	s.b.WriteString("</svg>\n")
	return []byte(s.b.String())
}

// rect draws a box.
func (s *svg) rect(class string, x, y, w, h int) {
	s.b.WriteString(`<rect class="` + cls(class) + `" x="` + strconv.Itoa(x) + `" y="` + strconv.Itoa(y) +
		`" width="` + strconv.Itoa(w) + `" height="` + strconv.Itoa(h) + `"/>` + "\n")
}

// line draws a segment; arrow ends it in an arrowhead.
func (s *svg) line(class string, x1, y1, x2, y2 int, arrow bool) {
	s.b.WriteString(`<line class="` + cls(class) + `" x1="` + strconv.Itoa(x1) + `" y1="` + strconv.Itoa(y1) +
		`" x2="` + strconv.Itoa(x2) + `" y2="` + strconv.Itoa(y2) + `"`)
	if arrow {
		s.b.WriteString(` marker-end="url(#arrow)"`)
	}
	s.b.WriteString("/>\n")
}

// text draws escaped text; anchor is start, middle or end.
func (s *svg) text(class string, x, y int, anchor, t string) {
	s.b.WriteString(`<text class="` + cls(class) + `" x="` + strconv.Itoa(x) + `" y="` + strconv.Itoa(y) +
		`" text-anchor="` + anchor + `">` + esc(t) + "</text>\n")
}

// esc makes trace text safe in an SVG document: control characters come
// out Go-quoted (termsafe.String — XML cannot hold most of them, and a
// terminal showing the file must not run them), then the XML specials
// are escaped. Every string drawn passes through here.
func esc(s string) string {
	s = termsafe.String(s)
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&#39;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// short cuts s to maxLabel runes, marking the cut with "…".
func short(s string) string {
	if utf8.RuneCountInString(s) <= maxLabel {
		return s
	}
	r := []rune(s)
	return string(r[:maxLabel-1]) + "…"
}
