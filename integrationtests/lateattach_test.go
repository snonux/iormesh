package integrationtests

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"iormesh/internal/schema"
)

// TestJavaLateAttach is `run -agent`'s late attach end to end (PLAN.md M3
// "Attach", task lh4): a -pattern session starts with no JVM; then
// http-async's server and client are exec'd, matching the pattern, and
// each waits at its startup gate. The tracer adopts both at their exec
// and loads the agent once each JVM accepts it; only once it reported
// both JVMs joined are the gates opened. Both ends' requests are http
// events and the client's lookup of "localhost" a dns event — records
// only the late-loaded agent can have written — attributed to Java.
func TestJavaLateAttach(t *testing.T) {
	enableParallelIfRequested(t)
	h := newHarness(t)
	h.AttachAgent = trustedAgentJar(t)
	h.AgentAtStart = true
	h.Spool = filepath.Join(h.Dir, "spool")
	run := judge(t, func() (tracedRun, error) { return traceLateAttach(t, h) })
	assertOnlyTracedPIDs(t, run.Trace.Events, run.pids)
	assertJavaConsistent(t, run.Trace)
	for _, pid := range run.pids {
		if !strings.Contains(run.Logged, fmt.Sprintf("agent loaded into pid %d;", pid)) {
			t.Errorf("the tracer does not report the late load into pid %d:\n%s", pid, run.Logged)
		}
	}
	assertLateJavaEvents(t, run.Trace.Events, run.pids[0], run.pids[1])
}

// traceLateAttach is one attempt of TestJavaLateAttach. The pattern names
// the attempt's own free port, so no other test's http-async JVMs match
// it when the tests run in parallel.
func traceLateAttach(t *testing.T, h *Harness) (tracedRun, error) {
	var r tracedRun
	port := freePort(t)
	h.Pattern = "*http-async --role * --port " + port
	h.runs++
	r.Dir = filepath.Join(h.Dir, fmt.Sprintf("trace-%d", h.runs))
	tracer, err := h.startTracer(nil, r.Dir)
	if err != nil {
		return r, err
	}
	server, err := h.StartNoDir("http-async", "--role", "server", "--port", port)
	if err != nil {
		return r, err
	}
	server.ReleasedWhen = "listening "
	client, err := h.StartNoDir("http-async", "--role", "client", "--port", port)
	if err != nil {
		return r, err
	}
	ws := []*Workload{server, client}
	for _, w := range ws {
		r.pids = append(r.pids, w.PID)
		if err := tracer.waitStderr(fmt.Sprintf("java agent of pid %d joined", w.PID), attachTimeout); err != nil {
			return r, err
		}
	}
	if err := release(ws); err != nil {
		return r, err
	}
	for _, w := range ws {
		if err := w.proc.wait(runTimeout); err != nil {
			return r, skipped(w, err)
		}
	}
	if err := stopPatternTracer(tracer); err != nil {
		return r, err
	}
	return r, finishRun(tracer, &r.Run)
}

// assertLateJavaEvents: both ends recorded http events and the client a
// dns event for "localhost", each attributed to Java.
func assertLateJavaEvents(t *testing.T, events []schema.Event, server, client int) {
	t.Helper()
	httpOf := map[int]int{}
	dns := 0
	for _, e := range events {
		switch {
		case e.Event == schema.KindHTTP:
			httpOf[int(e.PID)]++
		case e.Event == schema.KindDNS && int(e.PID) == client && e.DNS.Host == "localhost":
			dns++
		default:
			continue
		}
		if e.Attribution != schema.AttributionJava {
			t.Errorf("event seq %d (%s) of pid %d is attribution %s, want java",
				e.Seq, e.Event, e.PID, e.Attribution)
		}
	}
	if httpOf[server] == 0 || httpOf[client] == 0 {
		t.Errorf("http events per pid %v: want some of the server %d and the client %d", httpOf, server, client)
	}
	if dns == 0 {
		t.Errorf("no dns event of the client %d for localhost", client)
	}
}
