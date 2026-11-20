package integrationtests

import (
	"path/filepath"
	"strings"
	"testing"

	"iormesh/internal/normalize"
)

// startMicroservices starts the microservices topology (task jb4) as four
// workloads on loopback, one service each, all in the harness's one --dir
// (the TLS hops pin the certificate their callee published there): the
// servers on free ports, released leaves first — billing, inventory,
// orders — each only once the one before printed its listening line, then
// the gateway, whose checkout drives the run.
func startMicroservices(t *testing.T) starter {
	return func(h *Harness) ([]*Workload, error) {
		billing, inventory, orders := freePort(t), freePort(t), freePort(t)
		roles := [][]string{
			{"--role", "billing", "--port", billing},
			{"--role", "inventory", "--port", inventory},
			{"--role", "orders", "--port", orders, "--inventory-port", inventory,
				"--billing-port", billing},
			{"--role", "gateway", "--orders-port", orders, "--inventory-port", inventory},
		}
		var ws []*Workload
		for i, args := range roles {
			w, err := h.StartWorkload("microservices", args...)
			if err != nil {
				return nil, err
			}
			if i < len(roles)-1 {
				w.ReleasedWhen = "listening "
			}
			ws = append(ws, w)
		}
		return ws, nil
	}
}

// TestMicroservices: the four services under the agent satisfy the
// golden — the complete service graph: every hop one fused connection
// with an http event on each end (TLS hops scheme https), the chain,
// fan-out and fan-in in their declared order on each service's threads,
// and every service's config read and create-tmp-then-rename record
// write attributed to its Java method.
func TestMicroservices(t *testing.T) {
	enableParallelIfRequested(t)
	h := newHarness(t)
	h.JavaAgent = absPath(t, agentJarDefault)
	h.Spool = filepath.Join(h.Dir, "spool")
	run := traceJudged(t, h, startMicroservices(t))
	assertOnlyTracedPIDs(t, run.Trace.Events, run.pids)
	assertJavaConsistent(t, run.Trace)
	if !strings.Contains(run.Logged, "java rings 4 (") ||
		!strings.Contains(run.Logged, "(no connection 0)") {
		t.Errorf("summary does not report four rings and every request on its connection: %s",
			run.Logged)
	}
	assertHTTPTimed(t, run.Trace.Events, false)
	got := normalized(t, h, run, "microservices")
	want := expectation(t, "microservices")
	assertGolden(t, got, want)
	assertRequestsPaired(t, got.Events)
	assertNoOtel(t, got.Events)
	t.Run("wrong expectations are caught", func(t *testing.T) {
		assertCaught(t, got, want, map[string]func(*normalize.Expectation){
			"a plain hop declared TLS": func(x *normalize.Expectation) {
				httpOf(x, httpIndex(x, "/inventory/reserve")).Scheme = "https"
			},
			"the fan-in reversed": func(x *normalize.Expectation) {
				i := httpIndex(x, "/inventory/stock/widget")
				httpOf(x, i).Path = "/inventory/reserve"
			},
		})
	})
}

// httpIndex is the index of the first declared http event for path.
func httpIndex(x *normalize.Expectation, path string) int {
	for i, e := range x.Events {
		if e.HTTP != nil && e.HTTP.Path == path {
			return i
		}
	}
	panic("no declared http event for " + path)
}
