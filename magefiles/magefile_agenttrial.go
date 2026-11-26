//go:build mage

// M8's agent trial (docs/milestones/m8.md, decision #37,
// internal/agenttrial): AgentTrial runs one agent in one mode of the
// iormesh skill against the throwaway iom VMs. It is opt-in and never
// part of Test: it sends the docs and every command output from the VMs
// to the agent's model provider, so it runs only when an agent is named.
package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"

	"iormesh/internal/version"
)

// The trial's environment: the agent (claude or pi) is required, the
// rest has defaults (the mode automatic, the agent's model, iom1 and
// iom2, ssh's own config, `mage release`'s directory of this version).
const (
	trialAgentEnv      = "IORMESH_TRIAL_AGENT"
	trialModeEnv       = "IORMESH_TRIAL_MODE"
	trialModelEnv      = "IORMESH_TRIAL_MODEL"
	trialControllerEnv = "IORMESH_TRIAL_CONTROLLER"
	trialTracedEnv     = "IORMESH_TRIAL_TRACED"
	trialSSHConfigEnv  = "IORMESH_TRIAL_SSH_CONFIG"
	trialReleaseDirEnv = "IORMESH_TRIAL_RELEASE_DIR"
)

// AgentTrial runs cmd/iormeshtrial once with IORMESH_TRIAL_AGENT (claude
// or pi) in IORMESH_TRIAL_MODE (automatic or guided) against
// IORMESH_TRIAL_CONTROLLER and IORMESH_TRIAL_TRACED (default iom1, iom2),
// serving IORMESH_TRIAL_RELEASE_DIR (default dist/release/<version>, made
// by `mage release`). It builds the workload jar first. The result lands
// in eval/agent-trial/results/, the transcript and command log in
// eval/agent-trial/runs/ (gitignored).
func AgentTrial() error {
	agent := os.Getenv(trialAgentEnv)
	if agent == "" {
		return errors.New(trialAgentEnv + " is not set: the trial sends the docs and the VMs' command " +
			"output to that agent's model provider, so name one explicitly (claude or pi)")
	}
	mg.Deps(WorkloadBuild)
	commit, err := sh.Output("git", "rev-parse", "--short", "HEAD")
	if err != nil {
		return err
	}
	args := []string{"run", "./cmd/iormeshtrial", "-agent", agent, "-commit", strings.TrimSpace(commit),
		"-workload", workloadJar, "-release-dir", trialEnv(trialReleaseDirEnv,
			filepath.Join(distDir, "release", version.Version))}
	for flag, env := range map[string]string{"-mode": trialModeEnv, "-model": trialModelEnv,
		"-controller": trialControllerEnv, "-traced": trialTracedEnv, "-ssh-config": trialSSHConfigEnv} {
		if v := os.Getenv(env); v != "" {
			args = append(args, flag, v)
		}
	}
	return sh.RunWithV(map[string]string{"CGO_ENABLED": "0"}, "go", args...)
}

// trialEnv is the variable's value or def.
func trialEnv(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
