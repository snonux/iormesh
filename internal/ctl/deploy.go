package ctl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"regexp"
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
//     or build changed (decision #19; see deployedRecord), then print its
//     status.
//
// Only the installed binary ever runs under sudo, through the one sudoers
// rule, and only with the subcommands it lets through (upgrade, start,
// stop, status); never -allow-downgrade, which is root's alone.

const (
	// installedTracer is the binary the sudoers rule names.
	installedTracer = "/opt/iormesh/bin/iormesh"
	// stagingRel is the staging directory under the login user's home on
	// the server: private to the user and not shared like /tmp, where
	// another user could pre-create the name.
	stagingRel = ".iormesh/staging"
	// defaultDist is where `mage build` and `mage sign` leave the builds.
	defaultDist = "dist"
	// The paths the tracer pins confined sessions to (internal/cli
	// confine.go); a registry flag naming another is refused there.
	confinedTraceDir  = "/opt/iormesh/traces"
	confinedSpoolRoot = "/run/iormesh/spool"
)

// unameArch maps `uname -m` to the Go arch of the builds iormesh ships.
var unameArch = map[string]string{
	"x86_64": "amd64", "amd64": "amd64",
	"aarch64": "arm64", "arm64": "arm64",
}

// refusedFlags are run flags a session started through the sudoers rule
// may not carry at all (internal/cli confine.go): each creates, truncates
// or removes whatever file it names.
var refusedFlags = map[string]bool{"out": true, "cpuprofile": true, "ready": true}

// homePath is what deploy accepts as the login user's home: an absolute
// path Push accepts too, so the staged path is the same string everywhere.
var homePath = regexp.MustCompile(`^/[A-Za-z0-9._/-]*$`)

// deployer is one deploy run over several hosts.
type deployer struct {
	e       env
	key     minisign.PublicKey
	dist    string
	restart bool
	record  deployedRecord
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
	restart := fs.Bool("restart", false, "restart a running session even if its flags and build are unchanged")
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
	d := deployer{e: e, key: key, dist: *dist, restart: *restart, record: record}
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
// set. Any unregistered host, unknown project or entry whose flags the
// tracer would refuse fails the whole command before a single host is
// contacted (principle #3). A host named twice is deployed once.
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
		if slices.ContainsFunc(servers, func(s federate.Server) bool { return s.Host == host }) {
			continue
		}
		srv, err := selectProjects(r, host, project)
		if err != nil {
			return nil, err
		}
		servers = append(servers, srv)
	}
	return servers, nil
}

// selectProjects is host's registry entry, narrowed to project when set,
// with every remaining project's flags checked.
func selectProjects(r federate.Registry, host, project string) (federate.Server, error) {
	srv, err := r.Lookup(host)
	if err != nil {
		return federate.Server{}, fmt.Errorf("%w (iormeshctl add it first)", err)
	}
	if project != "" {
		i := slices.IndexFunc(srv.Projects, func(p federate.Project) bool { return p.Name == project })
		if i < 0 {
			return federate.Server{}, fmt.Errorf("%w: %q on %s", federate.ErrUnknownProject, project, host)
		}
		srv.Projects = []federate.Project{srv.Projects[i]}
	}
	for _, p := range srv.Projects {
		if err := checkDeployFlags(p); err != nil {
			return federate.Server{}, fmt.Errorf("registry entry %s project %s: %w", host, p.Name, err)
		}
	}
	return srv, nil
}

