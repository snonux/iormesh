package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"iormesh/internal/termsafe"
)

// Drawing a frame. A frame is exactly the terminal's height and no line
// is wider than the terminal: fit cuts every line to the width (ANSI-
// and wide-rune-aware) and pads it, and the body gets the rows between
// the title, the tab bar and the footer. Wide terminals show the list
// and the detail side by side, narrow ones one above the other.

// Layout bounds.
const (
	// minWidth and minHeight are the smallest frame worth drawing;
	// below them the explorer says so instead.
	minWidth, minHeight = 20, 6
	// sideBySide is the width from which list and detail share the rows.
	sideBySide = 100
	// chrome is the rows that are not body: title, tabs, footer.
	chrome = 3
)

// styles are the explorer's few text styles. Styling is applied after a
// line is cut to the width, and only to text that is already
// terminal-safe.
type styles struct {
	title, tab, head, sel, selDim, faint lipgloss.Style
}

func newStyles() styles {
	s := lipgloss.NewStyle()
	return styles{title: s.Bold(true), tab: s.Reverse(true), head: s.Bold(true).Underline(true),
		sel: s.Reverse(true), selDim: s.Underline(true), faint: s.Faint(true)}
}

// Render is the frame as text, for View and for tests.
func (m *Model) Render() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	if m.width < minWidth || m.height < minHeight {
		return fit("terminal too small", m.width)
	}
	lines := make([]string, 0, m.height)
	lines = append(lines, m.st.title.Render(fit(m.title(), m.width)), m.tabs())
	switch {
	case m.help:
		lines = append(lines, m.helpLines()...)
	case m.cur == scrHealth:
		lines = append(lines, m.detailLines(scrHealth, m.width, m.bodyHeight())...)
	default:
		lines = append(lines, m.body()...)
	}
	return strings.Join(append(lines, m.footer()), "\n")
}

// title names the run: projects, its time span, a windowed build's
// window (task hk4), its size.
func (m *Model) title() string {
	r := m.vm.Run
	t := "iormesh · " + dash(strings.Join(r.Projects, "+"))
	if !r.First.IsZero() {
		t += fmt.Sprintf(" · %s – %s UTC", r.First.Format(timeLayout), r.Last.Format("15:04:05"))
	}
	if r.Window != "" { // only a windowed build: a full build's title is as before
		t += " · window " + r.Window
	}
	t += fmt.Sprintf(" · %d services · %d edges", len(m.vm.Services), len(m.vm.Edges))
	if n := len(m.vm.Routes); n > 0 { // only with routes: a routeless title is as before
		t += fmt.Sprintf(" · %d routed", n)
	}
	return t + fmt.Sprintf(" · %d conversations", len(m.vm.Conversations))
}

// tabs is the screen bar, the current screen highlighted; short names
// when the full ones do not fit.
func (m *Model) tabs() string {
	names := screenNames
	if tabsWidth(names) > m.width {
		names = screenShort
	}
	var b strings.Builder
	for i, n := range names {
		label := fmt.Sprintf(" %d %s ", i+1, n)
		if screen(i) == m.cur {
			label = m.st.tab.Render(label)
		}
		b.WriteString(label)
	}
	return fit(b.String(), m.width)
}

func tabsWidth(names [numScreens]string) int {
	w := 0
	for _, n := range names {
		w += len(n) + 4
	}
	return w
}

// footer is the filter being typed, else the keys.
func (m *Model) footer() string {
	p := m.panes[m.cur]
	var f string
	switch {
	case m.filtering:
		f = "/" + termsafe.String(p.filter) + "_   ⏎ keep · esc clear"
	case m.help:
		f = "any key closes the help"
	case m.note != "":
		f = m.note + "   (any key goes on)"
	default:
		f = "↑↓ move · ⏎ open · esc back · tab detail · / filter · 1-7 screens · ? help · q quit"
		if p.filter != "" {
			f = "filter " + termsafe.String(p.filter) + " · " + f
		}
	}
	return m.st.faint.Render(fit(f, m.width))
}

