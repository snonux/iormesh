package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// start's unit (PLAN.md M4 "The tracer runs as a transient systemd unit",
// decision #19).
const (
	// defaultCPUQuota and defaultMemoryMax are the cgroup caps, the
	// enforced half of decision #25's overhead budget: one core — the
	// budget's unit, ≤ 2 µs per event is 500 k events/s on one core —
	// and 1 GiB, room for the 16 MiB ring, the BPF maps (charged to the
	// cgroup), the correlator's tables and the parquet writer's buffers
	// with a wide margin. -cpu-quota and -memory-max override them.
	defaultCPUQuota  = "100%"
	defaultMemoryMax = "1G"
	// runtimeGrace is RuntimeMaxSec beyond -duration: the tracer's own
	// bound fires first and ends the trace cleanly; the unit's is the
	// second, independent one, for a tracer that hangs.
	runtimeGrace = 5 * time.Minute
	// stopTimeout is TimeoutStopSec: how long a stop waits for the tracer
	// to drain (≤ 2 s), publish its segment and write its end before
	// systemd SIGKILLs it.
	stopTimeout = 30 * time.Second
	// defaultStartWait bounds start's wait for the run to attach: the BPF
	// load takes seconds, longer on a loaded host.
	defaultStartWait = 2 * time.Minute
	startPoll        = 250 * time.Millisecond
)

// The cap values reach systemd as one argv element each, never through a
// shell; the patterns keep them to the forms systemd documents anyway.
var (
	cpuQuotaRe  = regexp.MustCompile(`^[1-9][0-9]{0,5}%$`)
	memoryMaxRe = regexp.MustCompile(`^([1-9][0-9]{0,15}[KMGT]?|infinity)$`)
)

// startOnly are start's own flags; every other flag set on start's
// command line is run's and is forwarded to the unit's run.
var startOnly = map[string]bool{"cpu-quota": true, "memory-max": true, "wait": true}

// pathFlags are run's flags that name files: the unit runs in /, so start
// makes them absolute against its own working directory first.
var pathFlags = map[string]bool{"out": true, "ready": true, "cpuprofile": true,
	"spool": true, "agent": true, "trace-dir": true}

// control runs the unit subcommands (start, stop, status, uninstall).
// Every outside effect goes through it, so tests run them against a fake
// systemd and a temporary trace dir.
type control struct {
	sys commander
	// self is the tracer binary the unit runs (os.Executable).
	self string
	env  func(string) string
	now  func() time.Time
	// sleep waits d or until ctx is done.
	sleep  func(ctx context.Context, d time.Duration) error
	stdout io.Writer
	stderr io.Writer
}

// startConfig is a validated `iormesh start` command line.
type startConfig struct {
	run                 runConfig
	runArgs             []string
	unit                string
	cpuQuota, memoryMax string
	wait                time.Duration
}

