package segment

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Layout of a project directory, <trace-dir>/<project>/ (PLAN.md M4
// "Trace files"): the active segment's files at the top as .active-*,
// published segments in ready/.
const (
	// ReadyDir is the sync contract: files there are immutable and final.
	ReadyDir = "ready"
	// Ext is every segment file's extension.
	Ext = ".parquet"
	// TimeLayout is <start-ts> in segment names: UTC, second resolution,
	// sortable as text.
	TimeLayout = "20060102T150405Z"
	// activePrefix names the files of the segment being written. A dot
	// file, so `ls` and a glob over the directory skip it.
	activePrefix = ".active-"
	// lockFile is the project directory's writer lock (lockDir).
	lockFile = ".lock"
)

// Table names, the infix of a table file's name (TableName).
const (
	TableMethods = "methods"
	TableThreads = "threads"
	TableClasses = "classes"
	TableStacks  = "stacks"
)

// Owner is the user ready/ belongs to: the one who deployed the session
// (`start` passes SUDO_UID/SUDO_GID on as -ready-owner), so the
// controller's unprivileged rsync can read and delete published segments.
type Owner struct {
	UID, GID int
}

// Name is the file name of a segment's events,
// <fqdn>-<session>-<start-ts>-<seq>.parquet. The fqdn may itself contain
// dashes, so a reader parses a name from the right: seq (six digits or
// more), start-ts (TimeLayout), session (no dash), then the fqdn.
func Name(fqdn, session string, start time.Time, seq int) string {
	return base(fqdn, session, start, seq) + Ext
}

// TableName is the file name of one of a segment's tables, the events
// name with the table before the extension:
// <fqdn>-<session>-<start-ts>-<seq>.methods.parquet.
func TableName(fqdn, session string, start time.Time, seq int, table string) string {
	return base(fqdn, session, start, seq) + "." + table + Ext
}

func base(fqdn, session string, start time.Time, seq int) string {
	return fmt.Sprintf("%s-%s-%s-%06d", fqdn, session, start.UTC().Format(TimeLayout), seq)
}

// nameChars are the only characters a name part may hold: letters,
// digits, '-', '.', '_' and ':'. A traced host chooses its FQDN and
// session, the controller builds refs from them that it offers inside a
// shell command (`iormeshctl query -ref`) and prints them to terminals,
// so a part must be inert there (task 3i4); no '/' or NUL either, so it
// never leaves its directory. '_' is as inert and Linux accepts it in a
// hostname. Everything an honest tracer writes fits: a hostname, a pod's
// <name>.<namespace>.<uid[:8]> (k8s.PodFQDN), an 8-hex session id.
const nameChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._:"

// notNameChar reports whether r is outside nameChars.
func notNameChar(r rune) bool { return !strings.ContainsRune(nameChars, r) }

// validName rejects a name part that holds anything but nameChars, would
// leave the directory or would break the right-to-left parse of Name.
// The writer checks its config with it and ParseName every name it
// reads, so a name the controller would refuse is never published, and
// one a hostile server planted is never trusted.
func validName(what, s string, dashOK bool) error {
	switch {
	case s == "" || s == "." || s == "..":
		return fmt.Errorf("segment: %s %q: empty or a dot name", what, s)
	case strings.IndexFunc(s, notNameChar) >= 0:
		return fmt.Errorf("segment: %s %q: holds a character outside [A-Za-z0-9_.:-]", what, s)
	case !dashOK && strings.Contains(s, "-"):
		return fmt.Errorf("segment: %s %q: contains -", what, s)
	}
	return nil
}

// prepareDir creates the trace dir, the project directory and ready/
// and checks the two above ready/ before trusting them. The tracer runs
// as root: if another user owned (or could write) the trace dir or the
// project directory, they could swap ready/ for a symlink after the
// check below and have root publish — and rename over — files wherever
// it points. So both must be real directories owned by the tracer's
// euid and not group- or other-writable; a planted directory stops the
// start instead of being repaired. A directory prepareDir creates is set
// 0755 whatever the umask (077 would lock the collector out); one that
// already exists is never chmod'ed (MakeDirs), so a trace dir pointed at
// an existing root-owned tree cannot loosen it. ready/ itself must be a
// real directory too, then goes to owner.
func prepareDir(dir string, owner *Owner) error {
	ready := filepath.Join(dir, ReadyDir)
	if err := MakeDirs(ready); err != nil {
		return err
	}
	for _, d := range []string{filepath.Dir(dir), dir} {
		if err := trustedDir(d); err != nil {
			return err
		}
	}
	if err := realDir(ready); err != nil {
		return err
	}
	if owner == nil {
		return nil
	}
	if err := os.Lchown(ready, owner.UID, owner.GID); err != nil {
		return fmt.Errorf("segment: ready owner: %w", err)
	}
	return nil
}

// MakeDirs creates path and its missing parents, each it creates set
// 0755 exactly (Mkdir's mode passes through the umask). An existing
// directory is left as it is — never chmod'ed: run as root, a chmod of
// whatever directory a flag names would be a way to loosen any tree. A
// symlinked parent (/var/run) is followed; whether the trace and project
// directories may be symlinks is prepareDir's check (they may not).
func MakeDirs(path string) error {
	path = filepath.Clean(path)
	if fi, err := os.Stat(path); err == nil {
		if !fi.IsDir() {
			return fmt.Errorf("segment: %s is not a directory", path)
		}
		return nil
	}
	if parent := filepath.Dir(path); parent != path {
		if err := MakeDirs(parent); err != nil {
			return err
		}
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil // created meanwhile: not ours, so not chmod'ed
		}
		return fmt.Errorf("segment: %w", err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		return fmt.Errorf("segment: %w", err)
	}
	return nil
}

// trustedDir: a real directory, owned by the euid, not writable by group
// or others (see prepareDir).
func trustedDir(d string) error {
	fi, err := os.Lstat(d)
	if err != nil {
		return fmt.Errorf("segment: %w", err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	switch {
	case !fi.IsDir():
		return fmt.Errorf("segment: %s is not a directory", d)
	case !ok || int(st.Uid) != os.Geteuid():
		return fmt.Errorf("segment: %s is not owned by uid %d", d, os.Geteuid())
	case fi.Mode().Perm()&0o022 != 0:
		return fmt.Errorf("segment: %s is group- or other-writable (%v)", d, fi.Mode().Perm())
	}
	return nil
}

// realDir rejects a symlink (or anything else) where a directory must be:
// a symlinked ready/ would publish segments wherever it points, as root.
func realDir(d string) error {
	fi, err := os.Lstat(d)
	if err != nil {
		return fmt.Errorf("segment: %w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("segment: %s is not a directory", d)
	}
	return nil
}

// lockDir takes an exclusive flock on dir/.lock, without waiting: one
// writer per project directory. A second run of the same project would
// otherwise sweep the first run's live active files and the two would
// publish each other's half-written segments. The lock dies with the
// process, so a crashed run never blocks the next start.
func lockDir(dir string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dir, lockFile), os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("segment: lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("segment: %s is in use by another run of this project", dir)
		}
		return nil, fmt.Errorf("segment: lock: %w", err)
	}
	return f, nil
}

// sweepActive removes active files a crashed or killed run left in dir.
// It runs under lockDir's lock, so no live writer owns them. They have
// no footer, so no reader can open them; the minute they held
// is the loss PLAN.md accepts for a crash.
func sweepActive(dir string) error {
	stale, err := filepath.Glob(filepath.Join(dir, activePrefix+"*"))
	if err != nil {
		return fmt.Errorf("segment: %w", err)
	}
	var errs []error
	for _, p := range stale {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("segment: stale active file: %w", err))
		}
	}
	return errors.Join(errs...)
}

// syncDir fsyncs a directory, so the renames into it survive a crash.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("segment: %w", err)
	}
	return errors.Join(d.Sync(), d.Close())
}
