package javaattach

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"golang.org/x/sys/unix"

	"iormesh/internal/agentspool"
)

// Staging the agent inside a JVM's root (decision #14, M7 step 3, task
// cf4). A JVM resolves the jar and spool paths of its load request in its
// own mount namespace, and an ephemeral container cannot give the app
// container a volume, so for a JVM in another mount namespace the tracer
// creates, under the JVM's /tmp:
//
//	/tmp/iormesh-<random>/            root's, 0755
//	/tmp/iormesh-<random>/iormesh-agent.jar   root's, 0644
//	/tmp/iormesh-<random>/spool/      root's, 1733 (agentspool.SpoolMode)
//
// The JVM's filesystem is its users' to tamper with, and the tracer is
// root, so every step goes through descriptors opened from
// /proc/<pid>/root — /tmp with RESOLVE_IN_ROOT and no symlinks (openTmp),
// the rest by one *at call relative to a held fd, never a path walked
// again. The directory is created exclusively (an existing name is never
// adopted: the JVM's user may have planted it) under a random name, so a
// planted name costs one retry, not the attach; the jar is created
// O_EXCL|O_NOFOLLOW in it. The tracer's trust rule for the jar
// (cli.trustedAgent) is kept inside the JVM's root: / and /tmp must be
// root's or the JVM's user's, and not writable by group or others unless
// sticky — otherwise another user could rename the directory away and
// put a jar of their own at the path the JVM loads.

const (
	// stageJar and stageSpool are the stage's two entries.
	stageJar   = "iormesh-agent.jar"
	stageSpool = "spool"
	// stageTries bounds the fresh names tried when one is taken.
	stageTries = 8
)

var (
	// ErrUntrustedTmp: the JVM's / or /tmp belongs to another user, or
	// is writable by group or others without the sticky bit, so the
	// stage could be swapped under the JVM.
	ErrUntrustedTmp = errors.New("the JVM's / or /tmp is not safe to stage the agent in")
	// ErrNoStage: no stage directory could be made in the JVM's /tmp
	// (read-only, full, or every name tried taken).
	ErrNoStage = errors.New("cannot stage the agent in the JVM's /tmp")
)

// stageName returns a fresh, unguessable stage directory name; a
// variable so tests can plant the names it returns.
var stageName = func() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "iormesh-" + hex.EncodeToString(b[:]), nil
}

// Stage is the agent jar and a spool staged inside one JVM's root. It
// holds the JVM's /tmp, the stage directory and the spool open until
// Remove; a JVM of the same mount namespace and user can share it.
type Stage struct {
	tmp, dir, spool int // -1 when not open
	name            string
}

// Stage stages jar (the tracer's own trusted copy, by its resolved path)
// inside pid's root. A process that is no JVM gets ErrNotJVM before
// anything is created; an untrusted / or /tmp gets ErrUntrustedTmp; a
// /tmp nothing can be created in gets ErrNoStage.
func (a Attacher) Stage(pid uint32, jar string) (*Stage, error) {
	info, err := a.checkJVM(pid)
	if err != nil {
		return nil, fmt.Errorf("stage pid %d: %w", pid, err)
	}
	root, err := a.openRoot(pid)
	if err != nil {
		return nil, fmt.Errorf("stage pid %d: %w", pid, err)
	}
	defer func() { _ = unix.Close(root) }()
	s := &Stage{tmp: -1, dir: -1, spool: -1}
	if err := s.build(root, info.euid, jar); err != nil {
		return nil, errors.Join(fmt.Errorf("stage pid %d: %w", pid, err), s.Remove())
	}
	return s, nil
}

// checkJVM is what staging needs of the target: a JVM, and its euid.
func (a Attacher) checkJVM(pid uint32) (procInfo, error) {
	jvm, err := mapsJVM(a.ProcRoot, pid)
	switch {
	case err != nil:
		return procInfo{}, err
	case !jvm:
		return procInfo{}, ErrNotJVM
	}
	return readProcInfo(a.ProcRoot, pid)
}

// build checks / and /tmp, then makes the directory, the jar and the
// spool; Remove undoes whatever part of it was done.
func (s *Stage) build(root int, euid uint32, jar string) error {
	if err := trustedDir(root, "/", euid); err != nil {
		return err
	}
	var err error
	if s.tmp, err = openTmp(root); err != nil {
		return err
	}
	if err := trustedDir(s.tmp, "/tmp", euid); err != nil {
		return err
	}
	if err := s.makeDir(); err != nil {
		return err
	}
	if err := copyJar(s.dir, stageJar, jar); err != nil {
		return err
	}
	s.spool, err = agentspool.MakeSpoolAt(s.dir, stageSpool)
	return err
}

