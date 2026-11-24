package integrationtests

// M2 step 6 end to end: the fd lifecycle under the real tracer — fd reuse,
// a dup, and stdio a child JVM inherits — passes fd-lifecycle's golden,
// and the per-process bracket cut keeps the children a bracketed process
// forks (docs/milestones.md M2; docs/implementation.md M2 step 6).

import (
	"encoding/json"
	"slices"
	"testing"

	"iormesh/internal/normalize"
	"iormesh/internal/schema"
)

// TestFdLifecycle: the child JVM, which emits no markers, is kept through
// the forked edge, it issues its dup2s and its read and write on fd 0
// and 1, and in the parent the dup's copy is an instance of its own. fd
// symbols bind per process, so the golden cannot show the child's dup2
// acts on the parent's very instances; fork inheritance itself is unit-
// tested (TestForkInheritsTable).
func TestFdLifecycle(t *testing.T) {
	enableParallelIfRequested(t)
	h := newHarness(t)
	run := traceJudged(t, h, startScenario("fd-lifecycle"))
	assertTracedOrForked(t, run.Trace, run.pids)
	got := normalized(t, h, run, "fd-lifecycle")
	want := expectation(t, "fd-lifecycle")
	assertGolden(t, got, want)
	t.Run("wrong expectations are caught", func(t *testing.T) {
		assertCaught(t, got, want, map[string]func(*normalize.Expectation){
			"the dup's copy is the original": func(x *normalize.Expectation) {
				setFD(t, x, schema.KindClose, "$fd4", "$fd3")
			},
			"the child forked the parent": func(x *normalize.Expectation) {
				x.Edges = slices.Clone(x.Edges)
				for i, e := range x.Edges {
					if e.Type == schema.EdgeForked {
						x.Edges[i].From, x.Edges[i].To = e.To, e.From
					}
				}
			},
		})
	})
}

// TestForkedChildrenKept: pipe-ipc's and signals' child JVMs emit no
// markers either; the bracket cut must keep them (their goldens use kinds
// M2 step 7 adds, so only the cut is checked here).
func TestForkedChildrenKept(t *testing.T) {
	for _, scenario := range []string{"pipe-ipc", "signals"} {
		t.Run(scenario, func(t *testing.T) {
			enableParallelIfRequested(t)
			h := newHarness(t)
			run := traceJudged(t, h, func(h *Harness) ([]*Workload, error) {
				w, err := h.StartOptionless(scenario)
				return []*Workload{w}, err
			})
			assertTracedOrForked(t, run.Trace, run.pids)
			got := normalized(t, h, run, scenario)
			procs := map[uint32]bool{}
			for _, e := range got.Events {
				if e.PID != 0 {
					procs[e.PID] = true
				}
			}
			if len(procs) < 2 {
				t.Fatalf("bracketed trace holds %d process(es), want the workload and its children",
					len(procs))
			}
		})
	}
}

// assertTracedOrForked fails for an event of a process that is neither a
// target nor forked, directly or not, by one: following children must not
// let unrelated processes in.
func assertTracedOrForked(t *testing.T, tr normalize.Trace, pids []int) {
	t.Helper()
	family := map[string]bool{}
	for _, e := range tr.Events {
		if slices.Contains(pids, int(e.PID)) {
			family[schema.ProcessNodeID(e.HostID, e.PID, e.PIDInc)] = true
		}
	}
	for grew := true; grew; {
		grew = false
		for _, e := range tr.Graph.Edges {
			if e.Type == schema.EdgeForked && family[e.From] && !family[e.To] {
				family[e.To], grew = true, true
			}
		}
	}
	for _, e := range tr.Events {
		if e.PID != 0 && !family[schema.ProcessNodeID(e.HostID, e.PID, e.PIDInc)] {
			t.Fatalf("event %d (%s) of pid %d, neither traced (%v) nor forked by it",
				e.Seq, e.Event, e.PID, pids)
		}
	}
}

// setFD changes the fd symbol of every declared event of kind that has
// from to to. The events slice must be a copy (assertCaught).
func setFD(t *testing.T, x *normalize.Expectation, kind, from, to string) {
	t.Helper()
	var ref normalize.Ref
	if err := json.Unmarshal([]byte(`"`+to+`"`), &ref); err != nil {
		t.Fatal(err)
	}
	for i, e := range x.Events {
		if e.Event == kind && e.FD != nil && e.FD.String() == from {
			x.Events[i].FD = &ref
		}
	}
}
