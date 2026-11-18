package normalize

import (
	"errors"
	"fmt"
	"slices"

	"iormesh/internal/schema"
)

// Bracket errors. A golden check on a trace without a complete bracket
// would silently assert on the wrong window (JVM startup, or nothing), so
// every defect is an error rather than a best-effort cut.
var (
	// ErrNoBracket: no process emitted the scenario's begin marker.
	ErrNoBracket = errors.New("normalize: no begin marker for scenario")
	// ErrUnterminatedBracket: a process began the scenario but never
	// emitted its end marker (it crashed, or the trace stopped early).
	ErrUnterminatedBracket = errors.New("normalize: begin marker without end marker")
	// ErrMalformedBracket: markers out of order or repeated.
	ErrMalformedBracket = errors.New("normalize: malformed marker bracket")
)

// span is one process's bracket: indexes into the input, inclusive.
type span struct{ begin, end int }

// cut keeps each process's events between its own begin and end markers
// of scenario, inclusive. The bracket is per process because a
// multi-process scenario (tcp-loopback's server and client) brackets each
// role separately; one global window would let the first role's
// pre-scenario JVM startup in. A process forked inside a bracket belongs
// to it (inheritSpans): fd-lifecycle's, pipe-ipc's and signals' child
// JVMs emit no markers. Events of any other process that never began the
// scenario are dropped. Host-level synthetic events (PID 0: drop,
// session, end) are kept when they fall inside the union of all brackets,
// so a ring-buffer gap inside the scenario stays visible.
func cut(events []schema.Event, edges []schema.Edge, scenario string) ([]schema.Event, error) {
	spans, err := findSpans(events, scenario)
	if err != nil {
		return nil, err
	}
	inheritSpans(spans, events, edges)
	lo, hi := len(events), -1
	for _, s := range spans {
		lo, hi = min(lo, s.begin), max(hi, s.end)
	}
	var out []schema.Event
	for i, e := range events {
		if s, ok := spans[processKey(e)]; ok && i >= s.begin && i <= s.end {
			out = append(out, e)
		} else if e.PID == 0 && i >= lo && i <= hi {
			out = append(out, e)
		}
	}
	return out, nil
}

// inheritSpans gives a process without a bracket of its own the bracket
// of the process that forked it (a forked edge, M2 step 6) when the
// child's first event lies inside that bracket. A child can have no event
// before its fork, so a first event before the parent's begin marker
// means it was forked before the scenario began (a helper the harness or
// the JVM started), and it stays out; one forked inside the bracket but
// silent until after the end marker contributes nothing either way. The
// child is then cut to the parent's window: what it does after the
// parent's end marker is not the scenario's. Grandchildren inherit
// through their parent, so the loop runs until no span changes — at most
// once per edge, since every pass that changes something adds a span.
func inheritSpans(spans map[string]span, events []schema.Event, edges []schema.Edge) {
	first := firstEvents(events)
	for changed := true; changed; {
		changed = false
		for _, e := range edges {
			if e.Type != schema.EdgeForked {
				continue
			}
			if _, own := spans[e.To]; own {
				continue
			}
			ps, ok := spans[e.From]
			i, seen := first[e.To]
			if ok && seen && i >= ps.begin && i <= ps.end {
				spans[e.To] = ps
				changed = true
			}
		}
	}
}

// firstEvents maps each process node id to the index of its first event.
func firstEvents(events []schema.Event) map[string]int {
	first := map[string]int{}
	for i, e := range events {
		if e.PID == 0 {
			continue
		}
		if _, ok := first[processKey(e)]; !ok {
			first[processKey(e)] = i
		}
	}
	return first
}

// findSpans locates every process's begin and end marker of scenario.
// Markers of other scenarios are ignored: a trace may hold several.
func findSpans(events []schema.Event, scenario string) (map[string]span, error) {
	begin, end := scenario+"/begin", scenario+"/end"
	spans := map[string]span{}
	for i, e := range events {
		if e.Event != schema.KindMarker || e.Args.Marker == nil {
			continue
		}
		k := processKey(e)
		s, seen := spans[k]
		switch *e.Args.Marker {
		case begin:
			if seen {
				return nil, fmt.Errorf("%w: second %s in process %s", ErrMalformedBracket, begin, k)
			}
			spans[k] = span{begin: i, end: -1}
		case end:
			if !seen || s.end >= 0 {
				return nil, fmt.Errorf("%w: %s without a preceding %s in process %s",
					ErrMalformedBracket, end, begin, k)
			}
			spans[k] = span{begin: s.begin, end: i}
		}
	}
	return spans, checkSpans(spans, scenario)
}

// checkSpans requires at least one bracket and every bracket closed. The
// keys are sorted so the error names the same process on every run.
func checkSpans(spans map[string]span, scenario string) error {
	if len(spans) == 0 {
		return fmt.Errorf("%w %q", ErrNoBracket, scenario)
	}
	keys := make([]string, 0, len(spans))
	for k := range spans {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if spans[k].end < 0 {
			return fmt.Errorf("%w: scenario %q, process %s", ErrUnterminatedBracket, scenario, k)
		}
	}
	return nil
}

// processKey names one process incarnation on one host — the same triple
// a process node id is built from.
func processKey(e schema.Event) string {
	return schema.ProcessNodeID(e.HostID, e.PID, e.PIDInc)
}
