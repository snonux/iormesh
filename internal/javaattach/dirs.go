package javaattach

import (
	"errors"
	"fmt"
	"strconv"

	"golang.org/x/sys/unix"
)

// dirFlags open a directory for *at calls only.
const dirFlags = unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC

// jvmDirs are the two directories of the handshake, held open as fds:
// the JVM's working directory (where HotSpot looks for the trigger file
// first) and the /tmp of its root (the socket, and the trigger's
// fallback). Paths inside the target are the target's to plant symlinks
// in; resolved by name from /proc/<pid>/root they would lead the tracer,
// root, into the host's tree. So /tmp is opened with openat2's
// RESOLVE_IN_ROOT (and no symlinks at all), and every file in either
// directory is reached by one *at call relative to its fd, never
// re-resolved by path.
type jvmDirs struct {
	cwd int // -1 when the working directory cannot be opened
	tmp int
}

// openDirs opens pid's working directory and its root's /tmp. The cwd
// magic link is followed on purpose: it names the directory object the
// JVM runs in, not a path. A /tmp that is a symlink is refused.
func (a Attacher) openDirs(pid uint32) (jvmDirs, error) {
	d := jvmDirs{cwd: -1, tmp: -1}
	if fd, err := unix.Open(procPath(a.ProcRoot, pid, "cwd"), dirFlags, 0); err == nil {
		d.cwd = fd
	}
	root, err := unix.Open(procPath(a.ProcRoot, pid, "root"), dirFlags, 0)
	if err != nil {
		d.close()
		return d, fmt.Errorf("JVM root: %w", err)
	}
	defer func() { _ = unix.Close(root) }()
	d.tmp, err = unix.Openat2(root, "tmp", &unix.OpenHow{Flags: dirFlags,
		Resolve: unix.RESOLVE_IN_ROOT | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		d.tmp = -1
		d.close()
		return d, fmt.Errorf("the JVM's /tmp (a symlink is refused): %w", err)
	}
	return d, nil
}

func (d jvmDirs) close() {
	for _, fd := range []int{d.cwd, d.tmp} {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
	}
}

// statTmp lstats name in /tmp, never following a symlink.
func (d jvmDirs) statTmp(name string) (unix.Stat_t, error) {
	var st unix.Stat_t
	err := unix.Fstatat(d.tmp, name, &st, unix.AT_SYMLINK_NOFOLLOW)
	return st, err
}

// tmpPath names name in /tmp through the fd, for connect(2), which takes
// no directory fd: the walk through /proc/self/fd/N lands in the opened
// directory itself. The last component can still be swapped after
// checkSocket; checkPeer is what makes that harmless.
func (d jvmDirs) tmpPath(name string) string {
	return "/proc/self/fd/" + strconv.Itoa(d.tmp) + "/" + name
}

// checkSocket verifies name in /tmp is a socket, not a symlink or a
// planted file.
func (d jvmDirs) checkSocket(name string) error {
	st, err := d.statTmp(name)
	if err != nil {
		return fmt.Errorf("attach socket: %w", err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return fmt.Errorf("attach socket %s is not a socket", name)
	}
	return nil
}

// createTrigger creates the trigger file name in the working directory
// or, when that fails, in /tmp — where HotSpot's SIGQUIT handler looks,
// in that order — and returns what removes it again, through the same
// fd. O_EXCL|O_NOFOLLOW: a planted symlink is never followed and a file
// not created here is never removed. One there already is used only if
// it is a regular file of root or the JVM's user (euid), which the JVM
// accepts too; anything else refuses the attach before any signal.
func (d jvmDirs) createTrigger(name string, euid uint32) (remove func(), err error) {
	var errs []error
	for _, dir := range []int{d.cwd, d.tmp} {
		if dir < 0 {
			continue
		}
		fd, err := unix.Openat(dir, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|
			unix.O_CLOEXEC|unix.O_NONBLOCK, 0o600)
		if errors.Is(err, unix.EEXIST) {
			return func() {}, checkTrigger(dir, name, euid)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("trigger %s: %w", name, err))
			continue
		}
		_ = unix.Close(fd)
		return func() { _ = unix.Unlinkat(dir, name, 0) }, nil
	}
	return nil, errors.Join(append(errs, errors.New("no directory for the trigger file"))...)
}

// checkTrigger accepts an existing trigger file — another attacher's,
// which serves this attach too — only as a regular file owned by root or
// euid.
func checkTrigger(dir int, name string, euid uint32) error {
	var st unix.Stat_t
	if err := unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("trigger %s: %w", name, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || (st.Uid != 0 && st.Uid != euid) {
		return fmt.Errorf("%w: %s (mode %o, uid %d)", ErrForeignTrigger, name, st.Mode, st.Uid)
	}
	return nil
}
