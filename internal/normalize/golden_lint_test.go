package normalize

import (
	"path/filepath"
	"testing"
)

// maxSingleRun mirrors IORMESH_RW_INDIVIDUAL (internal/c/rwagg.c): the
// tracer sends at most this many hot reads or writes on one fd in a row as
// single events before it folds the rest (M2 step 11, decision #25).
const maxSingleRun = 4

// TestGoldensDeclareFoldableRunsShort: no golden declares more than
// maxSingleRun consecutive reads or writes of one thread on one fd. A
// declaration without args.count matches only a single call, and calls
// past the threshold may be folded when they come fast, so such a golden
// would pass or fail by timing.
func TestGoldensDeclareFoldableRunsShort(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "workload", "scenarios", "*", "expect.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no goldens found: %v", err)
	}
	for _, path := range paths {
		x, err := LoadExpectation(path)
		if err != nil {
			t.Fatal(err)
		}
		if n, at := longestSingleRun(x.Events); n > maxSingleRun {
			t.Errorf("%s: %d consecutive single %ss on one fd (from declared event %d), want ≤ %d",
				path, n, x.Events[at].Event, at+1, maxSingleRun)
		}
	}
}

// longestSingleRun returns the longest run of declared reads or writes
// without args.count by one thread on one fd, in that thread's order, and
// the index of its first event. Any other declared event of the thread
// ends a run.
func longestSingleRun(events []ExpectedEvent) (longest, start int) {
	type run struct {
		key        string
		n, started int
	}
	runs := map[string]run{}
	for i, e := range events {
		thread := e.Process + "/" + e.Thread
		if (e.Event != "read" && e.Event != "write") || e.FD == nil || e.Args.Count != nil {
			delete(runs, thread)
			continue
		}
		key := e.Event + " " + e.FD.String()
		r := runs[thread]
		if r.key != key {
			r = run{key: key, started: i}
		}
		r.n++
		runs[thread] = r
		if r.n > longest {
			longest, start = r.n, r.started
		}
	}
	return longest, start
}

// TestLongestSingleRun keeps the lint honest: five writes on one fd are a
// run of five; another fd, another thread's event in between, or a count
// do not extend it, another event of the thread ends it.
func TestLongestSingleRun(t *testing.T) {
	w := func(thread, fd string) ExpectedEvent {
		return ExpectedEvent{Thread: thread, Event: "write", FD: &Ref{fd}}
	}
	five := []ExpectedEvent{w("$t1", "$fd1"), w("$t1", "$fd1"), w("$t2", "$fd1"), w("$t1", "$fd1"),
		w("$t1", "$fd1"), w("$t1", "$fd1")}
	if n, at := longestSingleRun(five); n != 5 || at != 0 {
		t.Fatalf("run %d from %d, want 5 from 0", n, at)
	}
	broken := []ExpectedEvent{w("$t1", "$fd1"), w("$t1", "$fd1"), {Thread: "$t1", Event: "close"},
		w("$t1", "$fd1"), w("$t1", "$fd2")}
	if n, _ := longestSingleRun(broken); n != 2 {
		t.Fatalf("run %d, want 2", n)
	}
}
