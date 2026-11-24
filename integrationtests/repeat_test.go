package integrationtests

// docs/milestones.md M1 acceptance, second half: "the normalized trace passes
// the golden check on repeated runs", and docs/implementation.md M1 step 11's
// done-when: a second normalized run diffs empty against the first inside
// the bracket. The golden is a subset check; this one is exact, so it also
// catches run-to-run noise the golden does not declare.
//
// Exact per process, that is: the server and the client run at the same
// time, so how their events interleave in the host-wide sequence is up to
// the scheduler, and so are seq and both clocks, which the normalizer sets
// to the event's rank in that sequence. The comparison takes each
// process's events in their own order (stable by normalized PID) without
// those three fields; everything else - fds, tids, ports, connection ids,
// paths, the graph - must be identical.
//
// One exception, and only for tcp_state events: they are the kernel's
// asynchronous socket transitions, not the process's syscalls, so they are
// compared per process apart from the syscalls, and the shorter run's
// sequence need only be a prefix of the longer one's that still reaches
// teardown (see viewOf and tcpStatesDiffer). Observed under CPU load before this split: the client's
// FIN_WAIT2 and CLOSE landed before its close event in one run and after
// it in the next, or after the end marker altogether, and close(2)
// sometimes re-reported FIN_WAIT1 (see appendTransition).

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"testing"

	"iormesh/internal/normalize"
	"iormesh/internal/schema"
)

// maxDiffLines bounds what a failing comparison reports; the first
// difference is the one to read.
const maxDiffLines = 10

// traceView is a normalized trace as the comparison sees it: the
// non-tcp_state events grouped by process in each process's own order, the
// tcp_state events of each process in their own order, and the graph's
// nodes and edges - all as NDJSON lines with the rank fields zeroed (see
// the file comment).
type traceView struct {
	events    []string
	tcpStates map[uint32][]transition
	graph     []string
}

// transition is one tcp_state event: its NDJSON line, compared exactly,
// and its sock_state, which tcpStatesDiffer reads for the teardown check.
type transition struct {
	line, state string
}

// diffCase is one TestDiffNormalized row: two traces and how many
// differences diffNormalized must report between them.
type diffCase struct {
	name        string
	first, next normalize.Trace
	want        int
}

// TestSecondRunNormalizesIdentically traces each M1 scenario twice and
// requires the two normalized, bracketed traces to match: syscall events
// and graph line for line, tcp_state events per process up to repeats and
// a missing tail.
func TestSecondRunNormalizesIdentically(t *testing.T) {
	enableParallelIfRequested(t)
	scenarios := []struct {
		name  string
		start func(t *testing.T) starter
	}{
		{"file-basic", func(*testing.T) starter { return startScenario("file-basic") }},
		{"tcp-loopback", startTCPPair},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			// A harness per run: each run writes into a fresh tmp dir, so
			// the second cannot find (or trip over) the first one's files.
			h1, h2 := newHarness(t), newHarness(t)
			first := normalized(t, h1, traceJudged(t, h1, sc.start(t)), sc.name)
			second := normalized(t, h2, traceJudged(t, h2, sc.start(t)), sc.name)
			if len(first.Events) == 0 {
				t.Fatal("the first run has no events inside the bracket")
			}
			for _, d := range diffNormalized(first, second) {
				t.Error(d)
			}
		})
	}
}

// diffNormalized compares two normalized traces as their NDJSON lines -
// what `iormesh normalize -out` writes - and returns at most maxDiffLines
// differences, empty when they are equal. Syscall events and the graph must
// match line for line; each process's tcp_state events are compared on
// their own (see tcpStatesDiffer).
func diffNormalized(a, b normalize.Trace) []string {
	av, bv := viewOf(a), viewOf(b)
	var diffs []string
	add := func(d string) bool {
		if len(diffs) == maxDiffLines {
			diffs = append(diffs, "… more differences")
			return false
		}
		diffs = append(diffs, d)
		return true
	}
	if !diffLines("event", av.events, bv.events, add) {
		return diffs
	}
	for _, pid := range unionKeys(av.tcpStates, bv.tcpStates) {
		x, y := av.tcpStates[pid], bv.tcpStates[pid]
		if tcpStatesDiffer(x, y) && !add(fmt.Sprintf("pid %d tcp_state:\n  first:  %q\n  second: %q",
			pid, transitionLines(x), transitionLines(y))) {
			return diffs
		}
	}
	diffLines("graph", av.graph, bv.graph, add)
	return diffs
}

