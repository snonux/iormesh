package ctl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"iormesh/internal/federate"
	"iormesh/internal/k8sname"
	"iormesh/internal/project"
	"iormesh/internal/render"
	"iormesh/internal/report"
	"iormesh/internal/routemap"
	"iormesh/internal/schema"
	"iormesh/internal/servicedb"
	"iormesh/internal/site"
	"iormesh/internal/viewmodel"
)

// `iormeshctl build [-project P]...` (PLAN.md M4 "Build", "Three
// commands, two jobs"): merge what collect put in the store, offline.
//
//  1. federate.LoadSegments decodes every events segment of the projects
//     (with -from/-to or -last, only those of the window — window.go,
//     task yh4) once in a memory-capped child process (check.go: a compromised
//     server's file must not exhaust the build's memory), holds each
//     store host directory to one host identity (pinned in hosts.json),
//     and rebuilds each session's graph from its events (decision #3:
//     the server keeps none). A segment that fails is skipped whole and
//     reported (see "Bad segments"). With -namespace NS (task sh4) only
//     the sessions of pods in NS go on (inNamespace), and the service
//     database's views of step 3 keep only those pods' connections.
//  2. federate.Merge fuses the hosts' TCP ends into cross-host edges and
//     bounds the clocks; the events it reads also feed the run's tally.
//  3. servicedb.Observe and Import fold the mesh into the service
//     database (task hb4), one source per segment so request counts stay
//     exact after old segments are deleted; requests count per path
//     template (ids folded, the route map's path_templates first —
//     task xh4), never per raw id.
//  4. The view model (V1: run, health, services, edges; V2: the
//     conversations; M7: the routed edges, from the route map read
//     before step 1, routes.go) is built from the tally, the merge, the
//     database's view of the projects and the conversations' recording — whose http
//     events ride along with step 3's read, and whose file work and Java
//     frames take one more read (conversations.go). The per-run rollup
//     (internal/report, PLAN.md M5) is built from the events of step 2's
//     read, the sessions' methods tables (each checked in the child
//     first, as query reads them), the merge's conn-id renames and the
//     database's endpoints.
//  5. <builds>/<projects>[@<namespace>][~window]/graph.json (the merged
//     graph export),
//     model.json, report.json and the SVG exports (graph.svg,
//     sequence/<ID>.svg per conversation; internal/render) are written,
//     then the run's local site (internal/site: a new folder under
//     sites/ per run, kept, and sites/latest pointing at it — what
//     `iormeshctl open` opens), and the summary is printed.
//
// Build never contacts a server, and everything it writes is a function
// of the store and the database, so a rerun over the same segments
// writes the same bytes (the import is idempotent, the merge sorted, the
// model free of clocks and local paths).
//
// Bad segments: a segment that cannot be read, whose footer disagrees
// with its name or directory, whose events do not validate or whose host
// identity is in doubt is left out of the build, named in the summary and the model, and makes the
// command exit 1 after the outputs are written. Skipping keeps the mesh
// of every good segment available — one damaged file of a long run does
// not hide the rest — while the exit status keeps the loss from being
// silent (principle #6). The file stays in the store for the operator to
// inspect or delete; nothing is ever deleted by a build.
//
// Memory: segments are streamed (internal/segment's reader decodes 512
// rows at a time, plus a row group's pages) and no pass holds events;
// a segment is read in this process only after the capped child decoded
// it, and decoding is deterministic. What grows with the input is
// the merge's per-connection state (keys, the TCP data records' stream
// offsets the clock bounds need), the graph, the service database's
// per-connection rows, the tally's per-session counts, the methods
// tables (capped at maxMethodIDs rows) and the rollup's state (capped by
// internal/report).

const (
	// buildsDirName holds the build outputs under ~/.local/share/iormesh.
	buildsDirName = "builds"
	graphFileName = "graph.json"
	modelFileName = "model.json"
	// graphSVGName and sequenceDirName are the SVG exports (M5).
	graphSVGName    = "graph.svg"
	sequenceDirName = "sequence"
	// sitesDirName holds the build's local sites, one folder per run.
	sitesDirName = "sites"
	// maxBuildKey bounds a build directory's name: many projects built
	// together get a hashed name instead (a name may be 255 bytes).
	maxBuildKey = 200
)

