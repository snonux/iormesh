package viewmodel

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"

	"iormesh/internal/termsafe"
)

// The text views of conversations: `show conversations` lists them,
// `show sequence [ID]` prints one as numbered steps — the plain-text
// form of the sequence diagram (docs/visualization.md "Plain text").
// Every step says its ordering, and a step that is not causal says why
// (its hop), so a reader never takes a time-ordered or unordered step
// for a causal one.

// msLayout is a conversation's start: UTC, to the millisecond.
const msLayout = "2006-01-02 15:04:05.000"

// ErrNoConversation is a sequence view asked for an ID the model does
// not hold.
var ErrNoConversation = errors.New("no such conversation")

// WriteConversations lists the conversations, newest first.
func WriteConversations(w io.Writer, m Model) error {
	m = safeModel(m)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tSTART\tENTRY\tREQUEST\tHOPS\tSTEPS\tSERVICES")
	for _, c := range m.Conversations {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%s\n", c.ID, c.Start.Format(msLayout),
			c.Entry, c.Label, c.Hops, len(c.Steps)+c.MoreSteps, strings.Join(c.Services, " "))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	return writeMore(w, m)
}

// writeMore says what the model left out of the list.
func writeMore(w io.Writer, m Model) error {
	var err error
	if m.MoreConversations > 0 {
		_, err = fmt.Fprintf(w, "truncated: %d older conversations\n", m.MoreConversations)
	}
	if err == nil && m.Health.RequestsCut > 0 {
		_, err = fmt.Fprintf(w, "truncated: %d older requests read into none\n", m.Health.RequestsCut)
	}
	return err
}

// FindConversation returns the conversation id names, the newest for
// "".
func FindConversation(m Model, id string) (Conversation, error) {
	switch {
	case len(m.Conversations) == 0:
		return Conversation{}, fmt.Errorf("%w: the build holds none (no http events)", ErrNoConversation)
	case id == "":
		return m.Conversations[0], nil
	}
	i := slices.IndexFunc(m.Conversations, func(c Conversation) bool { return c.ID == id })
	if i < 0 {
		return Conversation{}, fmt.Errorf("%w: %q (iormeshctl show conversations)",
			ErrNoConversation, termsafe.String(id))
	}
	return m.Conversations[i], nil
}

