package viewmodel

import (
	"cmp"
	"maps"
	"slices"
	"time"

	"iormesh/internal/schema"
)

// SessionKey names one tracer run in the store: its project and host
// directories, the FQDN and the session id from the segment names.
type SessionKey struct {
	Project, Host, FQDN, ID string
}

// Tally accumulates per-session counts from the events a build merges.
// The build feeds it the events the merge reads anyway (one pass, no
// second read of the segments), segment by segment. Not safe for
// concurrent use.
type Tally struct {
	sessions map[SessionKey]*sessionTally
	// fqdns maps host ids to the FQDN their sessions carry, to name the
	// clock bounds' hosts.
	fqdns map[string]string
}

// sessionTally is one session's counts so far.
type sessionTally struct {
	Session
	lost, skipped, unattributed, attachGap uint64
	first, last                            uint64 // wall ns
	segments                               map[string]bool
}

// NewTally returns an empty tally.
func NewTally() *Tally {
	return &Tally{sessions: map[SessionKey]*sessionTally{}, fqdns: map[string]string{}}
}

// Add counts one event of segment (its source name) of session k. It
// keeps no reference to ev.
func (t *Tally) Add(k SessionKey, segment string, ev *schema.Event) {
	s := t.session(k)
	s.segments[segment] = true
	s.Events++
	if s.HostID == "" {
		s.HostID = ev.HostID
		t.fqdns[ev.HostID] = k.FQDN
	}
	if ev.WallNS != 0 {
		s.first = minNonZero(s.first, ev.WallNS)
		s.last = max(s.last, ev.WallNS)
	}
	switch ev.Event {
	case schema.KindDrop:
		s.drop(ev.Args)
	case schema.KindEnd:
		if ev.Args.EndReason != nil {
			s.End = *ev.Args.EndReason
		}
	}
}

// Segment counts a segment of session k even if it held no events.
func (t *Tally) Segment(k SessionKey, segment string) {
	t.session(k).segments[segment] = true
}

func (t *Tally) session(k SessionKey) *sessionTally {
	s := t.sessions[k]
	if s == nil {
		s = &sessionTally{Session: Session{Project: k.Project, Host: k.Host, FQDN: k.FQDN, ID: k.ID},
			segments: map[string]bool{}}
		t.sessions[k] = s
	}
	return s
}

// drop sorts a drop's count by its reason (schema Drop* constants).
func (s *sessionTally) drop(a schema.Args) {
	if a.Lost == nil {
		return
	}
	n := *a.Lost
	switch deref(a.DropReason) {
	case "":
		s.lost += n
	case schema.DropSkippedRuns:
		s.skipped += n
	case schema.DropUnattributed:
		s.unattributed += n
	case schema.DropAttachGap:
		s.attachGap += n
	}
}

// fill writes the sessions and their sums into m, sorted by key.
func (t *Tally) fill(m *Model) {
	keys := slices.SortedFunc(maps.Keys(t.sessions), compareKeys)
	hosts := map[string]bool{}
	for _, k := range keys {
		s := t.sessions[k]
		s.Segments, s.First, s.Last = len(s.segments), wallTime(s.first), wallTime(s.last)
		m.Health.Sessions = append(m.Health.Sessions, s.Session)
		m.Health.Lost += s.lost
		m.Health.SkippedRuns += s.skipped
		m.Health.Unattributed += s.unattributed
		m.Health.AttachGap += s.attachGap
		m.Run.Segments += s.Segments
		m.Run.Events += s.Events
		m.Run.First = earliest(m.Run.First, s.First)
		m.Run.Last = latest(m.Run.Last, s.Last)
		hosts[k.Host] = true
	}
	m.Run.Sessions = len(keys)
	m.Run.Hosts = slices.Sorted(maps.Keys(hosts))
}

func compareKeys(a, b SessionKey) int {
	return cmp.Or(cmp.Compare(a.Project, b.Project), cmp.Compare(a.Host, b.Host),
		cmp.Compare(a.FQDN, b.FQDN), cmp.Compare(a.ID, b.ID))
}

// wallTime is a wall-clock ns as UTC time; zero stays the zero time.
func wallTime(ns uint64) time.Time {
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, int64(ns)).UTC()
}

func earliest(a, b time.Time) time.Time {
	if a.IsZero() || (!b.IsZero() && b.Before(a)) {
		return b
	}
	return a
}

func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func minNonZero(a, b uint64) uint64 {
	if a == 0 {
		return b
	}
	return min(a, b)
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