// errBadSegments marks a build that skipped segments: outputs written,
// exit status 1.
var errBadSegments = errors.New("segments skipped")

// buildFlags are the paths build reads and writes. routes is the static
// route map and registry the registry file (-routes and -registry,
// registered by build and refresh only: routes.go); routes "" is
// routes.yaml beside the registry, registry "" the default one. The
// discovery snapshot is read beside db.
//
// namespace is -namespace (build, show, open and tui, addNamespaceFlag):
// the Kubernetes namespace whose pods' sessions a build keeps, "" for
// all (task sh4); the readers read that build (task hk4).
//
// window is build's -from/-to or -last (window.go, task yh4), and
// windowed marks a windowed build — set by build's window, or by the
// readers' -windowed (show, open, tui: addReadBuildFlags) — whose
// directory is the full build's name plus windowSuffix
// (buildFlags.key).
type buildFlags struct {
	projects          projectList
	store, db, builds string
	routes, registry  string
	namespace         string
	window            windowSpec
	windowed          bool
}

// addNamespaceFlag registers -namespace, build's narrowing to one
// Kubernetes namespace (PLAN.md M7 "Pod fields on events", task sh4),
// and the readers' choice of that build (addReadBuildFlags). refresh does not take it: its -namespace is the K8S flag, the
// kubectl namespace its collect reaches.
func addNamespaceFlag(fs *flag.FlagSet, f *buildFlags, usage string) {
	fs.StringVar(&f.namespace, "namespace", "", usage)
}

// checkNamespace refuses a -namespace no API server issues (an RFC 1123
// label): it names a build directory, so it must not be a path either.
func checkNamespace(ns string) error {
	if ns != "" && !k8sname.Label(ns) {
		return fmt.Errorf("%w: -namespace %q is not a Kubernetes namespace (an RFC 1123 label)", errUsage, ns)
	}
	return nil
}

// addBuildFlags registers build's flags on fs into f (refresh shares
// them).
func addBuildFlags(fs *flag.FlagSet, f *buildFlags) {
	fs.Var(&f.projects, "project", "build only this project (repeatable; default: every project in the store)")
	fs.StringVar(&f.store, "store", "", "local trace store (default ~/.local/share/iormesh/traces)")
	fs.StringVar(&f.db, "db", "", "service database (default ~/.local/share/iormesh/services.db)")
	fs.StringVar(&f.builds, "builds", "", "build outputs (default ~/.local/share/iormesh/builds)")
}

// addReadBuildFlags registers the same flags for show, open and tui,
// which read a build and never write one: their -project picks which
// build, and -db is accepted but unused (task vh4 fixed the help, which
// said "build only this project"). -namespace and -windowed pick a
// build -namespace or a windowed build made instead of the full one;
// all three read the directory buildFlags.key names, so they cannot
// disagree on which build a flag set means (task hk4).
func addReadBuildFlags(fs *flag.FlagSet, f *buildFlags) {
	addBuildFlags(fs, f)
	fs.Lookup("project").Usage = "read the build of this project (repeatable, the set build -project made; " +
		"default: every project in the store)"
	fs.Lookup("store").Usage = "local trace store, listed for the default project set " +
		"(default ~/.local/share/iormesh/traces)"
	fs.Lookup("db").Usage = "accepted and unused: the build already holds what the database gave it"
	addNamespaceFlag(fs, f, "read the build -namespace made of this Kubernetes namespace "+
		"(default: the unfiltered build)")
	fs.BoolVar(&f.windowed, "windowed", false, "read the last windowed build (build -from, -to or -last; "+
		"default: the full build)")
}

// parseReadBuildFlags parses the flags of show, open or tui
// (addReadBuildFlags, plus any the command added) and refuses a
// -namespace that is no namespace before anything is read.
func parseReadBuildFlags(fs *flag.FlagSet, f *buildFlags, args []string) error {
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	return checkNamespace(f.namespace)
}

// buildCommand parses build's flags and builds.
func buildCommand(args []string, e env) error {
	fs := newFlagSet("build", e.stderr)
	var f buildFlags
	addBuildFlags(fs, &f)
	addRoutesFlag(fs, &f)
	addNamespaceFlag(fs, &f, "build only the sessions of pods in this Kubernetes namespace, into "+
		"<projects>@<namespace> (default: every session)")
	var w windowFlags
	addWindowFlags(fs, &w)
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%w: build takes no arguments", errUsage)
	}
	if err := checkNamespace(f.namespace); err != nil {
		return err
	}
	if err := parseWindow(w, &f); err != nil {
		return err
	}
	return buildAndReport(e.root(), e, f)
}

