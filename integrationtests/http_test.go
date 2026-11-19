package integrationtests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"iormesh/internal/normalize"
	"iormesh/internal/schema"
)

// otelJarDefault is `mage workloadOtelBuild`'s output, which `mage
// integrationTest` builds first: the OTEL flavor's jar, run like
// workload.jar (its manifest names workload.jar beside it).
const otelJarDefault = "../workload-otel.jar"

// startHTTPPair starts an HTTP scenario's server and then its client on a
// free port, the client released only once the server listens. dir
// passes --dir to both (http-tls's certificate hand-over); the others
// take none.
func startHTTPPair(t *testing.T, scenario string, dir bool) starter {
	return func(h *Harness) ([]*Workload, error) {
		port := freePort(t)
		start := h.StartNoDir
		if dir {
			start = h.StartWorkload
		}
		server, err := start(scenario, "--role", "server", "--port", port)
		if err != nil {
			return nil, err
		}
		server.ReleasedWhen = "listening "
		client, err := start(scenario, "--role", "client", "--port", port)
		if err != nil {
			return nil, err
		}
		return []*Workload{server, client}, nil
	}
}

// traceHTTP traces one HTTP scenario's pair under the agent and returns
// it normalized, with what every HTTP run must show: only the pair's
// pids, a consistent Java side, two rings read and every request found
// on its connection.
func traceHTTP(t *testing.T, h *Harness, scenario string, dir bool) (tracedRun, normalize.Trace) {
	t.Helper()
	h.JavaAgent = absPath(t, agentJarDefault)
	h.Spool = filepath.Join(h.Dir, "spool")
	run := traceJudged(t, h, startHTTPPair(t, scenario, dir))
	assertOnlyTracedPIDs(t, run.Trace.Events, run.pids)
	assertJavaConsistent(t, run.Trace)
	if !strings.Contains(run.Logged, "java rings 2 (") || !strings.Contains(run.Logged, "(no connection 0)") {
		t.Errorf("summary does not report two rings and every request on its connection: %s",
			run.Logged)
	}
	assertHTTPTimed(t, run.Trace.Events, scenario == "http-async")
	return run, normalized(t, h, run, scenario)
}

// TestHTTPPlain: http-plain's pair under the agent (M3 step 7, task 3b4)
// records one http event per request on each end — method, scheme, path,
// status, the client's host — after the I/O that completed it, on the
// thread and in the method that made it, both ends on one connection and
// numbered 1, 2, 3 on it, so the hops pair by (conn_id, req_seq).
func TestHTTPPlain(t *testing.T) {
	enableParallelIfRequested(t)
	h := newHarness(t)
	_, got := traceHTTP(t, h, "http-plain", false)
	want := expectation(t, "http-plain")
	assertGolden(t, got, want)
	assertRequestsPaired(t, got.Events)
	assertNoOtel(t, got.Events)
	t.Run("wrong expectations are caught", func(t *testing.T) {
		assertCaught(t, got, want, map[string]func(*normalize.Expectation){
			"another status": func(x *normalize.Expectation) {
				s := uint16(500)
				httpOf(x, 3).Status = &s
			},
			"another ordinal": func(x *normalize.Expectation) {
				n := uint32(2)
				httpOf(x, 3).ReqSeq = &n
			},
			"another causing method": func(x *normalize.Expectation) {
				j := *x.Events[3].Java
				j.Method = "iormesh.workload.scenarios.HttpFetch#call"
				x.Events[3].Java = &j
			},
		})
	})
}

// TestHTTPAsync: http-async's client sends through HttpClient.sendAsync
// (task wd4): each response completes on the client's executor, yet its
// http event carries the sending thread and method (HttpFetch#callAsync).
// Its record is stamped when the agent's completion ran, after the app's
// own callbacks, so it has no latency and no req_seq (review of wd4:
// never a guessed ordinal), while the server numbers its end 1, 2, 3.
// The client names its server "localhost": its lookup is one dns event
// (decision #8) mapping the name to 127.0.0.1, on the thread that then
// connects.
func TestHTTPAsync(t *testing.T) {
	enableParallelIfRequested(t)
	h := newHarness(t)
	_, got := traceHTTP(t, h, "http-async", false)
	want := expectation(t, "http-async")
	assertGolden(t, got, want)
	assertAsyncUnnumbered(t, got.Events)
	t.Run("wrong expectations are caught", func(t *testing.T) {
		assertCaught(t, got, want, map[string]func(*normalize.Expectation){
			"the synchronous send": func(x *normalize.Expectation) {
				// Declared event 12 is the client's first http event.
				j := *x.Events[12].Java
				j.Method = "iormesh.workload.scenarios.HttpFetch#call"
				x.Events[12].Java = &j
			},
			// Declared event 10 is the client's lookup of localhost.
			"another address": func(x *normalize.Expectation) {
				x.Events[10].DNS = &normalize.ExpectedDNS{Host: "localhost",
					Addrs: []string{"10.0.0.1"}}
			},
		})
	})
}

