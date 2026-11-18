package integrationtests

// Adapted from ior integrationtests/helpers_test.go @ b21afc50328c
// (newTestHarness, absPath, enableParallelIfRequested, the judged-run
// retry). Changes: the harness paths are ../iormesh and ../workload.jar;
// the parallel switch is the -iormesh.parallel flag; the assertion helpers
// are iormesh's: the golden check of internal/normalize and graph checks.

import (
	"flag"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"iormesh/internal/normalize"
	"iormesh/internal/schema"
)

const (
	tracerBinaryDefault = "../iormesh"
	workloadJarDefault  = "../workload.jar"
	scenariosDir        = "../workload/scenarios"
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
// no record in the kernel (FirstRunWithoutKernelLoss). A run that lost
// records on every attempt cannot be judged: the test skips with the
// unexercised marker rather than pass or fail on loss. Undecodable records
// are a decoder bug and fail at once.
func traceJudged(t *testing.T, h *Harness, start starter) tracedRun {
	t.Helper()
	run, lost, err := FirstRunWithoutKernelLoss(lossAttempts, func() (tracedRun, KernelLoss, error) {
		ws, err := start(h)
		if err != nil {
			return tracedRun{}, KernelLoss{}, err
		}
		r, err := h.Trace(ws...)
		var pids []int
		for _, w := range ws {
			if !w.Untraced {
				pids = append(pids, w.PID)
			}
		}
		return tracedRun{Run: r, pids: pids}, r.Loss, err
	})
	if err != nil {
		t.Fatalf("traced run: %v", err)
	}
	if len(lost) == lossAttempts {
		SkipUnexercised(t, os.Stdout, "every one of "+strconv.Itoa(lossAttempts)+
			" runs lost records in the kernel (last: "+lost[len(lost)-1].String()+")")
	}
	dumpOnFailure(t, run)
	if n := run.Loss.Undecodable(); n > 0 {
		t.Fatalf("%d records reached userspace but could not be decoded (%s)", n, run.Loss)
	}
	return run
}

// expectation loads a scenario's golden.
func expectation(t *testing.T, scenario string) normalize.Expectation {
	t.Helper()
	want, err := normalize.LoadExpectation(filepath.Join(scenariosDir, scenario, "expect.json"))
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
