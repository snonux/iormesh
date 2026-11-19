package normalize

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"iormesh/internal/schema"
)

// TestOtelGoldenIDsDeclaredByScenario: every id the otel golden expects
// is one the scenario's source declares, so the two cannot drift apart
// (the workload's server already fails a run whose ids differ on the
// wire from the declaration). Before task 4b4 promoted it, this checked
// expect-pending.json.
func TestOtelGoldenIDsDeclaredByScenario(t *testing.T) {
	root := filepath.Join("..", "..", "workload")
	src, err := os.ReadFile(filepath.Join(root, "otel", "src", "iormesh", "workload", "otel",
		"OtelScenario.java"))
	if err != nil {
		t.Fatal(err)
	}
	x, err := LoadExpectation(filepath.Join(root, "scenarios", "otel", "expect.json"))
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, ev := range x.Events {
		if ev.Otel == nil {
			continue
		}
		seen++
		for _, id := range []string{ev.Otel.TraceID, ev.Otel.SpanID} {
			if !strings.Contains(string(src), `"`+id+`"`) {
				t.Errorf("otel id %s is not declared in OtelScenario.java", id)
			}
		}
	}
	if seen == 0 {
		t.Fatal("otel golden declares no otel ids")
	}
}

// TestNoPendingGoldens: the M3 goldens written ahead of the agent are
// all promoted (task 4b4 was the last); a new expect-pending.json would
// be checked by nothing.
func TestNoPendingGoldens(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "workload", "scenarios", "*",
		"expect-pending.json"))
	if err != nil || len(paths) != 0 {
		t.Fatalf("pending goldens %v (%v): load their fields into ExpectedEvent and promote them",
			paths, err)
	}
}

// TestExpectedOtelRejectsMalformed keeps the shape check honest: each
// malformed otel declaration is refused, a well-formed one loads.
func TestExpectedOtelRejectsMalformed(t *testing.T) {
	doc := func(event string) []byte {
		return []byte(`{"scenario": "s", "schema_version": 1, "events": [` + event + `],
			"edges": [], "must_not_appear": []}`)
	}
	bad := map[string]string{
		"otel without attribution": `{"thread": "$t1", "event": "read", "otel": {
			"trace_id": "a3ce929d0e0e47364bf92f3577b34d01", "span_id": "c11e0000000000a1"}}`,
		"short trace id": `{"thread": "$t1", "event": "read", "attribution": "java", "otel": {
			"trace_id": "a3ce929d", "span_id": "c11e0000000000a1"}}`,
		"zero span id": `{"thread": "$t1", "event": "read", "attribution": "java", "otel": {
			"trace_id": "a3ce929d0e0e47364bf92f3577b34d01", "span_id": "0000000000000000"}}`,
		"upper-case trace id": `{"thread": "$t1", "event": "read", "attribution": "java", "otel": {
			"trace_id": "A3CE929D0E0E47364BF92F3577B34D01", "span_id": "c11e0000000000a1"}}`,
		"unknown key": `{"thread": "$t1", "event": "read", "attribution": "java", "otel": {
			"trace_id": "a3ce929d0e0e47364bf92f3577b34d01", "span_id": "c11e0000000000a1",
			"flags": 1}}`,
	}
	for name, event := range bad {
		if _, err := ParseExpectation(doc(event)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	good := `{"thread": "$t1", "event": "read", "attribution": "java", "otel": {
		"trace_id": "a3ce929d0e0e47364bf92f3577b34d01", "span_id": "c11e0000000000a1"}}`
	if _, err := ParseExpectation(doc(good)); err != nil {
		t.Fatalf("well-formed otel declaration: %v", err)
	}
}

// TestOtelMatch: a declared span matches only that span; no declaration
// matches only an event outside any span.
func TestOtelMatch(t *testing.T) {
	span := &schema.Otel{TraceID: "a3ce929d0e0e47364bf92f3577b34d01", SpanID: "c11e0000000000a1"}
	other := &schema.Otel{TraceID: span.TraceID, SpanID: "c11e0000000000a2"}
	want := &ExpectedOtel{TraceID: span.TraceID, SpanID: span.SpanID}
	for _, c := range []struct {
		name string
		want *ExpectedOtel
		got  *schema.Otel
		ok   bool
	}{
		{"same span", want, span, true},
		{"other span", want, other, false},
		{"declared, none", want, nil, false},
		{"none declared, span", nil, span, false},
		{"neither", nil, nil, true},
	} {
		if otelMatch(c.want, c.got) != c.ok {
			t.Errorf("%s: match %v, want %v", c.name, !c.ok, c.ok)
		}
	}
}