// buildAndReport builds and prints the summary and the output paths.
func buildAndReport(ctx context.Context, e env, f buildFlags) error {
	out, err := build(ctx, e, f)
	if err != nil {
		return err
	}
	if err := viewmodel.WriteSummary(e.stdout, out.model); err != nil {
		return err
	}
	if out.window != nil {
		if _, err := fmt.Fprintf(e.stdout, "\nwindow     %s (whole segments overlapping it)",
			safe(windowText(out.window))); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(e.stdout, "\ngraph      %s\nmodel      %s\nreport     %s\nsite       %s\n"+
		"           %s\n",
		safe(filepath.Join(out.dir, graphFileName)), safe(filepath.Join(out.dir, modelFileName)),
		safe(filepath.Join(out.dir, reportFileName)), safe(filepath.Join(out.site, site.IndexName)),
		safe(readHint(f)))
	if err == nil && len(out.model.Health.BadSegments) > 0 {
		err = fmt.Errorf("%w: %d bad segments left out of the build (listed above)",
			errBadSegments, len(out.model.Health.BadSegments))
	}
	return err
}

// readHint names the commands that read the build just written, open
// and show, with the flags that pick it (-namespace NS, -windowed):
// since task hk4 open and tui read every build show does.
func readHint(f buildFlags) string {
	args := namespaceArg(f.namespace)
	if f.windowed {
		args += " -windowed"
	}
	return "iormeshctl open" + args + "  ·  iormeshctl show" + args
}

// buildOutput is what a build wrote.
type buildOutput struct {
	model viewmodel.Model
	dir   string
	// site is this run's site folder (internal/site).
	site string
	// window is the resolved window of a windowed build, nil otherwise.
	window *federate.Window
}

// build runs the whole build (see the file comment) and writes its
// outputs.
func build(ctx context.Context, e env, f buildFlags) (buildOutput, error) {
	paths, err := resolveBuildPaths(e, f)
	if err != nil {
		return buildOutput{}, err
	}
	// The route map is read first: a map others could write fails the
	// build before any segment is read, as a bad registry fails deploy.
	routes, err := loadRoutes(e, f, paths.db)
	if err != nil {
		return buildOutput{}, err
	}
	sel, err := selectSegments(ctx, e, f, paths)
	if err != nil {
		return buildOutput{}, err
	}
	methods, err := loadMethodTables(ctx, &sel.segs)
	if err != nil {
		return buildOutput{}, err
	}
	in, rep, err := mergeAndImport(ctx, paths.db, sel.scope(), &sel.segs, methods, routes)
	if err != nil {
		return buildOutput{}, err
	}
	in.Report = &rep // the processes view is projected from the rollup
	model := viewmodel.New(in)
	if sel.window != nil { // the site and the TUI say which window they show
		model.Run.Window = windowText(sel.window)
	}
	dir, err := writeOutputs(paths.builds, f.key(sel.projects), in.Merge.Graph, model)
	if err == nil {
		err = writeReport(dir, rep)
	}
	if err != nil {
		return buildOutput{}, err
	}
	run, err := site.Write(filepath.Join(dir, sitesDirName), in.Merge.Graph, model)
	if err != nil {
		return buildOutput{}, fmt.Errorf("build: %w", err)
	}
	return buildOutput{model: model, dir: dir, site: run, window: sel.window}, nil
}

// selection is what a build reads: its projects, the window resolved
// (nil without one), the segments loaded and the host ids a -namespace
// build narrows the database's views to (nil: every host).
type selection struct {
	projects []string
	window   *federate.Window
	segs     federate.Segments
	hosts    []string
}

// scope is what the build reads back of the service database: its
// projects, a -namespace build's hosts, and a windowed build's window
// (task yh4's review) — the database holds every import ever made, so
// without the window a windowed build's services and edges would be
// the whole history while its tally is the window's.
func (sel selection) scope() servicedb.Filter {
	f := servicedb.Filter{Projects: sel.projects, Hosts: sel.hosts}
	if sel.window != nil {
		f.From, f.To = wallNS(sel.window.From), wallNS(sel.window.To)
	}
	return f
}

// wallNS is t in wall ns, 0 for the zero time (an open side). The window
// lies in 1970–2262 (query.ParseTime, the segment names), so it fits.
func wallNS(t time.Time) uint64 {
	if t.IsZero() {
		return 0
	}
	return uint64(t.UnixNano())
}

// selectSegments picks the build's projects, resolves its window, loads
// the segments and narrows them to -namespace; a selection without one
// readable segment is an error naming what was asked for.
func selectSegments(ctx context.Context, e env, f buildFlags, paths buildPaths) (selection, error) {
	var sel selection
	var err error
	if sel.projects, err = buildProjects(paths.store, f.projects); err != nil {
		return selection{}, err
	}
	if sel.window, err = f.window.resolve(e, paths.store, sel.projects); err != nil {
		return selection{}, err
	}
	segs, err := loadSegments(ctx, paths, sel.projects, sel.window)
	if err != nil {
		return selection{}, err
	}
	sel.segs, sel.hosts = inNamespace(segs, f.namespace)
	if len(sel.segs.Sources) == 0 {
		return selection{}, fmt.Errorf("build: no readable segment for %s%s%s in %s (%d bad)",
			strings.Join(sel.projects, ", "), namespaceNote(f.namespace), windowNote(sel.window),
			paths.store, len(sel.segs.Bad))
	}
	return sel, nil
}

// loadSegments loads the projects' segments (only window's, when not
// nil), each checked in a memory-capped child (check.go) and each host
// directory held to the identity pinned for it, then pins the
// directories seen for the first time. A windowed load checks every
// segment of a directory not pinned yet (federate.Window), so what it
// pins is what a full build would.
func loadSegments(ctx context.Context, paths buildPaths, projects []string,
	window *federate.Window) (federate.Segments, error) {
	segs, pins, err := checkedSegmentsIn(ctx, paths, projects, window)
	if err != nil {
		return federate.Segments{}, err
	}
	return segs, pinNewHosts(pins.path, segs.Hosts)
}

// inNamespace narrows a build to the sessions of pods in namespace ns
// (-namespace, task sh4; federate.Segments.InNamespace: by the pinned pod
// identity, so a machine host is left out) and returns their host ids,
// which narrow the service database's views to the connections those
// pods traced. ns "" keeps everything, and the host ids are nil: every
// host. Pinning (loadSegments) has seen every segment by then, so a
// filtered build trusts what an unfiltered one would.
func inNamespace(segs federate.Segments, ns string) (federate.Segments, []string) {
	if ns == "" {
		return segs, nil
	}
	segs = segs.InNamespace(ns)
	hosts := []string{} // set, so no pod in ns selects no connection
	for _, s := range segs.Sources {
		if !slices.Contains(hosts, s.HostID()) {
			hosts = append(hosts, s.HostID())
		}
	}
	return segs, hosts
}

// buildName names a build in messages: its projects joined by '+', and
// "@<namespace>" for a -namespace build — buildKey before any hashing.
func buildName(projects []string, namespace string) string {
	if namespace == "" {
		return strings.Join(projects, "+")
	}
	return strings.Join(projects, "+") + "@" + namespace
}

// namespaceArg is " -namespace NS", the flag that rebuilds a -namespace
// build, for a message's hint; "" without one.
func namespaceArg(ns string) string {
	if ns == "" {
		return ""
	}
	return " -namespace " + ns
}

// namespaceNote is " in namespace NS" for a -namespace build's messages.
func namespaceNote(ns string) string {
	if ns == "" {
		return ""
	}
	return " in namespace " + ns
}

// hostPins are the pinned host identities and the file they live in.
type hostPins struct {
	path  string
	known map[string]federate.HostIdentity
}

// checkedSegments loads the projects' segments as build does — each
// decoded first in a memory-capped child, each host directory held to
// its pinned identity — without pinning anything new: query and expand, which
// read the store too, must not change what a later build trusts.
func checkedSegments(ctx context.Context, paths buildPaths, projects []string) (federate.Segments, hostPins, error) {
	return checkedSegmentsIn(ctx, paths, projects, nil)
}

// checkedSegmentsIn is checkedSegments narrowed to window (nil: every
// segment; federate.LoadOptions.Window).
func checkedSegmentsIn(ctx context.Context, paths buildPaths, projects []string,
	window *federate.Window) (federate.Segments, hostPins, error) {
	exe, err := os.Executable()
	if err != nil {
		return federate.Segments{}, hostPins{}, err
	}
	pins := hostPins{path: filepath.Join(filepath.Dir(paths.db), hostPinsFileName)}
	if pins.known, err = loadHostPins(pins.path); err != nil {
		return federate.Segments{}, hostPins{}, err
	}
	segs, err := federate.LoadSegments(ctx, paths.store, projects, federate.LoadOptions{
		Check: childChecker(exe, checkAddressSpace), Known: pins.known, Window: window})
	return segs, pins, err
}

// mergeAndImport merges the segments, folds the result into the service
// database, reads the database's view of the projects back and records
// the conversations (conversations.go); the rollup is built from the
// events the merge read and the database's endpoints. routes (nil
// without a route map) joins the routed edges and carries the operator's
// path templates the import counts requests under. scope names the
// projects built and, for a -namespace build, the hosts whose
// connections the database's views keep; for a windowed build, the
// window those connections must have been seen in (selection.scope).
func mergeAndImport(ctx context.Context, dbPath string, scope servicedb.Filter, segs *federate.Segments,
	methods map[string]map[uint64]schema.Method, routes *routemap.Table) (viewmodel.Input, report.Report, error) {
	tally := viewmodel.NewTally()
	col := report.NewCollector(methods)
	tallied := make([]federate.Source, len(segs.Sources))
	for i, s := range segs.Sources {
		tallied[i] = reportSegment{s, tally, col}
	}
	res, err := federate.Merge(ctx, tallied, federate.Options{})
	if err != nil {
		return viewmodel.Input{}, report.Report{}, err
	}
	in := viewmodel.Input{Projects: scope.Projects, Tally: tally, Merge: res,
		Conversations: viewmodel.NewRecorder(res.Renames), Routes: routes}
	recorded := make([]federate.Source, len(segs.Sources))
	for i, s := range segs.Sources {
		recorded[i] = recordedSegment{s, in.Conversations}
	}
	if err := importMesh(ctx, dbPath, recorded, &in, scope); err != nil {
		return viewmodel.Input{}, report.Report{}, err
	}
	if err := recordConversations(ctx, segs, in.Conversations, res.Clocks); err != nil {
		return viewmodel.Input{}, report.Report{}, err
	}
	// Read last: recording the frames may add a methods table to Bad.
	in.BadSegments, in.Ignored = segs.Bad, segs.Ignored
	return in, report.Build(col, report.Input{Renames: res.Renames, Endpoints: in.Endpoints}), nil
}

// importMesh runs Observe and Import and fills in's database view and
// observations. Requests are stored under their path templates — the
// route map's path_templates, then the built-in folding of ids (task
// xh4) — so the services view counts routes; the events keep the raw
// paths (decision #22) for query. The database's views are read back
// through filter: the build's projects, a -namespace build's hosts and a
// windowed build's window. The import itself is never narrowed: it folds
// every segment read into the database.
func importMesh(ctx context.Context, dbPath string, sources []federate.Source, in *viewmodel.Input,
	filter servicedb.Filter) error {
	snap, err := servicedb.Observe(ctx, sources, in.Merge, in.Routes.Paths())
	if err != nil {
		return err
	}
	in.Observations = snap.Conns
	d, err := servicedb.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }() // the import committed or failed already
	if err := d.Import(ctx, snap); err != nil {
		return err
	}
	if in.Endpoints, err = d.Endpoints(ctx, filter); err != nil {
		return err
	}
	in.Interactions, err = d.Interactions(ctx, filter)
	return err
}

