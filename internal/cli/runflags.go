package cli

import (
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"iormesh/internal/correlate"
	"iormesh/internal/k8s"
	"iormesh/internal/project"
	"iormesh/internal/segment"
	"iormesh/internal/target"
	"iormesh/internal/tracepoints"
)

// runConfig is a validated `iormesh run` command line.
type runConfig struct {
	// spec names the targets: -pid, -pattern or -listen-port, exactly
	// one (target.Spec.Validate).
	spec     target.Spec
	project  string
	out      string
	duration time.Duration
	// ready, if set, is created once every probe is attached: a harness
	// waits for it before releasing the target, since loading the object
	// takes seconds and a fixed sleep races.
	ready string
	// families are the event families to attach (-trace-families,
	// decision #7): the defaults plus whatever opt-in ones were named.
	families []tracepoints.TraceFamily
	// cpuProfile, if set, is where run writes a pprof CPU profile of
	// itself: how decision #25's per-event cost is found on a real trace
	// (docs/spikes/s3-overhead.md, "Follow-up (wc4)").
	cpuProfile string
	// noAggregation turns the kernel's folding of hot read/write loops off
	// (-aggregate=false, internal/c/rwagg.c): every call then reaches the
	// trace as its own event, at decision #25's per-event cost.
	noAggregation bool
	// nativeStacks captures each syscall's native user stack
	// (-native-stacks, M2 step 9): stack_id on events, the table in
	// stacks.ndjson. Off by default (decision #25).
	nativeStacks bool
	// spool is the directory the Java agent's rings appear in (-spool,
	// M3): JVMs started with -javaagent:iormesh-agent.jar=spool=DIR get
	// their I/O joined to Java stacks. "" joins nothing.
	spool string
	// agent is iormesh-agent.jar to live-load into the JVMs the session
	// starts with (-agent, decision #2, task ge4), absolute; "" loads
	// nothing. It needs spool.
	agent string
	// traceDir is where parquet segments go (-trace-dir, M4), under a
	// directory per project; "" writes none. Without -out it defaults to
	// defaultTraceDir.
	traceDir string
	// diskLimit and diskMinFree are the disk watchdog's limits on the
	// trace dir's partition (-disk-limit percent, -disk-min-free bytes;
	// decision #17: both), each 0 for off.
	diskLimit   float64
	diskMinFree uint64
	// diskMaxBytes caps the bytes under the trace dir itself
	// (-disk-max-bytes, 0 for off): in a pod, below the emptyDir's
	// sizeLimit, so the tracer stops before the kubelet evicts the pod.
	diskMaxBytes uint64
	// readyOwner owns <traceDir>/<project>/ready (-ready-owner), so the
	// deploying user's collector reads and deletes segments unprivileged;
	// nil leaves it root's.
	readyOwner *segment.Owner
	// clockSkew is added to every wall_ns the run stamps: the test-only
	// IORMESH_TEST_CLOCK_SKEW (clockskew.go), never a flag, so neither
	// usage nor start ever carries it. 0 outside tests.
	clockSkew time.Duration
}

// defaultDuration bounds every run, ior-style: no unbounded tracer on a
// server (PLAN.md, M4's 4h auto-exit, here from M1 on).
const defaultDuration = 4 * time.Hour

// maxDuration caps -duration: still no unbounded tracer, and start's
// RuntimeMaxSec (duration + 5 min) stays far from any overflow.
const maxDuration = 7 * 24 * time.Hour

// processAlive reports whether pid is a live process, and when startNS
// is not 0 the incarnation that started then (target.ProcFS.Alive). It
// is a variable so flag and loop tests need no real processes.
var processAlive = func(pid uint32, startNS uint64) bool {
	return target.HostProcFS().Alive(pid, startNS)
}

// processIdentity reads pid's comm and start time on the boot clock (its
// incarnation) in one /proc read, both zero when it cannot be read. It is
// what the correlator is seeded with (correlate.Seed). A variable like
// processAlive.
var processIdentity = func(pid uint32) correlate.Identity {
	comm, start := correlate.ReadProcInfo(pid)
	return correlate.Identity{Comm: comm, StartNS: start}
}

// processStart returns pid's start time (processIdentity), 0 when it
// cannot be read.
func processStart(pid uint32) uint64 {
	return processIdentity(pid).StartNS
}

// runFlags are run's flags, registered by addRunFlags on a flag set of
// its own (run) or one shared with start's (startCommand), which forwards
// what was set to the unit's run.
type runFlags struct {
	pids, pattern, project, out, ready, families, cpuProfile, spool, agent *string
	port                                                                   *uint
	duration                                                               *time.Duration
	aggregate, nativeStacks                                                *bool
	seg                                                                    segmentFlags
}

// parseRunFlags parses and validates `run`'s arguments. Every error wraps
// errUsage.
func parseRunFlags(args []string, stderr io.Writer) (runConfig, error) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	f := addRunFlags(fs)
	if err := fs.Parse(args); err != nil {
		return runConfig{}, fmt.Errorf("%w: %v", errUsage, err)
	}
	if fs.NArg() != 0 {
		return runConfig{}, fmt.Errorf("%w: unexpected argument %q", errUsage, fs.Arg(0))
	}
	if err := f.podDefaults(podEnv); err != nil {
		return runConfig{}, err
	}
	return f.config()
}

// podEnv reads the downward-API variables podDefaults looks at. A
// variable like processAlive, so flag tests do not depend on the test's
// own environment.
var podEnv = os.Getenv

