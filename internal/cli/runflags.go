package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// runConfig is a validated `iormesh run` command line.
type runConfig struct {
	pids     []uint32
	project  string
	out      string
	duration time.Duration
	// ready, if set, is created once every probe is attached: a harness
	// waits for it before releasing the target, since loading the object
	// takes seconds and a fixed sleep races.
	ready string
}

// defaultDuration bounds every run, ior-style: no unbounded tracer on a
// server (PLAN.md, M4's 4h auto-exit, here from M1 on).
const defaultDuration = 4 * time.Hour

// projectName keeps project labels usable as directory names on servers
// and controllers (PLAN.md: traces live under …/traces/<project>/).
var projectName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// processExists is a variable so flag tests need no real processes.
var processExists = func(pid uint32) bool {
	_, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
	return err == nil
}

// parseRunFlags parses and validates `run`'s arguments. Every error wraps
// errUsage.
func parseRunFlags(args []string, stderr io.Writer) (runConfig, error) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pids := fs.String("pid", "", "comma-separated PIDs to trace (required)")
	project := fs.String("project", "", "project label (required)")
	out := fs.String("out", "", "directory for the trace files (required)")
	duration := fs.Duration("duration", defaultDuration, "stop after this long")
	ready := fs.String("ready", "", "create this file once the probes are attached")
	if err := fs.Parse(args); err != nil {
		return runConfig{}, fmt.Errorf("%w: %v", errUsage, err)
	}
	if fs.NArg() != 0 {
		return runConfig{}, fmt.Errorf("%w: unexpected argument %q", errUsage, fs.Arg(0))
	}
	cfg := runConfig{project: *project, out: *out, duration: *duration, ready: *ready}
	var err error
	if cfg.pids, err = parsePids(*pids); err != nil {
		return runConfig{}, err
	}
	return cfg, validateRun(cfg)
}

func validateRun(cfg runConfig) error {
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
// running process, no duplicates.
func parsePids(s string) ([]uint32, error) {
	if s == "" {
		return nil, fmt.Errorf("%w: -pid is required", errUsage)
	}
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
		if !processExists(pid) {
			return nil, fmt.Errorf("%w: -pid %d: no such process", errUsage, pid)
		}
		seen[pid] = true
		pids = append(pids, pid)
	}
	return pids, nil
}
