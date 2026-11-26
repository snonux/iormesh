package agenttrial

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"time"
)

// ErrOptIn is the refusal without an agent: the trial sends the task,
// the docs and every command output from the VMs to the agent's model
// provider (decision #37, "Data"), so it runs only when one is named.
var ErrOptIn = errors.New("no agent: the trial sends the docs and the VMs' command output to the " +
	"agent's model provider, so it runs only when one is named explicitly (-agent claude|pi, or " +
	"IORMESH_TRIAL_AGENT for mage agentTrial), against throwaway VMs with synthetic data only")

// Options are the command line of cmd/iormeshtrial.
type Options struct {
	Agent, Mode, Model string
	// Controller is the VM the agent's shell lands on (the guide's
	// laptop), Traced the VM running the workload.
	Controller, Traced string
	// SSH is the ssh binary, SSHConfig an optional -F file naming the VMs.
	SSH, SSHConfig string
	// Dir holds the trial's inputs (task.md, questions.yaml, scenario.sh)
	// and its results/ and runs/; Repo is the checkout the docs are
	// copied from.
	Dir, Repo string
	// Workload is the workload jar; ReleaseDir is `mage release`'s
	// directory of the commit under test, served to the controller.
	Workload, ReleaseDir string
	ReleasePort          int
	Commit, Date         string
	MaxTurns             int
	Timeout              time.Duration

	// agent replaces the agent CLI: the tests' scripted stand-in, as M5's
	// tests stand in for the model. No flag sets it.
	agent Agent
}

// ParseFlags parses cmd/iormeshtrial's flags.
func ParseFlags(args []string, stderr io.Writer) (Options, error) {
	fs := flag.NewFlagSet("iormeshtrial", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o Options
	fs.StringVar(&o.Agent, "agent", "", "agent CLI: claude or pi (required)")
	fs.StringVar(&o.Mode, "mode", Automatic, "skill mode: automatic or guided")
	fs.StringVar(&o.Model, "model", "", "model (default: claude-sonnet-5-5 or glm-5.3-flash by agent)")
	fs.StringVar(&o.Controller, "controller", "iom1", "VM the agent's shell lands on")
	fs.StringVar(&o.Traced, "traced", "iom2", "VM running the workload")
	fs.StringVar(&o.SSH, "ssh", "ssh", "ssh binary")
	fs.StringVar(&o.SSHConfig, "ssh-config", "", "ssh config naming the VMs (-F)")
	fs.StringVar(&o.Dir, "dir", "eval/agent-trial", "the trial's inputs and outputs")
	fs.StringVar(&o.Repo, "repo", ".", "checkout the docs are copied from")
	fs.StringVar(&o.Workload, "workload", "bin/workload.jar", "workload jar")
	fs.StringVar(&o.ReleaseDir, "release-dir", "", "release directory to serve (required)")
	fs.IntVar(&o.ReleasePort, "release-port", DefaultReleasePort, "controller port of the release")
	fs.StringVar(&o.Commit, "commit", "", "commit under test (recorded in the result)")
	fs.StringVar(&o.Date, "date", time.Now().UTC().Format(time.DateOnly), "results date")
	fs.IntVar(&o.MaxTurns, "max-turns", DefaultMaxTurns, "agent turn budget")
	fs.DurationVar(&o.Timeout, "timeout", DefaultTimeout, "wall-time budget")
	if err := fs.Parse(args); err != nil {
		return Options{}, err
	}
	if fs.NArg() != 0 {
		return Options{}, errors.New("iormeshtrial takes no arguments")
	}
	o.Model = o.defaultModel()
	return o, o.validate()
}

// defaultModel is -model, or the agent's model of decision #37.
func (o Options) defaultModel() string {
	switch {
	case o.Model != "":
		return o.Model
	case o.Agent == AgentClaude:
		return DefaultClaudeModel
	case o.Agent == AgentPi:
		return DefaultPiModel
	}
	return ""
}

// validate refuses what would make the run meaningless or unsafe.
func (o Options) validate() error {
	switch {
	case o.Agent == "":
		return ErrOptIn
	case !slices.Contains([]string{AgentClaude, AgentPi}, o.Agent):
		return fmt.Errorf("-agent %q: want %s or %s", o.Agent, AgentClaude, AgentPi)
	case o.Mode != Automatic && o.Mode != Guided:
		return fmt.Errorf("-mode %q: want %s or %s", o.Mode, Automatic, Guided)
	case o.Controller == "" || o.Traced == "" || o.Controller == o.Traced:
		return fmt.Errorf("-controller %q and -traced %q: want two VMs", o.Controller, o.Traced)
	case o.ReleaseDir == "":
		return errors.New("-release-dir is required: `mage release`'s directory of the commit under test")
	case o.MaxTurns < 1 || o.Timeout <= 0:
		return fmt.Errorf("-max-turns %d, -timeout %v: want both positive", o.MaxTurns, o.Timeout)
	}
	if _, err := time.Parse(time.DateOnly, o.Date); err != nil {
		return fmt.Errorf("-date %q: want YYYY-MM-DD", o.Date)
	}
	return nil
}
