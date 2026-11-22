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
//     must be able to forget a server that is gone for good. Deploy's
//     record keeps the dropped projects as tombstones, so a later rm of
//     the host still collects them in step 4;
//  2. test -x /opt/iormesh/bin/iormesh — a host without the tracer (exit
//     status 1) has nothing to stop, sync or uninstall; any other failure
//     is an error, never "not installed";
//  3. per removed project: stop its session if live (sudo iormesh stop,
//     which publishes the last segment and fails when the run did not
//     end cleanly), then collect it as collect does;
//  4. when the host goes (rm HOST, or its last project): every other
//     project directory under /opt/iormesh/traces that the registry or
//     deploy's record (tombstones included) lists for the host is
//     stopped and collected too, since uninstall would stop it and
//     delete what it publishes. A directory neither lists (started by
//     hand, or planted by a compromised server) is not collected: the store's project comes
//     from the controller, never from the server, or a server could put
//     segments into another project's store. Then nothing may be left: a
//     published segment or an .active-* file still there (an unclean
//     stop, an unlisted project, a name that is no project label) refuses
//     the uninstall, naming the files;
//  5. sudo iormesh uninstall, which stops every unit, removes
//     /opt/iormesh and then the sudoers rule iormeshctl bootstrap
//     installed (its marker line; any other rule stays, task qh4);
//  6. only then the registry and deploy's record are edited.
//
// Any failure in 2–5 stops rm with the registry untouched, so the
// operator can rerun it; a stopped session stays stopped, and what
// already transferred is in the store. -force turns an unclean stop and
// the leftovers of step 4 into warnings: the operator accepts losing them.

// maxListed bounds how many leftover files an error names.
const maxListed = 10

// remover is one rm run: a collector plus -force and the projects the
// sweep may collect.
type remover struct {
	collector
	force bool
	// known is every project the registry or deploy's record lists for
	// the host: the only names the sweep collects into the store.
	known []string
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
		return fmt.Errorf("%w: %w", errUsage, err)
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
	sc, err := rmScope(*regPath, e, host, *name)
	if err != nil {
		return err
	}
	c, err := newCollector(e, *store)
	if err != nil {
		return err
	}
	r := remover{collector: c, force: *force, known: sc.known}
	// Bounded like collect's hosts (env.onHost): a host stalled after
	// the probe fails the teardown and the registry stays as it was.
	err = r.e.onHost(r.e.root(), func(ctx context.Context) error {
		return r.teardown(ctx, host, sc.projects, sc.hostGoes)
	})
	if errors.Is(err, errUnreachable) {
		return unregisterUnreachable(*regPath, e, host, *name, sc, err)
	} else if err != nil {
		return fmt.Errorf("%s: %w (registry unchanged)", host, err)
	}
	return unregister(*regPath, e, host, *name)
}

// unregisterUnreachable drops what rm removes from the registry when the
// host cannot be reached, warning that its tracer and traces stay on it.
// Deploy's record keeps the removed projects as tombstones, so a later rm
// of the host (still registered, or added again) collects them as its own
// rather than refusing over them as foreign directories.
func unregisterUnreachable(regPath string, e env, host, name string, sc rmTarget, cause error) error {
	what, later := "it", "add the host again and rm it to collect them"
	if name != "" {
		what = "project " + name
		if !sc.hostGoes {
			later = "a later rm " + host + " still collects it"
		}
	}
	_, _ = fmt.Fprintf(e.stderr, "iormeshctl: warning: %s: %s; its traces and tracer stay on it, "+
		"dropping %s from the registry only; %s\n", safe(host), errText(cause), safe(what),
		safe(later))
	if err := editRegistry(regPath, e, func(r *federate.Registry) error {
		return r.Remove(host, name)
	}, "removed "+removedWhat(host, name)); err != nil {
		return err
	}
	return editDeployed(e, func(r deployedRecord) bool { return r.drop(host, sc.projects) })
}

// removedWhat names what rm removed in the registry's edit message.
func removedWhat(host, name string) string {
	if name == "" {
		return host
	}
	return host + " project " + name
}

// rmTarget is what one rm removes, as rmScope works it out.
type rmTarget struct {
	// projects are the projects rm removes.
	projects []string
	// hostGoes is set when the host leaves the registry with them.
	hostGoes bool
	// known is every project the registry or deploy's record lists for
	// the host (see remover.known).
	known []string
}

// rmScope checks host (and project) are registered and returns what rm
// removes. Nothing is contacted before this passes.
func rmScope(regPath string, e env, host, name string) (rmTarget, error) {
	r, err := loadRegistry(regPath, e)
	if err != nil {
		return rmTarget{}, err
	}
	srv, err := r.Lookup(host)
	if err != nil {
		return rmTarget{}, err
	}
	var all []string
	for _, p := range srv.Projects {
		all = append(all, p.Name)
	}
	known, err := knownProjects(e, host, all)
	if err != nil {
		return rmTarget{}, err
	}
	if err := r.Remove(host, name); err != nil {
		return rmTarget{}, err
	}
	if name != "" {
		_, err := r.Lookup(host)
		return rmTarget{projects: []string{name}, hostGoes: errors.Is(err, federate.ErrUnknownHost),
			known: known}, nil
	}
	return rmTarget{projects: all, hostGoes: true, known: known}, nil
}

