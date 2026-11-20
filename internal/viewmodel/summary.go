package viewmodel

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"iormesh/internal/routemap"
)

// The plain-text renderer (docs/visualization.md "Plain text"): what
// `build` prints and `iormeshctl show` prints again. Plain lines on
// every output for now — piped, pasted into an LLM, diffed against a
// golden; terminal colors and box characters come with M5's renderers
// (durations print in Go's notation, "740µs"). Every line is a pure
// function of the model, so text goldens work.

// timeLayout is the summary's timestamp: UTC, to the second.
const timeLayout = "2006-01-02 15:04:05"

// WriteSummary prints the run summary: one line per aspect, then one
// line per unresolved edge, session that recorded no end, bad segment
// and stray store entry — what needs the operator's eye.
func WriteSummary(w io.Writer, m Model) error {
	m = safeModel(m)
	var b strings.Builder
	r := m.Run
	fmt.Fprintf(&b, "built      %s: %d hosts, %d sessions, %d segments, %d events%s\n",
		strings.Join(r.Projects, "+"), len(r.Hosts), r.Sessions, r.Segments, r.Events, span(r.First, r.Last))
	fused, mixed, unresolved := edgeStates(m.Edges)
	fmt.Fprintf(&b, "merged     %d services, %d edges (%d fused, %d mixed, %d unresolved); "+
		"tcp %d fused, %d unresolved\n", len(m.Services), len(m.Edges), fused, mixed, unresolved,
		r.FusedConns, r.UnresolvedConns)
	h := m.Health
	fmt.Fprintf(&b, "health     %d lost, %d skipped runs, %d unattributed, %d attach gap; %s\n",
		h.Lost, h.SkippedRuns, h.Unattributed, h.AttachGap, clockHeadline(h.Clocks))
	for _, c := range h.Clocks {
		fmt.Fprintf(&b, "clock      %s ~ %s: %s\n", c.HostA, c.HostB, clockLine(c))
	}
	fmt.Fprintf(&b, "sequence   %s\n", conversationsLine(m))
	for _, s := range h.Sessions {
		if s.End == "" {
			fmt.Fprintf(&b, "session    %s/%s %s: no end recorded (running, or cut short)\n",
				s.Project, s.Host, s.ID)
		}
	}
	for _, e := range m.Edges {
		if e.State != StateFused {
			fmt.Fprintf(&b, "unresolved %s -> %s (%d of %d connections)\n", e.From, e.To, e.Unresolved, e.Conns)
		}
	}
	writeRoutes(&b, m.Routes, h.RouteFlags)
	writeBad(&b, h)
	_, err := io.WriteString(w, b.String())
	return err
}

// writeRoutes prints one line per routed edge — its hops joined by
// arrows, then its basis — and one per route flag.
func writeRoutes(b *strings.Builder, rs []Route, fs []RouteFlag) {
	for _, r := range rs {
		hops := []string{r.From}
		for _, h := range []string{r.FQDN, r.Ingress, r.Service, r.Workload, r.Pod} {
			if h != "" {
				hops = append(hops, h)
			}
		}
		fmt.Fprintf(b, "routed     %s (%s, %d connections%s)\n", strings.Join(hops, " -> "), r.Basis,
			r.Conns, endpointsNote(r))
	}
	for _, f := range fs {
		fmt.Fprintf(b, "route flag %s %s: %s", f.Kind, f.Subject, f.Detail)
		if f.Conns > 0 {
			fmt.Fprintf(b, " (%d connections)", f.Conns)
		}
		b.WriteString("\n")
	}
}

// endpointsNote says why an untraced Service's edge names no pod: the
// map lists several endpoints, and the traces cannot say which one
// answered.
func endpointsNote(r Route) string {
	if r.Basis == routemap.BasisService && r.Pod == "" && r.Endpoints > 1 {
		return fmt.Sprintf("; %d endpoints, pod unknown", r.Endpoints)
	}
	return ""
}

// WriteServices prints one line per service.
func WriteServices(w io.Writer, m Model) error {
	m = safeModel(m)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SERVICE\tNAMED\tCONNS\tUNRESOLVED\tTLS\tREQUESTS\tENDPOINTS")
	for _, s := range m.Services {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%s\t%d\t%s\n", s.Name, yesNo(s.Named), s.Conns,
			s.Unresolved, dash(s.TLS), s.Requests, dash(strings.Join(s.Endpoints, " ")))
	}
	return tw.Flush()
}

