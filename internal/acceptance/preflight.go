package acceptance

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
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
	if err := r.notController(); err != nil {
		return err
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

// preflightLocal checks the controller side: rsync's version, or its
// package's backported fixes (security.md, decision #39: older receivers
// can be attacked by the server), the inputs (the signed agent jar among
// them, task kf4), and a fresh HOME for iormeshctl holding a copy of the
// pinned key.
func (r *Runner) preflightLocal() error {
	out, err := exec.Command("rsync", "--version").Output()
	if err != nil {
		return fmt.Errorf("controller rsync: %w", err)
	}
	if err := rsyncOK(string(out), rsyncChangelog()); err != nil {
		return err
	}
	for _, f := range []string{r.cfg.Jar, r.cfg.Ctl, r.cfg.Key, r.cfg.Goldens} {
		if _, err := os.Stat(f); err != nil {
			return err
		}
	}
	if err := r.checkAgentBuild(); err != nil {
		return err
	}
	return r.prepareWorkDir()
}

// localMachineID is the controller's /etc/machine-id; a variable so the
// tests can point it at a file of their own.
var localMachineID = "/etc/machine-id"

// notController refuses a host that is the controller itself (its
// machine id): rm would uninstall the operator's own /opt/iormesh, and
// the run's cleanup works in the same home as its results. A controller
// without the file (macOS) cannot be a Linux host anyway.
func (r *Runner) notController() error {
	data, err := os.ReadFile(localMachineID)
	if err != nil {
		return nil
	}
	id := strings.TrimSpace(string(data))
	for _, h := range []string{r.cfg.Host1, r.cfg.Host2} {
		if id != "" && r.info[h].machineID == id {
			return fmt.Errorf("%s is this controller (same /etc/machine-id): use two other machines", h)
		}
	}
	return nil
}

var rsyncVersion = regexp.MustCompile(`^rsync\s+version\s+v?(\d+)\.(\d+)\.(\d+)`)

// rsyncFixes are the two receiver-side fixes of January 2025 a
// distribution build must carry to stand in for 3.4.0 (decision #39),
// each as the ids a package changelog may name it by: the CVE (Debian,
// Ubuntu) or Red Hat's Jira id (el9's changelog names no CVEs).
var rsyncFixes = [][]string{
	{"CVE-2024-12087", "RHEL-70208"}, // path traversal (--inc-recursive)
	{"CVE-2024-12088", "RHEL-70210"}, // --safe-links bypass
}

// rsyncOK accepts `rsync --version` output of 3.4.0 or newer, or of
// 3.2.4 to 3.3.x when changelog — the package changelog of the rsync
// first in PATH, empty when it has none — names both rsyncFixes
// (decision #39: patched distribution builds, such as el9's
// rsync-3.2.7-1.el9_8). 3.2.4 is the floor whatever the changelog says:
// collect needs --fsync (federate.FetchFlags). openrsync and macOS's
// 2.6.9 are refused (docs/security.md).
func rsyncOK(out, changelog string) error {
	m := rsyncVersion.FindStringSubmatch(strings.TrimSpace(out))
	if m == nil {
		return fmt.Errorf("controller rsync: cannot read a version from %q; need rsync >= 3.4.0",
			firstLine(out))
	}
	v := [3]int{}
	for i := range v {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	if v[0] > 3 || (v[0] == 3 && v[1] >= 4) {
		return nil
	}
	if v[0] == 3 && (v[1] == 3 || (v[1] == 2 && v[2] >= 4)) && hasFixes(changelog) {
		return nil
	}
	return fmt.Errorf("controller rsync %d.%d.%d: need >= 3.4.0, or a distribution build "+
		"with CVE-2024-12087/12088 backported (decision #39)", v[0], v[1], v[2])
}

// hasFixes reports whether changelog names every one of rsyncFixes.
func hasFixes(changelog string) bool {
	for _, ids := range rsyncFixes {
		found := false
		for _, id := range ids {
			found = found || strings.Contains(changelog, id)
		}
		if !found {
			return false
		}
	}
	return true
}

// rsyncChangelog returns the package changelog of the rsync first in
// PATH, or "" when no package owns it (a self-built rsync, macOS). rpm
// is asked by the binary's path, so a changelog never vouches for
// another rsync; Debian's changelog belongs to /usr/bin/rsync only.
func rsyncChangelog() string {
	path, err := exec.LookPath("rsync")
	if err != nil {
		return ""
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	if out, err := exec.Command("rpm", "-q", "--changelog", "-f", path).Output(); err == nil {
		return string(out)
	}
	if path != "/usr/bin/rsync" {
		return ""
	}
	return gunzipFile("/usr/share/doc/rsync/changelog.Debian.gz")
}

// gunzipFile returns the decompressed contents of a gzip file, or ""
// when it is missing or damaged.
func gunzipFile(name string) string {
	f, err := os.Open(name)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }() // read-only: a close error loses nothing
	zr, err := gzip.NewReader(f)
	if err != nil {
		return ""
	}
	data, err := io.ReadAll(zr)
	if err != nil {
		return ""
	}
	return string(data)
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
