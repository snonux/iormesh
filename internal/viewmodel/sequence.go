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

// WriteSequence prints one conversation: a header, the lifelines, then
// one line per step — its number, ordering, actor (lifeline/thread),
// arrow, label and extras — with the step's top Java frame and its raw
// event's ref indented below it.
func WriteSequence(w io.Writer, c Conversation) error {
	c = safeConversation(c)
	var b strings.Builder
	fmt.Fprintf(&b, "conversation %s  %s UTC  %s  %s  (%d hops)\n", c.ID, c.Start.Format(msLayout),
		c.Entry, c.Label, c.Hops)
	fmt.Fprintf(&b, "lifelines    %s\n", strings.Join(c.Services, ", "))
	for i, s := range c.Steps {
		fmt.Fprintf(&b, "%3d  %-12s  %s\n", i+1, s.Ordering, stepLine(s))
		if len(s.Stack) > 0 {
			fmt.Fprintf(&b, "     %-12s    at %s\n", "", s.Stack[0])
		}
		fmt.Fprintf(&b, "     %-12s    ref %s\n", "", s.Ref)
	}
	if c.MoreSteps > 0 {
		fmt.Fprintf(&b, "truncated: %d more steps\n", c.MoreSteps)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// stepLine is "actor -> peer: label (extras)".
func stepLine(s Step) string {
	actor := s.Service
	if s.Thread != "" {
		actor += "/" + s.Thread
	}
	if s.To != "" {
		actor += " -> " + s.To
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
// the servers.
func safeConversation(c Conversation) Conversation {
	s := termsafe.String
	c.ID, c.Entry, c.Label, c.Services = s(c.ID), s(c.Entry), s(c.Label), safeAll(c.Services)
	c.Steps = slices.Clone(c.Steps)
	for i := range c.Steps {
		x := &c.Steps[i]
		x.Kind, x.Service, x.Host, x.Thread, x.To = s(x.Kind), s(x.Service), s(x.Host), s(x.Thread), s(x.To)
		x.Label, x.Ordering, x.Hop, x.Ref = s(x.Label), s(x.Ordering), s(x.Hop), s(x.Ref)
		x.Stack = safeAll(x.Stack)
	}
	return c
}
