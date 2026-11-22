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

// Dir finds the agents' rings in a spool directory: one file per JVM,
// <pid>.ring (pid as the JVM sees it, so the JVM must share the tracer's
// pid namespace), or <pid>.<hex>.ring when the agent could not delete a
// stale <pid>.ring another user owns (task 9j4, ringPID). The directory is the tracer's -spool, or one staged
// inside the root of a JVM in another mount namespace (decision #14),
// which the tracer names /proc/self/fd/<n>: a descriptor it opened
// through /proc/<pid>/root, so nothing in the JVM's tree is resolved by
// name again. Each file is opened once.
type Dir struct {
	path string
	// owner returns the uid of process pid, false if it is gone; a
	// variable so tests need no other users' processes.
	owner func(pid uint64) (uint32, bool)
	done  map[string]bool // opened, or rejected for good
}

// NewDir scans path; it does not create it (the tracer does, see
// MakeSpool and MakeSpoolAt).
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
	return setSpoolMode(fd, path)
}

// MakeSpoolAt creates the spool directory name in the directory dirfd
// and returns it open (O_RDONLY, close-on-exec), for reading through
// /proc/self/fd (decision #14: a spool staged inside another mount
// namespace's root is reached only through descriptors). Unlike MakeSpool
// it never adopts an existing entry: the caller made dirfd itself, so
// anything already there was planted. The same owner check and fchmod as
// MakeSpool follow, on the descriptor.
func MakeSpoolAt(dirfd int, name string) (int, error) {
	if err := syscall.Mkdirat(dirfd, name, 0o700); err != nil {
		return -1, fmt.Errorf("spool: %s: %w", name, err)
	}
	fd, err := syscall.Openat(dirfd, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|
		syscall.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("spool: %s: not a directory (or a symlink): %w", name, err)
	}
	if err := setSpoolMode(fd, name); err != nil {
		_ = syscall.Close(fd)
		return -1, err
	}
	return fd, nil
}

// setSpoolMode checks that the open spool directory fd belongs to the
// tracer's own uid and gives it SpoolMode exactly.
func setSpoolMode(fd int, path string) error {
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
// stale file, never reuses it) is opened too. A ring whose process is
// gone is removed instead (task 2j4, remove.go): its JVM died without
// its shutdown hook, or while no session ran, and nothing can verify,
// read or delete it otherwise — a failed removal is reported in skipped.
// err is a failure to read the directory itself.
func (d *Dir) Scan() (rings []*Ring, skipped []error, err error) {
	entries, err := os.ReadDir(d.path)
	if err != nil {
		return nil, nil, fmt.Errorf("spool: %w", err)
	}
	for _, e := range entries {
		key, ino, ok := ringKey(e)
		if !ok || d.done[key] {
			continue
		}
		if pid, ok := ringPID(e.Name()); ok {
			if _, alive := d.owner(pid); !alive {
				d.done[key] = true
				if err := d.removeDead(e.Name(), pid, ino); err != nil {
					skipped = append(skipped, err)
				}
				continue
			}
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

// ringKey names a directory entry by name and inode (lstat: a symlink's
// own) and returns the inode; ok is false for a name that is not *.ring
// or an entry gone since the listing.
func ringKey(e os.DirEntry) (key string, ino uint64, ok bool) {
	if !strings.HasSuffix(e.Name(), ".ring") {
		return "", 0, false
	}
	info, err := e.Info()
	if err != nil {
		return "", 0, false
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		ino = st.Ino
	}
	return e.Name() + "\x00" + strconv.FormatUint(ino, 10), ino, true
}

// ringPID is the pid a ring's name names: <pid>.ring, or <pid>.<hex>.ring
// (task 9j4) — the name the agent falls back to when it cannot delete a
// stale <pid>.ring that another user owns (or planted) in the sticky
// spool, so a planted file cannot keep its JVM's agent from installing.
// The suffix is the agent's random tag, 1 to 32 lowercase hex digits; the
// pid is as untrusted as in the plain name and passes the same owner and
// header checks (open). ok is false for any other name.
func ringPID(name string) (uint64, bool) {
	base, ok := strings.CutSuffix(name, ".ring")
	if !ok {
		return 0, false
	}
	if p, tag, suffixed := strings.Cut(base, "."); suffixed {
		if !isTag(tag) {
			return 0, false
		}
		base = p
	}
	pid, err := strconv.ParseUint(base, 10, 32)
	return pid, err == nil && pid != 0
}

// HasRing reports whether dir holds a regular file (not a symlink) named
// as a ring of pid, <pid>.ring or <pid>.<hex>.ring. It checks a name, not
// a ring: `run -agent` uses it to tell a load that installed the agent
// from one whose agent gave up, and the scan still checks every file it
// opens (open).
func HasRing(dir string, pid uint64) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if p, ok := ringPID(e.Name()); ok && p == pid && e.Type().IsRegular() {
			return true
		}
	}
	return false
}

// isTag reports whether s is a ring name's random suffix: 1 to 32
// lowercase hex digits.
func isTag(s string) bool {
	if len(s) == 0 || len(s) > 32 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// open opens one ring and checks that its JVM wrote it: the file belongs
// to the user of the process its name names — checked on the descriptor
// before anything is mapped — and the header names the same pid, so the
// header's pid is trusted only once the owner check passed and only when
// it is the pid whose process the file's owner runs. A suffixed name
// (<pid>.<hex>.ring, task 9j4) gets the same checks. A file
// another user planted for someone else's pid would otherwise put
// made-up Java frames on that process's events. A JVM gone before the
// scan cannot be checked, so its ring is never read (Scan removes it): a
// JVM that lives less than one scan interval (50 ms) after creating its
// ring stays attribution none. One gone between Scan's check and the
// open is refused here, and its ring is left to a later session's scan.
func (d *Dir) open(name string) (*Ring, error) {
	pid, ok := ringPID(name)
	if !ok {
		return nil, fmt.Errorf("spool: %s: not <pid>.ring or <pid>.<hex>.ring: %w", name, ErrNotRing)
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
