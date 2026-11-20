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

	"iormesh/internal/federate"
	"iormesh/internal/project"
	"iormesh/internal/schema"
	"iormesh/internal/servicedb"
	"iormesh/internal/viewmodel"
)

// `iormeshctl build [-project P]...` (PLAN.md M4 "Build", "Three
// commands, two jobs"): merge what collect put in the store, offline.
//
//  1. federate.LoadSegments reads every events segment of the projects
//     once: each event is checked, and each session's graph is rebuilt
//     from its events (decision #3: the server keeps none). A segment
//     that fails is skipped whole and reported (see "Bad segments").
//  2. federate.Merge fuses the hosts' TCP ends into cross-host edges and
//     bounds the clocks; the events it reads also feed the run's tally.
//  3. servicedb.Observe and Import fold the mesh into the service
//     database (task hb4), one source per segment so request counts stay
//     exact after old segments are deleted.
//  4. The view model (V1: run, health, services, edges) is built from the
//     tally, the merge and the database's view of the projects.
//  5. <builds>/<projects>/graph.json (the merged graph export) and
//     model.json are written, and the summary is printed.
//
// Build never contacts a server, and everything it writes is a function
// of the store and the database, so a rerun over the same segments
// writes the same bytes (the import is idempotent, the merge sorted, the
// model free of clocks and paths).
//
// Bad segments: a segment that cannot be read, whose footer disagrees
// with its name or directory, or whose events do not validate is left
// out of the build, named in the summary and the model, and makes the
// command exit 1 after the outputs are written. Skipping keeps the mesh
// of every good segment available — one damaged file of a long run does
// not hide the rest — while the exit status keeps the loss from being
// silent (principle #6). The file stays in the store for the operator to
// inspect or delete; nothing is ever deleted by a build.
//
// Memory: segments are streamed (internal/segment's reader decodes 512
// rows at a time) and no pass holds events; what grows with the input is
// the merge's per-connection state (keys, the TCP data records' stream
// offsets the clock bounds need), the graph, the service database's
// per-connection rows and the tally's per-session counts.

const (
	// buildsDirName holds the build outputs under ~/.local/share/iormesh.
	buildsDirName = "builds"
	graphFileName = "graph.json"
	modelFileName = "model.json"
	// maxBuildKey bounds a build directory's name: many projects built
	// together get a hashed name instead (a name may be 255 bytes).
	maxBuildKey = 200
)

// errBadSegments marks a build that skipped segments: outputs written,
// exit status 1.
var errBadSegments = errors.New("segments skipped")

// buildFlags are the paths build reads and writes.
type buildFlags struct {
	projects          projectList
	store, db, builds string
}

// addBuildFlags registers build's flags on fs into f (refresh shares
// them).
func addBuildFlags(fs *flag.FlagSet, f *buildFlags) {
	fs.Var(&f.projects, "project", "build only this project (repeatable; default: every project in the store)")
	fs.StringVar(&f.store, "store", "", "local trace store (default ~/.local/share/iormesh/traces)")
	fs.StringVar(&f.db, "db", "", "service database (default ~/.local/share/iormesh/services.db)")
	fs.StringVar(&f.builds, "builds", "", "build outputs (default ~/.local/share/iormesh/builds)")
}

// buildCommand parses build's flags and builds.
func buildCommand(args []string, e env) error {
	fs := newFlagSet("build", e.stderr)
	var f buildFlags
	addBuildFlags(fs, &f)
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%w: build takes no arguments", errUsage)
	}
	return buildAndReport(context.Background(), e, f)
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
	_, err = fmt.Fprintf(e.stdout, "\ngraph      %s\nmodel      %s\n",
		filepath.Join(out.dir, graphFileName), filepath.Join(out.dir, modelFileName))
	if err == nil && len(out.model.Health.BadSegments) > 0 {
		err = fmt.Errorf("%w: %d bad segments left out of the build (listed above)",
			errBadSegments, len(out.model.Health.BadSegments))
	}
	return err
}

// buildOutput is what a build wrote.
type buildOutput struct {
	model viewmodel.Model
	dir   string
}

