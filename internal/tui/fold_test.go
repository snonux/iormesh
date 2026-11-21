package tui

import (
	"strings"
	"testing"

	"iormesh/internal/viewmodel"
)

// TestFoldedStepDetail: a folded run's detail (task vg4) says its bytes
// and latency are the run's sums and its stack and ref its first
// call's, and gives its last ref and the query for the whole run; a
// single call's says none of it.
func TestFoldedStepDetail(t *testing.T) {
	run := viewmodel.Step{Kind: viewmodel.StepFile, Label: "read /m (3 calls)", Count: 3, Bytes: 12,
		Latency: 3000, Ordering: viewmodel.OrderCausal, Stack: []string{"a.B#c"}, Ref: "p/h/s/10",
		LastRef: "p/h/s/12", RunQuery: "iormeshctl query -thread 21", Lane: -1, ToLane: -1}
	one := run
	one.Count, one.LastRef, one.RunQuery, one.Label = 0, "", "", "read /m"
	c := viewmodel.Conversation{ID: "0001", Steps: []viewmodel.Step{run, one}}
	var b strings.Builder
	stepDetail(&b, c, 0)
	for _, want := range []string{"bytes      12 (3 calls)", "latency    3µs (sum of 3 calls)",
		"java stack (top first, first call)", "ref        p/h/s/10 (first of 3)", "last ref   p/h/s/12",
		"whole run  iormeshctl query -thread 21"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("folded step detail lacks %q:\n%s", want, b.String())
		}
	}
	b.Reset()
	stepDetail(&b, c, 1)
	if s := b.String(); strings.Contains(s, "calls") || strings.Contains(s, "first call") ||
		strings.Contains(s, "first of") || strings.Contains(s, "whole run") {
		t.Errorf("a single call's detail is qualified:\n%s", s)
	}
}
