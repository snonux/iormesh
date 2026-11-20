package integrationtests

// Adapted from ior integrationtests/helpers_test.go @ b21afc50328c
// (newTestHarness, absPath, enableParallelIfRequested, the judged-run
// retry). Changes: the harness paths are ../iormesh and ../workload.jar;
// the parallel switch is the -iormesh.parallel flag; the assertion helpers
// are iormesh's: the golden check of internal/normalize and graph checks;
// the judged-run retry is generic (judge), so a test with its own
// choreography (TestPIDReuse) is retried and skipped the same way, and
// every judged run's graph rebuilt from its segments must be its
// graph.json (task ze4).

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"iormesh/internal/federate"
	"iormesh/internal/normalize"
	"iormesh/internal/schema"
	"iormesh/internal/segment"
)

const (
	tracerBinaryDefault = "../iormesh"
	workloadJarDefault  = "../workload.jar"
	// edgePeerDefault is workload/edgepeer, built by mage workloadBuild.
	edgePeerDefault = "../edgepeer"
	scenariosDir    = "../workload/scenarios"
	// lossAttempts: one retry tells a busy moment from a host that loses
	// records all the time (ior's FirstRunWithoutKernelLoss callers).
	lossAttempts = 3
)

// parallel is gatecmd.IntegrationParallelFlag: the parallel mage target
// sets it, the serial one does not.
var parallel = flag.Bool("iormesh.parallel", false, "run the integration tests in parallel")

// newHarness returns a harness for one test, skipping without root (BPF
// needs it) or without the tracepoints M1 attaches to. Processes it
// started are killed when the test ends, however it ends.
func newHarness(t *testing.T) *Harness {
	t.Helper()
	requireTracepoints(t, m1Tracepoints...)
	h := &Harness{
		TracerBinary: absPath(t, tracerBinaryDefault),
		WorkloadJar:  absPath(t, workloadJarDefault),
		BpfObject:    os.Getenv(bpfObjectOverrideEnv),
		Dir:          t.TempDir(),
		Tmp:          t.TempDir(),
	}
	t.Cleanup(h.Close)
	return h
}

func absPath(t *testing.T, rel string) string {
	t.Helper()
	p, err := filepath.Abs(rel)
	if err != nil {
		t.Fatalf("resolve path %s: %v", rel, err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("%s: %v (build it: mage build workloadBuild)", p, err)
	}
	return p
}

// enableParallelIfRequested runs the test in parallel when the binary was
// started with -iormesh.parallel.
func enableParallelIfRequested(t *testing.T) {
	t.Helper()
	if *parallel {
		t.Parallel()
	}
}

// freePort returns a loopback port nothing listens on right now. The
// listener closes before the server binds it - a race, but the server
// fails loudly if it loses one, and a fixed port would fail on hosts where
// it is taken.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick a free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("close probe listener: %v", err)
	}
	return strconv.Itoa(port)
}

// starter starts the workloads of one run; each attempt starts fresh ones.
type starter func(h *Harness) ([]*Workload, error)

// startScenario starts one single-process scenario.
func startScenario(scenario string) starter {
	return func(h *Harness) ([]*Workload, error) {
		w, err := h.StartWorkload(scenario)
		if err != nil {
			return nil, err
		}
		return []*Workload{w}, nil
	}
}

// startTCPPair starts the tcp-loopback server and client on a free port.
// The server is released first and the client only once the server printed
// its "listening" line, so the connect never races the bind.
func startTCPPair(t *testing.T) starter {
	return func(h *Harness) ([]*Workload, error) {
		port := freePort(t)
		server, err := h.StartWorkload("tcp-loopback", "--role", "server", "--port", port)
		if err != nil {
			return nil, err
		}
		server.ReleasedWhen = "listening "
		client, err := h.StartWorkload("tcp-loopback", "--role", "client", "--port", port)
		if err != nil {
			return nil, err
		}
		return []*Workload{server, client}, nil
	}
}

