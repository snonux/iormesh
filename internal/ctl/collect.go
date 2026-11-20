package ctl

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"iormesh/internal/federate"
	"iormesh/internal/project"
	"iormesh/internal/servicedb"
)

// `iormeshctl collect [-project P]... [HOST...]` (PLAN.md M4 "Collect"):
// move each published segment from the servers' ready/ folders into the
// controller's store. Per host, every step an argv through the Transport:
//
//  1. true — reachability: a host that cannot be reached fails, it is
//     never mistaken for one with nothing to collect;
//  2. per project: test -d /opt/iormesh/traces/<project>/ready — a
//     project never started has no ready/ yet and is skipped;
//  3. Fetch (rsync -rt --remove-source-files) of that ready/ into
//     <store>/<project>/<host>/.
//
// Only ready/ is ever fetched. The tracer writes the active segment at
// the project directory's top (.active-*, internal/segment) and renames
// it into ready/ only once it is complete and synced, so every file
// rsync sees is final, and --remove-source-files deletes a server file
// only after it transferred: collect is safe to run any number of times
// during a live session, each run moving only what was published since.
// No sudo is involved: ready/ belongs to the deploying user (the start's
// -ready-owner), who may read and delete in it.
//
// Collect only syncs; it never merges or touches the service database
// (`build` does, offline; docs/implementation.md M4).

const (
	// readyDir is segment.ReadyDir, the sync contract. ctl does not
	// import internal/segment: it would pull the parquet writer into the
	// controller for one constant.
	readyDir = "ready"
	// tracesDirName is the store under ~/.local/share/iormesh.
	tracesDirName = "traces"
)

// errUnreachable marks a host the reachability probe could not reach.
var errUnreachable = errors.New("host unreachable")

// collectTarget is one host and the projects to collect from it.
type collectTarget struct {
	host     string
	projects []string
}

// collector syncs projects' ready/ folders into the store.
type collector struct {
	e     env
	store string
}

// projectList is the repeatable -project flag: one project, or several
// together (PLAN.md M4 "Collect", decision #21's explicit list).
type projectList []string

func (p *projectList) String() string { return strings.Join(*p, ",") }

func (p *projectList) Set(v string) error {
	*p = append(*p, v)
	return nil
}

// defaultStorePath is ~/.local/share/iormesh/traces, next to the service
// database: PLAN.md names that one path on every platform.
func defaultStorePath() (string, error) {
	db, err := servicedb.DefaultPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(db), tracesDirName), nil
}

