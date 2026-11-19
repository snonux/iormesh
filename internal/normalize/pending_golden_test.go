package normalize

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// M3's scenarios (tasks ya4, za4) landed before the Java agent, so part
// of their golden was written against fields the expectation format could
// not load yet, in expect-pending.json beside the loadable expect.json.
// Tasks 2b4 (attribution, java) and 3b4 (http events) taught
// ExpectedEvent those fields and promoted method-flow's and the HTTP
// scenarios' files. What is left pending is the otel object — the trace
// and span ids of the OpenTelemetry span current when the I/O ran, which
// the agent reads in M3 step 8 (task 4b4): otel's expect.json is its
// pending file without them.

// TestPendingGoldensExtendLoadable: each expect-pending.json, stripped of
// its otel objects, is exactly its scenario's expect.json — so the two
// cannot drift apart before the pending file is promoted — and the ids it
// adds are well-formed.
func TestPendingGoldensExtendLoadable(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "workload", "scenarios", "*",
		"expect-pending.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no pending goldens found: %v", err)
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		stripped, err := stripPending(raw)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		got, err := ParseExpectation(stripped)
		if err != nil {
			t.Fatalf("%s without its otel objects: %v", path, err)
		}
		want, err := LoadExpectation(filepath.Join(filepath.Dir(path), "expect.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s without its otel objects differs from expect.json", path)
		}
	}
}

// pendingEvent is the part of a declared event the pending check reads.
type pendingEvent struct {
	Event       string `json:"event"`
	Attribution string `json:"attribution"`
	Otel        *struct {
		TraceID string `json:"trace_id"`
		SpanID  string `json:"span_id"`
	} `json:"otel"`
}

// stripPending drops the otel key of every declared event, after checking
// what it drops: otel ids only with attribution "java", valid W3C ids.
func stripPending(raw []byte) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	var events []json.RawMessage
	if err := json.Unmarshal(doc["events"], &events); err != nil {
		return nil, err
	}
	kept := []map[string]json.RawMessage{}
	for i, ev := range events {
		var p pendingEvent
		var e map[string]json.RawMessage
		if err := json.Unmarshal(ev, &p); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(ev, &e); err != nil {
			return nil, err
		}
		if msg := p.problem(); msg != "" {
			return nil, fmt.Errorf("declared event %d: %s", i+1, msg)
		}
		delete(e, "otel")
		kept = append(kept, e)
	}
	b, err := json.Marshal(kept)
	if err != nil {
		return nil, err
	}
	doc["events"] = b
	return json.Marshal(doc)
}

// otelIDRE: W3C trace-context ids are lowercase hex — 32 digits for a
// trace id, 16 for a span id (all-zero ids are invalid, checked apart).
var (
	traceIDRE = regexp.MustCompile(`^[0-9a-f]{32}$`)
	spanIDRE  = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

// problem names what is malformed in the otel object of one event: otel
// ids come from the agent, so only with attribution java, and they must
// be valid W3C ids.
func (p pendingEvent) problem() string {
	switch {
	case p.Otel == nil:
		return ""
	case p.Attribution != "java":
		return "otel object without attribution java"
	case !traceIDRE.MatchString(p.Otel.TraceID) || strings.Trim(p.Otel.TraceID, "0") == "":
		return "otel.trace_id " + p.Otel.TraceID + " is not a valid W3C trace id"
	case !spanIDRE.MatchString(p.Otel.SpanID) || strings.Trim(p.Otel.SpanID, "0") == "":
		return "otel.span_id " + p.Otel.SpanID + " is not a valid W3C span id"
	}
	return ""
}

// TestOtelGoldenIDsDeclaredByScenario: every id the otel golden expects
// is one the scenario's source declares, so the two cannot drift apart
// (the workload's server already fails a run whose ids differ on the
// wire from the declaration).
func TestOtelGoldenIDsDeclaredByScenario(t *testing.T) {
	root := filepath.Join("..", "..", "workload")
	src, err := os.ReadFile(filepath.Join(root, "otel", "src", "iormesh", "workload", "otel",
		"OtelScenario.java"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "scenarios", "otel", "expect-pending.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Events []pendingEvent `json:"events"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, ev := range doc.Events {
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

// TestStripPendingRejectsMalformed keeps the shape check honest: each
// malformed otel declaration is refused, a well-formed one is dropped.
func TestStripPendingRejectsMalformed(t *testing.T) {
	doc := func(event string) []byte {
		return []byte(`{"scenario": "s", "schema_version": 1, "events": [` + event + `]}`)
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
	}
	for name, event := range bad {
		if _, err := stripPending(doc(event)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	good := `{"thread": "$t1", "event": "read", "attribution": "java", "otel": {
		"trace_id": "a3ce929d0e0e47364bf92f3577b34d01", "span_id": "c11e0000000000a1"}}`
	out, err := stripPending(doc(good))
	if err != nil || strings.Contains(string(out), "otel") || !strings.Contains(string(out), "java") {
		t.Fatalf("well-formed otel object: %s, %v", out, err)
	}
}
