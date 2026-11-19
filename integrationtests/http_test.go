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
	files := []string{eventsFile, graphFile, "methods.ndjson", "threads.ndjson"}
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
// HttpURLConnection hook's http events included. The otel ids the golden
// will add stay pending (expect-pending.json) until the agent reads span
// context (M3 step 8, task 4b4).
func TestOtelHTTP(t *testing.T) {
	enableParallelIfRequested(t)
	h := newHarness(t)
	h.WorkloadJar = absPath(t, otelJarDefault)
	_, got := traceHTTP(t, h, "otel", false)
	assertGolden(t, got, expectation(t, "otel"))
	assertRequestsPaired(t, got.Events)
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
			hs[0].Status != hs[1].Status {
			t.Errorf("request %+v: ends %+v, want a client and a server that agree", k, hs)
		}
	}
}
