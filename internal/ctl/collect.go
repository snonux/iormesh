package ctl

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"iormesh/internal/federate"
	"iormesh/internal/project"
	"iormesh/internal/segment"
	"iormesh/internal/servicedb"
)

// `iormeshctl collect [-project P]... [HOST...]` (PLAN.md M4 "Collect"):
// move each published segment from the servers' ready/ folders into the
// controller's store. Per host, every step an argv through the Transport:
//
//  1. true — reachability: a host that cannot be reached fails, it is
//     never mistaken for one with nothing to collect;
//  2. per project: test -d /opt/iormesh/traces/<project>/ready — a
//     project never started has no ready/ yet (exit status 1) and is
//     skipped; any other failure is the project's failure (remoteTest);
//  3. Fetch (federate.FetchFlags: rsync -rt --fsync
//     --remove-source-files, the non-dot *.parquet files directly in
//     ready/, none over 1 GiB) into <store>/<project>/<host>/; a server
//     file is deleted only once fsynced here.
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
// (`build` does, offline; docs/implementation.md M4). A registry pods:
// entry takes another path, without the shell probes (pods.go): the
// API server says which pods it is, and each is fetched through
// `kubectl exec … iormesh ready`.

const (
	// readyDir is the sync contract. The controller reads segments
	// through internal/segment since the build (task ib4), so the
	// constant is that package's, not a copy that could drift.
	readyDir = segment.ReadyDir
	// tracesDirName is the store under ~/.local/share/iormesh.
	tracesDirName = "traces"
)

// errUnreachable marks a host the reachability probe could not reach.
var errUnreachable = errors.New("host unreachable")

// collectTarget is one host, or one pods: entry (pod set, host its
// reference), and the projects to collect from it.
type collectTarget struct {
	host     string
	pod      *podEntry
	projects []string
}

// collector syncs projects' ready/ folders into the store. db is the
// service database whose host pins file a pod's store directory is
// pinned in (pods.go), the default one when empty.
type collector struct {
	e     env
	store string
	db    string
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
	db := fs.String("db", "", "service database whose host pins a pod's store directory is pinned in "+
		"(default ~/.local/share/iormesh/services.db; give build's -db)")
	kf := addK8sFlags(fs, false)
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	r, err := loadRegistry(*regPath, e)
	if err != nil {
		return err
	}
	targets, err := collectTargets(r, fs.Args(), projects, kf)
	if err != nil {
		return err
	}
	c, err := newCollector(e, *store)
	if err != nil {
		return err
	}
	// A pod's pin goes where the build that reads it looks (review N3).
	c.db = *db
	return c.all(e.root(), targets)
}