// knownProjects is listed (the registry's projects of host) plus every
// project deploy's record still holds for it: deploy started those, so
// their directories on the host are the controller's to collect.
func knownProjects(e env, host string, listed []string) ([]string, error) {
	path, err := e.deployed()
	if err != nil {
		return nil, err
	}
	rec, err := loadDeployed(path)
	if err != nil {
		return nil, err
	}
	known := slices.Clone(listed)
	for p := range rec[host] {
		if !slices.Contains(known, p) {
			known = append(known, p)
		}
	}
	return known, nil
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
		_, _ = fmt.Fprintf(r.e.stdout, "%s: no tracer installed, nothing to stop, sync or uninstall\n", safe(host))
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
			safe(host), safe(name), errText(err))
		return nil
	}
	return fmt.Errorf("stop: %w; its last segment may be unpublished: nothing uninstalled "+
		"(rm -force uninstalls anyway and loses it)", err)
}

// sweep handles what uninstall would delete beyond the projects rm
// removed: it stops and collects every other project directory under the
// trace dir that the registry or deploy's record lists for the host, then
// refuses (unless -force) while any published or active segment file is
// still there. The directory names are the server's: one not in r.known
// is never collected into the store, so its files count as leftovers.
func (r remover) sweep(ctx context.Context, t federate.Transport, host string, done []string) error {
	present, err := remoteTest(ctx, t, "test", "-d", confinedTraceDir)
	if err != nil || !present {
		return err
	}
	out, err := t.Run(ctx, "find", confinedTraceDir, "-mindepth", "1", "-maxdepth", "1", "-type", "d", "-print")
	if err != nil {
		return fmt.Errorf("listing %s: %w", confinedTraceDir, err)
	}
	var others, skipped []string
	for _, dir := range lines(out) {
		name := strings.TrimPrefix(dir, confinedTraceDir+"/")
		// A name no label rule admits cannot be a local path component,
		// and a project the controller never listed for this host is not
		// the server's to choose; the files of either show up as
		// leftovers below, the unlisted project named there.
		if !project.Valid(name) || slices.Contains(done, name) {
			continue
		}
		if slices.Contains(r.known, name) {
			others = append(others, name)
		} else {
			skipped = append(skipped, name)
		}
	}
	if err := r.syncProjects(ctx, t, host, others); err != nil {
		return err
	}
	return r.leftovers(ctx, t, host, skipped)
}

// leftovers lists the segment files still under the trace dir — in a
// ready/ or active — and refuses the uninstall over them unless -force.
// The refusal names the project directories of skipped (the sweep's
// unlisted projects) that hold such files, so the operator can tell them
// from a failed sync.
func (r remover) leftovers(ctx context.Context, t federate.Transport, host string, skipped []string) error {
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
	if names := holding(skipped, left); len(names) > 0 {
		msg += fmt.Sprintf("; project dirs not registered for this host, not collected: %s "+
			"(re-add them to collect them)", strings.Join(names, " "))
	}
	if r.force {
		_, _ = fmt.Fprintf(r.e.stderr, "iormeshctl: warning: %s: %s; -force: uninstalling, they are lost\n",
			safe(host), termsafe.String(msg))
		return nil
	}
	return fmt.Errorf("%s; nothing uninstalled (rm -force uninstalls anyway and loses them)", msg)
}

// holding is the projects whose directory under the trace dir holds one
// of the files.
func holding(projects, files []string) []string {
	var hold []string
	for _, p := range projects {
		prefix := confinedTraceDir + "/" + p + "/"
		if slices.ContainsFunc(files, func(f string) bool { return strings.HasPrefix(f, prefix) }) {
			hold = append(hold, p)
		}
	}
	return hold
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
	if err := editRegistry(regPath, e, func(r *federate.Registry) error {
		return r.Remove(host, name)
	}, "removed "+removedWhat(host, name)); err != nil {
		return err
	}
	// Forgetting the host drops its tombstones too: the teardown just
	// collected and uninstalled everything on it.
	return editDeployed(e, func(r deployedRecord) bool { return r.forget(host, name) })
}

// editDeployed applies change to deploy's record, so a host or project
// registered again later starts afresh. The file is rewritten only when
// change reports it changed something.
func editDeployed(e env, change func(deployedRecord) bool) error {
	path, err := e.deployed()
	if err != nil {
		return err
	}
	r, err := loadDeployed(path)
	if err != nil {
		return err
	}
	if !change(r) {
		return nil
	}
	return r.save(path)
}
