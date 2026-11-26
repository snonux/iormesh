package agenttrial

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Prep prepares the VMs as decision #37 lists ("What the agent gets"):
// the controller's SSH key on the traced host, the traced host's key in
// the controller's known_hosts, sudo without a password on both, a
// throwaway signing-key password in $IORMESH_MINISIGN_PASSWORD on the
// controller — each a Human step of guide chapter 2 an agent must not
// take — and the microservices scenario running on the traced host, the
// application under observation. It runs through the harness's own Shell,
// logged as ByHarness; nothing of it is the agent's.
type Prep struct {
	Shell              *Shell
	Controller, Traced string
	// Workload is the local workload jar (mage workloadBuild), Scenario
	// the loop that runs the scenario (eval/agent-trial/scenario.sh).
	Workload, Scenario string
	// Password is the throwaway signing-key password (NewPassword).
	Password string
}

// RemoteDir holds the workload and its loop on the traced host.
const RemoteDir = "iormesh-trial"

var pubKey = regexp.MustCompile(`^ssh-ed25519 [A-Za-z0-9+/=]+( [A-Za-z0-9@._-]+)?$`)

// NewPassword is a throwaway signing-key password: 24 hex digits.
func NewPassword() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b) // never fails (crypto/rand)
	return hex.EncodeToString(b)
}

// Run prepares both VMs in order and stops at the first failing step.
func (p Prep) Run(ctx context.Context) error {
	for _, h := range []string{p.Controller, p.Traced} {
		if err := p.must(ctx, Call{Host: h, Command: "sudo -n true"}); err != nil {
			return fmt.Errorf("sudo without a password on %s: %w", h, err)
		}
	}
	steps := []func(context.Context) error{p.trustKeys, p.setPassword, p.startScenario}
	for _, step := range steps {
		if err := step(ctx); err != nil {
			return err
		}
	}
	return nil
}

// trustKeys puts the controller's key on the traced host and the traced
// host's key in the controller's known_hosts, then proves the hop.
func (p Prep) trustKeys(ctx context.Context) error {
	out, code, err := p.Shell.Run(ctx, Call{Host: p.Controller, By: ByHarness, Command: "{ test -f " +
		"~/.ssh/id_ed25519 || ssh-keygen -q -t ed25519 -N '' -f ~/.ssh/id_ed25519; } && " +
		"cat ~/.ssh/id_ed25519.pub"})
	key := strings.TrimSpace(out)
	if err != nil || code != 0 || !pubKey.MatchString(key) {
		return fmt.Errorf("the controller's key on %s: exit %d, %v: %q", p.Controller, code, err, out)
	}
	calls := []Call{
		{Host: p.Traced, Command: "umask 077 && mkdir -p ~/.ssh && touch ~/.ssh/authorized_keys && " +
			"{ grep -qxF '" + key + "' ~/.ssh/authorized_keys || echo '" + key + "' >> ~/.ssh/authorized_keys; }"},
		{Host: p.Controller, Command: "ssh-keygen -F " + p.Traced + " >/dev/null || " +
			"ssh-keyscan -t ed25519 " + p.Traced + " >> ~/.ssh/known_hosts"},
		{Host: p.Controller, Command: "ssh -o BatchMode=yes " + p.Traced + " true"},
	}
	return p.mustAll(ctx, calls)
}

// setPassword writes the password into the first line of the
// controller's ~/.bashrc, which sshd's bash reads for every command, and
// proves a new session sees it. The log never holds the password.
func (p Prep) setPassword(ctx context.Context) error {
	const v = "IORMESH_MINISIGN_PASSWORD"
	if !regexp.MustCompile(`^[0-9a-f]{16,}$`).MatchString(p.Password) {
		return fmt.Errorf("the signing-key password must be hex (NewPassword)")
	}
	set := "{ printf 'export " + v + "=%s\\n' " + p.Password + "; grep -v '^export " + v + "=' " +
		"~/.bashrc 2>/dev/null || true; } > ~/.bashrc.trial && mv ~/.bashrc.trial ~/.bashrc"
	return p.mustAll(ctx, []Call{
		{Host: p.Controller, Command: set, LogAs: strings.ReplaceAll(set, p.Password, "<password>")},
		{Host: p.Controller, Command: `test -n "$` + v + `"`},
	})
}

// startScenario copies the workload and its loop to the traced host and
// starts the loop in the background, replacing one a previous
// preparation left. The stop and the start are separate calls: sshd runs
// each command as `bash -c <command>`, so a pkill pattern must not match
// anything in its own call. The bracket class ("[i]ormesh…") keeps the
// pattern from matching itself, but the start names the loop's path
// plainly, so in one call the pkill killed its own shell (ssh exit 255,
// found by the first real trial, task nm4).
func (p Prep) startScenario(ctx context.Context) error {
	for _, f := range [][2]string{{p.Workload, "workload.jar"}, {p.Scenario, "scenario.sh"}} {
		if err := p.copyFile(ctx, f[0], f[1]); err != nil {
			return err
		}
	}
	return p.mustAll(ctx, []Call{
		{Host: p.Traced, Command: stopScenarioCommand},
		{Host: p.Traced, Command: "nohup sh ~/" + RemoteDir + "/scenario.sh > ~/" +
			RemoteDir + "/scenario.log 2>&1 < /dev/null &"},
		{Host: p.Traced, Command: ScenarioAliveCommand},
	})
}

// stopScenarioCommand ends a loop and JVMs a previous preparation left;
// it exits 0 whether or not one ran (pkill exits 1 on no match).
const stopScenarioCommand = "pkill -f '[i]ormesh-trial/scenario.sh'; " +
	"pkill -f '[i]ormesh-trial/workload.jar'; true"

// ScenarioAliveCommand proves the scenario runs on the traced host: the
// loop and, inside its first round (billing starts at once, inventory
// after 5 s), a workload JVM. A java missing on the VM or a jar that will
// not start fails the preparation, not the agent.
const ScenarioAliveCommand = "sleep 8 && pgrep -f '[i]ormesh-trial/scenario.sh' && " +
	"pgrep -f '[i]ormesh-trial/workload.jar'"

// copyFile copies a local file to ~/RemoteDir/name on the traced host,
// through the remote command's stdin.
func (p Prep) copyFile(ctx context.Context, local, name string) error {
	f, err := os.Open(local)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return p.must(ctx, Call{Host: p.Traced, Stdin: f, Command: "mkdir -p ~/" + RemoteDir +
		" && cat > ~/" + RemoteDir + "/" + name})
}

// mustAll runs calls in order, each of which must exit 0.
func (p Prep) mustAll(ctx context.Context, calls []Call) error {
	for _, c := range calls {
		if err := p.must(ctx, c); err != nil {
			return err
		}
	}
	return nil
}

// must runs one harness call that must exit 0.
func (p Prep) must(ctx context.Context, c Call) error {
	c.By = ByHarness
	out, code, err := p.Shell.Run(ctx, c)
	if err != nil {
		return err
	}
	if code != 0 {
		shown := c.Command
		if c.LogAs != "" {
			shown = c.LogAs
		}
		return fmt.Errorf("prepare on %s: %q exited %d: %s", c.Host, shown, code, strings.TrimSpace(out))
	}
	return nil
}
