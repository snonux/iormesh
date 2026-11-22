package ctl

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"iormesh/internal/federate"
	"iormesh/internal/project"
	"iormesh/internal/termsafe"
)

// `iormeshctl rm [-project P] [-force] HOST` (PLAN.md M4 "Cleanup (rm)"):
// tear the deployment down, then unregister. rm never destroys an
// uncollected segment unless told to with -force:
//
//  1. true — reachability. An unreachable host is warned about and only
//     dropped from the registry: its traces stay on it, and an operator
//     must be able to forget a server that is gone for good;
//  2. test -x /opt/iormesh/bin/iormesh — a host without the tracer (exit
//     status 1) has nothing to stop, sync or uninstall; any other failure
//     is an error, never "not installed";
//  3. per removed project: stop its session if live (sudo iormesh stop,
//     which publishes the last segment and fails when the run did not
//     end cleanly), then collect it as collect does;
//  4. when the host goes (rm HOST, or its last project): every other
//     project directory under /opt/iormesh/traces — a project started by
//     hand, or dropped by an earlier rm while the host was unreachable —
//     is stopped and collected too, since uninstall would stop it and
//     delete what it publishes. Then nothing may be left: a published
//     segment or an .active-* file still there (an unclean stop, a name
//     that is no project label) refuses the uninstall, naming the files;
//  5. sudo iormesh uninstall, which stops every unit and removes
//     /opt/iormesh;
//  6. only then the registry and deploy's record are edited.
//
// Any failure in 2–5 stops rm with the registry untouched, so the
// operator can rerun it; a stopped session stays stopped, and what
// already transferred is in the store. -force turns an unclean stop and
// the leftovers of step 4 into warnings: the operator accepts losing them.

// maxListed bounds how many leftover files an error names.
const maxListed = 10

// remover is one rm run: a collector plus -force.
type remover struct {
	collector
	force bool
}

// rmCommand parses rm's flags, tears the deployment down and unregisters
// the host, or one project on it with -project. A pod entry reference
// takes rmPod's path instead (pods_rm.go).
func rmCommand(args []string, e env) error {
	fs := newFlagSet("rm", e.stderr)
	name := fs.String("project", "", "remove only this project (default: the whole host)")
	regPath := fs.String("registry", "", "registry file (default ~/.config/iormesh/servers.yaml)")
	store := fs.String("store", "", "local trace store (default ~/.local/share/iormesh/traces)")
	force := fs.Bool("force", false, "uninstall even if traces could not be synced off (they are lost)")
	kf := addK8sFlags(fs, false)
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("%w: rm takes exactly one HOST or pod entry", errUsage)
	}
	host := fs.Arg(0)
	if err := kf.refuse("rm"); err != nil {
		return err
	}
	if federate.IsPodRef(host) {
		return rmPod(host, rmPodOptions{regPath: *regPath, store: *store, project: *name, force: *force, kf: kf}, e)
	}
	projects, hostGoes, err := rmScope(*regPath, e, host, *name)
	if err != nil {
		return err
	}
	c, err := newCollector(e, *store)
	if err != nil {
		return err
	}
	r := remover{collector: c, force: *force}
	// Bounded like collect's hosts (env.onHost): a host stalled after
	// the probe fails the teardown and the registry stays as it was.
	err = r.e.onHost(r.e.root(), func(ctx context.Context) error {
		return r.teardown(ctx, host, projects, hostGoes)
	})
	if errors.Is(err, errUnreachable) {
		_, _ = fmt.Fprintf(e.stderr, "iormeshctl: warning: %s: %s; its traces and tracer stay on it, "+
			"dropping it from the registry only\n", host, errText(err))
	} else if err != nil {
		return fmt.Errorf("%s: %w (registry unchanged)", host, err)
	}
	return unregister(*regPath, e, host, *name)
}

// rmScope checks host (and project) are registered and returns the
// projects rm removes and whether the host leaves the registry with them.
// Nothing is contacted before this passes.
func rmScope(regPath string, e env, host, name string) ([]string, bool, error) {
	r, err := loadRegistry(regPath, e)
	if err != nil {
		return nil, false, err
	}
	srv, err := r.Lookup(host)
	if err != nil {
		return nil, false, err
	}
	if err := r.Remove(host, name); err != nil {
		return nil, false, err
	}
	if name != "" {
		_, err := r.Lookup(host)
		return []string{name}, errors.Is(err, federate.ErrUnknownHost), nil
	}
	var all []string
	for _, p := range srv.Projects {
		all = append(all, p.Name)
	}
	return all, true, nil
}

// teardown stops and collects projects on host and, when uninstall is
// set, every other project too, then uninstalls (see the steps above).
func (r remover) teardown(ctx context.Context, host string, projects []string, uninstall bool) error {
	t, err := r.e.transport(host)
	if err != nil {
		return err
	}
	if err := probe(ctx, t); err != nil {
		return err
	}
	installed, err := remoteTest(ctx, t, "test", "-x", installedTracer)
	if err != nil {
		return fmt.Errorf("checking for the tracer: %w", err)
	}
	if !installed {
		_, _ = fmt.Fprintf(r.e.stdout, "%s: no tracer installed, nothing to stop, sync or uninstall\n", host)
		return nil
	}
	if err := r.syncProjects(ctx, t, host, projects); err != nil {
		return err
	}
	if !uninstall {
		return nil
	}
	if err := r.sweep(ctx, t, host, projects); err != nil {
		return err
	}
	out, err := t.Run(ctx, sudoTracer("uninstall")...)
	relay(r.e.stdout, host, out)
	if err != nil {
		return fmt.Errorf("uninstall: %w", err)
	}
	return nil
}

