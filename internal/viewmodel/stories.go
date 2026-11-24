package viewmodel

import (
	"cmp"
	"slices"
)

// Thread stories (docs/visualization.md "Plain text", `show thread`;
// docs/milestones.md M5 "Sequence view": "one thread's story reads as one
// line"): for every lifeline with a thread, what that thread did across the
// kept conversations — received a request, read a file, made a call,
// got its answer, replied — in the thread's own order. Every entry is a
// step of a conversation (ID and number), so a story adds no reading of
// the trace, only another order of the same steps.
//
// The order is provable by construction: a platform lifeline is one
// kernel thread of one process incarnation on one host (pid,
// incarnation and tid are in its key), so its own clock orders its
// entries — never another host's. Inside one request span the thread
// worked on alone (request.owned) the order is the thread's program
// order, causal; between spans it is time-ordered on that clock when
// the instants differ, unordered when they tie.
//
// A virtual thread's I/O on its carrier is a lifeline of its own
// (Lifeline.Carrier), so it never reads as the carrier's own work. Its
// entries are all unordered: an http event names the carrier the
// virtual thread ran on when the event was emitted, but a virtual
// thread migrates — its request may have started on another carrier —
// so neither the instants nor the carrier prove which of its virtual
// threads' steps came first on it (task 0f4 review).

// Acts: what a thread did in a step.
const (
	// ActSend: it made the request (a request step's own end).
	ActSend = "send"
	// ActReceive: the request came in to it (a request step's far end).
	ActReceive = "receive"
	// ActReply: it answered (a response step's own end).
	ActReply = "reply"
	// ActResponse: the answer to its request came back.
	ActResponse = "response"
	// ActFile: its file operation.
	ActFile = "file"
)

// MaxThreadEntries caps a story's entries: the newest are kept,
// MoreEntries counts the older ones (and those of steps past MaxSteps).
const MaxThreadEntries = 200

// ThreadStory is one lifeline's story.
type ThreadStory struct {
	Lifeline
	Entries     []ThreadEntry `json:"entries"`
	MoreEntries int           `json:"more_entries"`
}

// ThreadEntry is one step of a conversation, seen from one thread.
type ThreadEntry struct {
	// Conversation and Step (1-based) name the step in the model.
	Conversation string `json:"conversation"`
	Step         int    `json:"step"`
	Act          string `json:"act"`
	Label        string `json:"label"`
	// Peer is the service on the step's other end; "" for a file step.
	Peer string `json:"peer,omitempty"`
	// Ordering is judged against the entry before it in the story: causal,
	// time-ordered or unordered (see above).
	Ordering string `json:"ordering"`
	Ref      string `json:"ref"`
}

// storyBook collects the stories while the conversations are emitted.
type storyBook struct {
	idx  map[Lifeline]int
	accs []*storyAcc
	seq  int // emission order: the tie-break after the instant
}

// storyAcc is one lifeline's entries so far.
type storyAcc struct {
	lane  Lifeline
	marks []storyMark
	cut   int // entries of steps past MaxSteps
}

// storyMark is an entry with what orders it.
type storyMark struct {
	wall  uint64
	seq   int
	span  int
	entry ThreadEntry
}

func newStoryBook() *storyBook { return &storyBook{idx: map[Lifeline]int{}} }

// mark records step number n of conversation conv at both its ends.
func (b *storyBook) mark(conv string, n int, s Step, from, to end) {
	acts := map[string][2]string{StepRequest: {ActSend, ActReceive},
		StepResponse: {ActReply, ActResponse}, StepFile: {ActFile, ""}}[s.Kind]
	b.add(from, storyMark{entry: ThreadEntry{Conversation: conv, Step: n, Act: acts[0], Label: s.Label,
		Peer: s.To, Ref: s.Ref}})
	if acts[1] != "" {
		b.add(to, storyMark{entry: ThreadEntry{Conversation: conv, Step: n, Act: acts[1], Label: s.Label,
			Peer: s.Service, Ref: s.Ref}})
	}
}

// add files m under end e's lifeline; a lifeline without a thread (an
// untraced caller, an unpaired peer) has no story.
func (b *storyBook) add(e end, m storyMark) {
	if a := b.acc(e.lane); a != nil {
		m.wall, m.span, m.seq = e.wall, e.span, b.seq
		b.seq++
		a.marks = append(a.marks, m)
	}
}

// cut counts a step past MaxSteps in its lifelines' stories.
func (b *storyBook) cut(lanes ...Lifeline) {
	for _, l := range lanes {
		if a := b.acc(l); a != nil {
			a.cut++
		}
	}
}

// acc finds or starts l's story; nil when l names no thread.
func (b *storyBook) acc(l Lifeline) *storyAcc {
	if l.Thread == "" {
		return nil
	}
	if i, ok := b.idx[l]; ok {
		return b.accs[i]
	}
	b.idx[l] = len(b.accs)
	b.accs = append(b.accs, &storyAcc{lane: l})
	return b.accs[len(b.accs)-1]
}

// stories orders each story on its thread's clock, judges every entry
// against the one before, keeps the newest MaxThreadEntries and sorts
// the stories by lifeline.
func (b *storyBook) stories() []ThreadStory {
	out := make([]ThreadStory, 0, len(b.accs))
	for _, a := range b.accs {
		slices.SortFunc(a.marks, func(x, y storyMark) int {
			return cmp.Or(cmp.Compare(x.wall, y.wall), cmp.Compare(x.seq, y.seq))
		})
		st := ThreadStory{Lifeline: a.lane, Entries: make([]ThreadEntry, 0, len(a.marks)), MoreEntries: a.cut}
		for i, m := range a.marks {
			m.entry.Ordering = storyOrder(a.marks, i)
			if a.lane.Carrier { // virtual threads migrate: nothing proves their order here
				m.entry.Ordering = OrderNone
			}
			st.Entries = append(st.Entries, m.entry)
		}
		if n := len(st.Entries); n > MaxThreadEntries {
			st.MoreEntries += n - MaxThreadEntries
			st.Entries = st.Entries[n-MaxThreadEntries:]
		}
		out = append(out, st)
	}
	slices.SortFunc(out, func(x, y ThreadStory) int { return compareLifeline(x.Lifeline, y.Lifeline) })
	return out
}

// storyOrder judges mark i against mark i-1 of one thread: causal inside
// one owned span, time-ordered when the thread's clock moved on,
// unordered on a tie. The first entry is placed by its own stamp.
func storyOrder(ms []storyMark, i int) string {
	switch {
	case i == 0:
		return OrderTime
	case ms[i].span >= 0 && ms[i].span == ms[i-1].span:
		return OrderCausal
	case ms[i].wall > ms[i-1].wall:
		return OrderTime
	}
	return OrderNone
}

// compareLifeline sorts lifelines by service, thread, host, process
// (pid, incarnation), thread id, and a carrier's lifeline after the
// platform one.
func compareLifeline(x, y Lifeline) int {
	return cmp.Or(cmp.Compare(x.Service, y.Service), cmp.Compare(x.Thread, y.Thread),
		cmp.Compare(x.Host, y.Host), cmp.Compare(x.PID, y.PID), cmp.Compare(x.Incarnation, y.Incarnation),
		cmp.Compare(x.TID, y.TID), cmp.Compare(btoi(x.Carrier), btoi(y.Carrier)))
}