// WriteEdges prints one line per edge; unresolvedOnly keeps the edges
// with an unresolved connection.
func WriteEdges(w io.Writer, m Model, unresolvedOnly bool) error {
	m = safeModel(m)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "FROM\tTO\tSTATE\tCONNS\tUNRESOLVED\tFIRST\tLAST")
	for _, e := range m.Edges {
		if unresolvedOnly && e.State == StateFused {
			continue
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\t%s\n", e.From, e.To, e.State, e.Conns,
			e.Unresolved, stamp(e.First), stamp(e.Last))
	}
	return tw.Flush()
}

// WriteHealth prints every session's end, every clock pair and what was
// skipped.
func WriteHealth(w io.Writer, m Model) error {
	m = safeModel(m)
	var b strings.Builder
	h := m.Health
	fmt.Fprintf(&b, "loss       %d lost, %d skipped runs, %d unattributed, %d attach gap\n",
		h.Lost, h.SkippedRuns, h.Unattributed, h.AttachGap)
	for _, s := range h.Sessions {
		fmt.Fprintf(&b, "session    %s/%s %s %s: %d segments, %d events%s, end: %s\n", s.Project, s.Host,
			s.FQDN, s.ID, s.Segments, s.Events, span(s.First, s.Last), dash(s.End))
	}
	for _, c := range h.Clocks {
		fmt.Fprintf(&b, "clock      %s ~ %s: %s\n", c.HostA, c.HostB, clockLine(c))
	}
	writeBad(&b, h)
	_, err := io.WriteString(w, b.String())
	return err
}

// conversationsLine counts the conversations and how their steps are
// ordered: the share that is not causal is what a reader must not
// over-read.
func conversationsLine(m Model) string {
	var steps, timed, unordered int
	for _, c := range m.Conversations {
		for _, s := range c.Steps {
			steps++
			switch s.Ordering {
			case OrderTime:
				timed++
			case OrderNone:
				unordered++
			}
		}
	}
	line := fmt.Sprintf("%d conversations, %d steps (%d time-ordered, %d unordered)",
		len(m.Conversations), steps, timed, unordered)
	if m.MoreConversations > 0 || m.Health.RequestsCut > 0 {
		line += fmt.Sprintf("; truncated: %d older conversations, %d requests",
			m.MoreConversations, m.Health.RequestsCut)
	}
	return line
}

// writeBad lists the skipped segments and stray entries.
func writeBad(b *strings.Builder, h Health) {
	for _, s := range h.BadSegments {
		fmt.Fprintf(b, "bad        %s: %s\n", s.Name, s.Error)
	}
	for _, s := range h.Ignored {
		fmt.Fprintf(b, "ignored    %s\n", s)
	}
}

func edgeStates(edges []Edge) (fused, mixed, unresolved int) {
	for _, e := range edges {
		switch e.State {
		case StateFused:
			fused++
		case StateMixed:
			mixed++
		default:
			unresolved++
		}
	}
	return fused, mixed, unresolved
}

// clockHeadline is the widest two-sided bound over every pair, the
// figure that says how far cross-host timestamps compare.
func clockHeadline(cs []Clock) string {
	if len(cs) == 0 {
		return "no clock bounds"
	}
	var widest time.Duration
	empty := 0
	for _, c := range cs {
		widest, empty = max(widest, c.MaxWidth), empty+c.Empty
	}
	s := fmt.Sprintf("clock bounds <= %v", widest)
	if empty > 0 {
		s += fmt.Sprintf(", %d EMPTY", empty)
	}
	return s
}

func clockLine(c Clock) string {
	s := fmt.Sprintf("%d windows", c.Windows)
	if c.MaxWidth > 0 {
		s += fmt.Sprintf(", width %v-%v", c.MinWidth, c.MaxWidth)
	}
	if c.OneSided > 0 {
		s += fmt.Sprintf(", %d one-sided", c.OneSided)
	}
	if c.Empty > 0 {
		s += fmt.Sprintf(", %d EMPTY (contradicting)", c.Empty)
	}
	return s
}

// span is ", FIRST - LAST UTC", or nothing without events.
func span(first, last time.Time) string {
	if first.IsZero() {
		return ""
	}
	return fmt.Sprintf(", %s - %s UTC", first.Format(timeLayout), last.Format(timeLayout))
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format(timeLayout)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
