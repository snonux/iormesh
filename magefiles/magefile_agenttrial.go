//go:build mage

// M8's agent trial (docs/milestones/m8.md, decision #37,
// internal/agenttrial): AgentTrial runs one agent in one mode of the
// iormesh skill against the throwaway iom VMs. It is opt-in and never
// part of Test: it sends the docs and every command output from the VMs
// to the agent's model provider, so it runs only when an agent is named.
// The environment's meaning lives in agenttrial.MageArgs, under test.
package main

import (
	"os"
	"strings"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"

	"iormesh/internal/agenttrial"
)

// AgentTrial runs cmd/iormeshtrial once with IORMESH_TRIAL_AGENT (claude
// or pi) in IORMESH_TRIAL_MODE (automatic or guided) against
// IORMESH_TRIAL_CONTROLLER and IORMESH_TRIAL_TRACED (default iom1, iom2),
// serving IORMESH_TRIAL_RELEASE_DIR (default dist/release/<version>, made
// by `mage release`). It builds the workload jar first. The result lands
// in eval/agent-trial/results/, the transcript and command log in
// eval/agent-trial/runs/ (gitignored).
func AgentTrial() error {
	args, err := agenttrial.MageArgs(os.Getenv)
	if err != nil {
		return err
	}
	mg.Deps(WorkloadBuild)
	commit, err := sh.Output("git", "rev-parse", "--short", "HEAD")
	if err != nil {
		return err
	}
	return sh.RunWithV(map[string]string{"CGO_ENABLED": "0"}, "go",
		append(args, "-commit", strings.TrimSpace(commit), "-workload", workloadJar)...)
}
