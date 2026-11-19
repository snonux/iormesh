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

// MakeSpool creates the spool directory if needed and sets its mode. The
// path is opened as a directory without following a symlink, must belong
// to the tracer's own uid (a target user could otherwise have pre-created
// it and keep control of it), and gets its mode by fchmod on that
// descriptor, so nothing can be swapped in between the checks and the
// chmod.
func MakeSpool(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("spool: %w", err)
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|
		syscall.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("spool: %s: not a directory (or a symlink): %w", path, err)
	}
	defer func() { _ = syscall.Close(fd) }()
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return fmt.Errorf("spool: %w", err)
	}
	if st.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("spool: %s belongs to uid %d, not the tracer's %d", path, st.Uid,
			os.Geteuid())
	}
	// Mkdir's mode passes through the umask; fchmod sets it exactly
	// (0o1733: S_ISVTX plus the permission bits).
	if err := syscall.Fchmod(fd, syscall.S_ISVTX|uint32(SpoolMode.Perm())); err != nil {
		return fmt.Errorf("spool: %w", err)
	}
	return nil
}

// Scan opens the ring files that appeared since the last scan. A ring
// still being set up (ErrNotReady) is tried again at the next scan; a
// file that fails otherwise — not a ring, a header it cannot trust, an
// owner other than its JVM's, a pid that does not match its name — is
// reported once in skipped and never opened again. Files are remembered
// by name and inode, so a reused pid's new ring (the agent replaces a
// stale file, never reuses it) is opened too. err is a failure to read
// the directory itself.
func (d *Dir) Scan() (rings []*Ring, skipped []error, err error) {
	entries, err := os.ReadDir(d.path)
	if err != nil {
		return nil, nil, fmt.Errorf("spool: %w", err)
	}
	for _, e := range entries {
		key, ok := ringKey(e)
		if !ok || d.done[key] {
			continue
		}
		r, err := d.open(e.Name())
		if errors.Is(err, ErrNotReady) {
			continue
		}
		d.done[key] = true
		if err != nil {
			skipped = append(skipped, err)
			continue
		}
		rings = append(rings, r)
	}
	return rings, skipped, nil
}

// ringKey names a directory entry by name and inode; ok is false for a
// name that is not *.ring or an entry gone since the listing.
func ringKey(e os.DirEntry) (string, bool) {
	if !strings.HasSuffix(e.Name(), ".ring") {
		return "", false
	}
	info, err := e.Info()
	if err != nil {
		return "", false
	}
	var ino uint64
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		ino = st.Ino
	}
	return e.Name() + "\x00" + strconv.FormatUint(ino, 10), true
}

// open opens one ring and checks that its JVM wrote it: the file belongs
// to the user of the process its name names — checked on the descriptor
// before anything is mapped — and the header names the same pid. A file
// another user planted for someone else's pid would otherwise put
// made-up Java frames on that process's events. A JVM gone before the
// scan cannot be checked, so its ring is never read: a JVM that lives
// less than one scan interval (50 ms) after creating its ring stays
// attribution none.
func (d *Dir) open(name string) (*Ring, error) {
	pid, err := strconv.ParseUint(strings.TrimSuffix(name, ".ring"), 10, 32)
	if err != nil || pid == 0 {
		return nil, fmt.Errorf("spool: %s: not <pid>.ring: %w", name, ErrNotRing)
	}
	r, err := OpenOwned(filepath.Join(d.path, name), func(fileUID uint32) error {
		uid, alive := d.owner(pid)
		switch {
		case !alive:
			return fmt.Errorf("spool: %s: process gone, owner unverifiable: %w", name, ErrNotRing)
		case uid != fileUID:
			return fmt.Errorf("spool: %s: owned by uid %d, process by %d: %w", name, fileUID,
				uid, ErrNotRing)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if r.Header().PID != pid {
		err = fmt.Errorf("spool: %s: header says pid %d: %w", name, r.Header().PID, ErrNotRing)
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
