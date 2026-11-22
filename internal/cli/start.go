package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

// pathFlags are run's flags that name files and that a confined start
// accepts: the unit runs in /, so start makes them absolute against its
// own working directory first, and finally forwards the very value the
// confinement checked (checkedPath) — never the caller's spelling, which
// for -agent may be a symlink the caller re-points after the check. A
// flag added here without a case in checkedPath fails every start that
// sets it (fail closed).
var pathFlags = map[string]bool{"spool": true, "agent": true, "trace-dir": true}

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
	// confine is what a started session may use (confine.go): start is
	// always confined, since its unit runs as root.
	confine confinement
	// selinux reports SELinux's mode and a file's label, for the hint on
	// a failed start (startdiag.go); nil gives no hint.
	selinux func(path string) selinuxState
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
		sleep: sleepCtx, stdout: stdout, stderr: stderr, confine: defaultConfinement(os.Getenv),
		selinux: hostSELinux}, nil
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
	if err := c.confine.prepare(cfg.run); err != nil {
		return err
	}
	launched := c.now()
	if _, err := c.sys.Output(ctx, "systemd-run", cfg.unitArgs(c.self)...); err != nil {
		// systemd-run says little (with --quiet often nothing): the
		// cause is in the unit's journal (startdiag.go).
		return c.diagnoseStart(ctx, cfg.unit, launched, err)
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
// bad flag fails here, not in the journal), plus the unit's own. The
// test-only clock skew is refused first: the unit would never see it.
func (c control) parseStart(args []string) (startConfig, error) {
	if err := refuseStartClockSkew(c.env); err != nil {
		return startConfig{}, err
	}
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	rf := addRunFlags(fs)
	cfg := startConfig{}
	fs.StringVar(&cfg.cpuQuota, "cpu-quota", defaultCPUQuota, "the unit's CPUQuota (100% is one core)")
	fs.StringVar(&cfg.memoryMax, "memory-max", defaultMemoryMax, "the unit's MemoryMax (bytes, K/M/G/T suffix)")
	fs.DurationVar(&cfg.wait, "wait", defaultStartWait, "wait this long for the run to attach (0: do not wait)")
	if err := fs.Parse(args); err != nil {
		return cfg, fmt.Errorf("%w: %w", errUsage, err)
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
	return c.finishStart(cfg, rf)
}

// finishStart validates run's flags into cfg.run and confines them: the
// unit's run is root, so a start takes no -out, -cpuprofile or -ready,
// segments go to the pinned trace dir (where state.json lives too) and a
// spool to the project's pinned one (confinement.check).
func (c control) finishStart(cfg startConfig, rf runFlags) (startConfig, error) {
	var err error
	if cfg.run, err = rf.config(); err != nil {
		return cfg, err
	}
	if cfg.run.traceDir, err = filepath.Abs(cfg.run.traceDir); err != nil {
		return cfg, fmt.Errorf("-trace-dir: %w", err)
	}
	if err := c.confine.check(cfg.run); err != nil {
		return cfg, err
	}
	if cfg.runArgs, err = forwardChecked(cfg.runArgs, cfg.run); err != nil {
		return cfg, err
	}
	cfg.unit, err = unitName(cfg.run.project)
	return cfg, err
}

// forwardChecked replaces each path flag in args with the value the
// confinement checked (task kf4's review): -agent with the jar's
// resolved real path, whose every directory is root's (trustedAgent), so
// nothing the caller owns lies between the check and the unit's run;
// -spool and -trace-dir with their cleaned absolute paths, equal to the
// pinned ones. The unit's run checks them once more (unitConfinedEnv).
func forwardChecked(args []string, run runConfig) ([]string, error) {
	out := make([]string, 0, len(args))
	for _, a := range args {
		name, val, _ := strings.Cut(strings.TrimPrefix(a, "-"), "=")
		if pathFlags[name] && val != "" { // an empty value names no file
			v, err := checkedPath(run, name)
			if err != nil {
				return nil, err
			}
			a = "-" + name + "=" + v
		}
		out = append(out, a)
	}
	return out, nil
}

// checkedPath is run's checked value of path flag name.
func checkedPath(run runConfig, name string) (string, error) {
	var v string
	switch name {
	case "agent":
		v = run.agent
	case "spool":
		v = filepath.Clean(run.spool)
	case "trace-dir":
		v = filepath.Clean(run.traceDir)
	}
	if v == "" || v == "." || !filepath.IsAbs(v) {
		return "", fmt.Errorf("%w: -%s: no checked absolute value to forward", errUsage, name)
	}
	return v, nil
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
// flag of its own), path values made absolute. A value with a $ is
// refused: systemd 254 and later expand $VAR in a unit's command line,
// and --expand-environment=no, which turns that off, is unknown to the
// older systemd-run of servers still in use.
func forwardedArgs(fs *flag.FlagSet) ([]string, error) {
	var args []string
	var err error
	fs.Visit(func(f *flag.Flag) {
		if startOnly[f.Name] || err != nil {
			return
		}
		v := f.Value.String()
		if strings.Contains(v, "$") {
			err = fmt.Errorf("%w: -%s %q: no $ in a value systemd would expand", errUsage, f.Name, v)
			return
		}
		if pathFlags[f.Name] && v != "" {
			v, err = filepath.Abs(v)
		}
		args = append(args, "-"+f.Name+"="+v)
	})
	if err != nil {
		return nil, err
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
		return nil, fmt.Errorf("%w: SUDO_UID/SUDO_GID: %v", errUsage, err)
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
		// The unit's run re-applies start's confinement (confine.go).
		"--setenv=" + unitConfinedEnv + "=1",
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
