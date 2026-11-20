package viewmodel

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"iormesh/internal/termsafe"
)

// The text views of threads and processes: `show thread` lists the
// thread stories, `show thread SERVICE/THREAD` prints one thread's story
// as one line and then step by step, `show processes` prints the
// processes per host. Every string goes through safeModel's rule
// (termsafe), and a carrier's lifeline says on every view that its
// virtual threads' work is not its own.

// ErrNoThread is a thread view asked for a SERVICE/THREAD the model
// holds no story of.
var ErrNoThread = errors.New("no such thread")

// FindThreads returns the stories of key ("SERVICE/THREAD"): every
// lifeline of that service and thread name — one per host, process
// incarnation and kernel thread that carries the name, and a carrier's
// virtual-thread lifeline beside its own.
func FindThreads(m Model, key string) ([]ThreadStory, error) {
	var out []ThreadStory
	for _, t := range m.Threads {
		if t.Service+"/"+t.Thread == key {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: %q (iormeshctl show thread lists them)", ErrNoThread, termsafe.String(key))
	}
	return out, nil
}

// WriteThreadList lists the stories: lifeline, where it ran, entries.
func WriteThreadList(w io.Writer, m Model) error {
	m = safeModel(m)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "THREAD\tHOST\tPID\tINC\tTID\tSTEPS\tNOTE")
	for _, t := range m.Threads {
		note := ""
		if t.Carrier {
			note = "virtual threads on this carrier"
		}
		_, _ = fmt.Fprintf(tw, "%s/%s\t%s\t%d\t%d\t%d\t%d\t%s\n", t.Service, t.Thread, t.Host, t.PID, t.Incarnation, t.TID,
			len(t.Entries)+t.MoreEntries, note)
	}
	return tw.Flush()
}

// WriteThreads prints each story: a header with the lifeline, the story
// as one line (none for a carrier's virtual threads), then one line per
// entry — its ordering against the entry
// before, the conversation step it is, the act and label — and its ref.
func WriteThreads(w io.Writer, ts []ThreadStory) error {
	var b strings.Builder
	for i, t := range ts {
		t = safeStory(t)
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "thread       %s\n", lifelineDetail(t.Lifeline))
		if t.MoreEntries > 0 {
			fmt.Fprintf(&b, "truncated: %d more steps (older, or past a conversation's cap)\n", t.MoreEntries)
		}
		if t.Carrier {
			// No " => " chain: its virtual threads migrate between
			// carriers, so nothing orders their steps on this one.
			b.WriteString("story        none: virtual threads' steps, in no provable order\n")
		} else {
			fmt.Fprintf(&b, "story        %s\n", storyLine(t.Entries))
		}
		for j, e := range t.Entries {
			fmt.Fprintf(&b, "%3d  %-12s  %s#%d  %s\n", j+1, e.Ordering, e.Conversation, e.Step, actText(e))
			fmt.Fprintf(&b, "     %-12s    ref %s\n", "", e.Ref)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// storyLine is the story in one line, "received GET /x from a => read
// /etc/c => replied 200 to a"; " => " separates the entries, as a
// label may hold "->" itself (a rename).
func storyLine(es []ThreadEntry) string {
	parts := make([]string, 0, len(es))
	for _, e := range es {
		parts = append(parts, actText(e))
	}
	return strings.Join(parts, " => ")
}

// actText says what the thread did in an entry.
func actText(e ThreadEntry) string {
	switch e.Act {
	case ActSend:
		return "sent " + e.Label + " to " + e.Peer
	case ActReceive:
		return "received " + e.Label + " from " + e.Peer
	case ActReply:
		return "replied " + e.Label + " to " + e.Peer
	case ActResponse:
		return "got " + e.Label + " from " + e.Peer
	}
	return e.Label
}