// newCollector returns a collector over store, the default when empty.
// The store is created and checked here, before any host is contacted:
// an unusable store must not be found only after a host was stopped.
func newCollector(e env, store string) (collector, error) {
	if store == "" {
		var err error
		if store, err = e.traces(); err != nil {
			return collector{}, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(store), 0o700); err != nil {
		return collector{}, fmt.Errorf("trace store: %w", err)
	}
	if err := ensureStoreDir(store); err != nil {
		return collector{}, err
	}
	return collector{e: e, store: store}, nil
}

// all collects every target in turn, each under its own deadline
// (env.onHost) derived from ctx, so a host that stalls is reported and
// the ones after it are still collected.
func (c collector) all(ctx context.Context, targets []collectTarget) error {
	var failed []string
	for _, tg := range targets {
		err := c.e.onHost(ctx, func(ctx context.Context) error { return c.host(ctx, tg) })
		if err != nil {
			_, _ = fmt.Fprintf(c.e.stderr, "iormeshctl: %s: %s\n", tg.host, errText(err))
			failed = append(failed, tg.host)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("collect failed on %d of %d hosts: %s", len(failed), len(targets),
			strings.Join(failed, " "))
	}
	return nil
}

// collectTargets resolves hosts and pod entry references (every
// registered one when none is given) and narrows each to projects when
// set. An unregistered host or pod entry, a context no cluster lists, a
// named target carrying none of the projects, or a project no selected
// target carries fails before anything is contacted (principle #3: only
// registered targets are reached). A target named twice is collected
// once.
func collectTargets(r federate.Registry, args, projects []string, kf *k8sFlags) ([]collectTarget, error) {
	explicit := len(args) > 0
	if !explicit {
		if err := kf.refuse("a collect of every target"); err != nil {
			return nil, err
		}
		args = allTargets(r)
	}
	var targets []collectTarget
	seen := map[string]bool{}
	for _, arg := range args {
		if slices.ContainsFunc(targets, func(tg collectTarget) bool { return tg.host == arg }) {
			continue
		}
		tg, carried, err := registeredTarget(&r, arg, kf)
		if err != nil {
			return nil, err
		}
		for _, p := range carried {
			if len(projects) == 0 || slices.Contains(projects, p.Name) {
				tg.projects = append(tg.projects, p.Name)
				seen[p.Name] = true
			}
		}
		if len(tg.projects) > 0 {
			targets = append(targets, tg)
		} else if explicit {
			return nil, fmt.Errorf("%w: %s carries none of %s", federate.ErrUnknownProject, arg,
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

// allTargets is every registered host, then every pod entry's reference.
func allTargets(r federate.Registry) []string {
	var all []string
	for _, s := range r.Servers {
		all = append(all, s.Host)
	}
	for _, p := range r.Pods {
		all = append(all, r.PodRef(p))
	}
	return all
}

// registeredTarget is arg's registry entry as a collect target, without
// projects yet, and the projects it carries.
func registeredTarget(r *federate.Registry, arg string, kf *k8sFlags) (collectTarget, []federate.Project, error) {
	if federate.IsPodRef(arg) {
		pe, err := resolvePodRef(r, arg, kf)
		if err != nil {
			return collectTarget{}, nil, err
		}
		return collectTarget{host: arg, pod: &pe}, pe.projects, nil
	}
	srv, err := r.Lookup(arg)
	if err != nil {
		return collectTarget{}, nil, err
	}
	return collectTarget{host: arg}, srv.Projects, nil
}

// host collects tg's projects from one server or pod entry. Projects are
// independent: one that fails does not keep the others from being
// collected.
func (c collector) host(ctx context.Context, tg collectTarget) error {
	if tg.pod != nil {
		return c.podEntryCollect(ctx, *tg.pod, tg.projects)
	}
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
// reports how many new files arrived. The names are checked first, before
// either reaches the remote command or a local path.
func (c collector) project(ctx context.Context, t federate.Transport, host, name string) error {
	if !project.Valid(name) || !federate.ValidHost(host) {
		return fmt.Errorf("unsafe path component %q/%q", name, host)
	}
	remote := path.Join(confinedTraceDir, name, readyDir)
	present, err := remoteTest(ctx, t, "test", "-d", remote)
	if err != nil {
		return err
	}
	if !present {
		_, _ = fmt.Fprintf(c.e.stdout, "%s: project %s: no %s yet, nothing to collect\n", host, name, remote)
		return nil
	}
	return c.fetch(ctx, t, host, host, name, remote)
}

// fetch moves remote's segments into <store>/<name>/<dir> and reports
// how many new files arrived, under label. dir is the host name for a
// server, "pod-<uid>" for a pod; both are checked again here, before
// either becomes a local path.
func (c collector) fetch(ctx context.Context, t federate.Transport, label, dir, name, remote string) error {
	if !project.Valid(name) || !federate.ValidHost(dir) {
		return fmt.Errorf("unsafe path component %q/%q", name, dir)
	}
	local, err := c.localDir(name, dir)
	if err != nil {
		return err
	}
	before := countFiles(local)
	if err := t.Fetch(ctx, remote, local); err != nil {
		return err
	}
	_, err = fmt.Fprintf(c.e.stdout, "%s: project %s: %d new files in %s\n", label, name,
		max(countFiles(local)-before, 0), local)
	return err
}

// remoteTest runs a `test` argv on the host: true when it holds, false
// only when test says no (exit status 1). Anything else — ssh's 255 on a
// dropped connection, a remote error — is an error, never "absent": rm
// would otherwise skip a sync and uninstall over what it did not see.
func remoteTest(ctx context.Context, t federate.Transport, argv ...string) (bool, error) {
	_, err := t.Run(ctx, argv...)
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// localDir creates and returns <store>/<project>/<host> (names checked by
// the caller). Every host gets its own directory: segment names carry
// the server's FQDN, but the server chooses its file names, so a shared
// directory would let one host overwrite another's segments.
//
// The store and the project directory must be this user's and writable
// by nobody else — whoever could write them could swap a host directory
// for a symlink and redirect rsync's writes. Each is checked before
// anything is created inside it, created 0700 when missing, and refused,
// never repaired, otherwise. The host directory itself is Fetch's: it
// forces it to 0700 after every transfer.
func (c collector) localDir(name, host string) (string, error) {
	projDir := filepath.Join(c.store, name)
	for _, d := range []string{c.store, projDir} {
		if err := ensureStoreDir(d); err != nil {
			return "", err
		}
	}
	dir := filepath.Join(projDir, host)
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("trace store: %w", err)
	}
	return dir, nil
}

// ensureStoreDir creates dir 0700 if it is missing, then checks it
// (checkStoreDir).
func ensureStoreDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("trace store: %w", err)
	}
	return checkStoreDir(dir)
}

// checkStoreDir requires dir to be a directory owned by this user and not
// group- or other-writable. The store root may be a symlink (another
// disk, a dotfile manager), so Stat follows it; reading is left to the
// operator, the segments themselves sit in 0700 host directories. Only
// the store and what is below it are checked: a -store under a directory
// others can write (and that lacks the sticky bit) is unsafe however the
// store itself looks (docs/security.md).
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

// countFiles counts dir's regular non-dot files, for collect's report
// only: temporaries and the k8s fetch lock are dot files. An unreadable
// dir counts as empty, and Fetch reports the real error.
func countFiles(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, en := range entries {
		if en.Type()&fs.ModeType == 0 && !strings.HasPrefix(en.Name(), ".") {
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
