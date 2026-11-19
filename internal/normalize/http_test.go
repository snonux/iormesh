package normalize

import (
	"strings"
	"testing"

	"iormesh/internal/schema"
)

// TestCheckHTTP: every declared request field must equal the event's and
// every declared header line must be among its headers; undeclared
// fields and extra headers are not compared.
func TestCheckHTTP(t *testing.T) {
	ev := schema.Event{SchemaVersion: 1, Seq: 1, PID: 1, TID: 1, Event: schema.KindHTTP,
		FD: schema.NoFD, ConnID: "c9", Attribution: schema.AttributionNone,
		HTTP: &schema.HTTP{Method: "GET", Scheme: "http", Host: "127.0.0.1",
			Path: "/login?token=***", Status: 404, ReqSeq: 2, Version: "HTTP/1.1",
			ReqHeaders:  []string{"accept: */*", "authorization: ***"},
			RespHeaders: []string{"set-cookie: ***"}, ReqBytes: new(uint64)}}
	got := Trace{Events: []schema.Event{ev}}
	doc := func(http string) Expectation {
		x, err := ParseExpectation([]byte(`{"scenario": "s", "schema_version": 1, "events": [
		  {"thread": "$t1", "event": "http", "conn_id": "$c1", "http": ` + http + `}],
		  "edges": [], "must_not_appear": []}`))
		if err != nil {
			t.Fatal(err)
		}
		return x
	}
	for _, pass := range []string{
		`{"method": "GET", "path": "/login?token=***", "status": 404, "req_seq": 2}`,
		`{"host": "127.0.0.1", "version": "HTTP/1.1", "req_headers": ["authorization: ***"],
		  "resp_headers": ["set-cookie: ***"]}`,
		`{"req_bytes": 0}`,
		`{}`,
	} {
		if vs := Check(got, doc(pass)); len(vs) != 0 {
			t.Errorf("%s: violations %v", pass, vs)
		}
	}
	for _, fail := range []string{
		`{"method": "POST"}`, `{"scheme": "https"}`, `{"host": "localhost"}`,
		`{"path": "/login?token=x"}`, `{"status": 200}`, `{"req_seq": 1}`,
		`{"version": "HTTP/2"}`, `{"req_headers": ["authorization: Bearer x"]}`,
		`{"resp_headers": ["accept: */*"]}`, `{"req_bytes": 1}`, `{"resp_bytes": 0}`,
	} {
		vs := Check(got, doc(fail))
		if len(vs) != 1 || !strings.Contains(vs[0].Msg, "http") {
			t.Errorf("%s: violations %v", fail, vs)
		}
	}
}

func TestExpectationHTTPOnlyOnHTTPEvents(t *testing.T) {
	_, err := ParseExpectation([]byte(`{"scenario": "s", "schema_version": 1, "events": [
	  {"thread": "$t1", "event": "write", "http": {"method": "GET"}}],
	  "edges": [], "must_not_appear": []}`))
	if err == nil {
		t.Error("http declared on a write accepted")
	}
}

// TestCheckDNS: a dns declaration matches by host and by each declared
// address among the event's; it is refused on another kind.
func TestCheckDNS(t *testing.T) {
	ev := schema.Event{SchemaVersion: 1, Seq: 1, PID: 1, TID: 1, Event: schema.KindDNS,
		FD: schema.NoFD, Attribution: schema.AttributionNone,
		DNS: &schema.DNS{Host: "localhost", Addrs: []string{"127.0.0.1", "0:0:0:0:0:0:0:1"}}}
	got := Trace{Events: []schema.Event{ev}}
	doc := func(kind, dns string) (Expectation, error) {
		return ParseExpectation([]byte(`{"scenario": "s", "schema_version": 1, "events": [
		  {"thread": "$t1", "event": "` + kind + `", "dns": ` + dns + `}],
		  "edges": [], "must_not_appear": []}`))
	}
	for dns, ok := range map[string]bool{
		`{"host": "localhost", "addrs": ["127.0.0.1"]}`: true,
		`{"host": "localhost"}`:                         true,
		`{"host": "example.org"}`:                       false,
		`{"host": "localhost", "addrs": ["10.0.0.1"]}`:  false,
	} {
		x, err := doc("dns", dns)
		if err != nil {
			t.Fatal(err)
		}
		if vs := Check(got, x); (len(vs) == 0) != ok {
			t.Errorf("%s: violations %v, want ok=%v", dns, vs, ok)
		}
	}
	if _, err := doc("write", `{"host": "h"}`); err == nil {
		t.Error("dns declared on a write accepted")
	}
}
