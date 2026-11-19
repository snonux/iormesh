package integrationtests

import (
	"testing"

	"iormesh/internal/normalize"
)

// libsJarDefault is `mage workloadLibsBuild`'s output, which `mage
// integrationTest` builds first: the third-party HTTP module's jar (task
// de4), run like workload.jar (its manifest names workload.jar and the
// vendored libraries beside it).
const libsJarDefault = "../workload-libs.jar"

// traceLibs traces one of workload-libs.jar's HTTP pairs under the agent
// and checks its golden, its cross-wire pairing and that no otel object
// appears (the module has no OTEL).
func traceLibs(t *testing.T, scenario string) (normalize.Trace, normalize.Expectation) {
	t.Helper()
	h := newHarness(t)
	h.WorkloadJar = absPath(t, libsJarDefault)
	_, got := traceHTTP(t, h, scenario, false)
	want := expectation(t, scenario)
	assertGolden(t, got, want)
	assertRequestsPaired(t, got.Events)
	assertNoOtel(t, got.Events)
	return got, want
}

// TestHTTPServlet: http-servlet's pair (task de4) — the JDK HttpClient
// against a servlet in an embedded Tomcat — records the server's http
// events through the agent's Servlet hook, rewritten into the app's class
// loader: one per request on Tomcat's request thread, after the reply's
// write (the servlet flushes), caused by HttpServlet#service, with the
// declared sizes and a latency, numbered 1–3 on the client's connection,
// so both ends pair.
func TestHTTPServlet(t *testing.T) {
	enableParallelIfRequested(t)
	got, want := traceLibs(t, "http-servlet")
	t.Run("wrong expectations are caught", func(t *testing.T) {
		assertCaught(t, got, want, map[string]func(*normalize.Expectation){
			// Declared event 3 is the server's first http event.
			"another causing method": func(x *normalize.Expectation) {
				j := *x.Events[3].Java
				j.Method = "iormesh.workload.libs.TomcatServe$Answer#service"
				x.Events[3].Java = &j
			},
			"another status": func(x *normalize.Expectation) {
				s := uint16(500)
				httpOf(x, 9).Status = &s
			},
			"another reply size": func(x *normalize.Expectation) {
				n := uint64(6)
				httpOf(x, 3).RespBytes = &n
			},
		})
	})
}