// podDefaults fills in what a run in a pod (all three downward-API
// variables set, k8s.PodFromEnv) needs no flag for: the project is the
// pod's namespace (decision #21), and without -out or -trace-dir the
// segments go to k8s.TraceRoot inside the pod's emptyDir — not the
// server default, where the image has no volume, nor the volume root,
// which the kubelet makes 0777. Outside a pod nothing changes, and a
// missing -project is then validateRun's usage error. Only run defaults
// them: start's unit is a systemd one, and there is no systemd in a pod
// (PLAN.md M7).
func (f runFlags) podDefaults(getenv func(string) string) error {
	pod, ok := k8s.PodFromEnv(getenv)
	if !ok {
		return nil
	}
	if *f.project == "" {
		label, err := k8s.ProjectForNamespace(pod.Namespace)
		if err != nil {
			return fmt.Errorf("%w: no -project and the pod's %v", errUsage, err)
		}
		*f.project = label
	}
	if *f.seg.traceDir == "" && *f.out == "" {
		*f.seg.traceDir = k8s.TraceRoot
	}
	return nil
}

func addRunFlags(fs *flag.FlagSet) runFlags {
	return runFlags{
		pids:    fs.String("pid", "", "comma-separated PIDs to trace, with their children"),
		pattern: fs.String("pattern", "", "glob over the command line or executable, matched on exec"),
		port:    fs.Uint("listen-port", 0, "trace the owners of the TCP socket listening on this port"),
		project: fs.String("project", "",
			"project label (required; in a pod run defaults to the pod's namespace, from the downward API)"),
		out:      fs.String("out", "", "directory for the NDJSON trace files"),
		duration: fs.Duration("duration", defaultDuration, "stop after this long"),
		ready:    fs.String("ready", "", "create this file once the probes are attached"),
		families: fs.String("trace-families", "",
			"opt-in event families to add to file and socket: ipc, signal, poll, zerocopy, or all"),
		cpuProfile: fs.String("cpuprofile", "", "write a CPU profile of the tracer to this file"),
		aggregate: fs.Bool("aggregate", true,
			"fold hot read/write loops into one event with args.count (false: one event per call, for debugging)"),
		nativeStacks: fs.Bool("native-stacks", false,
			"record each syscall's native user stack (stack_id, stacks.ndjson); not useful for JIT-compiled Java"),
		spool: fs.String("spool", "",
			"directory for the Java agent's rings (created, mode 1733); join their JVMs' I/O to Java stacks"),
		agent: fs.String("agent", "",
			"live-load this iormesh-agent.jar into the JVMs traced at start (needs -spool; JEP 451 warns)"),
		seg: addSegmentFlags(fs),
	}
}

// config validates the parsed flags into a runConfig.
func (f runFlags) config() (runConfig, error) {
	cfg := runConfig{project: *f.project, out: *f.out, duration: *f.duration, ready: *f.ready,
		spec: target.Spec{Pattern: *f.pattern}, cpuProfile: *f.cpuProfile, noAggregation: !*f.aggregate,
		nativeStacks: *f.nativeStacks, spool: *f.spool}
	if *f.port > math.MaxUint16 {
		return runConfig{}, fmt.Errorf("%w: -listen-port %d: want 1-65535", errUsage, *f.port)
	}
	cfg.spec.ListenPort = uint16(*f.port)
	cfg, err := f.seg.apply(cfg)
	if err != nil {
		return runConfig{}, err
	}
	return parseRunLists(cfg, *f.pids, *f.families, *f.agent)
}

// parseRunLists finishes parseRunFlags with the arguments that need more
// than a copy: the PID list, the event families and the agent jar (whose
// path, like the spool's, the JVM resolves: both become absolute).
func parseRunLists(cfg runConfig, pids, families, agent string) (runConfig, error) {
	var err error
	if cfg.families, err = tracepoints.ParseTraceFamilies(families); err != nil {
		return runConfig{}, fmt.Errorf("%w: -trace-families: %v", errUsage, err)
	}
	if pids != "" {
		if cfg.spec.PIDs, err = parsePids(pids); err != nil {
			return runConfig{}, err
		}
	}
	if agent != "" {
		if cfg.agent, cfg.spool, err = agentPaths(agent, cfg.spool); err != nil {
			return runConfig{}, err
		}
	}
	return cfg, validateRun(cfg)
}

func validateRun(cfg runConfig) error {
	if err := cfg.spec.Validate(); err != nil {
		return fmt.Errorf("%w: %v (-pid, -pattern and -listen-port exclude each other)", errUsage, err)
	}
	switch {
	case !project.Valid(cfg.project):
		// The registry (iormeshctl add) checks the same rule, so a
		// label the controller stored is one the tracer accepts.
		return fmt.Errorf("%w: -project %q: want %s", errUsage, cfg.project, project.Rule)
	case cfg.duration <= 0 || cfg.duration > maxDuration:
		return fmt.Errorf("%w: -duration %v: want more than 0, at most %v", errUsage, cfg.duration, maxDuration)
	}
	return nil
}

// parsePids parses "-pid 12,34": every entry a positive integer naming a
// running process (not a thread, not a zombie), no duplicates.
func parsePids(s string) ([]uint32, error) {
	seen := map[uint32]bool{}
	var pids []uint32
	for _, f := range strings.Split(s, ",") {
		n, err := strconv.ParseUint(strings.TrimSpace(f), 10, 32)
		if err != nil || n == 0 {
			return nil, fmt.Errorf("%w: -pid %q: not a positive integer", errUsage, f)
		}
		pid := uint32(n)
		if seen[pid] {
			return nil, fmt.Errorf("%w: -pid %d given twice", errUsage, pid)
		}
		if !processAlive(pid, 0) {
			return nil, fmt.Errorf("%w: -pid %d: no such process", errUsage, pid)
		}
		seen[pid] = true
		pids = append(pids, pid)
	}
	return pids, nil
}
