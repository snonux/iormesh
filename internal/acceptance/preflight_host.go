package acceptance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// archOf maps `uname -m` to the release arch, as deploy does.
var archOf = map[string]string{"x86_64": "amd64", "aarch64": "arm64"}

// preflightHost checks one server: reachable over key-based SSH with its
// host key known, bootstrapped (binary, root-owned key, the sudoers
// rule), the tools the run needs, and no iormesh session or trace on it
// already — the run's rm uninstalls /opt/iormesh, which must never
// destroy someone else's traces.
func (r *Runner) preflightHost(ctx context.Context, h string) (*hostInfo, error) {
	in := &hostInfo{}
	var err error
	if in.home, err = r.remote(ctx, h, "pwd"); err != nil {
		return nil, fmt.Errorf("ssh (key in the agent, host key in known_hosts?): %w", err)
	}
	if !strings.HasPrefix(in.home, "/") {
		return nil, fmt.Errorf("pwd printed %q, not an absolute home", in.home)
	}
	if err := r.hostFacts(ctx, h, in); err != nil {
		return nil, err
	}
	if err := r.checkBootstrap(ctx, h); err != nil {
		return nil, err
	}
	for _, tool := range []string{"java", "ss", "rsync", "systemd-run", "df", "stat", "fallocate"} {
		if ok, err := r.remoteTest(ctx, h, "sh", "-c", "command -v "+tool); err != nil || !ok {
			return nil, fmt.Errorf("%s not found on the host (%v)", tool, err)
		}
	}
	if err := r.checkIdle(ctx, h); err != nil {
		return nil, err
	}
	dist := filepath.Join(r.cfg.Dist, "iormesh-linux-"+in.arch)
	for _, f := range []string{dist, dist + ".minisig"} {
		if _, err := os.Stat(f); err != nil {
			return nil, fmt.Errorf("signed build for %s: %w (mage buildPodman && mage sign)", in.arch, err)
		}
	}
	return in, nil
}

// hostFacts reads arch, kernel and machine id.
func (r *Runner) hostFacts(ctx context.Context, h string, in *hostInfo) error {
	m, err := r.remote(ctx, h, "uname", "-m")
	if err != nil {
		return err
	}
	if in.arch = archOf[m]; in.arch == "" {
		return fmt.Errorf("arch %q: the tracer ships for x86_64 and aarch64", m)
	}
	if in.kernel, err = r.remote(ctx, h, "uname", "-r"); err != nil {
		return err
	}
	if in.machineID, err = r.remote(ctx, h, "cat", "/etc/machine-id"); err != nil || in.machineID == "" {
		return fmt.Errorf("no /etc/machine-id (%v)", err)
	}
	return nil
}

// checkBootstrap checks decision #1's first install: deploy only upgrades,
// so without it every later step would fail less clearly.
func (r *Runner) checkBootstrap(ctx context.Context, h string) error {
	for _, f := range []string{installedBin, installedKey} {
		if ok, err := r.remoteTest(ctx, h, "test", "-f", f); err != nil || !ok {
			return fmt.Errorf("%s missing: bootstrap the host first (runbook step 2) (%v)", f, err)
		}
	}
	// -l with a command exits 0 only when sudo would run it without a
	// password: the rule is checked, not exercised.
	if _, err := r.remote(ctx, h, "sudo", "-n", "-l", installedBin); err != nil {
		return fmt.Errorf("sudoers rule for %s missing or wants a password: %w", installedBin, err)
	}
	return nil
}

// checkIdle refuses a host with a live iormesh unit or any trace
// directory: the run must own every session it later uninstalls.
func (r *Runner) checkIdle(ctx context.Context, h string) error {
	units, err := r.remote(ctx, h, "systemctl", "list-units", "--all", "--plain", "--no-legend", "iormesh-*")
	if err != nil {
		return err
	}
	if len(lines(units)) > 0 {
		return fmt.Errorf("iormesh units already loaded: %s", strings.Join(lines(units), "; "))
	}
	if ok, err := r.remoteTest(ctx, h, "test", "-d", traceRoot); err != nil {
		return err
	} else if !ok {
		return nil
	}
	left, err := r.remote(ctx, h, "ls", "-A", traceRoot)
	if err != nil {
		return err
	}
	if len(lines(left)) > 0 {
		return errors.New(traceRoot + " is not empty: collect or remove those traces first")
	}
	return nil
}

// resolveAddrs fills each host's IPv4 as the other host reaches it: the
// configured one, else the other host's `getent ahostsv4` of its name.
func (r *Runner) resolveAddrs(ctx context.Context) error {
	pairs := []struct{ host, peer, addr string }{
		{r.cfg.Host1, r.cfg.Host2, r.cfg.Addr1}, {r.cfg.Host2, r.cfg.Host1, r.cfg.Addr2}}
	for _, p := range pairs {
		addr := p.addr
		if addr == "" {
			out, err := r.remote(ctx, p.peer, "getent", "ahostsv4", hostName(p.host))
			if err != nil || len(strings.Fields(out)) == 0 {
				return fmt.Errorf("%s does not resolve %s to IPv4 (an ssh_config alias?): set %s/%s (%v)",
					p.peer, hostName(p.host), EnvAddr1, EnvAddr2, err)
			}
			addr = strings.Fields(out)[0]
		}
		r.info[p.host].addr = addr
	}
	return nil
}

// stageJar copies the workload jar into the run's directory on h.
func (r *Runner) stageJar(ctx context.Context, h string) error {
	base := r.remoteBase(h)
	if _, err := r.remote(ctx, h, "mkdir", "-p", "-m", "0700", base); err != nil {
		return err
	}
	t, err := r.hosts.get(h)
	if err != nil {
		return err
	}
	r.logf("  push %s -> %s:%s", r.cfg.Jar, h, base)
	return t.Push(ctx, base, r.cfg.Jar)
}
