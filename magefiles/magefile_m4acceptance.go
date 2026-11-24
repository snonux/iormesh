//go:build mage

// M4Acceptance runs docs/milestones.md M4's acceptance against two real hosts
// (docs/testing.md, "M4 acceptance on two real hosts"): the runbook's
// prerequisites must hold first — both hosts bootstrapped, a signed
// build in dist/, the release key pinned. The sequence and its checks
// live in internal/acceptance; this target only builds what it runs
// (the workload jar, a cgo-free iormeshctl) and wires the real SSH.
package main

import (
	"context"
	"os"
	"path/filepath"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"

	"iormesh/internal/acceptance"
	"iormesh/internal/federate"
)

// M4Acceptance deploys to, traces on and finally removes iormesh from
// exactly HOST1 and HOST2, printing PASS/FAIL per acceptance criterion.
// It ends by uninstalling /opt/iormesh on both (rm is a criterion), so a
// rerun needs the hosts bootstrapped again. Options come from
// IORMESH_M4_* variables (internal/acceptance/config.go).
func M4Acceptance(host1, host2 string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	cfg, err := acceptance.FromEnv(host1, host2, home, os.Getenv)
	if err != nil {
		return err
	}
	mg.Deps(WorkloadBuild)
	binDir, err := os.MkdirTemp("", "iormesh-m4-ctl-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(binDir) }()
	// The controller under test is built as it ships: cgo-free.
	cfg.Ctl = filepath.Join(binDir, "iormeshctl")
	if err := sh.RunWith(map[string]string{"CGO_ENABLED": "0"}, "go", "build", "-o", cfg.Ctl,
		"./cmd/iormeshctl"); err != nil {
		return err
	}
	if err := m4AbsPaths(&cfg); err != nil {
		return err
	}
	return runM4Acceptance(cfg)
}

// m4AbsPaths makes the tree-relative inputs absolute: iormeshctl runs
// with another HOME, and deploy reads -dist relative to its own cwd.
func m4AbsPaths(cfg *acceptance.Config) error {
	var err error
	for _, p := range []struct {
		dst *string
		src string
	}{{&cfg.Jar, workloadJar}, {&cfg.Goldens, "workload/scenarios"}, {&cfg.Dist, cfg.Dist},
		{&cfg.WorkDir, cfg.WorkDir}, {&cfg.Key, cfg.Key}} {
		if *p.dst, err = filepath.Abs(p.src); err != nil {
			return err
		}
	}
	return nil
}

// runM4Acceptance wires the real iormeshctl and SSH and runs; ^C cancels
// the current step and goes on to the teardown (a second ^C, or a
// failed teardown, leaves the manual cleanup the runbook explains).
func runM4Acceptance(cfg acceptance.Config) error {
	ctl, err := acceptance.NewExecCtl(cfg.Ctl, cfg.WorkDir)
	if err != nil {
		return err
	}
	// The first ^C or SIGTERM cancels the run, whose teardown (rm, the
	// servers' cleanup) still runs, here, for up to its 5 minutes:
	// InterruptContext takes SIGINT away from mage's own handler, which
	// would exit 5 seconds in (task mi4). A second ^C exits at once.
	ctx, stop := acceptance.InterruptContext(context.Background())
	defer stop()
	r := acceptance.New(cfg, acceptance.Deps{Ctl: ctl, Out: os.Stdout,
		Dial: func(h string) (federate.Transport, error) { return federate.NewSSH(h) }})
	return r.Run(ctx)
}