func newControl(stdout, stderr io.Writer) (control, error) {
	self, err := os.Executable()
	if err != nil {
		return control{}, fmt.Errorf("own binary: %w", err)
	}
	return control{sys: execCommander{}, self: self, env: os.Getenv, now: time.Now,
		sleep: sleepCtx, stdout: stdout, stderr: stderr}, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// controlCommand runs one of the unit subcommands with the real systemd.
func controlCommand(cmd func(control, context.Context, []string) error, args []string,
	stdout, stderr io.Writer) error {
	c, err := newControl(stdout, stderr)
	if err != nil {
		return err
	}
	return cmd(c, context.Background(), args)
}

// start launches `iormesh run` as the transient unit iormesh-<project>:
// independent of the SSH session, capped by its cgroup, logged in the
// journal. A live unit for the project is refused — flag changes are
// stop-and-restart (decision #19) — and start waits until the run has
// attached (its state.json carries the unit's invocation id), so a run
// that cannot attach fails the start, not just the journal.
func (c control) start(ctx context.Context, args []string) error {
	cfg, err := c.parseStart(args)
	if err != nil {
		return err
	}
	u, err := showUnit(ctx, c.sys, cfg.unit)
	switch {
	case err != nil:
		return err
	case u.live():
		return fmt.Errorf("%s is already %s; stop it first (flags change by stop and start)",
			cfg.unit, u.ActiveState)
	case u.ActiveState == "failed":
		if _, err := c.sys.Output(ctx, "systemctl", "reset-failed", "--", cfg.unit); err != nil {
			return err
		}
	}
	if _, err := c.sys.Output(ctx, "systemd-run", cfg.unitArgs(c.self)...); err != nil {
		return err
	}
	if cfg.wait == 0 {
		_, _ = fmt.Fprintf(c.stdout, "iormesh: launched %s\n", cfg.unit)
		return nil
	}
	st, err := c.waitAttached(ctx, cfg)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(c.stdout, "iormesh: started %s: session %s on %s, pid %d, tracing %s until %s\n",
		cfg.unit, st.Session, st.FQDN, st.PID, st.Targets, st.EndsBy.Format(time.RFC3339))
	return nil
}

// parseStart parses start's flags: run's, validated as run would (so a
// bad flag fails here, not in the journal), plus the unit's own.
func (c control) parseStart(args []string) (startConfig, error) {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	rf := addRunFlags(fs)
	cfg := startConfig{}
	fs.StringVar(&cfg.cpuQuota, "cpu-quota", defaultCPUQuota, "the unit's CPUQuota (100% is one core)")
	fs.StringVar(&cfg.memoryMax, "memory-max", defaultMemoryMax, "the unit's MemoryMax (bytes, K/M/G/T suffix)")
	fs.DurationVar(&cfg.wait, "wait", defaultStartWait, "wait this long for the run to attach (0: do not wait)")
	if err := fs.Parse(args); err != nil {
		return cfg, fmt.Errorf("%w: %v", errUsage, err)
	}
	if fs.NArg() != 0 {
		return cfg, fmt.Errorf("%w: unexpected argument %q", errUsage, fs.Arg(0))
	}
	if err := cfg.validateCaps(); err != nil {
		return cfg, err
	}
	var err error
	if cfg.runArgs, err = forwardedArgs(fs); err != nil {
		return cfg, err
	}
	if *rf.seg.owner == "" {
		if cfg.runArgs, err = c.sudoOwner(cfg.runArgs); err != nil {
			return cfg, err
		}
	}
	return cfg.finish(rf)
}

// finish validates run's flags into cfg.run. start needs segments: they
// are what a server session is for, and state.json lives beside them.
func (cfg startConfig) finish(rf runFlags) (startConfig, error) {
	var err error
	if cfg.run, err = rf.config(); err != nil {
		return cfg, err
	}
	if cfg.run.traceDir == "" {
		return cfg, fmt.Errorf("%w: start writes segments: give -trace-dir with -out, or drop -out", errUsage)
	}
	if cfg.run.traceDir, err = filepath.Abs(cfg.run.traceDir); err != nil {
		return cfg, fmt.Errorf("-trace-dir: %w", err)
	}
	cfg.unit, err = unitName(cfg.run.project)
	return cfg, err
}

func (cfg startConfig) validateCaps() error {
	switch {
	case !cpuQuotaRe.MatchString(cfg.cpuQuota):
		return fmt.Errorf("%w: -cpu-quota %q: want a percentage like 100%%", errUsage, cfg.cpuQuota)
	case !memoryMaxRe.MatchString(cfg.memoryMax):
		return fmt.Errorf("%w: -memory-max %q: want bytes with an optional K, M, G or T suffix", errUsage,
			cfg.memoryMax)
	case cfg.wait < 0:
		return fmt.Errorf("%w: -wait %v: must not be negative", errUsage, cfg.wait)
	}
	return nil
}

// forwardedArgs rebuilds run's argv from the flags set on start's command
// line, each as one -name=value element (so no value can be read as a
// flag of its own), path values made absolute.
func forwardedArgs(fs *flag.FlagSet) ([]string, error) {
	var args []string
	var err error
	fs.Visit(func(f *flag.Flag) {
		if startOnly[f.Name] || err != nil {
			return
		}
		v := f.Value.String()
		if pathFlags[f.Name] && v != "" {
			v, err = filepath.Abs(v)
		}
		args = append(args, "-"+f.Name+"="+v)
	})
	if err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}
	return args, nil
}

