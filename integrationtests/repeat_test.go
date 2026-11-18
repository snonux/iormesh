package integrationtests

// PLAN.md M1 acceptance, second half: "the normalized trace passes the
// golden check on repeated runs", and docs/implementation.md M1 step 11's
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

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"iormesh/internal/normalize"
	"iormesh/internal/schema"
)

// maxDiffLines bounds what a failing comparison reports; the first
// difference is the one to read.
const maxDiffLines = 10

// TestSecondRunNormalizesIdentically traces each M1 scenario twice and
// requires the two normalized, bracketed traces - events and graph - to be
// equal line for line.
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
// differences, empty when they are equal.
func diffNormalized(a, b normalize.Trace) []string {
	al, bl := traceLines(a), traceLines(b)
	var diffs []string
	for i := range max(len(al), len(bl)) {
		x, y := lineAt(al, i), lineAt(bl, i)
		if x == y {
			continue
		}
		if len(diffs) == maxDiffLines {
			return append(diffs, "… more differences")
		}
		diffs = append(diffs, fmt.Sprintf("line %d:\n  first:  %s\n  second: %s", i+1, x, y))
	}
	return diffs
}

// traceLines renders the events one per line, grouped by process with
// their rank fields zeroed (see the file comment), then the graph's nodes
// and edges one per line. Marshalling a schema value cannot fail; an error
// would surface as a line that differs.
func traceLines(t normalize.Trace) []string {
	var lines []string
	add := func(v any) {
		data, err := json.Marshal(v)
		if err != nil {
			data = []byte("marshal error: " + err.Error())
		}
		lines = append(lines, string(data))
	}
	events := slices.Clone(t.Events)
	slices.SortStableFunc(events, func(a, b schema.Event) int { return cmp.Compare(a.PID, b.PID) })
	for _, e := range events {
		e.Seq, e.BootNS, e.WallNS = 0, 0, 0
		add(e)
	}
	for _, n := range t.Graph.Nodes {
		add(n)
	}
	for _, e := range t.Graph.Edges {
		add(e)
	}
	return lines
}

func lineAt(lines []string, i int) string {
	if i < len(lines) {
		return lines[i]
	}
	return "(none)"
}

func TestDiffNormalized(t *testing.T) {
	ev := func(seq uint64, kind string) schema.Event { return schema.Event{PID: 1, Seq: seq, Event: kind} }
	other := func(seq uint64, kind string) schema.Event { return schema.Event{PID: 2, Seq: seq, Event: kind} }
	base := normalize.Trace{Events: []schema.Event{ev(1, "openat"), ev(2, "close")},
		Graph: schema.Graph{Nodes: []schema.Node{{Type: schema.NodeHost, ID: "host:$h1"}}}}
	tests := []struct {
		name        string
		first, next normalize.Trace
		want        int
	}{
		{"equal", base, base, 0},
		{"one event differs", base, normalize.Trace{Events: []schema.Event{ev(1, "openat"), ev(2, "read")},
			Graph: base.Graph}, 1},
		{"second is shorter", base, normalize.Trace{Events: base.Events[:1], Graph: base.Graph}, 2},
		{"graph differs", base, normalize.Trace{Events: base.Events}, 1},
		{"another process interleaved differently",
			normalize.Trace{Events: []schema.Event{ev(1, "openat"), other(2, "read"), ev(3, "close")}, Graph: base.Graph},
			normalize.Trace{Events: []schema.Event{other(1, "read"), ev(2, "openat"), ev(3, "close")}, Graph: base.Graph}, 0},
		{"one process reordered",
			normalize.Trace{Events: []schema.Event{ev(1, "openat"), ev(2, "close")}},
			normalize.Trace{Events: []schema.Event{ev(1, "close"), ev(2, "openat")}}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := diffNormalized(tt.first, tt.next); len(got) != tt.want {
				t.Errorf("diffNormalized() = %q, want %d differences", got, tt.want)
			}
		})
	}
}
