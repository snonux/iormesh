package federate

import (
	"fmt"
	"time"

	"iormesh/internal/segment"
)

// Time windows over the store (task yh4): `iormeshctl build -from/-to`
// or `-last` builds only the segments whose time range overlaps a
// window, `iormeshctl query -from/-to` reads only those (task ik4), and
// `iormeshctl prune -older-than` deletes the ones whose range ended
// before a cutoff. A segment is placed by its file name alone —
// reading its events to find their clocks would cost the full decode a
// window is there to save — so the range is a conservative one, Span.

// spanSlack widens a segment's range on both sides: the writer rotates
// on the first tick at or after its interval (late by up to a tick), the
// name's start is cut to the second, and an event stamped in the kernel
// just before a rotation is written into the next segment. A minute is
// far more than any of these; erring wide only lets a window take one
// more segment at an edge.
const spanSlack = time.Minute

// Span is the time range [from, to) a segment file was written in, from
// its name: the named start, plus the writer's rotation interval
// (segment.DefaultInterval — the tracer never sets another; a segment
// rotated early at its size bound only ends sooner), each side widened by
// spanSlack.
//
// It bounds when events were written, not their wall_ns: an event is
// stamped when its call began and written when it completed, so a call
// that blocked longer than the slack (a read waiting minutes for data)
// lands in a segment written after its wall_ns. A -to window can miss
// such an event, and prune counts it with the later segment — the
// window selects segments, it does not promise every event whose
// wall_ns lies inside it.
func Span(f segment.FileName) (from, to time.Time) {
	return f.Start.Add(-spanSlack), f.Start.Add(segment.DefaultInterval + spanSlack)
}

// Window selects segments by time: those whose Span overlaps
// [From, To). A zero From or To leaves that side open.
type Window struct {
	From, To time.Time
}

// Overlaps reports whether the segment named f may hold events inside
// the window. A nil window takes every segment.
func (w *Window) Overlaps(f segment.FileName) bool {
	if w == nil {
		return true
	}
	from, to := Span(f)
	return (w.From.IsZero() || to.After(w.From)) && (w.To.IsZero() || from.Before(w.To))
}

// toCheck is the part of the listed segments LoadSegments decodes under
// a window: every segment inside it, and every segment of a host
// directory not pinned yet (known). The host vetting (vetHosts) then
// judges an unpinned directory on all of its sessions, exactly as a
// build without a window would — a forged session outside the window
// still puts the directory in doubt — and what it pins is as sound. A
// pinned directory needs only its window: the pin, not its other
// sessions, is what holds its sessions to their identity.
func (w *Window) toCheck(segs []*Segment, known map[string]HostIdentity) []*Segment {
	if w == nil {
		return segs
	}
	var out []*Segment
	for _, s := range segs {
		if _, pinned := known[s.Host]; !pinned || w.Overlaps(s.File) {
			out = append(out, s)
		}
	}
	return out
}

// narrow keeps the window's segments of the vetted sessions, dropping
// sessions left without one. Each session's graph is then rebuilt from
// its window alone (loadSession), as from a store whose older segments
// the operator deleted, so the build is the same whether or not the
// directory was pinned when it ran.
func (w *Window) narrow(groups [][]*Segment) [][]*Segment {
	if w == nil {
		return groups
	}
	var out [][]*Segment
	for _, g := range groups {
		var in []*Segment
		for _, s := range g {
			if w.Overlaps(s.File) {
				in = append(in, s)
			}
		}
		if len(in) > 0 {
			out = append(out, in)
		}
	}
	return out
}

// FirstStart is the earliest start an events segment of projects (every
// project directory when none is given) names in store: the trace's
// first day, which a -from or -to time of day is placed on. It reads
// only the directory listings.
func FirstStart(store string, projects []string) (time.Time, error) {
	var out Segments
	segs, err := listStore(store, projects, &out)
	if err != nil {
		return time.Time{}, err
	}
	if len(segs) == 0 {
		return time.Time{}, fmt.Errorf("federate: no segment collected in %s yet", store)
	}
	first := segs[0].File.Start
	for _, s := range segs[1:] {
		if s.File.Start.Before(first) {
			first = s.File.Start
		}
	}
	return first, nil
}