// trustedDir applies the jar's trust rule to one directory of the JVM's
// root: owned by root or the JVM's user (who can harm only its own JVM),
// and not writable by group or others unless sticky (then nobody else may
// rename root's entry).
func trustedDir(fd int, name string, euid uint32) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if st.Uid != 0 && st.Uid != euid {
		return fmt.Errorf("%w: %s belongs to uid %d, neither root nor the JVM's %d",
			ErrUntrustedTmp, name, st.Uid, euid)
	}
	if st.Mode&0o022 != 0 && st.Mode&unix.S_ISVTX == 0 {
		return fmt.Errorf("%w: %s is writable by group or others and not sticky (mode %o)",
			ErrUntrustedTmp, name, st.Mode&0o7777)
	}
	return nil
}

// makeDir creates the stage directory under a fresh name, skipping names
// already taken (never adopting one), and opens it.
func (s *Stage) makeDir() error {
	for range stageTries {
		name, err := stageName()
		if err != nil {
			return err
		}
		err = unix.Mkdirat(s.tmp, name, 0o700)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return fmt.Errorf("%w: mkdir /tmp/%s: %v", ErrNoStage, name, err)
		}
		s.name = name
		return s.openDir()
	}
	return fmt.Errorf("%w: %d fresh names were all taken", ErrNoStage, stageTries)
}

// openDir opens the directory makeDir created, checks it is the
// tracer's (with /tmp trusted nobody else can have swapped it, but the
// check costs one fstat) and gives it 0755 exactly, past the umask, so
// the JVM's user can reach the jar and the spool.
func (s *Stage) openDir() error {
	fd, err := unix.Openat(s.tmp, s.name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|
		unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open /tmp/%s: %w", s.name, err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return err
	}
	if st.Uid != uint32(os.Geteuid()) {
		// Not the directory mkdirat made: neither it nor anything in it
		// is touched again, Remove included.
		_ = unix.Close(fd)
		name := s.name
		s.name = ""
		return fmt.Errorf("%w: /tmp/%s belongs to uid %d", ErrUntrustedTmp, name, st.Uid)
	}
	s.dir = fd
	return unix.Fchmod(fd, 0o755)
}

// copyJar copies the jar at src into dirfd as name: created there, never
// an existing file or a symlink followed, and readable by every user.
func copyJar(dirfd int, name, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("agent jar: %w", err)
	}
	defer func() { _ = in.Close() }()
	if st, err := in.Stat(); err != nil || !st.Mode().IsRegular() {
		return fmt.Errorf("agent jar %s: not a regular file", src)
	}
	fd, err := unix.Openat(dirfd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|
		unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("stage %s: %w", name, err)
	}
	out := os.NewFile(uintptr(fd), name)
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Chmod(0o644)
	}
	return errors.Join(err, out.Close())
}

// Jar is the staged jar's path as the JVM sees it.
func (s *Stage) Jar() string { return "/tmp/" + s.name + "/" + stageJar }

// Spool is the staged spool's path as the JVM sees it: the agent's
// spool= option.
func (s *Stage) Spool() string { return "/tmp/" + s.name + "/" + stageSpool }

// ReadPath is the staged spool's path as the tracer reads it: its held
// descriptor, for agentspool.NewDir and the ring check.
func (s *Stage) ReadPath() string { return "/proc/self/fd/" + strconv.Itoa(s.spool) }

// Remove deletes the stage, through the held descriptors only: the rings
// the agents wrote (each JVM's agent keeps writing into its mapping of
// the unlinked file, read by no one, until the JVM exits), the spool, the
// jar and the directory. Entries a user made inside the spool are
// unlinked, an empty directory removed; nothing is followed or recursed
// into, so a non-empty one leaves the stage behind, reported. Remove is
// idempotent and closes the descriptors.
func (s *Stage) Remove() error {
	var errs []error
	if s.spool >= 0 {
		errs = append(errs, emptyDir(s.spool), unix.Close(s.spool))
		s.spool = -1
	}
	if s.dir >= 0 {
		errs = append(errs, unlinkAt(s.dir, stageSpool, unix.AT_REMOVEDIR),
			unlinkAt(s.dir, stageJar, 0), unix.Close(s.dir))
		s.dir = -1
	}
	if s.tmp >= 0 {
		if s.name != "" {
			errs = append(errs, unlinkAt(s.tmp, s.name, unix.AT_REMOVEDIR))
		}
		errs = append(errs, unix.Close(s.tmp))
		s.tmp = -1
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("remove stage /tmp/%s: %w", s.name, err)
	}
	return nil
}

// emptyDir unlinks every entry of the directory fd, never following one.
func emptyDir(fd int) error {
	dup, err := unix.Openat(fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(dup), "spool")
	entries, err := f.ReadDir(-1)
	errs := []error{err, f.Close()}
	for _, e := range entries {
		err := unix.Unlinkat(fd, e.Name(), 0)
		if errors.Is(err, unix.EISDIR) {
			err = unix.Unlinkat(fd, e.Name(), unix.AT_REMOVEDIR)
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// unlinkAt is unlinkat with an entry already gone counted as removed.
func unlinkAt(dirfd int, name string, flags int) error {
	if err := unix.Unlinkat(dirfd, name, flags); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