// collectCommand parses collect's flags, resolves every host and project
// in the registry before contacting any, and collects host by host. A
// failed host is reported and the others still collected; the command
// then fails naming it.
func collectCommand(args []string, e env) error {
	fs := newFlagSet("collect", e.stderr)
	var projects projectList
	fs.Var(&projects, "project", "collect only this project (repeatable; default: every project)")
	regPath := fs.String("registry", "", "registry file (default ~/.config/iormesh/servers.yaml)")
	store := fs.String("store", "", "local trace store (default ~/.local/share/iormesh/traces)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	r, err := loadRegistry(*regPath, e)
	if err != nil {
		return err
	}
	targets, err := collectTargets(r, fs.Args(), projects)
	if err != nil {
		return err
	}
	c, err := newCollector(e, *store)
	if err != nil {
		return err
	}
	return c.all(context.Background(), targets)
}

// newCollector returns a collector over store, the default when empty.
func newCollector(e env, store string) (collector, error) {
	if store == "" {
		var err error
		if store, err = e.traces(); err != nil {
			return collector{}, err
		}
	}
	return collector{e: e, store: store}, nil
}

// all collects every target in turn.
func (c collector) all(ctx context.Context, targets []collectTarget) error {
	var failed []string
	for _, tg := range targets {
		if err := c.host(ctx, tg); err != nil {
			_, _ = fmt.Fprintf(c.e.stderr, "iormeshctl: %s: %v\n", tg.host, err)
			failed = append(failed, tg.host)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("collect failed on %d of %d hosts: %s", len(failed), len(targets),
			strings.Join(failed, " "))
	}
	return nil
}

// collectTargets resolves hosts (every registered one when none is
// given) and narrows each to projects when set. An unregistered host, a
// named host carrying none of the projects, or a project no selected host
// carries fails before any host is contacted (principle #3: only
// registered hosts are reached). A host named twice is collected once.
func collectTargets(r federate.Registry, hosts, projects []string) ([]collectTarget, error) {
	explicit := len(hosts) > 0
	if !explicit {
		for _, s := range r.Servers {
			hosts = append(hosts, s.Host)
		}
	}
	var targets []collectTarget
	seen := map[string]bool{}
	for _, host := range hosts {
		if slices.ContainsFunc(targets, func(tg collectTarget) bool { return tg.host == host }) {
			continue
		}
		srv, err := r.Lookup(host)
		if err != nil {
			return nil, err
		}
		tg := collectTarget{host: host}
		for _, p := range srv.Projects {
			if len(projects) == 0 || slices.Contains(projects, p.Name) {
				tg.projects = append(tg.projects, p.Name)
				seen[p.Name] = true
			}
		}
		if len(tg.projects) > 0 {
			targets = append(targets, tg)
		} else if explicit {
			return nil, fmt.Errorf("%w: %s carries none of %s", federate.ErrUnknownProject, host,
				strings.Join(projects, ", "))
		}
	}
	for _, p := range projects {
		if !seen[p] {
			return nil, fmt.Errorf("%w: %q on no selected host", federate.ErrUnknownProject, p)
		}
	}
	return targets, nil
}

// host collects tg's projects from one server. Projects are independent:
// one that fails does not keep the others from being collected.
func (c collector) host(ctx context.Context, tg collectTarget) error {
	t, err := c.e.transport(tg.host)
	if err != nil {
		return err
	}
	if err := probe(ctx, t); err != nil {
		return err
	}
	var errs []error
	for _, p := range tg.projects {
		if err := c.project(ctx, t, tg.host, p); err != nil {
			errs = append(errs, fmt.Errorf("project %s: %w", p, err))
		}
	}
	return errors.Join(errs...)
}

// probe checks the host answers at all. Without it, a failed `test -d`
// on an unreachable host would read as "nothing published yet".
func probe(ctx context.Context, t federate.Transport) error {
	if _, err := t.Run(ctx, "true"); err != nil {
		return fmt.Errorf("%w: %w", errUnreachable, err)
	}
	return nil
}

// project moves one project's published segments into the store and
// reports how many new files arrived.
func (c collector) project(ctx context.Context, t federate.Transport, host, name string) error {
	remote := path.Join(confinedTraceDir, name, readyDir)
	if _, err := t.Run(ctx, "test", "-d", remote); err != nil {
		_, _ = fmt.Fprintf(c.e.stdout, "%s: project %s: no %s yet, nothing to collect\n", host, name, remote)
		return nil
	}
	local, err := c.localDir(name, host)
	if err != nil {
		return err
	}
	before := countFiles(local)
	if err := t.Fetch(ctx, remote, local); err != nil {
		return err
	}
	_, err = fmt.Fprintf(c.e.stdout, "%s: project %s: %d new files in %s\n", host, name,
		max(countFiles(local)-before, 0), local)
	return err
}

// localDir creates and returns <store>/<project>/<host>. Every host gets
// its own directory: segment names carry the server's FQDN, but the
// server chooses its file names, so a shared directory would let one host
// overwrite another's segments. Both components come from the registry
// and are checked again here, since they become local path components:
// neither rule admits "/", "." or "..".
//
// The store and the project directory must be this user's and writable
// by nobody else — whoever could write them could swap a host directory
// for a symlink and redirect rsync's writes. They are created 0700 and
// refused, never repaired, when not. The host directory itself is
// Fetch's: it forces it to 0700 after every transfer.
func (c collector) localDir(name, host string) (string, error) {
	if !project.Valid(name) || !federate.ValidHost(host) {
		return "", fmt.Errorf("unsafe path component %q/%q", name, host)
	}
	dir := filepath.Join(c.store, name, host)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("trace store: %w", err)
	}
	for _, d := range []string{c.store, filepath.Dir(dir)} {
		if err := checkStoreDir(d); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// checkStoreDir requires dir to be a directory owned by this user and not
// group- or other-writable. The store root may be a symlink (another
// disk, a dotfile manager), so Stat follows it; reading is left to the
// operator, the segments themselves sit in 0700 host directories.
func checkStoreDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("trace store: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("trace store: %s is not a directory", dir)
	}
	if perm := info.Mode().Perm(); perm&0o022 != 0 {
		return fmt.Errorf("trace store: %s has mode %v, writable by others (chmod go-w)", dir, perm)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("trace store: %s is owned by uid %d", dir, st.Uid)
	}
	return nil
}

// countFiles counts dir's regular files, for collect's report only: an
// unreadable dir counts as empty, and Fetch reports the real error.
func countFiles(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, en := range entries {
		if en.Type()&fs.ModeType == 0 {
			n++
		}
	}
	return n
}

// loadRegistry loads the registry at path, the default when empty.
func loadRegistry(path string, e env) (federate.Registry, error) {
	if path == "" {
		var err error
		if path, err = e.registry(); err != nil {
			return federate.Registry{}, err
		}
	}
	return federate.LoadRegistry(path)
}
