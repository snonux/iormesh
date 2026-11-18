package integrationtests

// Adapted from ior integrationtests/harness.go @ b21afc50328c (TestHarness,
// startWorkload, iorCommand, releaseWorkloadWhenIorReady, waitBoth).
// Changes: the workload is `java -jar workload.jar <scenario>` instead of
// ioworkload, and a run may trace several workloads at once (the
// tcp-loopback server and client), released one after another; the tracer
// is `iormesh run`, whose readiness is the -ready file it creates once
// every probe is attached rather than a line on its output; the result is
// the trace directory (events.ndjson + graph.json) read strictly, not an
// .ior.zst recording; no flamegraph, parquet, wrapper or per-PID-args
// variants yet. A run may bind the tracer to a pattern instead of PIDs
// (Harness.Pattern, M2), which ior has no counterpart of.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"iormesh/internal/normalize"
)

const (
	// workloadStartupTimeout bounds the JVM's start up to its PID line.
	workloadStartupTimeout = 20 * time.Second
	// tracerReadyTimeout bounds loading and attaching the BPF object,
	// which takes seconds.
	tracerReadyTimeout = 30 * time.Second
	// releaseLineTimeout bounds a released workload's wait-for line (the
	// server's "listening").
	releaseLineTimeout = 20 * time.Second
	// runTimeout bounds a released scenario's run and the tracer's drain
	// after it; tracerDuration is the tracer's own bound, longer, so a
	// hang shows as the harness's timeout and not as a clean -duration end.
	runTimeout     = 60 * time.Second
	tracerDuration = "90s"
	readyPoll      = 50 * time.Millisecond

	bpfObjectOverrideEnv   = "IORMESH_BPF_OBJECT"
	workloadStartupFileEnv = "IORMESH_WORKLOAD_STARTUP_FILE"
	tracerProject          = "iormesh-it"
)

// Harness starts workloads and traces them with the real tracer binary.
// One harness belongs to one test; Close (registered by newHarness) kills
// whatever is still running.
type Harness struct {
	TracerBinary string // path to the built ./iormesh
	WorkloadJar  string // path to the built workload.jar
	BpfObject    string // optional IORMESH_BPF_OBJECT override
	Dir          string // gate files and trace directories
	// Tmp is every workload's --dir and the normalizer's TmpPrefix, so
	// the scenario files normalize to "$tmp/…" as goldens declare them.
	Tmp string
	// Pattern, when set, binds the tracer to this glob (-pattern, M2
	// step 2) instead of the workloads' PIDs. A pattern session never
	// ends by itself (it waits for the app to re-appear, decision #23),
	// so Trace stops it with SIGINT once the workloads exited.
	Pattern string

	runs  int
	procs []*process
}

// Workload is one started, still gated workload process.
type Workload struct {
	PID      int
	Scenario string
	// ReleasedWhen, when set, is the stdout line prefix Trace waits for
	// after releasing this workload and before releasing the next one
	// (the server's "listening ", so the client never connects early).
	ReleasedWhen string
	// Untraced workloads are released and waited for like the others but
	// left out of the tracer's -pid list: a run's negative control.
	Untraced bool

	startup string // the startup gate file it polls for
	proc    *process
}

// Run is one traced run.
type Run struct {
	Dir    string          // trace directory the tracer wrote
	Trace  normalize.Trace // its events and graph, read strictly
	Logged string          // the tracer's stderr, summary line included
	Loss   KernelLoss      // what that summary reports as lost
}

// StartWorkload starts `java -jar workload.jar scenario args… --dir Tmp`
// behind its startup gate and returns once it printed its PID: the PID
// must be known before the tracer starts, and the gate keeps the scenario
// from running before the probes are attached.
func (h *Harness) StartWorkload(scenario string, args ...string) (*Workload, error) {
	return h.startWorkload(h.Tmp, scenario, args)
}

// StartUntraced starts a workload like StartWorkload, but in dir (so its
// files do not collide with a traced twin's) and marked Untraced.
func (h *Harness) StartUntraced(dir, scenario string, args ...string) (*Workload, error) {
	w, err := h.startWorkload(dir, scenario, args)
	if err != nil {
		return nil, err
	}
	w.Untraced = true
	return w, nil
}

