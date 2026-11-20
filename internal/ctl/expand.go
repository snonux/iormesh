package ctl

import (
	"context"
	"encoding/json"
	"fmt"

	"iormesh/internal/expand"
	"iormesh/internal/federate"
)

// `iormeshctl expand` (PLAN.md M6 "Discover"): list the unresolved peers
// of the store's merged mesh, which of them the next round would enroll
// and why each other one stays put. Listing is all it does — it reads the
// store and the registry, never a server, and edits nothing; enrolling
// is expand --apply's job (task tb4).
//
// It merges the store itself rather than reading the last build's
// outputs: the plan needs every unresolved half's 4-tuple, which the
// view model rightly does not carry, and a merge of the same segments
// gives the same halves, so the listing always matches what build
// would see now.

// expandFlags are expand's flags.
type expandFlags struct {
	build    buildFlags
	registry string
	hops     int
	asJSON   bool
}

// expandCommand parses expand's flags, plans one round and prints it.
func expandCommand(args []string, e env) error {
	fs := newFlagSet("expand", e.stderr)
	var f expandFlags
	fs.Var(&f.build.projects, "project", "merge only this project (repeatable; default: every project in the store)")
	fs.StringVar(&f.build.store, "store", "", "local trace store (default ~/.local/share/iormesh/traces)")
	fs.StringVar(&f.build.db, "db", "", "service database; host pins live next to it "+
		"(default ~/.local/share/iormesh/services.db)")
	fs.StringVar(&f.registry, "registry", "", "registry file (default ~/.config/iormesh/servers.yaml)")
	fs.IntVar(&f.hops, "hops", expand.DefaultMaxHops, "hop limit: rounds away from the hosts enrolled by hand")
	fs.BoolVar(&f.asJSON, "json", false, "print the plan as JSON")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%w: expand takes no arguments", errUsage)
	}
	if f.hops < 0 {
		return fmt.Errorf("%w: -hops %d is negative", errUsage, f.hops)
	}
	p, err := planExpansion(context.Background(), e, f)
	if err != nil {
		return err
	}
	if f.asJSON {
		data, err := json.MarshalIndent(p, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(e.stdout, "%s\n", data)
		return err
	}
	return expand.WriteText(e.stdout, p)
}

// planExpansion merges the store's projects and plans one round against
// the registry. Segments go through the same checks as a build (the
// memory-capped child, the pinned host identities), since they are the
// same untrusted files.
func planExpansion(ctx context.Context, e env, f expandFlags) (expand.Proposal, error) {
	reg, err := loadRegistry(f.registry, e)
	if err != nil {
		return expand.Proposal{}, err
	}
	allow, err := expand.NewAllowlist(reg.Expand)
	if err != nil {
		return expand.Proposal{}, err
	}
	paths, err := resolveBuildPaths(e, f.build)
	if err != nil {
		return expand.Proposal{}, err
	}
	projects, err := buildProjects(paths.store, f.build.projects)
	if err != nil {
		return expand.Proposal{}, err
	}
	segs, err := loadSegments(ctx, paths, projects)
	if err != nil {
		return expand.Proposal{}, err
	}
	sources := make([]federate.Source, len(segs.Sources))
	for i, s := range segs.Sources {
		sources[i] = s
	}
	res, err := federate.Merge(ctx, sources, federate.Options{})
	if err != nil {
		return expand.Proposal{}, err
	}
	in := expand.Input{Merge: res, HostDirs: hostDirs(segs.Hosts), Registry: reg}
	return expand.Plan(in, allow, expand.NewLimits(reg.Expand, f.hops)), nil
}

// hostDirs inverts the store's directory → identity map: the merge names
// a half's host by host_id, the registry by the directory collect wrote.
// LoadSegments holds each host_id to one directory, so the inverse is a
// function.
func hostDirs(ids map[string]federate.HostIdentity) map[string]string {
	out := make(map[string]string, len(ids))
	for dir, id := range ids {
		out[id.HostID] = dir
	}
	return out
}