// build runs the whole build (see the file comment) and writes its
// outputs.
func build(ctx context.Context, e env, f buildFlags) (buildOutput, error) {
	paths, err := resolveBuildPaths(e, f)
	if err != nil {
		return buildOutput{}, err
	}
	projects, err := buildProjects(paths.store, f.projects)
	if err != nil {
		return buildOutput{}, err
	}
	segs, err := federate.LoadSegments(ctx, paths.store, projects)
	if err != nil {
		return buildOutput{}, err
	}
	if len(segs.Sources) == 0 {
		return buildOutput{}, fmt.Errorf("build: no readable segment for %s in %s (%d bad)",
			strings.Join(projects, ", "), paths.store, len(segs.Bad))
	}
	in, err := mergeAndImport(ctx, paths.db, projects, segs)
	if err != nil {
		return buildOutput{}, err
	}
	model := viewmodel.New(in)
	dir, err := writeOutputs(paths.builds, projects, in.Merge.Graph, model)
	if err != nil {
		return buildOutput{}, err
	}
	return buildOutput{model: model, dir: dir}, nil
}

// mergeAndImport merges the segments, folds the result into the service
// database and reads the database's view of the projects back.
func mergeAndImport(ctx context.Context, dbPath string, projects []string,
	segs federate.Segments) (viewmodel.Input, error) {
	tally := viewmodel.NewTally()
	plain := make([]federate.Source, len(segs.Sources))
	tallied := make([]federate.Source, len(segs.Sources))
	for i, s := range segs.Sources {
		plain[i], tallied[i] = s, talliedSegment{s, tally}
	}
	res, err := federate.Merge(ctx, tallied, federate.Options{})
	if err != nil {
		return viewmodel.Input{}, err
	}
	in := viewmodel.Input{Projects: projects, Tally: tally, Merge: res,
		BadSegments: segs.Bad, Ignored: segs.Ignored}
	err = importMesh(ctx, dbPath, plain, &in)
	return in, err
}

// importMesh runs Observe and Import and fills in's database view.
func importMesh(ctx context.Context, dbPath string, sources []federate.Source, in *viewmodel.Input) error {
	snap, err := servicedb.Observe(ctx, sources, in.Merge)
	if err != nil {
		return err
	}
	d, err := servicedb.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }() // the import committed or failed already
	if err := d.Import(ctx, snap); err != nil {
		return err
	}
	filter := servicedb.Filter{Projects: in.Projects}
	if in.Endpoints, err = d.Endpoints(ctx, filter); err != nil {
		return err
	}
	in.Interactions, err = d.Interactions(ctx, filter)
	return err
}

// talliedSegment is a segment whose events also feed the run's tally as
// the merge reads them: the tally costs no pass of its own.
type talliedSegment struct {
	*federate.Segment
	tally *viewmodel.Tally
}

func (s talliedSegment) Events(ctx context.Context, fn func(*schema.Event) error) error {
	k := viewmodel.SessionKey{Project: s.Project, Host: s.Host, FQDN: s.File.FQDN, ID: s.File.Session}
	name := s.Name()
	s.tally.Segment(k, name)
	return s.Segment.Events(ctx, func(ev *schema.Event) error {
		s.tally.Add(k, name, ev)
		return fn(ev)
	})
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
// project character). Too many to name get a hash of the list.
func buildKey(projects []string) string {
	key := strings.Join(projects, "+")
	if len(key) <= maxBuildKey {
		return key
	}
	sum := sha256.Sum256([]byte(key))
	return "projects-" + hex.EncodeToString(sum[:8])
}

// writeOutputs writes graph.json and model.json into the build's
// directory, each atomically (temp file, rename), and returns the
// directory. The directories are 0700: the graph names hosts, processes
// and files of production servers.
func writeOutputs(builds string, projects []string, g schema.Graph, m viewmodel.Model) (string, error) {
	dir := filepath.Join(builds, buildKey(projects))
	if err := os.MkdirAll(dir, 0o700); err != nil {
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
	return dir, nil
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