// tracedRun is one judged run: the trace and the PIDs it targeted
// (Untraced workloads' excluded).
type tracedRun struct {
	Run
	pids []int
}

// traceJudged starts and traces the workloads of start until a run loses
// no record in the kernel and has no skipped program run (KernelLoss.Any,
// FirstRunWithoutKernelLoss). A run that lost records on every attempt
// cannot be judged: the test skips with the
// unexercised marker rather than pass or fail on loss. Undecodable records
// are a decoder bug and fail at once. A workload that exits with the skip
// code (the host cannot run its scenario) skips the test.
func traceJudged(t *testing.T, h *Harness, start starter) tracedRun {
	t.Helper()
	return judge(t, func() (tracedRun, error) {
		ws, err := start(h)
		if err != nil {
			return tracedRun{}, err
		}
		r, err := h.Trace(ws...)
		var pids []int
		for _, w := range ws {
			if !w.Untraced {
				pids = append(pids, w.PID)
			}
		}
		return tracedRun{Run: r, pids: pids}, err
	})
}

// judgeable is a judged run's result: a tracedRun, or a test's own run
// type embedding one with what its choreography learned on the way.
//
// sessions are the single-host tracer runs it holds, each checked by
// assertRebuiltGraph: the run itself, or each emulated host's (meshRun).
type judgeable interface {
	traced() tracedRun
	sessions() []Run
}

// traced makes tracedRun (and every type embedding it) judgeable.
func (r tracedRun) traced() tracedRun { return r }

// sessions is the one tracer run of a single-host run.
func (r tracedRun) sessions() []Run { return []Run{r.Run} }

// judge is traceJudged's verdict for any attempt function, so a test
// with its own choreography (TestPIDReuse) is judged the same way: retried
// on kernel loss, skipped as unexercised when every attempt lost records,
// failed at once on undecodable records.
func judge[T judgeable](t *testing.T, attempt func() (T, error)) T {
	t.Helper()
	run, lost, err := FirstRunWithoutKernelLoss(lossAttempts, func() (T, KernelLoss, error) {
		r, err := attempt()
		return r, r.traced().Loss, err
	})
	if errors.Is(err, ErrWorkloadSkipped) {
		t.Skipf("traced run: %v", err)
	}
	if err != nil {
		t.Fatalf("traced run: %v", err)
	}
	if len(lost) == lossAttempts {
		SkipUnexercised(t, os.Stdout, "every one of "+strconv.Itoa(lossAttempts)+
			" runs lost or skipped records in the kernel (last: "+lost[len(lost)-1].String()+")")
	}
	dumpOnFailure(t, run.traced())
	if n := run.traced().Loss.Undecodable(); n > 0 {
		t.Fatalf("%d records reached userspace but could not be decoded (%s)", n, run.traced().Loss)
	}
	for _, s := range run.sessions() {
		assertRebuiltGraph(t, s)
	}
	return run
}

// assertRebuiltGraph is M4's acceptance for single-host runs (task ze4,
// decision #3): the graph the controller rebuilds from a run's parquet
// segments (federate.RebuildGraph, the build's rule set) is exactly the
// graph.json the tracer wrote — every node and edge, comm and state.
func assertRebuiltGraph(t *testing.T, run Run) {
	t.Helper()
	// One project directory: the harness's, or an emulated host's own.
	readies, err := filepath.Glob(filepath.Join(run.Dir+segmentsSuffix, "*", segment.ReadyDir))
	if err != nil || len(readies) != 1 {
		t.Fatalf("segments of %s: %v, %v; want one ready/", run.Dir, readies, err)
	}
	ready := readies[0]
	entries, err := os.ReadDir(ready)
	if err != nil {
		t.Fatal(err)
	}
	var events []schema.Event
	for _, e := range entries { // name order: seq order within the session
		if isEventsSegment(e.Name()) {
			events = append(events, readParquet[schema.Event](t, filepath.Join(ready, e.Name()))...)
		}
	}
	got, want := graphLines(federate.RebuildGraph(events)), graphLines(run.Trace.Graph)
	for _, l := range want {
		if !slices.Contains(got, l) {
			t.Errorf("graph.json has %s, the graph rebuilt from the segments lacks it", l)
		}
	}
	for _, l := range got {
		if !slices.Contains(want, l) {
			t.Errorf("the graph rebuilt from the segments has %s, graph.json lacks it", l)
		}
	}
}

