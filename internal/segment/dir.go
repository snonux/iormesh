package segment

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// validName rejects a name part that would leave the directory or break
// the right-to-left parse of Name.
func validName(what, s string, dashOK bool) error {
	switch {
	case s == "" || s == "." || s == "..":
		return fmt.Errorf("segment: %s %q: empty or a dot name", what, s)
	case strings.ContainsAny(s, "/\x00"):
		return fmt.Errorf("segment: %s %q: contains / or NUL", what, s)
	case !dashOK && strings.Contains(s, "-"):
		return fmt.Errorf("segment: %s %q: contains -", what, s)
	}
	return nil
}

// prepareDir creates the project directory and ready/ (0755) and hands
// ready/ to owner. ready/ must be a real directory: a symlink there
// would publish segments wherever it points, as root.
func prepareDir(dir string, owner *Owner) error {
	ready := filepath.Join(dir, ReadyDir)
	if err := os.MkdirAll(ready, 0o755); err != nil {
		return fmt.Errorf("segment: %w", err)
	}
	fi, err := os.Lstat(ready)
	if err != nil {
		return fmt.Errorf("segment: %w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("segment: %s is not a directory", ready)
	}
	if owner == nil {
		return nil
	}
	if err := os.Lchown(ready, owner.UID, owner.GID); err != nil {
		return fmt.Errorf("segment: ready owner: %w", err)
	}
	return nil
}

// sweepActive removes active files a crashed or killed run left in dir.
// They have no footer, so no reader can open them; the minute they held
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
