package tui

import (
	"fmt"
	"slices"
	"strings"
	"text/tabwriter"

	"iormesh/internal/viewmodel"
)

// What each screen lists and what its detail pane says about the
// selected row. Rows and details are built from the (terminal-safe)
// view model on every frame: the model is bounded (100 conversations,
// capped lists), so there is no cache to keep in step with it.

// stampLayout is a conversation's start in a row: its own host's clock,
// UTC, to the millisecond (the date is in the title and the detail).
const stampLayout = "15:04:05.000"

// listing is one screen's rows: a column header, the rows aligned, and
// for each row the index of the model element it shows.
type listing struct {
	head string
	rows []string
	idx  []int
}

// listing returns screen s's rows after its scope and text filter.
func (m *Model) listing(s screen) listing {
	var head []string
	var cells [][]string
	var idx []int
	switch s {
	case scrServices:
		head, cells, idx = m.serviceRows()
	case scrEdges:
		head, cells, idx = m.edgeRows()
	case scrConversations:
		head, cells, idx = m.conversationRows()
	case scrSequence:
		head, cells, idx = m.stepRows()
	case scrThreads:
		head, cells, idx = m.threadRows()
	case scrProcesses:
		head, cells, idx = m.processRows()
	default:
		return listing{}
	}
	l := align(head, cells, idx)
	return l.filtered(m.panes[s].filter)
}

// align lays cells out in columns, as the text views' tabwriter does.
func align(head []string, cells [][]string, idx []int) listing {
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, strings.Join(head, "\t"))
	for _, c := range cells {
		_, _ = fmt.Fprintln(tw, strings.Join(c, "\t"))
	}
	_ = tw.Flush() // a strings.Builder does not fail
	lines := strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
	return listing{head: lines[0], rows: lines[1:], idx: idx}
}

// filtered keeps the rows holding f, case-insensitively.
func (l listing) filtered(f string) listing {
	if f == "" {
		return l
	}
	f = strings.ToLower(f)
	out := listing{head: l.head}
	for i, r := range l.rows {
		if strings.Contains(strings.ToLower(r), f) {
			out.rows, out.idx = append(out.rows, r), append(out.idx, l.idx[i])
		}
	}
	return out
}

// selected is the model index of screen s's selected row, -1 without
// rows.
func (m *Model) selected(s screen) int {
	l := m.listing(s)
	c := m.panes[s].cursor
	if c < 0 || c >= len(l.idx) {
		return -1
	}
	return l.idx[c]
}

func (m *Model) serviceRows() ([]string, [][]string, []int) {
	cells := make([][]string, 0, len(m.vm.Services))
	idx := make([]int, 0, len(m.vm.Services))
	for i, s := range m.vm.Services {
		mark := ""
		if s.Unresolved > 0 {
			mark = "unresolved"
		}
		cells = append(cells, []string{s.Name, fmt.Sprint(s.Conns), fmt.Sprint(s.Requests), dash(s.TLS), mark})
		idx = append(idx, i)
	}
	return []string{"SERVICE", "CONNS", "REQS", "TLS", ""}, cells, idx
}

// edgeRows lists the traced edges, then the routed edges and the route
// flags (routes.go).
func (m *Model) edgeRows() ([]string, [][]string, []int) {
	cells := make([][]string, 0, len(m.vm.Edges))
	idx := make([]int, 0, len(m.vm.Edges))
	for i, e := range m.vm.Edges {
		cells = append(cells, []string{e.From, "->", e.To, e.State, fmt.Sprint(e.Conns)})
		idx = append(idx, i)
	}
	cells, idx = m.routeRows(cells, idx)
	return []string{"FROM", "", "TO", "STATE", "CONNS"}, cells, idx
}

// conversationRows lists the conversations in the current scope.
func (m *Model) conversationRows() ([]string, [][]string, []int) {
	var cells [][]string
	var idx []int
	for i, c := range m.vm.Conversations {
		if !m.scope.holds(c) {
			continue
		}
		cells = append(cells, []string{c.ID, c.Start.Format(stampLayout), c.Entry, c.Label,
			fmt.Sprintf("%d hops", c.Hops)})
		idx = append(idx, i)
	}
	return []string{"ID", "START", "ENTRY", "REQUEST", "HOPS"}, cells, idx
}

// stepRows lists the sequence screen's conversation, one row per step:
// its number, ordering and the text view's step line.
func (m *Model) stepRows() ([]string, [][]string, []int) {
	c, ok := m.conversation()
	if !ok {
		return []string{"#", "ORDERING", "STEP"}, nil, nil
	}
	cells := make([][]string, 0, len(c.Steps))
	idx := make([]int, 0, len(c.Steps))
	for i, s := range c.Steps {
		cells = append(cells, []string{fmt.Sprint(i + 1), s.Ordering, viewmodel.StepText(c, s)})
		idx = append(idx, i)
	}
	return []string{"#", "ORDERING", "STEP " + c.ID + " " + c.Label}, cells, idx
}

func (m *Model) threadRows() ([]string, [][]string, []int) {
	cells := make([][]string, 0, len(m.vm.Threads))
	idx := make([]int, 0, len(m.vm.Threads))
	for i, t := range m.vm.Threads {
		note := ""
		if t.Carrier {
			note = "virtual"
		}
		cells = append(cells, []string{viewmodel.LifelineName(t.Lifeline), t.Host,
			fmt.Sprintf("%d/%d", t.PID, t.TID), fmt.Sprint(len(t.Entries) + t.MoreEntries), note})
		idx = append(idx, i)
	}
	return []string{"THREAD", "HOST", "PID/TID", "STEPS", ""}, cells, idx
}

func (m *Model) processRows() ([]string, [][]string, []int) {
	cells := make([][]string, 0, len(m.vm.Processes))
	idx := make([]int, 0, len(m.vm.Processes))
	for i, p := range m.vm.Processes {
		restart := ""
		if p.Incarnations > 1 {
			restart = fmt.Sprintf("restarted (%d)", p.Incarnations)
		}
		cells = append(cells, []string{p.Host, fmt.Sprintf("%s[%d]", p.Comm, p.PID), p.Service, restart})
		idx = append(idx, i)
	}
	return []string{"HOST", "PROCESS", "SERVICE", ""}, cells, idx
}

// conversation is the sequence screen's conversation: the one opened,
// else the newest.
func (m *Model) conversation() (viewmodel.Conversation, bool) {
	cs := m.vm.Conversations
	if len(cs) == 0 {
		return viewmodel.Conversation{}, false
	}
	if m.conv < 0 || m.conv >= len(cs) {
		return cs[0], true
	}
	return cs[m.conv], true
}

// holds reports whether c is in the scope: it touches the service, or
// has a request from the edge's client to its server.
func (sc scope) holds(c viewmodel.Conversation) bool {
	switch {
	case sc.service != "":
		return slices.Contains(c.Services, sc.service)
	case sc.from != "":
		return slices.ContainsFunc(c.Steps, func(s viewmodel.Step) bool {
			return s.Kind == viewmodel.StepRequest && s.Service == sc.from && s.To == sc.to
		})
	}
	return true
}

// String names the scope for the conversations screen's header.
func (sc scope) String() string {
	switch {
	case sc.service != "":
		return "touching " + sc.service
	case sc.from != "":
		return sc.from + " -> " + sc.to
	}
	return ""
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
