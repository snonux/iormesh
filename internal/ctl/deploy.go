package ctl

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"aead.dev/minisign"

	"iormesh/internal/federate"
	"iormesh/internal/release"
)

// `iormeshctl deploy [-project P] HOST...` (PLAN.md M4 "Deploy"): put the
// signed tracer build on each registered host and start its projects. Per
// host, every step an argv through the Transport (docs/implementation.md
// M4 "Controller side"):
//
//  1. preflight: uname -m picks the build (amd64/arm64); rsync and
//     systemd-run must be there (decision #12: fail fast), and so must the
//     installed tracer — deploy only upgrades: the first install needs the
//     operator's own sudo or configuration management (decision #1), and
//     deploy says so instead of pretending otherwise;
//  2. verify dist/iormesh-linux-<arch> and its .minisig against the pinned
//     key (decision #28) — before anything is pushed;
//  3. stage both in the login user's ~/.iormesh/staging, never in /opt:
//     the user can only hand files to the tracer, which re-verifies them;
//  4. sudo -n /opt/iormesh/bin/iormesh upgrade STAGED — the same build
//     again is a no-op there, and a refusal (bad signature, older build)
//     is reported verbatim and nothing is started;
//  5. per project: start it, or stop and start a live session whose flags
//     changed (decision #19; see deployedRecord), then print its status.
//
// Only the installed binary ever runs under sudo, through the one sudoers
// rule, and only with the subcommands it lets through (upgrade, start,
// stop, status); never -allow-downgrade, which is root's alone.

const (
	// installedTracer is the binary the sudoers rule names.
	installedTracer = "/opt/iormesh/bin/iormesh"
	// stagingDir is relative, so under the login user's home on the
	// server: private to the user and not shared like /tmp, where another
	// user could pre-create the name.
	stagingDir = ".iormesh/staging"
	// defaultDist is where `mage build` and `mage sign` leave the builds.
	defaultDist = "dist"
)

// unameArch maps `uname -m` to the Go arch of the builds iormesh ships.
var unameArch = map[string]string{
	"x86_64": "amd64", "amd64": "amd64",
	"aarch64": "arm64", "arm64": "arm64",
}

// deployer is one deploy run over several hosts.
type deployer struct {
	e       env
	key     minisign.PublicKey
	dist    string
	restart bool
	record  deployedRecord
	// verified caches the local check per arch: hosts of one arch share
	// one build.
	verified map[string]string
}

// deployCommand parses deploy's flags, checks every host against the
// registry before contacting any, and deploys host by host. A failed host
// is reported and the next one still deployed; the command then fails.
func deployCommand(args []string, e env) error {
	fs := newFlagSet("deploy", e.stderr)
	name := fs.String("project", "", "deploy only this project (default: every project of the host)")
	path := fs.String("registry", "", "registry file (default ~/.config/iormesh/servers.yaml)")
	dist := fs.String("dist", defaultDist, "directory holding iormesh-linux-<arch> and its "+release.SigSuffix)
	keyPath := fs.String("key", "", "pinned minisign public key (default ~/.config/iormesh/"+keyFileName+")")
	restart := fs.Bool("restart", false, "restart a running session even if its flags are unchanged")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("%w: deploy needs one or more HOSTs", errUsage)
	}
	servers, err := deployTargets(*path, e, fs.Args(), *name)
	if err != nil {
		return err
	}
	key, err := pinnedKey(*keyPath, e)
	if err != nil {
		return err
	}
	recPath, err := e.deployed()
	if err != nil {
		return err
	}
	record, err := loadDeployed(recPath)
	if err != nil {
		return err
	}
	d := deployer{e: e, key: key, dist: *dist, restart: *restart, record: record, verified: map[string]string{}}
	return d.all(context.Background(), servers, recPath)
}

// all deploys each server in turn and saves the record of what started,
// failed hosts or not.
func (d deployer) all(ctx context.Context, servers []federate.Server, recPath string) error {
	var failed []string
	for _, srv := range servers {
		if err := d.host(ctx, srv); err != nil {
			_, _ = fmt.Fprintf(d.e.stderr, "iormeshctl: %s: %v\n", srv.Host, err)
			failed = append(failed, srv.Host)
		}
	}
	if err := d.record.save(recPath); err != nil {
		return err
	}
	if len(failed) > 0 {
		return fmt.Errorf("deploy failed on %d of %d hosts: %s", len(failed), len(servers),
			strings.Join(failed, " "))
	}
	return nil
}

