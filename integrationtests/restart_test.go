package integrationtests

// M2 steps 2–3 end to end: a pattern-bound session traces both
// incarnations of the restart scenario's app — never its launcher — and
// links them under one app node (PLAN.md M2 "Target spec", decision #23;
// docs/implementation.md M2 step 2).

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"iormesh/internal/normalize"
	"iormesh/internal/schema"
)

// restartPattern matches RestartApp's command line (java … -cp <jar>
// iormesh.workload.scenarios.RestartApp <dir>) and never the launcher's
// (java -jar workload.jar restart --dir <dir>).
const restartPattern = "*iormesh.workload.scenarios.RestartApp*"

// TestRestartPattern: the restart scenario under -pattern passes its
// golden — two incarnations, each with its own file work, linked by
// incarnation edges from one app node — while the launcher stays out of
// the trace, every attach gap lies before its incarnation's bracket, and
// the session ended on the stop, not by itself.
func TestRestartPattern(t *testing.T) {
	enableParallelIfRequested(t)
	h := newHarness(t)
	h.Pattern = restartPattern
	run := traceJudged(t, h, startScenario("restart"))
	launcher := run.pids[0]
	assertNoEventsOf(t, run.Trace.Events, launcher)
	assertEndReason(t, run.Trace.Events, schema.EndStop)
	assertIncarnations(t, run.Trace, schema.AppNodeID(tracerProject, restartPattern), launcher)

	got := normalized(t, h, run, "restart")
	want := expectation(t, "restart")
	assertGolden(t, got, want)
	for _, e := range got.Events {
		if e.Event == schema.KindDrop && e.Args.DropReason != nil {
			t.Errorf("attach_gap drop inside the bracket (seq %d): the attach came too late", e.Seq)
		}
	}

	t.Run("wrong expectations are caught", func(t *testing.T) {
		assertCaught(t, got, want, map[string]func(*normalize.Expectation){
			"second incarnation under another app": func(x *normalize.Expectation) {
				x.Edges = slices.Clone(x.Edges)
				for i, e := range x.Edges {
					if e.Type == schema.EdgeIncarnation && e.To == "$p2" {
						x.Edges[i].From = "$a2"
					}
				}
			},
			"one incarnation only": func(x *normalize.Expectation) {
				x.Edges = append(slices.Clone(x.Edges), normalize.ExpectedEdge{
					Type: schema.EdgeIncarnation, From: "$a1", To: "$p3"})
			},
		})
	})
}

// assertNoEventsOf fails for any event of pid.
func assertNoEventsOf(t *testing.T, events []schema.Event, pid int) {
	t.Helper()
	for _, e := range events {
		if int(e.PID) == pid {
			t.Fatalf("event %d (%s) of pid %d, which the pattern does not match", e.Seq, e.Event, pid)
		}
	}
}

// assertEndReason checks the trace's last event.
func assertEndReason(t *testing.T, events []schema.Event, want string) {
	t.Helper()
	last := events[len(events)-1]
	if last.Event != schema.KindEnd || *last.Args.EndReason != want {
		t.Fatalf("trace ends with %s %v, want end %q", last.Event, last.Args.EndReason, want)
	}
}

// assertIncarnations checks the raw trace: the app node links exactly two
// distinct process nodes (pid and incarnation), neither the launcher's, each of
// which bracketed the scenario itself, and every attach_gap drop precedes
// its process's begin marker.
func assertIncarnations(t *testing.T, tr normalize.Trace, app string, launcher int) {
	t.Helper()
	var procs []string
	for _, e := range tr.Graph.Edges {
		if e.Type == schema.EdgeIncarnation && e.From == app {
			procs = append(procs, e.To)
		}
	}
	if len(procs) != 2 || procs[0] == procs[1] {
		t.Fatalf("incarnation edges from %s to %v, want two processes", app, procs)
	}
	for _, p := range procs {
		if strings.Contains(p, fmt.Sprintf("/%d@", launcher)) {
			t.Fatalf("the launcher %d is an incarnation: %s", launcher, p)
		}
		begin, gap := -1, -1
		for i, e := range tr.Events {
			if schema.ProcessNodeID(e.HostID, e.PID, e.PIDInc) != p {
				continue
			}
			if e.Event == schema.KindMarker && *e.Args.Marker == "restart/begin" {
				begin = i
			}
			if e.Event == schema.KindDrop && e.Args.DropReason != nil {
				gap = i
			}
		}
		if begin < 0 {
			t.Fatalf("incarnation %s has no begin marker", p)
		}
		if gap > begin {
			t.Fatalf("incarnation %s: attach_gap drop at %d after its begin marker at %d", p, gap, begin)
		}
	}
}
