package cli

import (
	"flag"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

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
}

// defaultDuration bounds every run, ior-style: no unbounded tracer on a
// server (PLAN.md, M4's 4h auto-exit, here from M1 on).
const defaultDuration = 4 * time.Hour

// projectName keeps project labels usable as directory names on servers
// and controllers (PLAN.md: traces live under …/traces/<project>/).
var projectName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// processAlive reports whether pid is a live process, and when startNS
// is not 0 the incarnation that started then (target.ProcFS.Alive). It
// is a variable so flag and loop tests need no real processes.
var processAlive = func(pid uint32, startNS uint64) bool {
	return target.HostProcFS().Alive(pid, startNS)
}

// processStart returns pid's start time on the boot clock (its
// incarnation), 0 when it cannot be read. A variable like processAlive.
var processStart = func(pid uint32) uint64 {
	st, err := target.HostProcFS().Stat(pid)
	if err != nil {
		return 0
	}
	return st.StartNS
}

// parseRunFlags parses and validates `run`'s arguments. Every error wraps
// errUsage.
func parseRunFlags(args []string, stderr io.Writer) (runConfig, error) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pids := fs.String("pid", "", "comma-separated PIDs to trace, with their children")
	pattern := fs.String("pattern", "", "glob over the command line or executable, matched on exec")
	port := fs.Uint("listen-port", 0, "trace the owners of the TCP socket listening on this port")
	project := fs.String("project", "", "project label (required)")
	out := fs.String("out", "", "directory for the trace files (required)")
	duration := fs.Duration("duration", defaultDuration, "stop after this long")
	ready := fs.String("ready", "", "create this file once the probes are attached")
	families := fs.String("trace-families", "",
		"opt-in event families to add to file and socket: ipc, signal, poll, zerocopy, or all")
	cpuProfile := fs.String("cpuprofile", "", "write a CPU profile of the tracer to this file")
	if err := fs.Parse(args); err != nil {
		return runConfig{}, fmt.Errorf("%w: %v", errUsage, err)
	}
	if fs.NArg() != 0 {
		return runConfig{}, fmt.Errorf("%w: unexpected argument %q", errUsage, fs.Arg(0))
	}
	cfg := runConfig{project: *project, out: *out, duration: *duration, ready: *ready,
		spec: target.Spec{Pattern: *pattern}, cpuProfile: *cpuProfile}
	if *port > math.MaxUint16 {
		return runConfig{}, fmt.Errorf("%w: -listen-port %d: want 1-65535", errUsage, *port)
	}
	cfg.spec.ListenPort = uint16(*port)
	var err error
	if cfg.families, err = tracepoints.ParseTraceFamilies(*families); err != nil {
		return runConfig{}, fmt.Errorf("%w: -trace-families: %v", errUsage, err)
	}
	if *pids != "" {
		if cfg.spec.PIDs, err = parsePids(*pids); err != nil {
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
	case !projectName.MatchString(cfg.project):
		return fmt.Errorf("%w: -project %q: want 1-64 of [A-Za-z0-9._-], not starting with . _ -",
			errUsage, cfg.project)
	case cfg.out == "":
		return fmt.Errorf("%w: -out is required", errUsage)
	case cfg.duration <= 0:
		return fmt.Errorf("%w: -duration %v: must be positive", errUsage, cfg.duration)
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