// graphLines renders a graph's nodes and edges one per line.
func graphLines(g schema.Graph) []string {
	var out []string
	for _, n := range g.Nodes {
		out = append(out, fmt.Sprintf("node %s %s %v", n.Type, n.ID, n.Attrs))
	}
	for _, e := range g.Edges {
		out = append(out, fmt.Sprintf("edge %s %s -> %s %s", e.Type, e.From, e.To, e.State))
	}
	return out
}

// expectation loads a scenario's golden.
func expectation(t *testing.T, scenario string) normalize.Expectation {
	t.Helper()
	return expectationFile(t, scenario, "expect.json")
}

// expectationFile loads one golden file of a scenario: a multi-host
// scenario has one per host besides its merged expect.json (task kb4).
func expectationFile(t *testing.T, scenario, file string) normalize.Expectation {
	t.Helper()
	want, err := normalize.LoadExpectation(filepath.Join(scenariosDir, scenario, file))
	if err != nil {
		t.Fatalf("load expectation: %v", err)
	}
	return want
}

// normalized cuts the run to the scenario's bracket and normalizes it the
// way `iormesh normalize -bracket S -tmp Tmp` does.
func normalized(t *testing.T, h *Harness, run tracedRun, scenario string) normalize.Trace {
	t.Helper()
	got, err := normalize.Normalize(run.Trace, normalize.Options{Bracket: scenario, TmpPrefix: h.Tmp})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	return got
}

// assertGolden is the integration-level assertion (PLAN.md "Integration
// tests - follow ior"): the normalized, bracketed trace holds everything
// the scenario declares and nothing on its must-not-appear list.
func assertGolden(t *testing.T, got normalize.Trace, want normalize.Expectation) {
	t.Helper()
	for _, v := range normalize.Check(got, want) {
		t.Errorf("golden %s: %v", want.Scenario, v)
	}
}

// assertOnlyTracedPIDs fails for an event of a process the run did not
// target. PID 0 marks host-level events (session, end, drop).
func assertOnlyTracedPIDs(t *testing.T, events []schema.Event, pids []int) {
	t.Helper()
	for _, e := range events {
		if e.PID != 0 && !slices.Contains(pids, int(e.PID)) {
			t.Fatalf("event %d (%s) of pid %d, which is not traced (%v)", e.Seq, e.Event, e.PID, pids)
		}
	}
}

// dumpOnFailure keeps a failed run's trace for the reader: t.TempDir is
// removed when the test ends, so the directory is copied to
// IORMESH_IT_KEEP when that is set. Each copy gets its own fresh
// directory: a test with two harnesses (TestSecondRunNormalizesIdentically)
// has two runs both named trace-1, and copying the second onto the first
// failed with "file exists" and lost it.
func dumpOnFailure(t *testing.T, run tracedRun) {
	t.Helper()
	keep := os.Getenv("IORMESH_IT_KEEP")
	if keep == "" {
		return
	}
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		parent := filepath.Join(keep, strings.ReplaceAll(t.Name(), "/", "_"))
		if err := os.MkdirAll(parent, 0o755); err != nil {
			t.Logf("keep trace: %v", err)
			return
		}
		dst, err := os.MkdirTemp(parent, filepath.Base(run.Dir)+"-")
		if err != nil {
			t.Logf("keep trace: %v", err)
			return
		}
		if err := os.CopyFS(dst, os.DirFS(run.Dir)); err != nil {
			t.Logf("keep trace: %v", err)
			return
		}
		t.Logf("trace kept in %s", dst)
	})
}