// deployTargets resolves hosts in the registry, narrowed to project when
// set. Any unregistered host or unknown project fails the whole command
// before a single host is contacted (principle #3). A host named twice is
// deployed once.
func deployTargets(path string, e env, hosts []string, project string) ([]federate.Server, error) {
	if path == "" {
		var err error
		if path, err = e.registry(); err != nil {
			return nil, err
		}
	}
	r, err := federate.LoadRegistry(path)
	if err != nil {
		return nil, err
	}
	var servers []federate.Server
	for _, host := range hosts {
		srv, err := r.Lookup(host)
		if err != nil {
			return nil, fmt.Errorf("%w (iormeshctl add it first)", err)
		}
		if slices.ContainsFunc(servers, func(s federate.Server) bool { return s.Host == host }) {
			continue
		}
		if project != "" {
			i := slices.IndexFunc(srv.Projects, func(p federate.Project) bool { return p.Name == project })
			if i < 0 {
				return nil, fmt.Errorf("%w: %q on %s", federate.ErrUnknownProject, project, host)
			}
			srv.Projects = []federate.Project{srv.Projects[i]}
		}
		servers = append(servers, srv)
	}
	return servers, nil
}

// host runs the deploy steps on one server. An upgrade failure ends it
// before any start: the host keeps what it ran. Projects are independent,
// so one that fails to start does not keep the others from starting.
func (d deployer) host(ctx context.Context, srv federate.Server) error {
	t, err := d.e.transport(srv.Host)
	if err != nil {
		return err
	}
	arch, err := preflight(ctx, t)
	if err != nil {
		return err
	}
	bin, err := d.verify(arch)
	if err != nil {
		return err
	}
	staged, err := stage(ctx, t, bin)
	if err != nil {
		return err
	}
	out, err := t.Run(ctx, sudoTracer("upgrade", staged)...)
	d.relay(srv.Host, out)
	if err != nil {
		return fmt.Errorf("upgrade refused, nothing started: %w", err)
	}
	var errs []error
	for _, p := range srv.Projects {
		if err := d.project(ctx, t, srv.Host, p); err != nil {
			errs = append(errs, fmt.Errorf("project %s: %w", p.Name, err))
		}
	}
	return errors.Join(errs...)
}

// preflight returns the server's Go arch and checks the tools deploy and
// collect need and the installed tracer are there.
func preflight(ctx context.Context, t federate.Transport) (string, error) {
	out, err := t.Run(ctx, "uname", "-m")
	if err != nil {
		return "", err
	}
	machine := strings.TrimSpace(string(out))
	arch, ok := unameArch[machine]
	if !ok {
		return "", fmt.Errorf("unsupported architecture %q: iormesh ships linux amd64 and arm64", machine)
	}
	// command is a shell builtin; Run's quoting leaves it one.
	for _, tool := range []string{"rsync", "systemd-run"} {
		if _, err := t.Run(ctx, "command", "-v", tool); err != nil {
			return "", fmt.Errorf("%s not found on the server, deploy and collect need it: %w", tool, err)
		}
	}
	if _, err := t.Run(ctx, "test", "-x", installedTracer); err != nil {
		return "", fmt.Errorf("%s is not installed: deploy only upgrades an installed tracer; "+
			"the first install takes the operator's own sudo or configuration management "+
			"(PLAN.md decision #1, docs/security.md): %w", installedTracer, err)
	}
	return arch, nil
}

// verify checks the local build for arch against the pinned key and
// returns its path. VerifyFile also requires the signed name to be the
// file's own, so another arch's build renamed into place fails here, not
// only on the server. The server verifies the bytes it receives again,
// so a file changed between this check and the push is refused there.
func (d deployer) verify(arch string) (string, error) {
	path := filepath.Join(d.dist, release.TracerFile(arch))
	if _, ok := d.verified[arch]; ok {
		return path, nil
	}
	m, err := release.VerifyFile(d.key, path)
	if err != nil {
		return "", fmt.Errorf("local build, nothing pushed: %w", err)
	}
	_, _ = fmt.Fprintf(d.e.stdout, "verified %s: %s %s\n", path, m.File, m.Version)
	d.verified[arch] = m.Version
	return path, nil
}

