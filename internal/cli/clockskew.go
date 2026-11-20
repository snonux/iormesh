package cli

import (
	"fmt"
	"os"
	"time"
)

// Hosts emulated as network namespaces on one machine share one kernel
// clock, so the merge's clock reconciliation would only ever see an
// offset of zero (PLAN.md "Testing strategy": "Emulated hosts share a
// kernel — compensate"). The test-only clock skew shifts every wall_ns
// one tracer stamps by a fixed amount, as if its host's wall clock were
// that far off, and leaves boot_ns alone: the merge's fusion window and
// clock bounds work on wall_ns, while boot_ns, still one clock for all
// tracers, tells a test the true order of any two events.
//
// It is an environment variable on purpose, not a flag: a flag would
// show in run's usage and be forwarded by start to its unit. It is read
// by a foreground run only and refused wherever a session is not the
// operator's own:
//   - run through the sudoers rule (sudoGated) refuses it — a sudoer must
//     not be able to falsify the timestamps a root session records
//     (sudo's env_reset normally drops it anyway);
//   - start refuses it rather than silently not applying it: systemd-run
//     never passes the caller's environment to the unit.
const (
	testClockSkewEnv = "IORMESH_TEST_CLOCK_SKEW"
	// maxTestClockSkew keeps the skew far from overflowing the wall clock
	// arithmetic, and is ample: the merge's fusion window is minutes.
	maxTestClockSkew = 24 * time.Hour
)

// testClockSkew parses the test-only skew from env ("" is none): a Go
// duration such as 1.5s or -3m, at most a day either way. gated says the
// process came through the sudoers rule, which refuses any skew. Every
// error wraps errUsage.
func testClockSkew(env func(string) string, gated bool) (time.Duration, error) {
	v := env(testClockSkewEnv)
	if v == "" {
		return 0, nil
	}
	if gated {
		return 0, fmt.Errorf("%w: %s is test-only and not allowed under sudo", errUsage, testClockSkewEnv)
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%w: %s: %v", errUsage, testClockSkewEnv, err)
	}
	if d > maxTestClockSkew || d < -maxTestClockSkew {
		return 0, fmt.Errorf("%w: %s %v: at most %v either way", errUsage, testClockSkewEnv, d,
			maxTestClockSkew)
	}
	return d, nil
}

// runClockSkew is testClockSkew for this process's run, gated as
// confineRun gates it (an unknown own path counts as gated).
func runClockSkew() (time.Duration, error) {
	self, err := os.Executable()
	if err != nil {
		self = ""
	}
	return testClockSkew(os.Getenv, sudoGated(self, os.Getenv, os.Lstat))
}

// refuseStartClockSkew fails a start with the test-only skew set: the
// unit would trace without it, so the caller's test would check a skew
// that never happened.
func refuseStartClockSkew(env func(string) string) error {
	if env(testClockSkewEnv) == "" {
		return nil
	}
	return fmt.Errorf("%w: %s is test-only and applies to a foreground run only; "+
		"start's unit never inherits it", errUsage, testClockSkewEnv)
}
