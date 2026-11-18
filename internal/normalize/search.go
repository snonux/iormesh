package normalize

import (
	"fmt"
	"strconv"
	"strings"

	"iormesh/internal/schema"
)

// threadPlan is one declared thread and its events, in declared order.
type threadPlan struct {
	sym    string
	events []ExpectedEvent
}

// collectedThread is one traced thread and its events, in trace order.
type collectedThread struct {
	key    string // process node id + "/t" + tid: what a $t symbol binds to
	events []schema.Event
}

// eventSearch assigns declared threads to collected threads. Within a
// thread it matches greedily — each declared event takes the first
// collected event after the previous match that fits — and it backtracks
// over candidate threads only (PLAN.md M0, docs/implementation.md step 9):
// a JVM has dozens of threads but a scenario declares one or two, and the
// greedy in-thread match is sound for the deterministic sequences goldens
// declare.
type eventSearch struct {
	plans   []threadPlan
	threads []collectedThread
	b       *bindings
	best    partial
}

// partial is the furthest the search got: the deepest declared thread,
// and on it the candidate that matched the most declared events.
type partial struct {
	plan    int
	thread  string
	matched int
	b       *bindings
}

// groupDeclared splits declared events by thread, threads in order of
// first declaration.
func groupDeclared(want []ExpectedEvent) []threadPlan {
	var plans []threadPlan
	index := map[string]int{}
	for _, d := range want {
		i, ok := index[d.Thread]
		if !ok {
			i = len(plans)
			index[d.Thread] = i
			plans = append(plans, threadPlan{sym: d.Thread})
		}
		plans[i].events = append(plans[i].events, d)
	}
	return plans
}

// groupCollected splits the trace by thread, threads in order of first
// appearance, so the search tries them deterministically.
func groupCollected(got []schema.Event) []collectedThread {
	var threads []collectedThread
	index := map[string]int{}
	for _, e := range got {
		key := processKey(e) + "/t" + strconv.FormatUint(uint64(e.TID), 10)
		i, ok := index[key]
		if !ok {
			i = len(threads)
			index[key] = i
			threads = append(threads, collectedThread{key: key})
		}
		threads[i].events = append(threads[i].events, e)
	}
	return threads
}

// solve assigns plans[i:] to collected threads, undoing every binding of
// a failed candidate before trying the next.
func (s *eventSearch) solve(i int) bool {
	if i == len(s.plans) {
		return true
	}
	p := s.plans[i]
	s.note(i, "", -1) // the plan was reached, even if no thread fits
	for _, t := range s.threads {
		m := s.b.mark()
		if s.b.bind(p.sym, t.key) {
			n := s.b.matchSequence(p.events, t.events)
			if n == len(p.events) && s.solve(i+1) {
				return true
			}
			s.note(i, t.key, n)
		}
		s.b.undo(m)
	}
	return false
}

// note records an attempt if it got further than the best so far: a
// deeper plan wins, then more matched events on the same plan.
func (s *eventSearch) note(plan int, thread string, matched int) {
	if s.best.b != nil && (plan < s.best.plan ||
		(plan == s.best.plan && matched <= s.best.matched)) {
		return
	}
	s.best = partial{plan: plan, thread: thread, matched: matched, b: s.b.clone()}
}

// matchSequence greedily matches declared events, in order, as a
// subsequence of collected ones and returns how many matched.
func (b *bindings) matchSequence(want []ExpectedEvent, got []schema.Event) int {
	j := 0
	for k, d := range want {
		for j < len(got) && !b.matchEvent(d, got[j]) {
			j++
		}
		if j == len(got) {
			return k
		}
		j++
	}
	return len(want)
}

// violation describes the best partial match's first missing event.
func (p partial) violation(plans []threadPlan) Violation {
	plan := plans[p.plan]
	// A whole plan never stays best (the deeper plan it led to is
	// recorded later), but clamp rather than index out of range.
	n := min(max(p.matched, 0), len(plan.events)-1)
	missing := plan.events[n]
	where := "no collected thread fits"
	if p.thread != "" {
		where = fmt.Sprintf("best candidate %s matched %d of %d", p.thread,
			n, len(plan.events))
	}
	return Violation{ViolationEvent, fmt.Sprintf(
		"thread %s: declared event %d (%s) not found in order; %s",
		plan.sym, n+1, describe(missing), where)}
}

// describe renders a declared event compactly for a violation message.
func describe(d ExpectedEvent) string {
	parts := []string{d.Event}
	add := func(k, v string) { parts = append(parts, k+"="+v) }
	if d.Process != "" {
		add("process", d.Process)
	}
	if d.FD != nil {
		add("fd", d.FD.String())
	}
	if d.Bytes != nil {
		add("bytes", strconv.FormatUint(*d.Bytes, 10))
	}
	if d.Args.Path != nil {
		add("path", *d.Args.Path)
	}
	if d.Args.Count != nil {
		add("count", strconv.FormatUint(*d.Args.Count, 10))
	}
	if d.Args.Flags != nil {
		add("flags", strconv.FormatUint(uint64(*d.Args.Flags), 10))
	}
	if d.Args.LocalPort != nil {
		add("local_port", d.Args.LocalPort.String())
	}
	if d.Args.RemotePort != nil {
		add("remote_port", d.Args.RemotePort.String())
	}
	if d.Attribution != "" {
		add("attribution", d.Attribution)
	}
	return strings.Join(parts, " ")
}
