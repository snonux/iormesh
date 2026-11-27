package agenttrial

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"slices"

	"iormesh/internal/version"
)

// Command is cmd/iormeshtrial: -version, the flags, the run, and the
// exit code (0 passed, 1 failed or refused, 2 help). It lives here, not
// in main, so its branches are under test.
func Command(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if slices.Equal(args, []string{"-version"}) {
		_, _ = fmt.Fprintln(stdout, version.Version)
		return 0
	}
	o, err := ParseFlags(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 2
	}
	if err == nil {
		err = Main(ctx, o, stdout)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "iormeshtrial: %v\n", err)
		return 1
	}
	return 0
}

// The environment of `mage agentTrial`: the agent is required, the rest
// has defaults (the mode automatic, the agent's model, claude's medium
// effort, iom1 and iom2,
// ssh's own config, `mage releaseUntagged`'s directory of this version:
// the commit under test is untagged until a release is cut, decision
// #40; a tagged release is served by naming its directory).
const (
	EnvAgent      = "IORMESH_TRIAL_AGENT"
	EnvMode       = "IORMESH_TRIAL_MODE"
	EnvModel      = "IORMESH_TRIAL_MODEL"
	EnvEffort     = "IORMESH_TRIAL_EFFORT"
	EnvController = "IORMESH_TRIAL_CONTROLLER"
	EnvTraced     = "IORMESH_TRIAL_TRACED"
	EnvSSHConfig  = "IORMESH_TRIAL_SSH_CONFIG"
	EnvReleaseDir = "IORMESH_TRIAL_RELEASE_DIR"
)

// mageFlags maps each optional variable to its flag, in a fixed order.
var mageFlags = [][2]string{{EnvMode, "-mode"}, {EnvModel, "-model"}, {EnvEffort, "-effort"},
	{EnvController, "-controller"}, {EnvTraced, "-traced"}, {EnvSSHConfig, "-ssh-config"}}

// MageArgs is the `go run ./cmd/iormeshtrial …` command line `mage
// agentTrial` runs, from the environment (getenv). Without an agent it
// refuses: the trial sends the docs and the VMs' command output to that
// agent's model provider.
func MageArgs(getenv func(string) string) ([]string, error) {
	agent := getenv(EnvAgent)
	if agent == "" {
		return nil, fmt.Errorf("%s is not set: %w", EnvAgent, ErrOptIn)
	}
	release := getenv(EnvReleaseDir)
	if release == "" {
		release = filepath.Join("dist", "untagged", version.Version)
	}
	args := []string{"run", "./cmd/iormeshtrial", "-agent", agent, "-release-dir", release}
	for _, f := range mageFlags {
		if v := getenv(f[0]); v != "" {
			args = append(args, f[1], v)
		}
	}
	return args, nil
}
