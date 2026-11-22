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

// TestRawEventCommandIsInert (task 3i4): a ref is built from segment
// file names a traced host chooses, so the offered `iormeshctl query
// -ref` command quotes it as one inert shell word; a plain ref stays
// unquoted.
func TestRawEventCommandIsInert(t *testing.T) {
	for ref, want := range map[string]string{
		"p/h/x;touch PWNED;y-s/1": `raw event  iormeshctl query -ref 'p/h/x;touch PWNED;y-s/1'` + "\n",
		"p/h/x'$(id)-s/1":         `raw event  iormeshctl query -ref 'p/h/x'\''$(id)-s/1'` + "\n",
		"p/h/app-1.example.org-a1b2c3d4/7": "raw event  iormeshctl query -ref " +
			"p/h/app-1.example.org-a1b2c3d4/7\n",
	} {
		c := viewmodel.Conversation{ID: "0001", Steps: []viewmodel.Step{{Kind: viewmodel.StepFile,
			Label: "read /m", Ref: ref, Lane: -1, ToLane: -1}}}
		var b strings.Builder
		stepDetail(&b, c, 0)
		if !strings.Contains(b.String(), want) {
			t.Errorf("ref %q: detail lacks %q:\n%s", ref, want, b.String())
		}
	}
}

// TestWholeRunKeepsShellQuoting (task 1i4): the TUI's model goes through
// viewmodel.Safe, which must not Go-quote a run's query — that broke its
// single quotes, so pasting it ran the path's text. A rune that is not
// printable becomes '?' and the quoting stays as built.
func TestWholeRunKeepsShellQuoting(t *testing.T) {
	q := `iormeshctl query -file '/up/x'\'';echo>PWNED;'\''x` + "\x01'"
	run := viewmodel.Step{Kind: viewmodel.StepFile, Label: "read /m (2 calls)", Count: 2,
		Ref: "p/h/s/10", LastRef: "p/h/s/11", RunQuery: q, Lane: -1, ToLane: -1}
	m := New(viewmodel.Model{Conversations: []viewmodel.Conversation{{ID: "0001",
		Steps: []viewmodel.Step{run}}}})
	var b strings.Builder
	stepDetail(&b, m.vm.Conversations[0], 0)
	want := "whole run  " + strings.Replace(q, "\x01", "?", 1) + "\n"
	if !strings.Contains(b.String(), want) {
		t.Errorf("whole run line is not %q:\n%s", want, b.String())
	}
}
