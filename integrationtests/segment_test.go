package integrationtests

// The M4 segment writer against the real tracer (task 8b4): a run that
// writes NDJSON and parquet segments at once must put the same trace in
// both — the parquet files are the same schema, columnar (PLAN.md "Two
// serializations, one schema").

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	parquetgo "github.com/parquet-go/parquet-go"

	"iormesh/internal/schema"
	"iormesh/internal/segment"
)

// TestParquetSegments: non-java-peer with -native-stacks and -trace-dir
// beside -out (every harness run has it). ready/ holds only published segments (no active file
// left, every file 0644), the events segments in name order are exactly
// the NDJSON events, and the union of the stacks tables is exactly
// stacks.ndjson.
func TestParquetSegments(t *testing.T) {
	enableParallelIfRequested(t)
	h := newHarness(t)
	h.NativeStacks = true
	run := traceJudged(t, h, startScenario("non-java-peer"))
	dir := SegmentsDir(run.Run)
	if active, _ := filepath.Glob(filepath.Join(dir, ".active-*")); len(active) != 0 {
		t.Fatalf("active files left after the run: %v", active)
	}
	events, stacks := readSegments(t, filepath.Join(dir, segment.ReadyDir))
	assertSameEvents(t, events, run.Trace.Events)
	want := readStacks(t, run)
	if len(stacks) != len(want) {
		t.Fatalf("segments hold %d stacks, stacks.ndjson %d", len(stacks), len(want))
	}
	for _, st := range stacks {
		// Whole rows: every frame's address, module, offset, build id and
		// symbol, not only the stack's shape.
		if w, ok := want[st.StackID]; !ok || !reflect.DeepEqual(st, w) {
			t.Fatalf("stack %d differs from stacks.ndjson:\nparquet %+v\nndjson  %+v", st.StackID, st, w)
		}
	}
}

// readSegments reads ready/: the events files in name order (seq order
// within a session) and the union of the stacks tables. Only the stacks
// table is expected beside the events: the run has no -spool.
func readSegments(t *testing.T, ready string) ([]schema.Event, []schema.Stack) {
	t.Helper()
	entries, err := os.ReadDir(ready)
	if err != nil {
		t.Fatal(err)
	}
	var events []schema.Event
	var stacks []schema.Stack
	for _, e := range entries {
		path := filepath.Join(ready, e.Name())
		if fi, err := e.Info(); err != nil || fi.Mode().Perm() != 0o644 {
			t.Fatalf("%s: mode %v, %v; want 0644", e.Name(), fi.Mode(), err)
		}
		switch {
		case strings.HasSuffix(e.Name(), "."+segment.TableStacks+segment.Ext):
			stacks = append(stacks, readParquet[schema.Stack](t, path)...)
		case isEventsSegment(e.Name()):
			events = append(events, readParquet[schema.Event](t, path)...)
		default:
			t.Fatalf("unexpected file in ready/: %s", e.Name())
		}
	}
	return events, stacks
}

// isEventsSegment: an events file ends in -<seq>.parquet, a table in
// -<seq>.<table>.parquet.
func isEventsSegment(name string) bool {
	base, ok := strings.CutSuffix(name, segment.Ext)
	if !ok {
		return false
	}
	i := strings.LastIndexByte(base, '-')
	return i >= 0 && !strings.Contains(base[i:], ".")
}

func readParquet[T any](t *testing.T, path string) []T {
	t.Helper()
	rows, err := parquetgo.ReadFile[T](path)
	if err != nil {
		t.Fatalf("%s: %v", filepath.Base(path), err)
	}
	return rows
}

// assertSameEvents compares the two serializations record by record as
// NDJSON lines, the contract's own equality.
func assertSameEvents(t *testing.T, got, want []schema.Event) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("segments hold %d events, events.ndjson %d", len(got), len(want))
	}
	for i := range want {
		g, w := schema.AppendEvent(nil, &got[i]), schema.AppendEvent(nil, &want[i])
		if string(g) != string(w) {
			t.Fatalf("event %d differs:\nparquet %s\nndjson  %s", i, g, w)
		}
	}
}
