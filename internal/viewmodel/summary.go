package viewmodel

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"iormesh/internal/routemap"
	"iormesh/internal/schema"
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
// line per session that recorded no end or ended abnormally, unresolved
// edge, bad segment and stray store entry, and one line when connections
// are pending (Run.PendingConns) — what needs the operator's eye.
func WriteSummary(w io.Writer, m Model) error {
	m = safeModel(m)
	var b strings.Builder
	r := m.Run
	fmt.Fprintf(&b, "built      %s: %d hosts, %d sessions, %d segments, %d events%s\n",
		strings.Join(r.Projects, "+"), len(r.Hosts), r.Sessions, r.Segments, r.Events, span(r.First, r.Last))
	fused, mixed, unresolved := edgeStates(m.Edges)
	fmt.Fprintf(&b, "merged     %d services, %d edges (%d fused, %d mixed, %d unresolved); "+
		"tcp %d fused, %d unresolved%s\n", len(m.Services), len(m.Edges), fused, mixed, unresolved,
		r.FusedConns, r.UnresolvedConns, pendingNote(r.PendingConns))
	h := m.Health
	fmt.Fprintf(&b, "health     %d lost, %d skipped runs, %d unattributed, %d attach gap; %s\n",
		h.Lost, h.SkippedRuns, h.Unattributed, h.AttachGap, clockHeadline(h.Clocks))
	for _, c := range h.Clocks {
		fmt.Fprintf(&b, "clock      %s ~ %s: %s\n", c.HostA, c.HostB, clockLine(c))
	}
	fmt.Fprintf(&b, "sequence   %s\n", conversationsLine(m))
	writeSessions(&b, h.Sessions)
	for _, e := range m.Edges {
		if e.State != StateFused {
			fmt.Fprintf(&b, "unresolved %s -> %s (%d of %d connections)\n", e.From, e.To, e.Unresolved, e.Conns)
		}
	}
	if r.PendingConns > 0 {
		fmt.Fprintf(&b, "pending    %d connections in the build's last minute: their far end's "+
			"segment is not collected yet; collect and build again\n", r.PendingConns)
	}
	writeRoutes(&b, m.Routes, h.RouteFlags)
	writeBad(&b, h)
	_, err := io.WriteString(w, b.String())
	return err
}

// pendingNote is the tcp count's pending part, empty when none: a build
// with nothing pending prints the line it always printed.
func pendingNote(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(", %d pending", n)
}

// writeSessions prints one line per session the operator must look at:
// one that recorded no end, and one whose end reason is not a normal
// one (normalEnd) — the disk watchdog's disk_limit, a tracer error, or
// a reason this build does not know. docs/milestones.md M4 wants the exit
// reason in the summary, not only in `show health`: a run the watchdog cut
// short must not read like a complete one. A normal end stays out, so
// the summary still lists only what needs the operator's eye.
func writeSessions(b *strings.Builder, ss []Session) {
	for _, s := range ss {
		switch {
		case s.End == "":
			fmt.Fprintf(b, "session    %s/%s %s: no end recorded (running, or cut short)\n",
				s.Project, s.Host, s.ID)
		case !normalEnd(s.End):
			fmt.Fprintf(b, "session    %s/%s %s: ended %s%s\n", s.Project, s.Host, s.ID, s.End, endNote(s.End))
		}
	}
}

// normalEnd reports whether a session that ended with reason r ran its
// course: the -duration bound, a user or controller stop, or every
// target exited (docs/schema.md, "end").
func normalEnd(r string) bool {
	switch r {
	case schema.EndDuration, schema.EndStop, schema.EndTargetsExited:
		return true
	}
	return false
}

// endNote says, in parentheses, what an abnormal end means for the
// trace; nothing for a reason it does not know.
func endNote(r string) string {
	switch r {
	case schema.EndDiskLimit:
		return " (the disk watchdog stopped the tracer)"
	case schema.EndError:
		return " (the tracer failed; the trace is incomplete)"
	}
	return ""
}

// writeRoutes prints one line per routed edge — its hops joined by
// arrows, then its basis — and one per route flag.
func writeRoutes(b *strings.Builder, rs []Route, fs []RouteFlag) {
	for _, r := range rs {
		fmt.Fprintf(b, "routed     %s (%s)\n", strings.Join(r.Hops(), " -> "), RouteNote(r))
	}
	for _, f := range fs {
		fmt.Fprintf(b, "route flag %s\n", FlagText(f))
	}
}

// Hops are r's hops in order — From, FQDN, ingress, Service, workload,
// pod — the empty ones left out: the chain every surface draws.
func (r Route) Hops() []string {
	hops := []string{r.From}
	for _, h := range []string{r.FQDN, r.Ingress, r.Service, r.Workload, r.Pod} {
		if h != "" {
			hops = append(hops, h)
		}
	}
	return hops
}

// RouteNote is what a routed edge stands on and carries: its basis, its
// connections and, for an untraced Service of several endpoints, that
// the pod is unknown. The summary prints it in parentheses; the
// renderers beside the drawn chain.
func RouteNote(r Route) string {
	return fmt.Sprintf("%s, %d connections%s", r.Basis, r.Conns, endpointsNote(r))
}

// FlagText is one route flag as the summary prints it after "route
// flag": kind, subject, detail and the connections it covers.
func FlagText(f RouteFlag) string {
	s := fmt.Sprintf("%s %s: %s", f.Kind, f.Subject, f.Detail)
	if f.Conns > 0 {
		s += fmt.Sprintf(" (%d connections)", f.Conns)
	}
	return s
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
// figure that says how far cross-host timestamps compare. Only a pair
// with a two-sided, non-empty window has a bound: one-sided and empty
// windows leave MaxWidth at zero, and printing that as "<= 0s" would
// claim the tightest agreement exactly when cross-host order cannot be
// proven. So with no bounded pair the headline says so, and with some
// it counts the pairs left without a bound.
func clockHeadline(cs []Clock) string {
	if len(cs) == 0 {
		return "no clock bounds"
	}
	var widest time.Duration
	empty, unbounded := 0, 0
	for _, c := range cs {
		empty += c.Empty
		if c.Windows-c.OneSided-c.Empty <= 0 {
			unbounded++
			continue
		}
		widest = max(widest, c.MaxWidth)
	}
	var s string
	switch {
	case unbounded == len(cs):
		s = "no two-sided clock bound"
	case unbounded == 1:
		s = fmt.Sprintf("clock bounds <= %v, 1 pair without a bound", widest)
	case unbounded > 1:
		s = fmt.Sprintf("clock bounds <= %v, %d pairs without a bound", widest, unbounded)
	default:
		s = fmt.Sprintf("clock bounds <= %v", widest)
	}
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
