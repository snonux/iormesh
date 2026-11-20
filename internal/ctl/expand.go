package ctl

import (
	"context"
	"encoding/json"
	"fmt"

	"iormesh/internal/expand"
	"iormesh/internal/federate"
	"iormesh/internal/termsafe"
)

// `iormeshctl expand` (PLAN.md M6 "Discover"): list the unresolved peers
// of the store's merged mesh, which of them the next round would enroll
// and why each other one stays put. Listing is all it does — it reads the
// store, the host pins and the registry, never a server, and writes
// nothing (not even the host pins build records: checkedSegments); enrolling
// is expand --apply's job (task tb4).
//
// It merges the store itself rather than reading the last build's
// outputs: the plan needs every unresolved half's 4-tuple, which the
// view model rightly does not carry, and a merge of the same segments
// gives the same halves, so the listing always matches what build
// would see now.
//
// It merges every project in the store whatever -project says: the
// addresses traced hosts own are the visited set, and a host traced only
// under another project is still in the mesh. -project narrows which
// projects' connections may propose peers, nothing else.

// expandFlags are expand's flags.
type expandFlags struct {
	build    buildFlags
	registry string
	hops     int
	asJSON   bool
}

// expandCommand parses expand's flags, plans one round and prints it. A
// segment left out of the merge makes it exit 1 after printing, as build
// does: a damaged host's addresses drop out of the visited set, so the
// plan may propose a peer that is in fact traced.
func expandCommand(args []string, e env) error {
	fs := newFlagSet("expand", e.stderr)
	var f expandFlags
	fs.Var(&f.build.projects, "project", "propose only from this project's connections (repeatable; default: all)")
	fs.StringVar(&f.build.store, "store", "", "local trace store (default ~/.local/share/iormesh/traces)")
	fs.StringVar(&f.build.db, "db", "", "service database; host pins are read next to it "+
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
	p, bad, err := planExpansion(context.Background(), e, f)
	if err != nil {
		return err
	}
	if err := writeProposal(e, p, f.asJSON); err != nil {
		return err
	}
	return reportExpandBad(e, bad)
}

// planExpansion merges the whole store and plans one round against the
// registry. Segments go through the same checks as a build (the
// memory-capped child, the pinned host identities), since they are the
// same untrusted files; the ones that fail are returned.
func planExpansion(ctx context.Context, e env, f expandFlags) (expand.Proposal, []federate.BadSegment, error) {
	reg, err := loadRegistry(f.registry, e)
	if err != nil {
		return expand.Proposal{}, nil, err
	}
	allow, err := expand.NewAllowlist(reg.Expand)
	if err != nil {
		return expand.Proposal{}, nil, err
	}
	paths, err := resolveBuildPaths(e, f.build)
	if err != nil {
		return expand.Proposal{}, nil, err
	}
	res, segs, err := mergeStore(ctx, paths, f.build.projects)
	if err != nil {
		return expand.Proposal{}, nil, err
	}
	in := expand.Input{Merge: res, HostDirs: hostDirs(segs.Hosts), Registry: reg, Projects: f.build.projects}
	return expand.Plan(in, allow, expand.NewLimits(reg.Expand, f.hops)), segs.Bad, nil
}

// mergeStore merges every project of the store, after checking that the
// -project names are valid ones.
func mergeStore(ctx context.Context, paths buildPaths, flagged []string) (
	federate.Result, federate.Segments, error) {
	if _, err := buildProjects(paths.store, flagged); err != nil {
		return federate.Result{}, federate.Segments{}, err
	}
	all, err := buildProjects(paths.store, nil)
	if err != nil {
		return federate.Result{}, federate.Segments{}, err
	}
	segs, _, err := checkedSegments(ctx, paths, all)
	if err != nil {
		return federate.Result{}, federate.Segments{}, err
	}
	sources := make([]federate.Source, len(segs.Sources))
	for i, s := range segs.Sources {
		sources[i] = s
	}
	res, err := federate.Merge(ctx, sources, federate.Options{})
	return res, segs, err
}

// writeProposal prints the plan as text or JSON.
func writeProposal(e env, p expand.Proposal, asJSON bool) error {
	if !asJSON {
		return expand.WriteText(e.stdout, p)
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(e.stdout, "%s\n", data)
	return err
}

// reportExpandBad names the segments left out on stderr and turns them into
// exit status 1.
func reportExpandBad(e env, bad []federate.BadSegment) error {
	if len(bad) == 0 {
		return nil
	}
	for _, b := range bad {
		_, _ = fmt.Fprintf(e.stderr, "iormeshctl: bad segment %s: %s\n", termsafe.String(b.Name),
			errText(b.Err))
	}
	return fmt.Errorf("%w: %d bad segments left out of the merge; their hosts' addresses are missing "+
		"from the visited set, so a listed peer may already be traced", errBadSegments, len(bad))
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
