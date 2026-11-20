package acceptance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// installed is what decision #1's bootstrap lays down on a server.
const (
	installedBin = "/opt/iormesh/bin/iormesh"
	installedKey = "/opt/iormesh/etc/minisign.pub"
	traceRoot    = "/opt/iormesh/traces"
)

// preflight refuses to start unless the controller and both hosts are
// ready, so a run never half-deploys onto an unprepared or busy server.
// It also stages the workload jar and the iormeshctl HOME.
func (r *Runner) preflight(ctx context.Context) error {
	r.phase("preflight")
	if err := r.preflightLocal(); err != nil {
		return err
	}
	for _, h := range []string{r.cfg.Host1, r.cfg.Host2} {
		in, err := r.preflightHost(ctx, h)
		if err != nil {
			return fmt.Errorf("%s: %w", h, err)
		}
		r.info[h] = in
	}
	if r.info[r.cfg.Host1].machineID == r.info[r.cfg.Host2].machineID {
		return errors.New("HOST1 and HOST2 have the same /etc/machine-id: one machine under two names, or cloned")
	}
	if err := r.resolveAddrs(ctx); err != nil {
		return err
	}
	for _, h := range []string{r.cfg.Host1, r.cfg.Host2} {
		if err := r.stageJar(ctx, h); err != nil {
			return fmt.Errorf("%s: staging the workload: %w", h, err)
		}
	}
	r.res.set(cPreflight, Pass, "%s: %s %s; %s: %s %s", r.cfg.Host1, r.info[r.cfg.Host1].arch,
		r.info[r.cfg.Host1].kernel, r.cfg.Host2, r.info[r.cfg.Host2].arch, r.info[r.cfg.Host2].kernel)
	return nil
}

// preflightLocal checks the controller side: rsync's version (security.md:
// older receivers can be attacked by the server), the inputs, and a fresh
// HOME for iormeshctl holding a copy of the pinned key.
func (r *Runner) preflightLocal() error {
	out, err := exec.Command("rsync", "--version").Output()
	if err != nil {
		return fmt.Errorf("controller rsync: %w", err)
	}
	if err := rsyncOK(string(out)); err != nil {
		return err
	}
	for _, f := range []string{r.cfg.Jar, r.cfg.Ctl, r.cfg.Key, r.cfg.Goldens} {
		if _, err := os.Stat(f); err != nil {
			return err
		}
	}
	return r.prepareWorkDir()
}

var rsyncVersion = regexp.MustCompile(`^rsync\s+version\s+v?(\d+)\.(\d+)\.(\d+)`)

// rsyncOK accepts `rsync --version` output of 3.4.0 or newer; openrsync
// and macOS's 2.6.9 are refused (docs/security.md).
func rsyncOK(out string) error {
	m := rsyncVersion.FindStringSubmatch(strings.TrimSpace(out))
	if m == nil {
		return fmt.Errorf("controller rsync: cannot read a version from %q; need rsync >= 3.4.0",
			firstLine(out))
	}
	v := [3]int{}
	for i := range v {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	if v[0] < 3 || (v[0] == 3 && v[1] < 4) {
		return fmt.Errorf("controller rsync %d.%d.%d: need >= 3.4.0 (CVE-2024-12087/12088)", v[0], v[1], v[2])
	}
	return nil
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

// prepareWorkDir makes iormeshctl's HOME: it must not exist or be empty,
// so an earlier run's registry or store never leaks into this one.
func (r *Runner) prepareWorkDir() error {
	if ents, err := os.ReadDir(r.cfg.WorkDir); err == nil && len(ents) > 0 {
		return fmt.Errorf("%s is not empty (an earlier run?): move it away or set %s", r.cfg.WorkDir, EnvWorkDir)
	}
	cfgDir := filepath.Join(r.cfg.WorkDir, ".config", "iormesh")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(r.cfg.WorkDir, 0o700); err != nil {
		return err
	}
	key, err := os.ReadFile(r.cfg.Key)
	if err != nil {
		return err
	}
	// deploy verifies every build against this copy, as it would against
	// the operator's ~/.config/iormesh/minisign.pub.
	return os.WriteFile(filepath.Join(cfgDir, "minisign.pub"), key, 0o600)
}
