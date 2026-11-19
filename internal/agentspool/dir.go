package agentspool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Dir finds the agents' rings in the tracer's spool directory: one file
// per JVM, <pid>.ring (pid as the JVM sees it, so the JVM must share the
// tracer's pid namespace; containers are M7's). Each file is opened once.
type Dir struct {
	path string
	// owner returns the uid of process pid, false if it is gone; a
	// variable so tests need no other users' processes.
	owner func(pid uint64) (uint32, bool)
	done  map[string]bool // opened, or rejected for good
}

// NewDir scans path; it does not create it (the tracer does, see
// MakeSpool).
func NewDir(path string) *Dir {
	return &Dir{path: path, owner: procOwner, done: map[string]bool{}}
}

// SpoolMode is the spool directory's mode: every target user may create
// its ring there (write+search), none may list the others' or delete
// them (sticky), and the tracer, root, owns it.
const SpoolMode = 0o733 | os.ModeSticky

// MakeSpool creates the spool directory if needed and sets its mode. An
// existing path must be a directory, not a symlink.
func MakeSpool(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("spool: %w", err)
	}
	st, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("spool: %w", err)
	}
	if !st.IsDir() {
		return fmt.Errorf("spool: %s is not a directory", path)
	}
	// Mkdir's mode passes through the umask; Chmod sets it exactly.
	if err := os.Chmod(path, SpoolMode); err != nil {
		return fmt.Errorf("spool: %w", err)
	}
	return nil
}

// Scan opens the ring files that appeared since the last scan. A ring
// still being set up (ErrNotReady) is tried again at the next scan; a
// file that fails otherwise — not a ring, a header it cannot trust, an
// owner other than its JVM's, a pid that does not match its name — is
// reported once in skipped and never opened again. err is a failure to
// read the directory itself.
func (d *Dir) Scan() (rings []*Ring, skipped []error, err error) {
	entries, err := os.ReadDir(d.path)
	if err != nil {
		return nil, nil, fmt.Errorf("spool: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		if d.done[name] || !strings.HasSuffix(name, ".ring") {
			continue
		}
		r, err := d.open(name)
		if errors.Is(err, ErrNotReady) {
			continue
		}
		d.done[name] = true
		if err != nil {
			skipped = append(skipped, err)
			continue
		}
		rings = append(rings, r)
	}
	return rings, skipped, nil
}

// open opens one ring and checks that its JVM wrote it: the pid of the
// name is the header's, and the file belongs to that process's user. A
// file another user planted for someone else's pid would otherwise put
// made-up Java frames on that process's events.
func (d *Dir) open(name string) (*Ring, error) {
	pid, err := strconv.ParseUint(strings.TrimSuffix(name, ".ring"), 10, 32)
	if err != nil || pid == 0 {
		return nil, fmt.Errorf("spool: %s: not <pid>.ring: %w", name, ErrNotRing)
	}
	r, err := Open(filepath.Join(d.path, name))
	if err != nil {
		return nil, err
	}
	uid, alive := d.owner(pid)
	switch {
	case r.Header().PID != pid:
		err = fmt.Errorf("spool: %s: header says pid %d: %w", name, r.Header().PID, ErrNotRing)
	case !alive:
		err = fmt.Errorf("spool: %s: process gone, owner unverifiable: %w", name, ErrNotRing)
	case uid != r.Owner():
		err = fmt.Errorf("spool: %s: owned by uid %d, process by %d: %w", name, r.Owner(), uid,
			ErrNotRing)
	}
	if err != nil {
		return nil, errors.Join(err, r.Close())
	}
	return r, nil
}

// procOwner is the uid owning /proc/<pid>: the process's effective uid.
func procOwner(pid uint64) (uint32, bool) {
	st, err := os.Stat("/proc/" + strconv.FormatUint(pid, 10))
	if err != nil {
		return 0, false
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return sys.Uid, true
}