// sudoOwner hands ready/ to the user who ran `sudo iormesh start` (PLAN.md
// M4: start records who invoked sudo), so their collector reads and
// deletes segments unprivileged. Without sudo, or under root's own sudo,
// ready/ stays root's.
func (c control) sudoOwner(args []string) ([]string, error) {
	uid, gid := c.env("SUDO_UID"), c.env("SUDO_GID")
	if uid == "" || uid == "0" {
		return args, nil
	}
	owner := uid + ":" + gid
	if _, err := parseOwner(owner); err != nil {
		return nil, fmt.Errorf("SUDO_UID/SUDO_GID: %w", err)
	}
	return append(args, "-ready-owner="+owner), nil
}

// unitArgs is systemd-run's argv. --service-type=exec makes systemd-run
// return only once the binary is exec'd, so a missing or unrunnable
// binary fails the start itself; --collect lets a failed unit vanish,
// its state.json and journal keeping what happened.
func (cfg startConfig) unitArgs(self string) []string {
	return append([]string{
		"--unit=" + cfg.unit,
		"--description=iormesh tracer, project " + cfg.run.project,
		"--collect", "--quiet", "--service-type=exec",
		"--property=RuntimeMaxSec=" + seconds(cfg.run.duration+runtimeGrace),
		"--property=CPUQuota=" + cfg.cpuQuota,
		"--property=MemoryMax=" + cfg.memoryMax,
		"--property=KillSignal=SIGTERM",
		"--property=TimeoutStopSec=" + seconds(stopTimeout),
		"--", self, "run",
	}, cfg.runArgs...)
}

// seconds formats d for a systemd time property, rounded up to whole
// seconds.
func seconds(d time.Duration) string {
	return fmt.Sprintf("%ds", int64((d+time.Second-1)/time.Second))
}

// waitAttached polls until the unit's run wrote its running state (the
// state's invocation id is the unit's), the unit is gone, or cfg.wait
// passed. A timeout leaves the unit running: it may still attach. The
// invocation id is remembered from the first look, since a run that ends
// at once takes its unit (--collect) and the id with it.
func (c control) waitAttached(ctx context.Context, cfg startConfig) (sessionState, error) {
	dir := filepath.Join(cfg.run.traceDir, cfg.run.project)
	deadline := c.now().Add(cfg.wait)
	var inv string
	for {
		u, err := showUnit(ctx, c.sys, cfg.unit)
		if err != nil {
			return sessionState{}, err
		}
		if inv == "" {
			inv = u.InvocationID
		}
		st, serr := readState(dir)
		mine := serr == nil && inv != "" && st.InvocationID == inv
		switch {
		case mine && st.State == stateRunning:
			return st, nil
		case mine:
			return st, fmt.Errorf("%s ended at once (%s)", cfg.unit, st.EndReason)
		case !u.live():
			return st, fmt.Errorf("%s exited before the tracer attached; see journalctl -u %s",
				cfg.unit, cfg.unit)
		case !c.now().Before(deadline):
			return st, fmt.Errorf("%s has not attached after %v; it keeps running (iormesh status, "+
				"journalctl -u %s)", cfg.unit, cfg.wait, cfg.unit)
		}
		if err := c.sleep(ctx, startPoll); err != nil {
			return sessionState{}, fmt.Errorf("%s: wait cancelled: %w", cfg.unit, err)
		}
	}
}
