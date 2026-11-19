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

// M3's scenarios (task ya4) land before the Java agent, so part of their
// golden is written against fields the expectation format cannot load
// yet: attribution "java", the java object (thread name, causing method
// "class#method") and http events (task 2b4 joins the agent's brackets,
// 3b4 adds the HTTP hooks). That part lives in expect-pending.json beside
// the loadable expect.json; 2b4/3b4 teach ExpectedEvent the fields and
// promote the file. The otel scenario (task za4) adds the otel object —
// the trace and span ids of the OpenTelemetry span current when the I/O
// ran, which the agent reads (M3 step 8).

// TestPendingGoldensExtendLoadable: each expect-pending.json, stripped of
// the pending declarations, is exactly its scenario's expect.json — so the
// two cannot drift apart before the pending file is promoted — and what it
// adds is well-formed.
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
			t.Fatalf("%s without its pending fields: %v", path, err)
		}
		want, err := LoadExpectation(filepath.Join(filepath.Dir(path), "expect.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s without its pending fields differs from expect.json", path)
		}
	}
}

// pendingEvent is the part of a declared event only expect-pending.json
// carries.
type pendingEvent struct {
	Event       string `json:"event"`
	Attribution string `json:"attribution"`
	Java        *struct {
		Thread string `json:"thread"`
		Method string `json:"method"`
	} `json:"java"`
	Otel *struct {
		TraceID string `json:"trace_id"`
		SpanID  string `json:"span_id"`
	} `json:"otel"`
	HTTP *struct {
		Method string `json:"method"`
		Scheme string `json:"scheme"`
		Host   string `json:"host"`
		Path   string `json:"path"`
		Status int    `json:"status"`
		ReqSeq int    `json:"req_seq"`
	} `json:"http"`
}

// stripPending drops http events and the attribution and java keys of the
// others, after checking what it drops: java only with attribution
// "java", a method as "class#method", an http event with its request.
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
		if p.Event == "http" {
			continue
		}
		delete(e, "attribution")
		delete(e, "java")
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

// problem names what is malformed in the pending part of one event.
func (p pendingEvent) problem() string {
	if p.Otel != nil {
		return p.otelProblem()
	}
	switch {
	case p.Java != nil && p.Attribution != "java":
		return "java object without attribution java"
	case p.Java != nil && p.Java.Method != "" && strings.Count(p.Java.Method, "#") != 1:
		return "java.method " + p.Java.Method + " is not class#method"
	case p.Event == "http" && (p.HTTP == nil || p.HTTP.Method == "" || p.HTTP.Scheme == "" ||
		p.HTTP.Path == "" || p.HTTP.Status == 0 || p.HTTP.ReqSeq < 1):
		return "http event without method, scheme, path, status and req_seq"
	case p.HTTP != nil && strings.ContainsAny(p.HTTP.Host, "/?#@"):
		// host is a bare host (client side only): no scheme, path or userinfo.
		return "http.host " + p.HTTP.Host + " is not a bare host"
	case p.Event != "http" && p.HTTP != nil:
		return "http object on a " + p.Event + " event"
	}
	return ""
}

// otelProblem: otel ids come from the agent, so only with attribution
// java, and they must be valid W3C ids.
func (p pendingEvent) otelProblem() string {
	switch {
	case p.Attribution != "java":
		return "otel object without attribution java"
	case !traceIDRE.MatchString(p.Otel.TraceID) || strings.Trim(p.Otel.TraceID, "0") == "":
		return "otel.trace_id " + p.Otel.TraceID + " is not a valid W3C trace id"
	case !spanIDRE.MatchString(p.Otel.SpanID) || strings.Trim(p.Otel.SpanID, "0") == "":
		return "otel.span_id " + p.Otel.SpanID + " is not a valid W3C span id"
	}
	p.Otel = nil
	return p.problem()
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
// malformed pending declaration is refused, a well-formed one is dropped.
func TestStripPendingRejectsMalformed(t *testing.T) {
	doc := func(event string) []byte {
		return []byte(`{"scenario": "s", "schema_version": 1, "events": [` + event + `]}`)
	}
	bad := map[string]string{
		"java without attribution": `{"thread": "$t1", "event": "read", "java": {"method": "C#m"}}`,
		"method without #":         `{"thread": "$t1", "event": "read", "attribution": "java", "java": {"method": "C.m"}}`,
		"http without req_seq": `{"thread": "$t1", "event": "http", "http": {"method": "GET",
			"scheme": "http", "path": "/", "status": 200}}`,
		"http object on a read": `{"thread": "$t1", "event": "read", "http": {"method": "GET"}}`,
		"host with a path": `{"thread": "$t1", "event": "http", "http": {"method": "GET",
			"scheme": "http", "host": "127.0.0.1/x", "path": "/", "status": 200, "req_seq": 1}}`,
		"otel without attribution": `{"thread": "$t1", "event": "read", "otel": {
			"trace_id": "a3ce929d0e0e47364bf92f3577b34d01", "span_id": "c11e0000000000a1"}}`,
		"short trace id": `{"thread": "$t1", "event": "read", "attribution": "java", "otel": {
			"trace_id": "a3ce929d", "span_id": "c11e0000000000a1"}}`,
		"zero span id": `{"thread": "$t1", "event": "read", "attribution": "java", "otel": {
			"trace_id": "a3ce929d0e0e47364bf92f3577b34d01", "span_id": "0000000000000000"}}`,
		"upper-case trace id": `{"thread": "$t1", "event": "read", "attribution": "java", "otel": {
			"trace_id": "A3CE929D0E0E47364BF92F3577B34D01", "span_id": "c11e0000000000a1"}}`,
		"otel on a malformed java": `{"thread": "$t1", "event": "read", "attribution": "java",
			"java": {"method": "C.m"}, "otel": {
			"trace_id": "a3ce929d0e0e47364bf92f3577b34d01", "span_id": "c11e0000000000a1"}}`,
	}
	for name, event := range bad {
		if _, err := stripPending(doc(event)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	good := `{"thread": "$t1", "event": "http", "attribution": "java", "java": {"method": "C#m"},
		"http": {"method": "GET", "scheme": "http", "path": "/", "status": 200, "req_seq": 1}}`
	out, err := stripPending(doc(good))
	if err != nil || strings.Contains(string(out), "http") {
		t.Fatalf("well-formed http event: %s, %v", out, err)
	}
}