// buildPaths are the resolved store, database and outputs paths.
type buildPaths struct{ store, db, builds string }

// resolveBuildPaths fills the defaults and checks the store: a store
// others could write is refused here as collect refuses it.
func resolveBuildPaths(e env, f buildFlags) (buildPaths, error) {
	p := buildPaths{store: f.store, db: f.db, builds: f.builds}
	var err error
	for _, d := range []struct {
		dst *string
		def func() (string, error)
	}{{&p.store, e.traces}, {&p.db, e.servicedb}, {&p.builds, e.builds}} {
		if *d.dst == "" {
			if *d.dst, err = d.def(); err != nil {
				return buildPaths{}, err
			}
		}
	}
	if err := checkStoreDir(p.store); err != nil {
		return buildPaths{}, err
	}
	return p, nil
}

// buildProjects is the -project list, sorted and deduplicated, or every
// project directory of the store.
func buildProjects(store string, flagged []string) ([]string, error) {
	if len(flagged) > 0 {
		for _, p := range flagged {
			if !project.Valid(p) {
				return nil, fmt.Errorf("%w: invalid project %q (%s)", errUsage, p, project.Rule)
			}
		}
		return slices.Compact(slices.Sorted(slices.Values(flagged))), nil
	}
	entries, err := os.ReadDir(store)
	if err != nil {
		return nil, fmt.Errorf("trace store: %w", err)
	}
	var out []string
	for _, en := range entries {
		if en.IsDir() && project.Valid(en.Name()) {
			out = append(out, en.Name())
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("build: nothing collected in %s yet (iormeshctl collect)", store)
	}
	return out, nil
}

// buildKey names a build's output directory after its projects:
// "shop", or "client+server" for several built together ('+' is no
// project character), and "shop@prod" for a -namespace build of
// namespace prod ('@' is in neither alphabet, so no two builds share a
// name). Too many to name get a hash of the name.
func buildKey(projects []string, namespace string) string {
	key := strings.Join(projects, "+")
	if namespace != "" {
		key += "@" + namespace
	}
	if len(key) <= maxBuildKey {
		return key
	}
	sum := sha256.Sum256([]byte(key))
	return "projects-" + hex.EncodeToString(sum[:8])
}

// writeOutputs writes graph.json and model.json into the build's
// directory, builds/<key> (buildKey), each atomically (temp file,
// rename), and returns the directory. The directories are 0700: the
// graph names hosts, processes and files of production servers.
func writeOutputs(builds, key string, g schema.Graph, m viewmodel.Model) (string, error) {
	dir := filepath.Join(builds, key)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("build: %w", err)
	}
	// An existing builds directory is held to the store's rule (owned,
	// not writable by others: whoever could write it could swap the
	// build's directory for a symlink), and the build's own directory
	// is made 0700 whatever an earlier run or umask left.
	if err := checkStoreDir(builds); err != nil {
		return "", fmt.Errorf("build outputs: %w", err)
	}
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("build outputs: %s is not a directory (a symlink?)", dir)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("build: %w", err)
	}
	if err := schema.WriteGraphFile(filepath.Join(dir, graphFileName), g); err != nil {
		return "", fmt.Errorf("build: %w", err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", fmt.Errorf("build: model: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(dir, modelFileName), append(data, '\n')); err != nil {
		return "", fmt.Errorf("build: %w", err)
	}
	if err := writeExports(dir, g, m); err != nil {
		return "", fmt.Errorf("build: %w", err)
	}
	return dir, nil
}

// writeExports writes the deterministic SVG exports (internal/render):
// graph.svg, and sequence/<ID>.svg per conversation. The sequence
// directory is written whole under a temporary name and swapped in, so
// a conversation an earlier build had and this one has not never
// lingers there next to the new ones.
func writeExports(dir string, g schema.Graph, m viewmodel.Model) error {
	if err := writeFileAtomic(filepath.Join(dir, graphSVGName), render.Graph(g, render.NamesOf(m))); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(dir, "."+sequenceDirName+"-*")
	if err != nil {
		return err
	}
	for _, c := range m.Conversations {
		if err := os.WriteFile(filepath.Join(tmp, c.ID+".svg"), render.Sequence(c), 0o600); err != nil {
			return errors.Join(err, os.RemoveAll(tmp))
		}
	}
	final := filepath.Join(dir, sequenceDirName)
	if err := os.RemoveAll(final); err != nil {
		return errors.Join(err, os.RemoveAll(tmp))
	}
	return os.Rename(tmp, final)
}

// writeFileAtomic writes data to a synced temp file next to path and
// renames it into place, so a reader never sees half a model.
func writeFileAtomic(path string, data []byte) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Sync(); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
