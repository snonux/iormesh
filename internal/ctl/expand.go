package ctl

import (
	"context"
	"fmt"

	"iormesh/internal/expand"
	"iormesh/internal/federate"
	"iormesh/internal/termsafe"
)

// `iormeshctl expand` (docs/milestones.md M6 "Discover"): list the unresolved
// peers of the store's merged mesh, which of them the next round would enroll
// and why each other one stays put. Without -apply, listing is all it
// does — it reads the store, the host pins and the registry, never a
// server, and writes nothing (not even the host pins build records:
// checkedSegments). -apply enrolls and deploys (expandapply.go,
// docs/milestones.md M6 "Apply").
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
	// apply enrolls and deploys the plan's candidates (expandapply.go);
	// dist and key are deploy's, for the hosts it enrolls.
	apply     bool
	dist, key string
}

// expansion is one planned round and what -apply needs to act on it.
type expansion struct {
	reg      federate.Registry
	regPath  string
	allow    expand.Allowlist
	proposal expand.Proposal
	bad      []federate.BadSegment
	// hostIDs maps every host_id the store holds to its directory: the
	// machines already traced, which -apply must not enroll again under
	// another address.
	hostIDs map[string]string
}

// expandCommand parses expand's flags, plans one round and prints it. A
// segment left out of the merge makes it exit 1 after printing, as build
// does: a damaged host's addresses drop out of the visited set, so the
// plan may propose a peer that is in fact traced. With -apply it then
// enrolls and deploys the candidates — never past a bad segment.
func expandCommand(args []string, e env) error {
	f, err := parseExpandFlags(args, e)
	if err != nil {
		return err
	}
	ctx := e.root()
	x, err := planExpansion(ctx, e, f)
	if err != nil {
		return err
	}
	if err := writeProposal(e, x.proposal, f.asJSON); err != nil {
		return err
	}
	if err := reportExpandBad(e, x.bad); err != nil {
		if f.apply {
			return fmt.Errorf("%w; nothing enrolled", err)
		}
		return err
	}
	if !f.apply {
		return nil
	}
	return applyExpansion(ctx, e, f, x)
}

// parseExpandFlags parses and checks expand's command line.
func parseExpandFlags(args []string, e env) (expandFlags, error) {
	fs := newFlagSet("expand", e.stderr)
	var f expandFlags
	fs.Var(&f.build.projects, "project", "propose only from this project's connections (repeatable; default: all)")
	fs.StringVar(&f.build.store, "store", "", "local trace store (default ~/.local/share/iormesh/traces)")
	fs.StringVar(&f.build.db, "db", "", "service database; host pins are read next to it "+
		"(default ~/.local/share/iormesh/services.db)")
	fs.StringVar(&f.registry, "registry", "", "registry file (default ~/.config/iormesh/servers.yaml)")
	fs.IntVar(&f.hops, "hops", expand.DefaultMaxHops, "hop limit: rounds away from the hosts enrolled by hand")
	fs.BoolVar(&f.asJSON, "json", false, "print the plan as JSON")
	fs.BoolVar(&f.apply, "apply", false, "enroll the candidates in the registry and deploy to them")
	fs.StringVar(&f.dist, "dist", defaultDist, "with -apply: directory holding the signed tracer builds")
	fs.StringVar(&f.key, "key", "", "with -apply: pinned minisign public key (default ~/.config/iormesh/"+
		keyFileName+")")
	if err := fs.Parse(args); err != nil {
		return f, fmt.Errorf("%w: %w", errUsage, err)
	}
	switch {
	case fs.NArg() != 0:
		return f, fmt.Errorf("%w: expand takes no arguments", errUsage)
	case f.hops < 0:
		return f, fmt.Errorf("%w: -hops %d is negative", errUsage, f.hops)
	case f.apply && f.asJSON:
		// The deploy's progress lines would follow the JSON on stdout.
		return f, fmt.Errorf("%w: -apply and -json do not mix", errUsage)
	}
	return f, nil
}

// planExpansion merges the whole store and plans one round against the
// registry. Segments go through the same checks as a build (the
// memory-capped child, the pinned host identities), since they are the
// same untrusted files; the ones that fail are returned in bad.
func planExpansion(ctx context.Context, e env, f expandFlags) (expansion, error) {
	x := expansion{regPath: f.registry}
	var err error
	if x.regPath == "" {
		if x.regPath, err = e.registry(); err != nil {
			return x, err
		}
	}
	if x.reg, err = federate.LoadRegistry(x.regPath); err != nil {
		return x, err
	}
	if x.allow, err = expand.NewAllowlist(x.reg.Expand); err != nil {
		return x, err
	}
	paths, err := resolveBuildPaths(e, f.build)
	if err != nil {
		return x, err
	}
	res, segs, err := mergeStore(ctx, paths, f.build.projects)
	if err != nil {
		return x, err
	}
	x.hostIDs, x.bad = hostDirs(segs.Hosts), segs.Bad
	in := expand.Input{Merge: res, HostDirs: x.hostIDs, Registry: x.reg, Projects: f.build.projects}
	x.proposal = expand.Plan(in, x.allow, expand.NewLimits(x.reg.Expand, f.hops))
	return x, nil
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

// writeProposal prints the plan as text or JSON; the JSON is
// terminal-safe (printJSON), as its peers come from traced data.
func writeProposal(e env, p expand.Proposal, asJSON bool) error {
	if !asJSON {
		return expand.WriteText(e.stdout, p)
	}
	return printJSON(e.stdout, p)
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