// checkDeployFlags refuses, up front, pass-through flags the start would
// refuse on the server: the ones the entry itself owns (ownedFlags; a
// hand-edited registry may carry them), the ones a confined session may
// not use at all, and -trace-dir/-spool naming any but the pinned path.
// Failing here names the registry entry and contacts no host.
func checkDeployFlags(p federate.Project) error {
	if f := firstOwnedFlag(p.Flags); f != "" {
		return fmt.Errorf("flag %s: the entry sets it itself (iormeshctl add)", f)
	}
	pinned := map[string]string{"trace-dir": confinedTraceDir, "spool": confinedSpoolRoot + "/" + p.Name}
	for i := 0; i < len(p.Flags); i++ {
		f := p.Flags[i]
		if !strings.HasPrefix(f, "-") {
			continue
		}
		key, val, hasVal := strings.Cut(strings.TrimLeft(f, "-"), "=")
		if refusedFlags[key] {
			return fmt.Errorf("flag %s: not allowed through the sudoers rule", f)
		}
		want, ok := pinned[key]
		if !ok {
			continue
		}
		if !hasVal && i+1 < len(p.Flags) {
			i++
			val = p.Flags[i]
		}
		if path.Clean(val) != want {
			return fmt.Errorf("flag -%s %s: a deployed session may only use %s", key, val, want)
		}
	}
	return nil
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
	bin, version, err := d.verify(arch)
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
		if err := d.project(ctx, t, srv.Host, p, version); err != nil {
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
// returns its path and signed version. It runs for every host, right
// before that host's push, so a file replaced in dist/ during a long
// multi-host deploy is caught at the next host, not trusted from the
// first check. VerifyFile also requires the signed name to be the file's
// own, so another arch's build renamed into place fails here. The server
// verifies the bytes it receives again, so a file changed between this
// check and the push is refused there.
func (d deployer) verify(arch string) (string, string, error) {
	path := filepath.Join(d.dist, release.TracerFile(arch))
	m, err := release.VerifyFile(d.key, path)
	if err != nil {
		return "", "", fmt.Errorf("local build, nothing pushed: %w", err)
	}
	_, _ = fmt.Fprintf(d.e.stdout, "verified %s: %s %s\n", path, m.File, m.Version)
	return path, m.Version, nil
}

// stage pushes the build and its signature into the staging directory and
// returns the staged binary's absolute path. Absolute, because the path
// is handed to sudo: a relative one would depend on sudo keeping the
// working directory, which a `Defaults runcwd` in sudoers changes.
func stage(ctx context.Context, t federate.Transport, bin string) (string, error) {
	// Every ssh command starts in the login user's home; pwd names it.
	out, err := t.Run(ctx, "pwd")
	if err != nil {
		return "", err
	}
	home := strings.TrimSpace(string(out))
	if !homePath.MatchString(home) || strings.Contains(home, "..") {
		return "", fmt.Errorf("home directory %q: want an absolute path of [A-Za-z0-9._/-]", home)
	}
	dir := path.Join(home, stagingRel)
	// -m applies to the last component only; staging/ is the directory
	// the files land in, so that one is kept private.
	if _, err := t.Run(ctx, "mkdir", "-p", "-m", "0700", dir); err != nil {
		return "", err
	}
	if err := t.Push(ctx, dir, bin, bin+release.SigSuffix); err != nil {
		return "", err
	}
	return dir + "/" + filepath.Base(bin), nil
}

// project starts one project's session. A live session deploy started
// with this very argv on this very build is left alone (re-deploy is a
// no-op) unless -restart; any other live session is stopped first, since
// flags change only by stop and start (decision #19), start refuses a
// live unit, and a running session keeps the binary it was started from —
// after a fixed build is deployed, the old one must not keep running.
func (d deployer) project(ctx context.Context, t federate.Transport, host string, p federate.Project,
	version string) error {
	argv := startArgs(p)
	live, err := unitLive(ctx, t, p.Name)
	if err != nil {
		return err
	}
	if live && !d.restart && d.record.same(host, p.Name, argv, version) {
		_, _ = fmt.Fprintf(d.e.stdout, "%s: project %s already running this build with these flags, "+
			"left alone (-restart restarts it)\n", host, p.Name)
	} else {
		if err := d.start(ctx, t, host, argv, live); err != nil {
			return err
		}
		d.record.set(host, p.Name, argv, version)
	}
	if err := d.sudo(ctx, t, host, "status", "-project", p.Name); err != nil {
		return fmt.Errorf("status: %w", err)
	}
	return nil
}

// start starts the session argv describes, stopping the live one first.
// A start that fails after a successful stop says so: the project then
// has no session at all, which the operator must know.
func (d deployer) start(ctx context.Context, t federate.Transport, host string, argv []string, live bool) error {
	project := argv[2]
	if live {
		if err := d.sudo(ctx, t, host, "stop", "-project", project); err != nil {
			return fmt.Errorf("stop for restart, session left running: %w", err)
		}
	}
	if err := d.sudo(ctx, t, host, argv...); err != nil {
		if live {
			return fmt.Errorf("session stopped, not restarted: start: %w", err)
		}
		return fmt.Errorf("start: %w", err)
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

// relay prints a remote command's stdout on deploy's stdout.
func (d deployer) relay(host string, out []byte) {
	relay(d.e.stdout, host, out)
}

// relay prints a remote command's stdout to w, each line prefixed by the
// host so output from several hosts stays attributable.
func relay(w io.Writer, host string, out []byte) {
	for line := range strings.Lines(string(out)) {
		_, _ = fmt.Fprintf(w, "%s: %s", host, line)
		if !strings.HasSuffix(line, "\n") {
			_, _ = fmt.Fprintln(w)
		}
	}
}

// sudoTracer is the argv running the installed tracer under sudo. -n:
// a missing or changed sudoers rule fails at once instead of prompting.
func sudoTracer(args ...string) []string {
	return append([]string{"sudo", "-n", installedTracer}, args...)
}

// startArgs is the tracer's start argv for a registry project: its label
// (always argv[2], start relies on it), its target, then its pass-through
// flags. The target is one -name=value element, so a pattern starting
// with "-" is never taken for a flag.
func startArgs(p federate.Project) []string {
	args := []string{"start", "-project", p.Name}
	if p.Target.Pattern != "" {
		args = append(args, "-pattern="+p.Target.Pattern)
	} else {
		args = append(args, "-listen-port="+strconv.Itoa(int(p.Target.ListenPort)))
	}
	return append(args, p.Flags...)
}

// unitLive asks systemd, unprivileged, whether project's unit is live:
// deploy restarts a live session, rm stops it before the last sync.
func unitLive(ctx context.Context, t federate.Transport, project string) (bool, error) {
	out, err := t.Run(ctx, "systemctl", "show", "--property=ActiveState", "--value", "--",
		"iormesh-"+project+".service")
	if err != nil {
		return false, err
	}
	return liveState(strings.TrimSpace(string(out))), nil
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
