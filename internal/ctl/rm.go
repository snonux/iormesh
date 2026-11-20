package ctl

import (
	"context"
	"errors"
	"fmt"

	"iormesh/internal/federate"
)

// `iormeshctl rm [-project P] HOST` (PLAN.md M4 "Cleanup (rm)"): tear the
// deployment down, then unregister. The traces are synced off first, so
// rm never destroys an uncollected segment:
//
//  1. true — reachability. An unreachable host is warned about and only
//     dropped from the registry: its traces stay on it, and an operator
//     must be able to forget a server that is gone for good;
//  2. test -x /opt/iormesh/bin/iormesh — a host without the tracer has
//     nothing to stop, sync or uninstall;
//  3. per removed project: stop its session if live (sudo iormesh stop,
//     which publishes the last segment), then collect it as collect does;
//  4. when the host goes (rm HOST, or its last project): sudo iormesh
//     uninstall, which stops every unit and removes /opt/iormesh — with
//     it the traces of any project the registry does not list, which rm
//     cannot know to sync (only registry names become local paths);
//  5. only then the registry and deploy's record are edited.
//
// Any failure in 2–4 stops rm with the registry untouched, so the
// operator can rerun it; a stopped session stays stopped, and what
// already transferred is in the store.

// rmCommand parses rm's flags, tears the deployment down and unregisters
// the host, or one project on it with -project.
func rmCommand(args []string, e env) error {
	fs := newFlagSet("rm", e.stderr)
	name := fs.String("project", "", "remove only this project (default: the whole host)")
	regPath := fs.String("registry", "", "registry file (default ~/.config/iormesh/servers.yaml)")
	store := fs.String("store", "", "local trace store (default ~/.local/share/iormesh/traces)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("%w: rm takes exactly one HOST", errUsage)
	}
	host := fs.Arg(0)
	projects, hostGoes, err := rmScope(*regPath, e, host, *name)
	if err != nil {
		return err
	}
	c, err := newCollector(e, *store)
	if err != nil {
		return err
	}
	err = c.teardown(context.Background(), host, projects, hostGoes)
	if errors.Is(err, errUnreachable) {
		_, _ = fmt.Fprintf(e.stderr, "iormeshctl: warning: %s: %v; its traces and tracer stay on it, "+
			"dropping it from the registry only\n", host, err)
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

// teardown stops and collects projects on host and uninstalls the tracer
// when uninstall is set (see the steps above).
func (c collector) teardown(ctx context.Context, host string, projects []string, uninstall bool) error {
	t, err := c.e.transport(host)
	if err != nil {
		return err
	}
	if err := probe(ctx, t); err != nil {
		return err
	}
	if _, err := t.Run(ctx, "test", "-x", installedTracer); err != nil {
		_, _ = fmt.Fprintf(c.e.stdout, "%s: no tracer installed, nothing to stop, sync or uninstall\n", host)
		return nil
	}
	for _, p := range projects {
		if err := c.stopProject(ctx, t, host, p); err != nil {
			return fmt.Errorf("project %s: %w", p, err)
		}
		if err := c.project(ctx, t, host, p); err != nil {
			return fmt.Errorf("project %s: sync before removal: %w", p, err)
		}
	}
	if !uninstall {
		return nil
	}
	out, err := t.Run(ctx, sudoTracer("uninstall")...)
	relay(c.e.stdout, host, out)
	if err != nil {
		return fmt.Errorf("uninstall: %w", err)
	}
	return nil
}

// stopProject stops the project's session if it is live, so its last
// segment is published before the sync (stop waits for that).
func (c collector) stopProject(ctx context.Context, t federate.Transport, host, name string) error {
	live, err := unitLive(ctx, t, name)
	if err != nil || !live {
		return err
	}
	out, err := t.Run(ctx, sudoTracer("stop", "-project", name)...)
	relay(c.e.stdout, host, out)
	if err != nil {
		return fmt.Errorf("stop: %w", err)
	}
	return nil
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
func forgetDeployed(e env, host, project string) error {
	path, err := e.deployed()
	if err != nil {
		return err
	}
	r, err := loadDeployed(path)
	if err != nil {
		return err
	}
	if !r.forget(host, project) {
		return nil
	}
	return r.save(path)
}