// TestHTTPTLS: the same over TLS — the events come from the API
// boundary, so the requests are seen in the clear without touching the
// wire, scheme https on both ends.
func TestHTTPTLS(t *testing.T) {
	enableParallelIfRequested(t)
	h := newHarness(t)
	_, got := traceHTTP(t, h, "http-tls", true)
	want := expectation(t, "http-tls")
	assertGolden(t, got, want)
	assertRequestsPaired(t, got.Events)
	t.Run("wrong expectations are caught", func(t *testing.T) {
		assertCaught(t, got, want, map[string]func(*normalize.Expectation){
			"plain scheme": func(x *normalize.Expectation) {
				httpOf(x, 7).Scheme = "http"
			},
		})
	})
}

// TestHTTPRedaction: the planted Authorization, X-Api-Token, Cookie and
// Set-Cookie values and the query secret never reach the trace (decision
// #9 / #22) — not the trace files and not the agent's rings in the
// spool — while their headers are recorded, masked, and the query keeps
// its names with every value masked.
func TestHTTPRedaction(t *testing.T) {
	enableParallelIfRequested(t)
	h := newHarness(t)
	run, got := traceHTTP(t, h, "http-redaction", false)
	want := expectation(t, "http-redaction")
	assertGolden(t, got, want)
	assertRequestsPaired(t, got.Events)
	files := []string{eventsFile, graphFile, "methods.ndjson", "threads.ndjson", "classes.ndjson"}
	for i, f := range files {
		files[i] = filepath.Join(run.Dir, f)
	}
	// The agent redacts before the spool: the rings it wrote must not
	// hold a planted secret either, not even until the tracer read them.
	rings, err := filepath.Glob(filepath.Join(h.Spool, "*.ring"))
	if err != nil || len(rings) == 0 {
		t.Fatalf("no agent ring left in the spool: %v", err)
	}
	for _, path := range append(files, rings...) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "Hunter2Planted") {
			t.Errorf("%s holds a planted secret", path)
		}
	}
	t.Run("wrong expectations are caught", func(t *testing.T) {
		assertCaught(t, got, want, map[string]func(*normalize.Expectation){
			// The walker must reach the headers: the masked form is in them.
			"the masked header is forbidden": func(x *normalize.Expectation) {
				x.MustNotAppear = []string{"authorization: ***"}
			},
			"the header in the clear": func(x *normalize.Expectation) {
				httpOf(x, 3).ReqHeaders = []string{"x-api-token: Hunter2PlantedApiTokenHeader"}
			},
		})
	})
}

// TestOtelHTTP: the OTEL flavor's pair (HttpURLConnection against the
// JDK's HttpServer) under the agent satisfies its golden — the
// HttpURLConnection hook's http events included, and since M3 step 8
// (task 4b4) the otel ids: every client event of a call and the server's
// reply and http event carry the declared span, the server's accept and
// request reads (before its handler makes the span current) none.
func TestOtelHTTP(t *testing.T) {
	enableParallelIfRequested(t)
	h := newHarness(t)
	h.WorkloadJar = absPath(t, otelJarDefault)
	_, got := traceHTTP(t, h, "otel", false)
	want := expectation(t, "otel")
	assertGolden(t, got, want)
	assertRequestsPaired(t, got.Events)
	t.Run("wrong expectations are caught", func(t *testing.T) {
		assertCaught(t, got, want, map[string]func(*normalize.Expectation){
			// Declared event 3 is the server's first reply write, in span 5e7e…a1.
			"another span": func(x *normalize.Expectation) {
				x.Events[2].Otel = &normalize.ExpectedOtel{TraceID: x.Events[2].Otel.TraceID,
					SpanID: "5e7e0000000000a2"}
			},
			"a span ends early": func(x *normalize.Expectation) { x.Events[2].Otel = nil },
			// Declared event 2 is the server's request read, outside any span.
			"a span starts early": func(x *normalize.Expectation) {
				x.Events[1].Otel = x.Events[2].Otel
			},
		})
	})
}

