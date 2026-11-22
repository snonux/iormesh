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
// (Harness.Pattern, M2) or a listening port (Harness.ListenPort, task
// mh4), which ior has no counterpart of, and may add
// opt-in event families (Harness.Families, M2 step 7) and native stacks
// (Harness.NativeStacks, M2 step 9). A workload exiting
// with the skip code 3 ends the run with ErrWorkloadSkipped. A gated
// workload may also be a Go helper binary speaking the same gate protocol
// (StartHelper, workload/edgepeer), optionally held on its stdin. A run
// may also live-load the agent into JVMs started without it
// (Harness.AttachAgent, task ge4): by the tracer at start (-agent) or by
// `iormesh attach` from a Midway step between the workloads' release and
// their exit, on a chosen JDK (Harness.Java) and user (Credential). The
// java command line (workloadCmd) and a tracer's wait-and-read
// (finishRun) are shared with the emulated hosts of multihost.go (M4,
// task kb4), where each host runs its own tracer. A JVM may run under a
// wrapper command (Harness.JVMWrap: its own mount namespace and /tmp,
// task cf4). Every run also writes
// parquet segments (-trace-dir, task ze4), from which the judged runs
// rebuild the graph.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
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
	// attachTimeout bounds `iormesh attach`: S1 measured a fraction of a
	// second; a loaded host may take several.
	attachTimeout = 20 * time.Second

	bpfObjectOverrideEnv   = "IORMESH_BPF_OBJECT"
	workloadStartupFileEnv = "IORMESH_WORKLOAD_STARTUP_FILE"
	tracerProject          = "iormesh-it"
	// segmentsSuffix names a run's -trace-dir beside its -out directory.
	segmentsSuffix = ".segments"
	// workloadSkipCode is the workload's EXIT_SKIP (workload Main.java).
	workloadSkipCode = 3
)

// ErrWorkloadSkipped: a workload exited with the skip code — the host
// lacks what its scenario needs (tcp-ipv6 without an IPv6 loopback). Not
// a pass and not a failure: the test skips (docs/implementation.md M2
// step 7).
var ErrWorkloadSkipped = errors.New("workload skipped")

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
	// ListenPort, when set, binds the tracer to the owner of this
	// listening port (-listen-port) instead of the workloads' PIDs, and
	// ListenGrace, when set, is its -listen-grace: how long the session
	// waits for a restarted owner once the owner exited (task mh4).
	ListenPort  int
	ListenGrace string
	// Families, when set, is the tracer's -trace-families: the opt-in
	// event families (ipc, signal, poll, zerocopy) a scenario needs on
	// top of the default file and socket families (decision #7).
	Families string
	// NativeStacks runs the tracer with -native-stacks (M2 step 9): events
	// carry stack_id and the trace directory holds stacks.ndjson.
	NativeStacks bool
	// JavaAgent, when set, is iormesh-agent.jar: every Java workload
	// starts with -javaagent writing its ring into Spool, and the tracer
	// runs with -spool Spool and joins their I/O to Java stacks (M3).
	// The harness creates Spool before the JVMs start — the agent never
	// creates it — and the tracer gives it its mode.
	JavaAgent string
	Spool     string
	// AttachAgent, when set, is iormesh-agent.jar for a live attach
	// (decision #2, task ge4): the workloads start without -javaagent,
	// the tracer runs with -spool Spool (which it creates), and the agent
	// is loaded by the tracer at start when AgentAtStart is set (-agent),
	// or by the test (Attach) otherwise.
	AttachAgent  string
	AgentAtStart bool
	// Midway, when set, runs after Trace released the workloads and
	// before it waits for them: a live attach in the middle of a
	// scenario. tracer is the running `iormesh run`.
	Midway func(tracer *process) error
	// Java is the java binary the workloads run on; "" is the one on
	// PATH. JVMFlags go before -jar.
	Java     string
	JVMFlags []string
	// JVMWrap, when set, is a command the java command line is appended
	// to and which must exec it in the same process: privateTmpWrap runs
	// each JVM in a mount namespace of its own (decision #14, task cf4).
	JVMWrap []string
	// Credential, when set, runs the workloads as another user (a root
	// attacher reaching a JVM of another uid).
	Credential *syscall.Credential

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
	// Stdin, set by StartHeldHelper, is the write end of the process's
	// stdin: a held helper runs until it is closed.
	Stdin io.WriteCloser

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

// StartOptionless starts a scenario that takes no options at all, not
// even --dir (pipe-ipc, signals): it writes no files.
func (h *Harness) StartOptionless(scenario string) (*Workload, error) {
	return h.startWorkload("", scenario, nil)
}

// StartNoDir starts a scenario role that takes options but no --dir: the
// udp-loopback roles and the unix-socket client, which write no files.
func (h *Harness) StartNoDir(scenario string, args ...string) (*Workload, error) {
	return h.startWorkload("", scenario, args)
}

// StartHelper starts `binary role args…`, a Go helper that speaks the
// workload's gate protocol (PID first, then the startup file; see
// workload/edgepeer): for edge cases a JVM cannot force deterministically.
func (h *Harness) StartHelper(binary, role string, args ...string) (*Workload, error) {
	return h.startGated(role, exec.Command(binary, append([]string{role}, args...)...), false)
}

// StartHeldHelper is StartHelper with the helper's stdin kept open in
// Workload.Stdin: a helper that reads it to EOF runs until the test
// closes it, so the test decides when it exits.
func (h *Harness) StartHeldHelper(binary, role string, args ...string) (*Workload, error) {
	return h.startGated(role, exec.Command(binary, append([]string{role}, args...)...), true)
}