// viewOf splits t into a traceView. tcp_state events go apart from the
// rest because they are not the process's own work: the kernel reports a
// socket's transitions from sock:inet_sock_set_state, mostly in softirq
// when the peer's segments arrive (SYN_SENT -> ESTABLISHED, and, after the
// client's close, FIN_WAIT1 -> FIN_WAIT2 -> CLOSE as the server's ACK and
// FIN come in). Where they fall between the process's syscalls - before or
// after its close returns - and whether the tail lands before the end
// marker is up to the scheduler, not the workload. Marshalling a schema
// value cannot fail; an error would surface as a line that differs.
func viewOf(t normalize.Trace) traceView {
	v := traceView{tcpStates: map[uint32][]transition{}}
	events := slices.Clone(t.Events)
	slices.SortStableFunc(events, func(a, b schema.Event) int { return cmp.Compare(a.PID, b.PID) })
	for _, e := range events {
		e.Seq, e.BootNS, e.WallNS = 0, 0, 0
		if e.Event == schema.KindTCPState {
			v.tcpStates[e.PID] = appendTransition(v.tcpStates[e.PID], transitionOf(e))
			continue
		}
		v.events = append(v.events, jsonLine(e))
	}
	for _, n := range t.Graph.Nodes {
		v.graph = append(v.graph, jsonLine(n))
	}
	for _, e := range t.Graph.Edges {
		v.graph = append(v.graph, jsonLine(e))
	}
	return v
}

// appendTransition appends a tcp_state line unless it repeats the last
// one. A repeat is no state change: Java's Socket.close runs shutdown(2)
// first (ESTABLISHED -> FIN_WAIT1), and close(2)'s __tcp_close then stores
// the socket's current state again, which the tracepoint reports as
// FIN_WAIT1 -> FIN_WAIT1 when the server's ACK has not arrived yet, and not
// at all when it has (bpftrace on 7.2: inet_sk_state_store from
// __tcp_close). Collapsing it keeps the transitions themselves.
func appendTransition(ts []transition, t transition) []transition {
	if len(ts) > 0 && ts[len(ts)-1] == t {
		return ts
	}
	return append(ts, t)
}

func transitionOf(e schema.Event) transition {
	t := transition{line: jsonLine(e)}
	if e.Args.SockState != nil {
		t.state = *e.Args.SockState
	}
	return t
}

// tcpStatesDiffer reports whether two runs' tcp_state sequences of one
// process disagree. The transitions themselves, their order, sockets,
// tuples and attribution must match - the TCP state machine walks the same
// path for the same workload - but the shorter run may stop early: a
// transition the peer drives after the process's last syscall (FIN_WAIT2,
// CLOSE) can land after the end marker and so outside the bracket. One
// sequence must therefore be a prefix of the other.
//
// Only the tail may be missing, though, never the whole teardown: an empty
// prefix (a process with no tcp_state in one run) or one cut after
// SYN_SENT would match anything. The setup states come before connect or
// accept returns, and the first teardown state is set before the
// process's own close or EOF read returns (FIN_WAIT1 by its shutdown(2),
// CLOSE_WAIT before its read(2) sees EOF), all inside the bracket. So when
// the longer run reached teardown, the shorter one must have too. Syscall
// events get no leeway at all; they are compared exactly in diffNormalized.
func tcpStatesDiffer(a, b []transition) bool {
	if len(a) > len(b) {
		a, b = b, a
	}
	if !slices.Equal(a, b[:len(a)]) {
		return true
	}
	return slices.ContainsFunc(b, isTeardown) && !slices.ContainsFunc(a, isTeardown)
}

// isTeardown reports whether t left the connection setup states: any state
// but SYN_SENT, SYN_RECV and ESTABLISHED (the latter also as the accepted
// socket's snapshot). An empty sequence has none, so it never reaches
// teardown.
func isTeardown(t transition) bool {
	switch t.state {
	case "SYN_SENT", "SYN_RECV", "ESTABLISHED":
		return false
	}
	return true
}

// diffLines compares two line lists exactly, reporting each differing
// line through add, and returns false once add refused one.
func diffLines(what string, al, bl []string, add func(string) bool) bool {
	for i := range max(len(al), len(bl)) {
		x, y := lineAt(al, i), lineAt(bl, i)
		if x != y && !add(fmt.Sprintf("%s line %d:\n  first:  %s\n  second: %s", what, i+1, x, y)) {
			return false
		}
	}
	return true
}

