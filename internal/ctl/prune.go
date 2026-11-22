package ctl

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"iormesh/internal/federate"
	"iormesh/internal/project"
	"iormesh/internal/segment"
)

// `iormeshctl prune -older-than DUR [-project P]... [-store DIR]
// [-apply]` (task yh4): delete old segments from the controller's trace
// store. The store is never cleaned on its own (PLAN.md M4 "Collect":
// nothing is auto-cleaned); prune is the operator's explicit command,
// and without -apply it only lists what it would delete.
//
// A file is the store's own only when it sits directly in a host
// directory, <store>/<project>/<host>/, and its name parses as a segment
// file (segment.ParseName); anything else is foreign and never touched:
// other names, dot files (rsync's in-flight temporaries, the fetch
// lock), symlinks and directories, and every entry of a project or host
// directory that is a symlink or not a valid name. A segment is old when
// its time range (federate.Span, from its name) ended before now−DUR —
// exactly the segments a build -last DUR would never take. A table file
// (methods, threads, classes, stacks) is old by its own name too, but
// it goes only once no events segment of its session stays in that
// directory: the union of a session's table files is its whole table,
// and a newer segment's events may name rows an older file holds.
//
// Deleting goes through an os.Root on the store, so a directory swapped
// for a symlink between the listing and the delete cannot lead outside
// it. The service database keeps what the deleted segments counted
// (each segment is its own source, task hb4), and the host pins stay.

// pruneFlags are prune's flags.
type pruneFlags struct {
	projects  projectList
	store     string
	olderThan string
	apply     bool
}

// pruneCommand parses prune's flags and lists, or with -apply deletes,
// the old segment files.
func pruneCommand(args []string, e env) error {
	fs := newFlagSet("prune", e.stderr)
	var f pruneFlags
	fs.Var(&f.projects, "project", "prune only this project (repeatable; default: every project in the store)")
	fs.StringVar(&f.store, "store", "", "local trace store (default ~/.local/share/iormesh/traces)")
	fs.StringVar(&f.olderThan, "older-than", "", "required: the segments whose time range ended "+
		"more than DUR ago (e.g. 720h)")
	fs.BoolVar(&f.apply, "apply", false, "delete them (default: only list what would be deleted)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%w: prune takes no arguments", errUsage)
	}
	age, err := time.ParseDuration(f.olderThan)
	if err != nil || age <= 0 {
		return fmt.Errorf("%w: prune needs -older-than DUR, a positive duration (720h)", errUsage)
	}
	for _, p := range f.projects {
		if !project.Valid(p) {
			return fmt.Errorf("%w: invalid project %q (%s)", errUsage, p, project.Rule)
		}
	}
	store, err := pruneStore(e, f.store)
	if err != nil {
		return err
	}
	cutoff := e.clock().Add(-age).UTC()
	files, err := planPrune(store, f.projects, cutoff)
	if err != nil {
		return err
	}
	return applyPrune(e, store, files, cutoff, f.apply)
}

// pruneStore is the store prune works on, -store or the default, held to
// collect's rule (owned, not writable by others).
func pruneStore(e env, flagged string) (string, error) {
	store := flagged
	if store == "" {
		var err error
		if store, err = e.traces(); err != nil {
			return "", err
		}
	}
	return store, checkStoreDir(store)
}

// planPrune lists the old segment files of projects (every valid,
// non-symlink project directory when none is given), as paths below
// store, sorted. A -project given twice is listed once; one that is not
// a real directory of the store (missing, a file, a symlink) is an error.
func planPrune(store string, projects []string, cutoff time.Time) ([]string, error) {
	if len(projects) == 0 {
		var err error
		if projects, err = realSubdirs(store, project.Valid); err != nil {
			return nil, fmt.Errorf("trace store: %w", err)
		}
	} else {
		projects = slices.Compact(slices.Sorted(slices.Values(projects)))
		for _, p := range projects {
			if fi, err := os.Lstat(filepath.Join(store, p)); err != nil || !fi.IsDir() {
				return nil, fmt.Errorf("prune: nothing collected for project %s in %s", p, store)
			}
		}
	}
	var out []string
	for _, p := range projects {
		hosts, err := realSubdirs(filepath.Join(store, p), federate.ValidHost)
		if err != nil {
			return nil, fmt.Errorf("trace store: %w", err)
		}
		for _, h := range hosts {
			old, err := oldInHost(filepath.Join(store, p, h), cutoff)
			if err != nil {
				return nil, fmt.Errorf("trace store: %w", err)
			}
			for _, name := range old {
				out = append(out, filepath.Join(p, h, name))
			}
		}
	}
	slices.Sort(out)
	return out, nil
}

// realSubdirs lists dir's subdirectories whose name passes valid; a
// symlink to a directory is not one (the listing does not follow it).
func realSubdirs(dir string, valid func(string) bool) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, en := range entries {
		if en.IsDir() && valid(en.Name()) {
			out = append(out, en.Name())
		}
	}
	return out, nil
}

// oldInHost lists the names of one host directory's old files: the
// regular, non-dot files whose name parses as a segment file and whose
// range ended by cutoff — a table file only when no events segment of
// its session stays.
func oldInHost(dir string, cutoff time.Time) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	stays := map[string]bool{} // sessions with an events segment that stays
	var old, tables []string
	session := map[string]string{}
	for _, en := range entries {
		name := en.Name()
		if strings.HasPrefix(name, ".") || !en.Type().IsRegular() {
			continue
		}
		f, err := segment.ParseName(name)
		if err != nil {
			continue
		}
		session[name] = f.FQDN + "-" + f.Session
		_, end := federate.Span(f)
		switch {
		case f.Table == "" && end.After(cutoff):
			stays[session[name]] = true
		case f.Table == "":
			old = append(old, name)
		case !end.After(cutoff):
			tables = append(tables, name)
		}
	}
	for _, name := range tables {
		if !stays[session[name]] {
			old = append(old, name)
		}
	}
	return old, nil
}

// applyPrune prints each file (paths below store) as it would be, or
// with apply is, deleted, then a total. A file already gone is skipped;
// any other failure is reported and the rest still go.
func applyPrune(e env, store string, files []string, cutoff time.Time, apply bool) error {
	when := cutoff.Format(time.RFC3339)
	if !apply {
		for _, rel := range files {
			if _, err := fmt.Fprintf(e.stdout, "would delete  %s\n", safe(rel)); err != nil {
				return err
			}
		}
		_, err := fmt.Fprintf(e.stdout, "%d segment files ended before %s; prune again with -apply to delete them\n",
			len(files), safe(when))
		return err
	}
	root, err := os.OpenRoot(store)
	if err != nil {
		return fmt.Errorf("trace store: %w", err)
	}
	defer func() { _ = root.Close() }() // only removals went through it
	var errs []error
	n := 0
	for _, rel := range files {
		if err := root.Remove(rel); err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, fmt.Errorf("prune: %w", err))
			}
			continue
		}
		n++
		if _, err := fmt.Fprintf(e.stdout, "deleted       %s\n", safe(rel)); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(e.stdout, "deleted %d segment files that ended before %s\n", n, safe(when)); err != nil {
		return err
	}
	return errors.Join(errs...)
}