// startWorkload starts a workload; an empty dir passes no --dir.
func (h *Harness) startWorkload(dir, scenario string, args []string) (*Workload, error) {
	cmd, err := h.workloadCmd(dir, h.Spool, scenario, args)
	if err != nil {
		return nil, err
	}
	return h.startGated(scenario, cmd, false)
}

// workloadCmd is the java command line of a workload, with the agent, the
// JVM flags and the user the harness asks for; an empty dir passes no
// --dir. spool is the agent's spool: the harness's, or an emulated
// host's own (Host.Spool). It creates the spool first: the agent never
// does.
func (h *Harness) workloadCmd(dir, spool, scenario string, args []string) (*exec.Cmd, error) {
	argv := append(slices.Clone(h.JVMFlags), "-jar", h.WorkloadJar, scenario)
	argv = append(argv, args...)
	if h.JavaAgent != "" {
		if err := os.MkdirAll(spool, 0o755); err != nil {
			return nil, err
		}
		argv = append([]string{"-javaagent:" + h.JavaAgent + "=spool=" + spool}, argv...)
	}
	if dir != "" {
		argv = append(argv, "--dir", dir)
	}
	java := h.Java
	if java == "" {
		java = "java"
	}
	cmd := exec.Command(java, argv...)
	if len(h.JVMWrap) > 0 {
		cmd = exec.Command(h.JVMWrap[0], slices.Concat(h.JVMWrap[1:], []string{java}, argv)...)
	}
	if h.Credential != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: h.Credential}
		cmd.Dir = h.Tmp // the test's own working directory may be closed to that user
	}
	return cmd, nil
}

// startGated starts cmd behind a fresh startup gate and returns once it
// printed its PID; held keeps a pipe to its stdin.
func (h *Harness) startGated(scenario string, cmd *exec.Cmd, held bool) (*Workload, error) {
	startup := filepath.Join(h.Dir, fmt.Sprintf("startup-%d-%s", len(h.procs), scenario))
	_ = os.Remove(startup) // a stale gate would release it before the tracer attached
	cmd.Env = append(os.Environ(), workloadStartupFileEnv+"="+startup)
	var stdin io.WriteCloser
	if held {
		var err error
		if stdin, err = cmd.StdinPipe(); err != nil {
			return nil, fmt.Errorf("workload %s: stdin: %w", scenario, err)
		}
	}
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
	return &Workload{PID: pid, Scenario: scenario, Stdin: stdin, startup: startup, proc: proc}, nil
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
	if h.Midway != nil {
		if err := h.Midway(tracer); err != nil {
			return run, err
		}
	}
	for _, w := range ws {
		if err := w.proc.wait(runTimeout); err != nil {
			return run, skipped(w, err)
		}
	}
	if h.Pattern != "" {
		if err := stopPatternTracer(tracer); err != nil {
			return run, err
		}
	}
	return run, finishRun(tracer, &run)
}

// skipped turns a workload's exit with the skip code into
// ErrWorkloadSkipped, keeping its stderr (which says why); any other
// error passes unchanged.
func skipped(w *Workload, err error) error {
	var exit *exec.ExitError
	if !errors.As(w.proc.err, &exit) || exit.ExitCode() != workloadSkipCode {
		return err
	}
	return fmt.Errorf("%w: %s: %v", ErrWorkloadSkipped, w.Scenario, err)
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
	if h.Families != "" {
		args = append(args, "-trace-families", h.Families)
	}
	if h.NativeStacks {
		args = append(args, "-native-stacks")
	}
	// Always -trace-dir beside -out (task ze4): the judged runs check that
	// the graph the controller rebuilds from the segments is graph.json.
	// The disk watchdog is off: it is not under test here, and a nearly
	// full developer disk under TMPDIR would end runs as disk_limit.
	args = append(args, "-trace-dir", out+segmentsSuffix, "-disk-limit", "0", "-disk-min-free", "0")
	if h.JavaAgent != "" || h.AttachAgent != "" {
		args = append(args, "-spool", h.Spool)
	}
	if h.AgentAtStart {
		args = append(args, "-agent", h.AttachAgent)
	}
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

// Attach live-loads AttachAgent into w's JVM with `iormesh attach`,
// writing its ring into Spool, and returns the command's stderr. The
// spool must exist (a tracer running with -spool Spool created it).
func (h *Harness) Attach(w *Workload) (string, error) {
	cmd := exec.Command(h.TracerBinary, "attach", "-pid", strconv.Itoa(w.PID),
		"-agent", h.AttachAgent, "-spool", h.Spool, "-timeout", attachTimeout.String())
	var stderr OutputCapture
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stderr.String(), fmt.Errorf("iormesh attach: %w; stderr:\n%s", err, stderr.String())
	}
	return stderr.String(), nil
}

// targetArgs is the tracer's target spec: the pattern, the listening
// port (with its grace), or the PIDs of the traced workloads.
func (h *Harness) targetArgs(ws []*Workload) []string {
	if h.Pattern != "" {
		return []string{"-pattern", h.Pattern}
	}
	if h.ListenPort != 0 {
		args := []string{"-listen-port", strconv.Itoa(h.ListenPort)}
		if h.ListenGrace != "" {
			args = append(args, "-listen-grace", h.ListenGrace)
		}
		return args
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

// SegmentsDir is the project directory a run's tracer wrote its parquet
// segments under (ready/ inside).
func SegmentsDir(run Run) string {
	return filepath.Join(run.Dir+segmentsSuffix, tracerProject)
}