// helpLines is the key help, filling the body.
func (m *Model) helpLines() []string {
	help := []string{
		"↑ ↓  k j      move (in the detail when it has the focus)",
		"pgup pgdn     page;  g G / home end: first, last",
		"⏎             open: service or edge -> its conversations -> sequence",
		"              -> a step's thread story -> its newest conversation",
		"esc ⌫         back; drops a conversations scope",
		"tab           focus the list or the detail (scroll a long stack)",
		"/             filter the rows (⏎ keep, esc clear)",
		"1-7 s h       screens: services, edges, conversations, sequence,",
		"              threads, processes, health",
		"q ctrl+c      quit",
		"",
		"A step's detail holds its whole Java stack and the",
		"`iormeshctl query -ref` that prints its raw event.",
	}
	out := make([]string, 0, m.bodyHeight())
	for i := range m.bodyHeight() {
		l := ""
		if i < len(help) {
			l = help[i]
		}
		out = append(out, fit(l, m.width))
	}
	return out
}

// body is the list and the detail, side by side or stacked.
func (m *Model) body() []string {
	lw, lh, dw, dh := m.layout()
	list, det := m.listLines(lw, lh), m.detailLines(m.cur, dw, dh)
	out := make([]string, 0, m.bodyHeight())
	if m.width >= sideBySide {
		for i := range list {
			out = append(out, list[i]+m.st.faint.Render("│")+det[i])
		}
		return out
	}
	out = append(out, list...)
	out = append(out, m.st.faint.Render(strings.Repeat("─", m.width)))
	return append(out, det...)
}

// layout is the list's and the detail's width and height.
func (m *Model) layout() (lw, lh, dw, dh int) {
	h := m.bodyHeight()
	if m.width >= sideBySide {
		lw = m.width * 2 / 5
		return lw, h, m.width - lw - 1, h
	}
	lh = max(2, (h-1)/2)
	return m.width, lh, m.width, max(0, h-lh-1)
}

func (m *Model) bodyHeight() int { return max(0, m.height-chrome) }

// listLines is the column header and the visible rows, the cursor's
// highlighted (underlined only while the detail has the focus).
func (m *Model) listLines(w, h int) []string {
	l, p := m.listing(m.cur), m.panes[m.cur]
	head := l.head
	if m.cur == scrConversations && m.scope != (scope{}) {
		head = "conversations " + m.scope.String() + " (esc: all)"
	}
	// One column of indent, as the rows have, so the header sits over
	// its columns.
	out := []string{m.st.head.Render(fit(" "+head, w))}
	for i := p.offset; i < len(l.rows) && len(out) < h; i++ {
		row := fit(" "+l.rows[i], w)
		switch {
		case i != p.cursor:
		case m.detailFocus:
			row = m.st.selDim.Render(row)
		default:
			row = m.st.sel.Render(row)
		}
		out = append(out, row)
	}
	for len(out) < h {
		out = append(out, fit("", w))
	}
	return out[:h]
}

// detailLines is screen s's detail, wrapped to w, from its scroll, h
// lines.
func (m *Model) detailLines(s screen, w, h int) []string {
	lines := wrapped(m.detail(s), w)
	out := make([]string, 0, h)
	for i := m.panes[s].scroll; i < len(lines) && len(out) < h; i++ {
		out = append(out, fit(lines[i], w))
	}
	for len(out) < h {
		out = append(out, fit("", w))
	}
	return out
}

// wrapped is text wrapped to w columns, as lines.
func wrapped(text string, w int) []string {
	if w <= 0 {
		return nil
	}
	return strings.Split(strings.TrimRight(ansi.Wrap(text, w, ""), "\n"), "\n")
}

// fit cuts s to w columns and pads it to exactly w.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "…")
	return s + strings.Repeat(" ", max(0, w-ansi.StringWidth(s)))
}

// clamp keeps the current screen's cursor on a row, the list scrolled
// to it, and the detail's scroll inside its text.
func (m *Model) clamp() {
	p := &m.panes[m.cur]
	n := len(m.listing(m.cur).rows)
	p.cursor = max(0, min(p.cursor, n-1))
	if m.width < minWidth || m.height < minHeight {
		return
	}
	_, lh, dw, dh := m.layout()
	if m.cur == scrHealth || m.help {
		dw, dh = m.width, m.bodyHeight()
	}
	vis := max(1, lh-1) // the header takes a row
	p.offset = max(0, min(p.offset, p.cursor), p.cursor-vis+1)
	lines := len(wrapped(m.detail(m.cur), dw))
	p.scroll = max(0, min(p.scroll, lines-dh))
}
