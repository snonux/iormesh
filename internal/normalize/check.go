package normalize

import (
	"fmt"
	"strconv"

	"iormesh/internal/schema"
)

// Violation kinds.
const (
	ViolationEvent         = "event"
	ViolationEdge          = "edge"
	ViolationMustNotAppear = "must_not_appear"
)

// accessMode is O_ACCMODE: the open flags' read/write mode bits.
const accessMode = 0o3

// Violation is one way a trace fails its expectation.
type Violation struct {
	Kind string // ViolationEvent, ViolationEdge or ViolationMustNotAppear
	Msg  string
}

// String formats the violation for test output.
func (v Violation) String() string { return v.Kind + ": " + v.Msg }

// Check asserts declared ⊆ collected on a normalized, bracketed trace and
// returns every violation it finds (nil when the trace passes). Events
// are matched first, because they bind the symbols edges reuse; edges and
// must_not_appear are checked even when events fail, so one run reports
// all three kinds.
func Check(got Trace, want Expectation) []Violation {
	b, vs := checkEvents(got.Events, want.Events)
	vs = append(vs, checkEdges(got.Graph.Edges, want.Edges, b)...)
	return append(vs, checkForbidden(got, want.MustNotAppear)...)
}

// checkEvents runs the thread search. On failure it reports the deepest
// declared thread the search could not satisfy — later threads depend on
// its bindings, so their diagnoses would be noise — and returns the
// bindings of the best partial match, so edges are still checked against
// the symbols that did bind.
func checkEvents(got []schema.Event, want []ExpectedEvent) (*bindings, []Violation) {
	s := eventSearch{plans: groupDeclared(want), threads: groupCollected(got),
		b: newBindings()}
	if s.solve(0) {
		return s.b, nil
	}
	return s.best.b, []Violation{s.best.violation(s.plans)}
}

// checkEdges requires each declared edge in the graph, binding its
// symbols greedily in declared order.
func checkEdges(got []schema.Edge, want []ExpectedEdge, b *bindings) []Violation {
	var vs []Violation
	for i, w := range want {
		if !b.matchAnyEdge(w, got) {
			vs = append(vs, Violation{ViolationEdge, fmt.Sprintf(
				"declared edge %d (%s %s → %s%s) not in the graph",
				i+1, w.Type, w.From, w.To, stateSuffix(w.State))})
		}
	}
	return vs
}

func (b *bindings) matchAnyEdge(w ExpectedEdge, got []schema.Edge) bool {
	for _, e := range got {
		if e.Type != w.Type || (w.State != "" && e.State != w.State) {
			continue
		}
		m := b.mark()
		if b.matchTemplate(w.From, e.From) && b.matchTemplate(w.To, e.To) {
			return true
		}
		b.undo(m)
	}
	return false
}

func stateSuffix(state string) string {
	if state == "" {
		return ""
	}
	return ", " + state
}

// matchEvent reports whether collected event e satisfies declared d under
// the current bindings, binding d's symbols if so (and nothing if not).
func (b *bindings) matchEvent(d ExpectedEvent, e schema.Event) bool {
	if !plainFieldsMatch(d, e) {
		return false
	}
	m := b.mark()
	if b.refsMatch(d, e) {
		return true
	}
	b.undo(m)
	return false
}

// refsMatch compares the fields that bind symbols.
func (b *bindings) refsMatch(d ExpectedEvent, e schema.Event) bool {
	if d.Process != "" && !b.bind(d.Process, processKey(e)) {
		return false
	}
	if d.FD != nil && !b.matchRef(*d.FD, fdValue(d.FD, e)) {
		return false
	}
	if d.Args.FD2 != nil && (e.Args.FD2 == nil || !b.matchRef(*d.Args.FD2, fd2Value(d.Args.FD2, e))) {
		return false
	}
	if d.ConnID != nil && (e.ConnID == "" || !b.matchRef(*d.ConnID, e.ConnID)) {
		return false
	}
	return b.portMatch(d.Args.LocalPort, e.Args.LocalPort) &&
		b.portMatch(d.Args.RemotePort, e.Args.RemotePort)
}

// fdValue is what an fd ref compares against: the fd instance
// (process, fd, fd_gen) for a symbol, since one fd number is reused by
// successive opens; the bare number for a literal (0–2).
func fdValue(r *Ref, e schema.Event) string {
	if r.symbol() {
		return fmt.Sprintf("%s#%d.%d", processKey(e), e.FD, e.FDGen)
	}
	return strconv.Itoa(int(e.FD))
}

// fd2Value is fdValue for args.fd2: the same instance key, so a pipe end
// bound through pipe2's fd2 is the one its writes name by fd.
func fd2Value(r *Ref, e schema.Event) string {
	var gen uint32
	if e.Args.FD2Gen != nil {
		gen = *e.Args.FD2Gen
	}
	if r.symbol() {
		return fmt.Sprintf("%s#%d.%d", processKey(e), *e.Args.FD2, gen)
	}
	return strconv.Itoa(int(*e.Args.FD2))
}

func (b *bindings) portMatch(want *Ref, got *uint16) bool {
	if want == nil {
		return true
	}
	return got != nil && b.matchRef(*want, strconv.Itoa(int(*got)))
}

// plainFieldsMatch compares the fields that bind nothing.
func plainFieldsMatch(d ExpectedEvent, e schema.Event) bool {
	if d.Event != e.Event || (d.Bytes != nil && *d.Bytes != e.Bytes) ||
		(d.Attribution != "" && d.Attribution != e.Attribution) {
		return false
	}
	a, g := d.Args, e.Args
	return strMatch(a.Path, g.Path) && strMatch(a.NewPath, g.NewPath) &&
		strMatch(a.LocalAddr, g.LocalAddr) && strMatch(a.RemoteAddr, g.RemoteAddr) &&
		strMatch(a.SockState, g.SockState) && strMatch(a.Marker, g.Marker) &&
		flagsMatch(a.Flags, g.Flags) && signalMatch(a.Signal, g.Signal)
}

func signalMatch(want, got *int32) bool {
	return want == nil || (got != nil && *want == *got)
}

func strMatch(want, got *string) bool {
	return want == nil || (got != nil && *want == *got)
}

// flagsMatch requires the same access mode and every declared bit, but
// tolerates extra bits: the JDK, libc and architecture may add O_CLOEXEC
// or O_LARGEFILE to an open the scenario cannot control, while O_CREAT
// and O_TRUNC — what a golden actually means by 577 — must be there.
// Declared 0 therefore matches any read-only open.
func flagsMatch(want, got *uint32) bool {
	if want == nil {
		return true
	}
	if got == nil {
		return false
	}
	return *want&accessMode == *got&accessMode && *got&*want == *want
}