func (h *Harness) startWorkload(dir, scenario string, args []string) (*Workload, error) {
	startup := filepath.Join(h.Dir, fmt.Sprintf("startup-%d-%s", len(h.procs), scenario))
	_ = os.Remove(startup) // a stale gate would release it before the tracer attached
	argv := append([]string{"-jar", h.WorkloadJar, scenario}, args...)
	cmd := exec.Command("java", append(argv, "--dir", dir)...)
	cmd.Env = append(os.Environ(), workloadStartupFileEnv+"="+startup)
	proc, err := startProcess("workload "+scenario, cmd)
	if err != nil {
		return nil, err
	}
	h.procs = append(h.procs, proc)
	line, err := proc.waitLine("", workloadStartupTimeout)
	if err != nil {
		return nil, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		return nil, fmt.Errorf("workload %s: first line %q is no PID: %w", scenario, line, err)
	}
	return &Workload{PID: pid, Scenario: scenario, startup: startup, proc: proc}, nil
}

// Trace runs the tracer against every traced workload's PID, releases the
// workloads in order once it is attached, waits for all of them and for
// the tracer (which stops once every target exited), and reads the trace.
func (h *Harness) Trace(ws ...*Workload) (Run, error) {
	h.runs++
	run := Run{Dir: filepath.Join(h.Dir, fmt.Sprintf("trace-%d", h.runs))}
	tracer, err := h.startTracer(ws, run.Dir)
	if err != nil {
		return run, err
	}
	if err := release(ws); err != nil {
		return run, err
	}
	for _, w := range ws {
		if err := w.proc.wait(runTimeout); err != nil {
			return run, err
		}
	}
	if h.Pattern != "" {
		if err := stopPatternTracer(tracer); err != nil {
			return run, err
		}
	}
	waitErr := tracer.wait(runTimeout)
	run.Logged = tracer.stderr.String()
	if waitErr != nil {
		return run, waitErr
	}
	if run.Loss, err = ParseKernelLoss(run.Logged); err != nil {
		return run, err
	}
	run.Trace, err = ReadTrace(run.Dir)
	return run, err
}

// Close kills every process the harness started that is still running.
func (h *Harness) Close() {
	for _, p := range h.procs {
		p.kill()
	}
}

// startTracer starts `iormesh run` and returns once it created its ready
// file, i.e. every probe is attached.
func (h *Harness) startTracer(ws []*Workload, out string) (*process, error) {
	ready := out + ".ready"
	args := append([]string{"run"}, h.targetArgs(ws)...)
	args = append(args, "-project", tracerProject, "-out", out, "-duration", tracerDuration, "-ready", ready)
	cmd := exec.Command(h.TracerBinary, args...)
	if h.BpfObject != "" {
		cmd.Env = append(os.Environ(), bpfObjectOverrideEnv+"="+h.BpfObject)
	}
	tracer, err := startProcess("iormesh run", cmd)
	if err != nil {
		return nil, err
	}
	h.procs = append(h.procs, tracer)
	return tracer, waitReady(tracer, ready)
}

// targetArgs is the tracer's target spec: the pattern, or the PIDs of
// the traced workloads.
func (h *Harness) targetArgs(ws []*Workload) []string {
	if h.Pattern != "" {
		return []string{"-pattern", h.Pattern}
	}
	var pids []string
	for _, w := range ws {
		if !w.Untraced {
			pids = append(pids, strconv.Itoa(w.PID))
		}
	}
	return []string{"-pid", strings.Join(pids, ",")}
}

// stopPatternTracer stops a pattern session the way a user does, with
// SIGINT, once its workloads exited: their last records are still in
// flight, and the tracer drains them before it writes the end event
// (internal/cli drainStopped).
func stopPatternTracer(tracer *process) error {
	if err := tracer.cmd.Process.Signal(os.Interrupt); err != nil {
		return tracer.failure(fmt.Sprintf("could not be stopped: %v", err))
	}
	return nil
}

// waitReady polls for the tracer's ready file. The tracer exiting first
// (no root, no BTF, a target already gone) ends the wait with its stderr.
func waitReady(tracer *process, ready string) error {
	deadline := time.Now().Add(tracerReadyTimeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(ready); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("ready file: %w", err)
		}
		if tracer.exited() {
			return tracer.failure("exited before it was ready")
		}
		time.Sleep(readyPoll)
	}
	tracer.kill()
	return tracer.failure(fmt.Sprintf("was not ready within %v", tracerReadyTimeout))
}

// release opens each workload's startup gate in order, waiting for its
// ReleasedWhen line before the next.
func release(ws []*Workload) error {
	for _, w := range ws {
		if err := os.WriteFile(w.startup, []byte("ready\n"), 0o600); err != nil {
			return fmt.Errorf("release %s: %w", w.Scenario, err)
		}
		if w.ReleasedWhen == "" {
			continue
		}
		if _, err := w.proc.waitLine(w.ReleasedWhen, releaseLineTimeout); err != nil {
			return err
		}
	}
	return nil
}