// syncProjects stops and collects each project in turn.
func (r remover) syncProjects(ctx context.Context, t federate.Transport, host string, projects []string) error {
	for _, p := range projects {
		if err := r.stopProject(ctx, t, host, p); err != nil {
			return fmt.Errorf("project %s: %w", p, err)
		}
		if err := r.project(ctx, t, host, p); err != nil {
			return fmt.Errorf("project %s: sync before removal: %w", p, err)
		}
	}
	return nil
}

// stopProject stops the project's session if it is live, so its last
// segment is published before the sync (stop waits for that). A stop
// whose run did not end cleanly fails, unless -force.
func (r remover) stopProject(ctx context.Context, t federate.Transport, host, name string) error {
	live, err := unitLive(ctx, t, name)
	if err != nil || !live {
		return err
	}
	out, err := t.Run(ctx, sudoTracer("stop", "-project", name)...)
	relay(r.e.stdout, host, out)
	if err == nil {
		return nil
	}
	if r.force {
		_, _ = fmt.Fprintf(r.e.stderr, "iormeshctl: warning: %s: project %s: stop: %s; -force: going on\n",
			host, name, errText(err))
		return nil
	}
	return fmt.Errorf("stop: %w; its last segment may be unpublished: nothing uninstalled "+
		"(rm -force uninstalls anyway and loses it)", err)
}

// sweep handles what uninstall would delete beyond the registry's
// projects: it stops and collects every other project directory under
// the trace dir, then refuses (unless -force) while any published or
// active segment file is still there.
func (r remover) sweep(ctx context.Context, t federate.Transport, host string, done []string) error {
	present, err := remoteTest(ctx, t, "test", "-d", confinedTraceDir)
	if err != nil || !present {
		return err
	}
	out, err := t.Run(ctx, "find", confinedTraceDir, "-mindepth", "1", "-maxdepth", "1", "-type", "d", "-print")
	if err != nil {
		return fmt.Errorf("listing %s: %w", confinedTraceDir, err)
	}
	var others []string
	for _, dir := range lines(out) {
		name := strings.TrimPrefix(dir, confinedTraceDir+"/")
		// A name no label rule admits cannot be a local path component;
		// its files show up as leftovers below.
		if project.Valid(name) && !slices.Contains(done, name) {
			others = append(others, name)
		}
	}
	if err := r.syncProjects(ctx, t, host, others); err != nil {
		return err
	}
	return r.leftovers(ctx, t, host)
}

// leftovers lists the segment files still under the trace dir — in a
// ready/ or active — and refuses the uninstall over them unless -force.
func (r remover) leftovers(ctx context.Context, t federate.Transport, host string) error {
	out, err := t.Run(ctx, "find", confinedTraceDir, "-mindepth", "2", "-type", "f",
		"(", "-path", "*/"+readyDir+"/*", "-o", "-name", ".active-*", ")", "-print")
	if err != nil {
		return fmt.Errorf("listing %s: %w", confinedTraceDir, err)
	}
	left := lines(out)
	if len(left) == 0 {
		return nil
	}
	shown := left[:min(len(left), maxListed)]
	msg := fmt.Sprintf("%d trace files could not be synced off: %s", len(left), strings.Join(shown, " "))
	if len(left) > len(shown) {
		msg += " …"
	}
	if r.force {
		_, _ = fmt.Fprintf(r.e.stderr, "iormeshctl: warning: %s: %s; -force: uninstalling, they are lost\n",
			host, termsafe.String(msg))
		return nil
	}
	return fmt.Errorf("%s; nothing uninstalled (rm -force uninstalls anyway and loses them)", msg)
}

// lines splits a remote command's output into its non-empty lines.
func lines(out []byte) []string {
	var ls []string
	for l := range strings.Lines(string(out)) {
		if l = strings.TrimRight(l, "\n"); l != "" {
			ls = append(ls, l)
		}
	}
	return ls
}

// unregister drops host, or its project name, from the registry and from
// deploy's record.
func unregister(regPath string, e env, host, name string) error {
	what := "removed " + host
	if name != "" {
		what += " project " + name
	}
	if err := editRegistry(regPath, e, func(r *federate.Registry) error {
		return r.Remove(host, name)
	}, what); err != nil {
		return err
	}
	return forgetDeployed(e, host, name)
}

// forgetDeployed drops what rm removed from deploy's record too, so a
// host or project registered again later starts afresh. The file is
// rewritten only when it held the entry.
func forgetDeployed(e env, host, name string) error {
	path, err := e.deployed()
	if err != nil {
		return err
	}
	r, err := loadDeployed(path)
	if err != nil {
		return err
	}
	if !r.forget(host, name) {
		return nil
	}
	return r.save(path)
}
