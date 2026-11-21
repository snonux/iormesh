package ctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"iormesh/internal/viewmodel"
)

// `iormeshctl refresh` and `iormeshctl show` (PLAN.md M4 "Three
// commands, two jobs"; docs/visualization.md "Plain text").

// refreshCommand is collect, then build — the everyday command. A
// collect that fails on some host does not stop the build: what reached
// the store is built, and the command still exits 1 naming the host
// (collect's own report), so a flaky server never hides the rest of the
// mesh. HOST arguments narrow the collect only; the build reads every
// host directory of the projects in the store.
func refreshCommand(args []string, e env) error {
	fs := newFlagSet("refresh", e.stderr)
	var f buildFlags
	addBuildFlags(fs, &f)
	addRoutesFlag(fs, &f)
	kf := addK8sFlags(fs, false)
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	// -registry (addRoutesFlag) serves both halves: the collect's targets
	// and the build's route map and snapshot check.
	r, err := loadRegistry(f.registry, e)
	if err != nil {
		return err
	}
	targets, err := collectTargets(r, fs.Args(), f.projects, kf)
	if err != nil {
		return err
	}
	c, err := newCollector(e, f.store)
	if err != nil {
		return err
	}
	// The build reads its pins beside -db: a pod's pin goes there too.
	c.db = f.db
	ctx := context.Background()
	collectErr := c.all(ctx, targets)
	if collectErr != nil {
		_, _ = fmt.Fprintf(e.stderr, "iormeshctl: %s; building what the store has\n", errText(collectErr))
	}
	return errors.Join(collectErr, buildAndReport(ctx, e, f))
}

// showViews are show's views; summary is the default.
var showViews = []string{"summary", "services", "edges", "health", "conversations", "sequence", "thread",
	"processes", "report"}

// showCommand prints one view of the last build's model (model.json):
// the summary build printed, services, edges, health, the
// conversations, or one conversation's sequence (`show sequence [ID]`,
// the newest without an ID), the thread stories or one thread's (`show
// thread [SERVICE/THREAD]`), the processes; or the report view, the summary followed by
// the per-run rollup (report.json); -json the JSON for that view. It
// reads only the build output, never the store's segments or the
// network.
func showCommand(args []string, e env) error {
	view, id := "summary", ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		view, args = args[0], args[1:]
	}
	if (view == "sequence" || view == "thread") && len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		id, args = args[0], args[1:]
	}
	fs := newFlagSet("show "+view, e.stderr)
	var f buildFlags
	addBuildFlags(fs, &f)
	asJSON := fs.Bool("json", false, "print the view model's JSON for the view")
	unresolved := fs.Bool("unresolved", false, "edges: only edges with an unresolved connection")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%w: show takes one view: %s", errUsage, strings.Join(showViews, ", "))
	}
	m, err := loadModel(e, f)
	if err != nil {
		return err
	}
	if view == "report" {
		return showReport(e, f, m, *asJSON)
	}
	if *asJSON {
		return writeViewJSON(e, view, id, m)
	}
	return writeView(e, view, id, m, *unresolved)
}

// loadModel reads the model.json of the build of f's projects.
func loadModel(e env, f buildFlags) (viewmodel.Model, error) {
	p, err := resolveShowPaths(e, f)
	if err != nil {
		return viewmodel.Model{}, err
	}
	projects, err := buildProjects(p.store, f.projects)
	if err != nil {
		return viewmodel.Model{}, err
	}
	path := filepath.Join(p.builds, buildKey(projects), modelFileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return viewmodel.Model{}, fmt.Errorf("show: no build of %s yet (iormeshctl build)",
			strings.Join(projects, "+"))
	}
	if err != nil {
		return viewmodel.Model{}, fmt.Errorf("show: %w", err)
	}
	var m viewmodel.Model
	if err := json.Unmarshal(data, &m); err != nil {
		return viewmodel.Model{}, fmt.Errorf("show: %s: %w", path, err)
	}
	return m, nil
}

// resolveShowPaths is resolveBuildPaths without the store check: show
// reads the store's directory names only when no -project is given.
func resolveShowPaths(e env, f buildFlags) (buildPaths, error) {
	p := buildPaths{store: f.store, builds: f.builds}
	var err error
	if p.store == "" && len(f.projects) == 0 {
		if p.store, err = e.traces(); err != nil {
			return buildPaths{}, err
		}
	}
	if p.builds == "" {
		if p.builds, err = e.builds(); err != nil {
			return buildPaths{}, err
		}
	}
	return p, nil
}

// writeView renders a view as text; id picks sequence's conversation
// or thread's SERVICE/THREAD.
func writeView(e env, view, id string, m viewmodel.Model, unresolved bool) error {
	switch view {
	case "thread":
		if id == "" {
			return viewmodel.WriteThreadList(e.stdout, m)
		}
		ts, err := viewmodel.FindThreads(m, id)
		if err != nil {
			return fmt.Errorf("show: %w", err)
		}
		return viewmodel.WriteThreads(e.stdout, ts)
	case "processes":
		return viewmodel.WriteProcesses(e.stdout, m)
	case "conversations":
		return viewmodel.WriteConversations(e.stdout, m)
	case "sequence":
		c, err := viewmodel.FindConversation(m, id)
		if err != nil {
			return fmt.Errorf("show: %w", err)
		}
		return viewmodel.WriteSequence(e.stdout, c)
	case "summary":
		return viewmodel.WriteSummary(e.stdout, m)
	case "services":
		return viewmodel.WriteServices(e.stdout, m)
	case "edges":
		return viewmodel.WriteEdges(e.stdout, m, unresolved)
	case "health":
		return viewmodel.WriteHealth(e.stdout, m)
	}
	return fmt.Errorf("%w: unknown view %q (%s)", errUsage, view, strings.Join(showViews, ", "))
}

// writeViewJSON prints the part of the model a view shows.
func writeViewJSON(e env, view, id string, m viewmodel.Model) error {
	var v any
	switch view {
	case "thread":
		v = m.Threads
		if id != "" {
			ts, err := viewmodel.FindThreads(m, id)
			if err != nil {
				return fmt.Errorf("show: %w", err)
			}
			v = ts
		}
	case "processes":
		v = m.Processes
	case "conversations":
		v = m.Conversations
	case "sequence":
		c, err := viewmodel.FindConversation(m, id)
		if err != nil {
			return fmt.Errorf("show: %w", err)
		}
		v = c
	case "summary":
		v = m
	case "services":
		v = m.Services
	case "edges":
		v = m.Edges
	case "health":
		v = m.Health
	default:
		return fmt.Errorf("%w: unknown view %q (%s)", errUsage, view, strings.Join(showViews, ", "))
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(e.stdout, "%s\n", data)
	return err
}