// assertNoOtel: an app without OpenTelemetry gets no otel object on any
// event — the agent arms span reading only when the app's OTEL
// initializes (task 4b4).
func assertNoOtel(t *testing.T, events []schema.Event) {
	t.Helper()
	for _, e := range events {
		if e.Otel != nil {
			t.Fatalf("event seq %d (%s) carries otel %+v without OTEL in the app",
				e.Seq, e.Event, e.Otel)
		}
	}
}

// httpOf replaces declared event i's http object with a copy and returns
// it: assertCaught copies the events, not what they point to.
func httpOf(x *normalize.Expectation, i int) *normalize.ExpectedHTTP {
	h := *x.Events[i].HTTP
	x.Events[i].HTTP = &h
	return &h
}

// assertRequestsPaired: the client's and the server's http events pair
// one to one by (conn_id, req_seq) with the same method, path and status
// — the cross-wire pairing the merge relies on (PLAN.md "Request pairing
// across the wire").
func assertRequestsPaired(t *testing.T, events []schema.Event) {
	t.Helper()
	type key struct {
		conn string
		seq  uint32
	}
	ends := map[key][]*schema.HTTP{}
	for _, e := range events {
		if e.Event == schema.KindHTTP && e.HTTP.ReqSeq > 0 {
			k := key{e.ConnID, e.HTTP.ReqSeq}
			ends[k] = append(ends[k], e.HTTP)
		}
	}
	if len(ends) == 0 {
		t.Fatal("no numbered http event")
	}
	for k, hs := range ends {
		if len(hs) != 2 || hs[0].Method != hs[1].Method || hs[0].Path != hs[1].Path ||
			hs[0].Status != hs[1].Status || !sizesAgree(hs[0].ReqBytes, hs[1].ReqBytes) ||
			!sizesAgree(hs[0].RespBytes, hs[1].RespBytes) {
			t.Errorf("request %+v: ends %+v, want a client and a server that agree", k, hs)
		}
	}
}

// sizesAgree: two ends' body sizes of one request are equal where both
// knew them (task wd4); an end that did not know (absent) agrees with any.
func sizesAgree(a, b *uint64) bool {
	return a == nil || b == nil || *a == *b
}

// assertAsyncUnnumbered: http-async's client events (the ones with a
// host) carry no req_seq and no latency, its server events req_seq 1-3.
func assertAsyncUnnumbered(t *testing.T, events []schema.Event) {
	t.Helper()
	var server []uint32
	for _, e := range events {
		switch {
		case e.Event != schema.KindHTTP:
		case e.HTTP.Host != "" && (e.HTTP.ReqSeq != 0 || e.LatencyNS != 0):
			t.Errorf("sendAsync event seq %d: req_seq %d, latency %d, want neither",
				e.Seq, e.HTTP.ReqSeq, e.LatencyNS)
		case e.HTTP.Host == "":
			server = append(server, e.HTTP.ReqSeq)
		}
	}
	if len(server) != 3 || server[0] != 1 || server[1] != 2 || server[2] != 3 {
		t.Errorf("server req_seq %v, want 1, 2, 3", server)
	}
}

// assertHTTPTimed: every http event of the JDK's synchronous libraries
// spans its request (task wd4) — the client's send or response read and
// the server's dispatch are entry-stamped on the thread that records the
// event, so each has a start and a latency. With asyncClient the client
// events are sendAsync's, which have none (their stamp follows the app's
// callbacks: assertAsyncUnnumbered), and are skipped.
func assertHTTPTimed(t *testing.T, events []schema.Event, asyncClient bool) {
	t.Helper()
	for _, e := range events {
		skip := asyncClient && e.HTTP != nil && e.HTTP.Host != ""
		if e.Event == schema.KindHTTP && e.LatencyNS == 0 && !skip {
			t.Errorf("http event seq %d (%s %s) has no latency", e.Seq, e.HTTP.Method, e.HTTP.Path)
		}
	}
}