// stage pushes the build and its signature into the staging directory and
// returns the staged binary's path as the server resolves it (relative to
// the login user's home, where every ssh command starts).
func stage(ctx context.Context, t federate.Transport, bin string) (string, error) {
	// -m applies to the last component only; staging/ is the directory
	// the files land in, so that one is kept private.
	if _, err := t.Run(ctx, "mkdir", "-p", "-m", "0700", stagingDir); err != nil {
		return "", err
	}
	if err := t.Push(ctx, stagingDir, bin, bin+release.SigSuffix); err != nil {
		return "", err
	}
	return stagingDir + "/" + filepath.Base(bin), nil
}

// project starts one project's session. A live session started by deploy
// with this very argv is left alone (re-deploy is a no-op) unless
// -restart; any other live session is stopped first, since flags change
// only by stop and start (decision #19) and start refuses a live unit.
func (d deployer) project(ctx context.Context, t federate.Transport, host string, p federate.Project) error {
	argv := startArgs(p)
	out, err := t.Run(ctx, "systemctl", "show", "--property=ActiveState", "--value", "--",
		"iormesh-"+p.Name+".service")
	if err != nil {
		return err
	}
	live := liveState(strings.TrimSpace(string(out)))
	switch {
	case live && !d.restart && d.record.same(host, p.Name, argv):
		_, _ = fmt.Fprintf(d.e.stdout, "%s: project %s already running with these flags, left alone "+
			"(-restart restarts it)\n", host, p.Name)
	case live:
		if err := d.sudo(ctx, t, host, "stop", "-project", p.Name); err != nil {
			return fmt.Errorf("stop for restart: %w", err)
		}
		fallthrough
	default:
		if err := d.sudo(ctx, t, host, argv...); err != nil {
			return fmt.Errorf("start: %w", err)
		}
		d.record.set(host, p.Name, argv)
	}
	if err := d.sudo(ctx, t, host, "status", "-project", p.Name); err != nil {
		return fmt.Errorf("status: %w", err)
	}
	return nil
}

// sudo runs the installed tracer's subcommand through the sudoers rule and
// relays its output.
func (d deployer) sudo(ctx context.Context, t federate.Transport, host string, args ...string) error {
	out, err := t.Run(ctx, sudoTracer(args...)...)
	d.relay(host, out)
	return err
}

// relay prints a remote command's stdout, each line prefixed by the host
// so output from several hosts stays attributable.
func (d deployer) relay(host string, out []byte) {
	for line := range strings.Lines(string(out)) {
		_, _ = fmt.Fprintf(d.e.stdout, "%s: %s", host, line)
		if !strings.HasSuffix(line, "\n") {
			_, _ = fmt.Fprintln(d.e.stdout)
		}
	}
}

// sudoTracer is the argv running the installed tracer under sudo. -n:
// a missing or changed sudoers rule fails at once instead of prompting.
func sudoTracer(args ...string) []string {
	return append([]string{"sudo", "-n", installedTracer}, args...)
}

// startArgs is the tracer's start argv for a registry project: its label,
// its target, then its pass-through flags. The target is one
// -name=value element, so a pattern starting with "-" is never taken for
// a flag.
func startArgs(p federate.Project) []string {
	args := []string{"start", "-project", p.Name}
	if p.Target.Pattern != "" {
		args = append(args, "-pattern="+p.Target.Pattern)
	} else {
		args = append(args, "-listen-port="+strconv.Itoa(int(p.Target.ListenPort)))
	}
	return append(args, p.Flags...)
}

// liveState mirrors the tracer's unitState.live: a unit running or on its
// way up or down, which start would refuse.
func liveState(active string) bool {
	switch active {
	case "active", "activating", "deactivating", "reloading", "refreshing":
		return true
	}
	return false
}
