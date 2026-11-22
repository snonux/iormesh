package agentspool

import (
	"errors"
	"fmt"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Removing ring files (task 2j4). A ring is 8 MiB by default, the
// deployed spool a tmpfs under /run, and every JVM start creates one, so
// a dead JVM's ring must go — but only a dead one's: a later session
// reads a live JVM's ring again (task 5j4). Three parties remove: the
// agent its own ring in its shutdown hook (after its last watermark), the
// Java join a ring whose JVM's thread group died once it read it for the
// last time (Ring.Remove), and the spool scan a ring whose process is
// gone when it first sees it (Dir.Scan) — a JVM killed by SIGKILL, or
// one that died while no session ran.
//
// The spool is sticky and writable by every target user (SpoolMode) and
// the tracer is root, which may unlink any entry there. So nothing is
// unlinked by a name resolved twice blindly: the entry is looked at with
// fstatat through a descriptor of the directory, without following a
// symlink, and unlinked only if it is a regular file with one link and
// the very inode the caller means — the one the ring mapped, or the one
// the scan listed. A new JVM that reused the pid and replaced the file
// (the agent deletes a stale ring and creates a new inode, never reuses
// it) keeps its ring. Between the fstatat and the unlinkat only the
// file's owner (or root) can swap the entry, and only after the old one
// is gone, so the window could cost at most a ring its own user planted
// there in that instant. A tracer that still maps a removed ring keeps
// reading it: unlink does not touch a mapping.

// Remove unlinks the ring's file if its name still names the inode the
// ring mapped; a missing file or another inode under the name is not an
// error. Call it only once the JVM is dead: its agent never recreates a
// ring it lost, and a later session would find nothing to read. The
// mapping stays valid until Close.
func (r *Ring) Remove() error {
	if r.path == "" {
		return nil
	}
	return unlinkIf(filepath.Dir(r.path), filepath.Base(r.path), func(st *unix.Stat_t) bool {
		return uint64(st.Dev) == r.dev && uint64(st.Ino) == r.ino
	})
}

// removeDead unlinks a ring of process pid, which the scan found gone,
// if name still names the inode the listing showed and the process is
// still gone after the entry was looked at.
func (d *Dir) removeDead(name string, pid, ino uint64) error {
	return unlinkIf(d.path, name, func(st *unix.Stat_t) bool {
		_, alive := d.owner(pid)
		return uint64(st.Ino) == ino && !alive
	})
}

// unlinkIf unlinks name in dir when it is a regular file with one link
// that ok accepts, looked at and unlinked through one descriptor of dir
// (see the comment at the top of this file). dir is the spool's path,
// which may be /proc/self/fd/<n> for a staged spool: the descriptor
// then refers to that held directory.
func unlinkIf(dir, name string, ok func(st *unix.Stat_t) bool) error {
	dfd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("agentspool: remove %s: %w", name, err)
	}
	defer func() { _ = unix.Close(dfd) }()
	var st unix.Stat_t
	err = unix.Fstatat(dfd, name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("agentspool: remove %s: %w", name, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 || !ok(&st) {
		return nil
	}
	if err := unix.Unlinkat(dfd, name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("agentspool: remove %s: %w", name, err)
	}
	return nil
}
