package tui

import (
	"fmt"
	"strings"

	"iormesh/internal/viewmodel"
)

// The detail pane: what the selected row is, in full. Where a text view
// already prints it (`show sequence`, `show thread`, `show processes`,
// `show health`), the pane is that view's text, so the TUI and `show`
// read alike; services, edges and steps get a block of their own built
// from the same fields.

// maxListed caps the conversations a service or edge detail names; the
// full list is one ⏎ away.
const maxListed = 5

// detail is screen s's detail text for the selected row.
func (m *Model) detail(s screen) string {
	if s == scrHealth {
		return m.healthText()
	}
	i := m.selected(s)
	if i < 0 {
		return m.emptyText(s) + m.partialNote(s)
	}
	var b strings.Builder
	switch s {
	case scrServices:
		m.serviceDetail(&b, m.vm.Services[i])
	case scrEdges:
		m.edgesDetail(&b, i)
	case scrConversations:
		_ = viewmodel.WriteSequence(&b, m.vm.Conversations[i])
		b.WriteString("\n⏎ opens the sequence\n")
	case scrSequence:
		c, _ := m.conversation()
		stepDetail(&b, c, i)
	case scrThreads:
		_ = viewmodel.WriteThreads(&b, []viewmodel.ThreadStory{m.vm.Threads[i]})
		b.WriteString("\n⏎ opens its newest step's conversation\n")
	case scrProcesses:
		_ = viewmodel.WriteProcess(&b, m.vm.Processes[i])
		b.WriteString(m.partialNote(s))
	}
	return b.String()
}

// partialNote is, on the processes screen, the line `show processes`
// starts with when the rollup hit its tracking cap (task 2i4): the
// detail pane says the list is partial whichever process is selected,
// or none. "" elsewhere and for a complete rollup.
func (m *Model) partialNote(s screen) string {
	l := viewmodel.PartialLine(m.vm)
	if s != scrProcesses || l == "" {
		return ""
	}
	return "\n" + l + "\n"
}

// emptyText says why a screen has nothing to select.
func (m *Model) emptyText(s screen) string {
	switch {
	case m.panes[s].filter != "":
		return "no row matches the filter (esc in / clears it)"
	case (s == scrConversations || s == scrSequence) && len(m.vm.Conversations) == 0:
		return "the build holds no conversations (no http events)"
	case s == scrConversations && m.scope != (scope{}):
		return "no kept conversation is " + m.scope.String()
	}
	return "nothing in this build"
}

// healthText is the summary `build` prints, then `show health`.
func (m *Model) healthText() string {
	var b strings.Builder
	_ = viewmodel.WriteSummary(&b, m.vm)
	b.WriteString("\n")
	_ = viewmodel.WriteHealth(&b, m.vm)
	return b.String()
}

// serviceDetail is one service: names, counts, paths, its edges and the
// newest conversations touching it.
func (m *Model) serviceDetail(b *strings.Builder, s viewmodel.Service) {
	named := "unnamed (no naming rule)"
	if s.Named {
		named = "named by a rule"
	}
	fmt.Fprintf(b, "service    %s  %s\n", s.Name, named)
	fmt.Fprintf(b, "endpoints  %s\n", dash(strings.Join(s.Endpoints, ", ")))
	fmt.Fprintf(b, "conns      %d (%d unresolved), TLS %s\n", s.Conns, s.Unresolved, dash(s.TLS))
	fmt.Fprintf(b, "requests   %d\n", s.Requests)
	for _, p := range s.TopPaths {
		fmt.Fprintf(b, "  %-6s %s  %d\n", p.Method, p.Path, p.Count)
	}
	if s.MorePaths > 0 {
		fmt.Fprintf(b, "  truncated: %d more paths\n", s.MorePaths)
	}
	for _, e := range m.vm.Edges {
		if e.From == s.Name {
			fmt.Fprintf(b, "calls      %s (%s, %d conns)\n", e.To, e.State, e.Conns)
		}
		if e.To == s.Name {
			fmt.Fprintf(b, "called by  %s (%s, %d conns)\n", e.From, e.State, e.Conns)
		}
	}
	m.servicesRoutes(b, s.Name)
	m.listConversations(b, scope{service: s.Name})
}

// edgeDetail is one edge: its state and counts, then the newest
// conversations with a request along it.
func (m *Model) edgeDetail(b *strings.Builder, e viewmodel.Edge) {
	fmt.Fprintf(b, "edge       %s -> %s\n", e.From, e.To)
	fmt.Fprintf(b, "state      %s: %d conns, %d unresolved\n", e.State, e.Conns, e.Unresolved)
	if !e.First.IsZero() {
		fmt.Fprintf(b, "seen       %s - %s UTC\n", e.First.Format(timeLayout), e.Last.Format(timeLayout))
	}
	m.listConversations(b, scope{from: e.From, to: e.To})
}

