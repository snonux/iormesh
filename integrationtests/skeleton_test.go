package integrationtests

// The M1 acceptance tests (PLAN.md "M1 - Walking skeleton"; guide
// docs/implementation.md M1 step 11): the real tracer against the real
// workload, golden-checked, plus the negative controls - a wrong golden
// must fail, and an untraced process must not show up.

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"iormesh/internal/normalize"
	"iormesh/internal/schema"
)

// TestWalkingSkeleton: client and server on loopback, each doing file data
// ops, yield a trace that passes the tcp-loopback golden and a graph with
// both processes and exactly one fused connection - two connected edges,
// both fused, from the two processes to the same socket node.
func TestWalkingSkeleton(t *testing.T) {
	enableParallelIfRequested(t)
	h := newHarness(t)
	run := traceJudged(t, h, startTCPPair(t))
	assertOnlyTracedPIDs(t, run.Trace.Events, run.pids)

	got := normalized(t, h, run, "tcp-loopback")
	want := expectation(t, "tcp-loopback")
	assertGolden(t, got, want)
	assertOneFusedConnection(t, run.Trace.Graph, run.pids)

	t.Run("wrong expectations are caught", func(t *testing.T) {
		assertCaught(t, got, want, map[string]func(*normalize.Expectation){
			"connection unresolved": func(x *normalize.Expectation) {
				x.Edges = slices.Clone(x.Edges)
				for i := range x.Edges {
					if x.Edges[i].Type == schema.EdgeConnected {
						x.Edges[i].State = schema.StateUnresolved
					}
				}
			},
			"echo one byte longer": func(x *normalize.Expectation) {
				setBytes(x, "write", 5, 6)
			},
		})
	})
}

// TestFileBasic: one process's open/write/close/open/read/close of one file
// pass the file-basic golden.
func TestFileBasic(t *testing.T) {
	enableParallelIfRequested(t)
	h := newHarness(t)
	run := traceJudged(t, h, startScenario("file-basic"))
	assertOnlyTracedPIDs(t, run.Trace.Events, run.pids)

	got := normalized(t, h, run, "file-basic")
	want := expectation(t, "file-basic")
	assertGolden(t, got, want)

	t.Run("wrong expectations are caught", func(t *testing.T) {
		assertCaught(t, got, want, map[string]func(*normalize.Expectation){
			"write one byte longer": func(x *normalize.Expectation) {
				setBytes(x, "write", 4096, 4097)
			},
			"traced path forbidden": func(x *normalize.Expectation) {
				x.MustNotAppear = []string{"file-basic.dat"}
			},
			"file never opened": func(x *normalize.Expectation) {
				x.Edges = append(slices.Clone(x.Edges), normalize.ExpectedEdge{
					Type: schema.EdgeOpened, From: "$p1", To: "file:$h1:$tmp/never-opened.dat"})
			},
		})
	})
}

// TestUntracedProcessStaysOut: a second file-basic process, started and
// released alongside the traced one but not named in -pid, leaves no event
// and no graph node, while the traced one still passes its golden.
func TestUntracedProcessStaysOut(t *testing.T) {
	enableParallelIfRequested(t)
	h := newHarness(t)
	var untraced int
	run := traceJudged(t, h, func(h *Harness) ([]*Workload, error) {
		traced, err := h.StartWorkload("file-basic")
		if err != nil {
			return nil, err
		}
		other, err := h.StartUntraced(t.TempDir(), "file-basic")
		if err != nil {
			return nil, err
		}
		untraced = other.PID
		return []*Workload{traced, other}, nil
	})
	if len(run.pids) != 1 || run.pids[0] == untraced {
		t.Fatalf("traced pids %v, untraced %d: the control is not set up", run.pids, untraced)
	}
	assertOnlyTracedPIDs(t, run.Trace.Events, run.pids)
	marker := fmt.Sprintf("/%d@", untraced)
	for _, n := range run.Trace.Graph.Nodes {
		if n.Type == schema.NodeProcess && strings.Contains(n.ID, marker) {
			t.Errorf("graph has a node for the untraced pid %d: %s", untraced, n.ID)
		}
	}
	assertGolden(t, normalized(t, h, run, "file-basic"), expectation(t, "file-basic"))
}

// assertCaught checks got against each mutated copy of want and fails for
// every mutation the golden check does not report: a check that passes a
// wrong golden would pass anything.
func assertCaught(t *testing.T, got normalize.Trace, want normalize.Expectation,
	mutations map[string]func(*normalize.Expectation)) {
	t.Helper()
	for name, mutate := range mutations {
		wrong := want
		wrong.Events = slices.Clone(want.Events)
		mutate(&wrong)
		if len(normalize.Check(got, wrong)) == 0 {
			t.Errorf("%s: the golden check passed an expectation that does not hold", name)
		}
	}
}

// setBytes changes the declared byte count of every event of kind whose
// count is from to to. The events slice must be a copy (assertCaught).
func setBytes(x *normalize.Expectation, kind string, from, to uint64) {
	for i, e := range x.Events {
		if e.Event == kind && e.Bytes != nil && *e.Bytes == from {
			x.Events[i].Bytes = &to
		}
	}
}

// assertOneFusedConnection checks the raw graph: one process node per
// traced pid, exactly one socket node, and exactly two connected edges,
// both fused, from the two processes to that socket node.
func assertOneFusedConnection(t *testing.T, g schema.Graph, pids []int) {
	t.Helper()
	if len(pids) != 2 {
		t.Fatalf("want a client and a server pid, got %v", pids)
	}
	var sockets []string
	for _, n := range g.Nodes {
		if n.Type == schema.NodeSocket {
			sockets = append(sockets, n.ID)
		}
	}
	if len(sockets) != 1 {
		t.Fatalf("want exactly one socket node, got %v", sockets)
	}
	var from []string
	for _, e := range g.Edges {
		if e.Type != schema.EdgeConnected {
			continue
		}
		if e.State != schema.StateFused || e.To != sockets[0] {
			t.Errorf("connected edge %s -> %s is %q, want fused to %s", e.From, e.To, e.State, sockets[0])
		}
		from = append(from, e.From)
	}
	if len(from) != 2 {
		t.Fatalf("want one fused connection (2 connected edges), got %d: %v", len(from), from)
	}
	for _, pid := range pids {
		if !slices.ContainsFunc(from, func(id string) bool { return strings.Contains(id, fmt.Sprintf("/%d@", pid)) }) {
			t.Errorf("no connected edge from pid %d: %v", pid, from)
		}
	}
}
