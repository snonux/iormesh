package normalize

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// M3's scenarios (task ya4) land before the Java agent, so part of their
// golden is written against fields the expectation format cannot load
// yet: attribution "java", the java object (thread name, causing method
// "class#method") and http events (task 2b4 joins the agent's brackets,
// 3b4 adds the HTTP hooks). That part lives in expect-pending.json beside
// the loadable expect.json; 2b4/3b4 teach ExpectedEvent the fields and
// promote the file.

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
		kept = append(kept, e)
	}
	b, err := json.Marshal(kept)
	if err != nil {
		return nil, err
	}
	doc["events"] = b
	return json.Marshal(doc)
}

// problem names what is malformed in the pending part of one event.
func (p pendingEvent) problem() string {
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