// timeLayout is a timestamp in a detail: UTC, to the second.
const timeLayout = "2006-01-02 15:04:05"

// listConversations names the newest conversations in sc.
func (m *Model) listConversations(b *strings.Builder, sc scope) {
	n := 0
	for _, c := range m.vm.Conversations {
		if !sc.holds(c) {
			continue
		}
		if n == 0 {
			b.WriteString("\nconversations (newest first; ⏎ lists them all)\n")
		}
		if n < maxListed {
			fmt.Fprintf(b, "  %s  %s  %s  %d hops\n", c.ID, c.Start.Format(stampLayout), c.Label, c.Hops)
		}
		n++
	}
	if n > maxListed {
		fmt.Fprintf(b, "  … %d more\n", n-maxListed)
	}
	if n == 0 {
		b.WriteString("\nno kept conversation\n")
	}
}

// stepDetail is one step of c in full: its lifelines, how it is ordered
// and joined, sizes, the whole Java stack and the query that prints its
// raw event — the bottom of the walk from the mesh. A folded run's
// whole-run query is printed as built (viewmodel.Safe leaves its shell
// quoting intact, task 1i4), ready to paste. The ref is built from
// segment file names a traced host chose, so its raw-event query quotes
// it as one inert shell word (viewmodel.ShellWord, task 3i4), as the
// site's does; segment.ParseName already refuses names outside a safe
// set, and the quoting keeps the pasted line inert whatever reaches it.
func stepDetail(b *strings.Builder, c viewmodel.Conversation, i int) {
	s := c.Steps[i]
	fmt.Fprintf(b, "step       %d of conversation %s (%s)\n", i+1, c.ID, s.Kind)
	fmt.Fprintf(b, "what       %s\n", s.Label)
	if s.Lane >= 0 && s.Lane < len(c.Lifelines) {
		fmt.Fprintf(b, "on         %s\n", viewmodel.LifelineDetail(c.Lifelines[s.Lane]))
	}
	if s.ToLane >= 0 && s.ToLane < len(c.Lifelines) {
		fmt.Fprintf(b, "to         %s\n", viewmodel.LifelineDetail(c.Lifelines[s.ToLane]))
	}
	fmt.Fprintf(b, "ordering   %s\n", orderingText(s.Ordering))
	if s.Hop != "" {
		fmt.Fprintf(b, "hop        %s\n", s.Hop)
	}
	run := s.Count > 1 // a folded run: sums, and its first call's stack and ref
	if s.Bytes > 0 {
		fmt.Fprintf(b, "bytes      %d%s\n", s.Bytes, ifRun(run, fmt.Sprintf(" (%d calls)", s.Count)))
	}
	if s.Latency > 0 {
		fmt.Fprintf(b, "latency    %s%s\n", s.Latency, ifRun(run, fmt.Sprintf(" (sum of %d calls)", s.Count)))
	}
	fmt.Fprintf(b, "java stack (top first%s)\n", ifRun(run, ", first call"))
	for _, f := range s.Stack {
		fmt.Fprintf(b, "  at %s\n", f)
	}
	if len(s.Stack) == 0 {
		b.WriteString("  none recorded\n")
	}
	fmt.Fprintf(b, "ref        %s%s\n", s.Ref, ifRun(run, fmt.Sprintf(" (first of %d)", s.Count)))
	fmt.Fprintf(b, "raw event  iormeshctl query -ref %s\n", viewmodel.ShellWord(s.Ref))
	if run {
		fmt.Fprintf(b, "last ref   %s\n", s.LastRef)
		fmt.Fprintf(b, "whole run  %s\n", s.RunQuery)
	}
	if s.Lane >= 0 && s.Lane < len(c.Lifelines) && c.Lifelines[s.Lane].Thread != "" {
		b.WriteString("\n⏎ opens this thread's story\n")
	}
}

// ifRun is note when the step is a folded run (viewmodel.Step.Count >
// 1, task vg4), else "": its bytes and latency are sums, its stack and
// ref its first call's, and each field says so.
func ifRun(run bool, note string) string {
	if run {
		return note
	}
	return ""
}

// orderingText says what an ordering means, so a time-ordered or
// unordered step is never read as causal.
func orderingText(o string) string {
	switch o {
	case viewmodel.OrderCausal:
		return o + " (thread order or a paired send -> receive)"
	case viewmodel.OrderTime:
		return o + " (the clocks prove it came later; no causal edge)"
	case viewmodel.OrderNone:
		return o + " (neither causality nor the clocks prove its place)"
	}
	return o
}