// WriteSequence prints one conversation: a header, the services and the
// per-thread lifelines, then one line per step — its number, ordering,
// actor (service/thread), arrow to the peer's lifeline, label and
// extras — with the step's top Java frame and its raw event's ref
// indented below it; a folded run's says they are its first call's and
// adds its last call's ref and the query that prints the whole run.
func WriteSequence(w io.Writer, c Conversation) error {
	c = safeConversation(c)
	var b strings.Builder
	fmt.Fprintf(&b, "conversation %s  %s UTC  %s  %s  (%d hops)\n", c.ID, c.Start.Format(msLayout),
		c.Entry, c.Label, c.Hops)
	fmt.Fprintf(&b, "services     %s\n", strings.Join(c.Services, ", "))
	for i, l := range c.Lifelines {
		fmt.Fprintf(&b, "lifeline %-3d %s\n", i+1, LifelineDetail(l))
	}
	for i, s := range c.Steps {
		fmt.Fprintf(&b, "%3d  %-12s  %s\n", i+1, s.Ordering, stepLine(s, peerLane(c, s)))
		if len(s.Stack) > 0 {
			fmt.Fprintf(&b, "     %-12s    at %s%s\n", "", s.Stack[0], firstOf(s, " (first call)"))
		}
		fmt.Fprintf(&b, "     %-12s    ref %s%s\n", "", s.Ref, firstOf(s, fmt.Sprintf(" (first of %d)", s.Count)))
		if s.Count > 1 {
			fmt.Fprintf(&b, "     %-12s    last %s\n", "", s.LastRef)
			fmt.Fprintf(&b, "     %-12s    run %s\n", "", s.RunQuery)
		}
	}
	if c.MoreSteps > 0 {
		fmt.Fprintf(&b, "truncated: %d more steps\n", c.MoreSteps)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// firstOf is note for a folded run's step (Count > 1), whose stack and
// ref are its first call's, else "".
func firstOf(s Step, note string) string {
	if s.Count > 1 {
		return note
	}
	return ""
}

// LifelineName is a lifeline as a sequence view heads its column:
// "service/thread", and for a virtual thread's I/O "service/thread
// (virtual)" — the carrier's name with the mark that the work was not
// the carrier's own.
func LifelineName(l Lifeline) string {
	name := l.Service
	if l.Thread != "" {
		name += "/" + l.Thread
	}
	if l.Carrier {
		name += " (virtual)"
	}
	return name
}

// LifelineDetail is a lifeline's name with the host, process and kernel
// thread that tell two lifelines of one name apart. Like StepText and
// ActText it formats what it is given: the caller passes strings made
// terminal-safe (Safe), as the writers here do.
func LifelineDetail(l Lifeline) string {
	d := LifelineName(l)
	if l.Host != "" {
		d += fmt.Sprintf("  %s pid %d@%d tid %d", l.Host, l.PID, l.Incarnation, l.TID)
	}
	if l.Carrier {
		d += "  virtual threads' I/O on this carrier, not its own work"
	}
	return d
}

// peerLane names step s's peer lifeline, or just its To service when
// the model has no lifeline for it (a file step, a model built before
// lifelines existed).
func peerLane(c Conversation, s Step) string {
	if s.ToLane >= 0 && s.ToLane < len(c.Lifelines) && s.To != "" {
		return LifelineName(c.Lifelines[s.ToLane])
	}
	return s.To
}

// StepText is step s of conversation c as `show sequence` prints it:
// "actor -> peer lifeline: label [extras]". The TUI lists a sequence's
// steps with it, so its rows read as the text view's do.
func StepText(c Conversation, s Step) string {
	return stepLine(s, peerLane(c, s))
}

// stepLine is "actor -> peer: label (extras)"; to names the peer's
// lifeline.
func stepLine(s Step, to string) string {
	actor := s.Service
	if s.Thread != "" {
		actor += "/" + s.Thread
	}
	if to != "" {
		actor += " -> " + to
	}
	var extra []string
	if s.Bytes > 0 {
		extra = append(extra, fmt.Sprintf("%d B", s.Bytes))
	}
	if s.Latency > 0 {
		extra = append(extra, s.Latency.String())
	}
	if s.Hop != "" && s.Hop != HopPaired {
		extra = append(extra, s.Hop)
	}
	if s.Carrier {
		extra = append(extra, "carrier")
	}
	line := actor + ": " + s.Label
	if len(extra) > 0 {
		line += " [" + strings.Join(extra, ", ") + "]"
	}
	return line
}

// safeConversation is c with every string terminal-safe (safeModel's
// rule): service names, threads, paths, frames and refs all come from
// the servers. A step's RunQuery is never Go-quoted: it is a finished
// shell command, and quoting it would break its single quotes, making a
// path's text shell code once pasted (task 1i4). runQuery builds it
// from printable runes only; safeCommand only guards that.
func safeConversation(c Conversation) Conversation {
	s := termsafe.String
	c.ID, c.Entry, c.Label, c.Services = s(c.ID), s(c.Entry), s(c.Label), safeAll(c.Services)
	c.Lifelines = slices.Clone(c.Lifelines)
	for i := range c.Lifelines {
		c.Lifelines[i] = safeLifeline(c.Lifelines[i])
	}
	c.Steps = slices.Clone(c.Steps)
	for i := range c.Steps {
		x := &c.Steps[i]
		x.Kind, x.Service, x.Host, x.Thread, x.To = s(x.Kind), s(x.Service), s(x.Host), s(x.Thread), s(x.To)
		x.Label, x.Ordering, x.Hop, x.Ref = s(x.Label), s(x.Ordering), s(x.Hop), s(x.Ref)
		x.LastRef, x.RunQuery = s(x.LastRef), safeCommand(x.RunQuery)
		x.Stack = safeAll(x.Stack)
	}
	return c
}

// safeCommand is cmd, a shell command meant to be pasted, with each
// rune wild reports replaced by '?'. Unlike termsafe.String it
// adds no quote or backslash, so the command's shell quoting stays as
// built; runQuery's output passes through unchanged.
func safeCommand(cmd string) string {
	return strings.Map(func(r rune) rune {
		if wild(r) {
			return '?'
		}
		return r
	}, cmd)
}