// unionKeys returns the keys of both maps, sorted, so the report order is
// stable.
func unionKeys(a, b map[uint32][]transition) []uint32 {
	keys := slices.Collect(maps.Keys(a))
	for k := range b {
		if _, ok := a[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return keys
}

func transitionLines(ts []transition) []string {
	lines := make([]string, len(ts))
	for i, t := range ts {
		lines[i] = t.line
	}
	return lines
}

func jsonLine(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return "marshal error: " + err.Error()
	}
	return string(data)
}

func lineAt(lines []string, i int) string {
	if i < len(lines) {
		return lines[i]
	}
	return "(none)"
}

func TestDiffNormalized(t *testing.T) {
	for _, tt := range append(syscallDiffCases(), tcpStateDiffCases()...) {
		t.Run(tt.name, func(t *testing.T) {
			if got := diffNormalized(tt.first, tt.next); len(got) != tt.want {
				t.Errorf("diffNormalized() = %q, want %d differences", got, tt.want)
			}
		})
	}
}

// diffEvent is a test event of PID pid; rank is its seq.
func diffEvent(pid uint32, rank uint64, kind string) schema.Event {
	return schema.Event{PID: pid, Seq: rank, Event: kind}
}

// diffState is a tcp_state test event of PID pid reaching state.
func diffState(pid uint32, rank uint64, state string) schema.Event {
	return schema.Event{PID: pid, Seq: rank, Event: schema.KindTCPState, Args: schema.Args{SockState: &state}}
}

// syscallDiffCases: everything but tcp_state is compared exactly.
func syscallDiffCases() []diffCase {
	ev := func(seq uint64, kind string) schema.Event { return diffEvent(1, seq, kind) }
	other := func(seq uint64, kind string) schema.Event { return diffEvent(2, seq, kind) }
	base := normalize.Trace{Events: []schema.Event{ev(1, "openat"), ev(2, "close")},
		Graph: schema.Graph{Nodes: []schema.Node{{Type: schema.NodeHost, ID: "host:$h1"}}}}
	return []diffCase{
		{"equal", base, base, 0},
		{"one event differs", base, normalize.Trace{Events: []schema.Event{ev(1, "openat"), ev(2, "read")},
			Graph: base.Graph}, 1},
		// One line, not two: events and graph are compared as separate lists,
		// so a missing event no longer shifts the graph lines too.
		{"second is shorter", base, normalize.Trace{Events: base.Events[:1], Graph: base.Graph}, 1},
		{"graph differs", base, normalize.Trace{Events: base.Events}, 1},
		{"another process interleaved differently",
			normalize.Trace{Events: []schema.Event{ev(1, "openat"), other(2, "read"), ev(3, "close")}, Graph: base.Graph},
			normalize.Trace{Events: []schema.Event{other(1, "read"), ev(2, "openat"), ev(3, "close")}, Graph: base.Graph}, 0},
		{"one process reordered",
			normalize.Trace{Events: []schema.Event{ev(1, "openat"), ev(2, "close")}},
			normalize.Trace{Events: []schema.Event{ev(1, "close"), ev(2, "openat")}}, 2},
	}
}

// tcpStateDiffCases: tcp_state may move across syscalls, lose its tail or
// repeat a state, but must not change a transition or lose its head.
func tcpStateDiffCases() []diffCase {
	trace := func(events ...schema.Event) normalize.Trace { return normalize.Trace{Events: events} }
	ev := func(seq uint64, kind string) schema.Event { return diffEvent(1, seq, kind) }
	state := func(rank uint64, s string) schema.Event { return diffState(1, rank, s) }
	peer := func(rank uint64, s string) schema.Event { return diffState(2, rank, s) }
	return []diffCase{
		{"tcp_state moved across a syscall",
			trace(state(1, "FIN_WAIT1"), state(2, "FIN_WAIT2"), ev(3, "close")),
			trace(state(1, "FIN_WAIT1"), ev(2, "close"), state(3, "FIN_WAIT2")), 0},
		{"tcp_state tail outside the bracket",
			trace(state(1, "FIN_WAIT1"), ev(2, "close"), state(3, "CLOSE")),
			trace(state(1, "FIN_WAIT1"), ev(2, "close")), 0},
		{"tcp_state repeated by close",
			trace(state(1, "FIN_WAIT1"), state(2, "FIN_WAIT1"), ev(3, "close")),
			trace(state(1, "FIN_WAIT1"), state(2, "FIN_WAIT2"), ev(3, "close")), 0},
		{"tcp_state transitions differ",
			trace(state(1, "FIN_WAIT1"), state(2, "FIN_WAIT2")),
			trace(state(1, "FIN_WAIT1"), state(2, "CLOSING")), 1},
		{"tcp_state head missing",
			trace(state(1, "SYN_SENT"), state(2, "ESTABLISHED")),
			trace(state(1, "ESTABLISHED")), 1},
		{"tcp_state missing entirely in one run",
			trace(state(1, "FIN_WAIT1"), ev(2, "close")),
			trace(ev(1, "close")), 1},
		{"tcp_state cut before teardown",
			trace(state(1, "SYN_SENT"), state(2, "ESTABLISHED"), state(3, "FIN_WAIT1")),
			trace(state(1, "SYN_SENT")), 1},
		{"setup only in both runs, one shorter",
			trace(state(1, "SYN_SENT"), state(2, "ESTABLISHED")),
			trace(state(1, "SYN_SENT")), 0},
		{"two processes, each prefix-tolerant",
			trace(state(1, "FIN_WAIT1"), peer(2, "ESTABLISHED"), state(3, "FIN_WAIT2"), peer(4, "CLOSE_WAIT")),
			trace(peer(1, "ESTABLISHED"), state(2, "FIN_WAIT1"), peer(3, "CLOSE_WAIT"), peer(4, "LAST_ACK")), 0},
		{"two processes, one lost its teardown",
			trace(state(1, "FIN_WAIT1"), peer(2, "ESTABLISHED"), peer(3, "CLOSE_WAIT")),
			trace(state(1, "FIN_WAIT1"), peer(2, "ESTABLISHED")), 1},
		{"syscall missing next to tcp_state",
			trace(state(1, "FIN_WAIT1"), ev(2, "close")),
			trace(state(1, "FIN_WAIT1")), 1},
	}
}
