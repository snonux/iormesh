package cli

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"

	"aead.dev/minisign"

	"iormesh/internal/release"
	"iormesh/internal/version"
)

// `iormesh upgrade FILE` replaces the installed binary with a staged one
// (PLAN.md decisions #1 and #28). It runs through the one sudoers rule, so
// it is the most sensitive subcommand there is: whoever controls what
// lands at installedBinary owns every later sudo call. What makes it safe
// to offer a deploying user:
//
//   - the only input is the staged file (and FILE.minisig next to it);
//     the key, the install path and the layout are fixed, not flags;
//   - the signature must verify against the root-owned pinned key
//     (pinnedKeyRel), with the trusted comment naming this host's tracer
//     (release.TracerFile) — not iormeshctl, not the other arch — or the
//     Java agent jar (release.AgentFile, task kf4): the signed name, not
//     the staged file's, decides what is installed where;
//   - an agent jar's signed version must equal the running tracer's
//     (upgrade_agent.go): the jar and the tracer speak one ring format,
//     and an older signed jar may carry a fixed hole like an older tracer;
//   - the signed version may not be older than the installed binary's,
//     unless root itself, not the rule, passes -allow-downgrade: an old
//     signed build may carry a fixed hole. Through the rule a same
//     version with other bytes is refused too, so every signed build a
//     caller can install bumps Version. The check and the rename run
//     under an exclusive lock on bin/, and through the rule only while
//     the installed binary is still the one this process runs — else an
//     upgrade started (and stopped, ^Z) before a fix could install the
//     old build over it (lockedInstall);
//   - verification happens on bytes read into memory once, and exactly
//     those bytes are installed: the staged file is the caller's and may
//     change after the check;
//   - the install is a fresh root-owned 0755 regular file renamed over
//     installedBinary in its root-only directory — never through a
//     symlink, and only into the layout docs/security.md requires.
//
// Running sessions keep the old binary (their inode); the next start runs
// the new one. The agent jar lands at agentRel, in its own root-only
// directory, readable by every JVM user; a confined session's -agent may
// name only that file (confine.go), so a deployed session loads exactly
// the jar that verified here.
const (
	// pinnedKeyRel is the pinned release key under installRoot: the
	// root-owned server copy of the controller's key (decision #28).
	pinnedKeyRel = "etc/minisign.pub"
	// binRel is the installed binary under installRoot.
	binRel = "bin/iormesh"
	// agentRel is the installed Java agent jar under installRoot (task
	// kf4): lib/ is created by its first upgrade, root's and 0755, so a
	// bootstrap made before the jar shipped needs nothing new.
	agentRel = "lib/" + release.AgentFile
)

// upgrader is one upgrade, its environment injected so tests run on a
// temporary layout their own uid owns.
type upgrader struct {
	// root is installRoot; owner the uid that must own it (0), group the
	// gid the new binary gets (0).
	root         string
	owner, group int
	// sudoUID is the sudo caller (-1 without sudo), who must own the
	// staged file (stagedCheck).
	sudoUID int
	// gated is sudoGated: this run came through the sudoers rule.
	gated bool
	// running is this binary's version, goarch its arch.
	running, goarch string
	// self stats this process's own executable (/proc/self/exe, which
	// still names the old inode after a rename over it).
	self   func() (fs.FileInfo, error)
	stdout io.Writer
}

// newUpgrader is the production upgrader for this process.
func newUpgrader(stdout io.Writer) upgrader {
	self, err := os.Executable()
	if err != nil {
		self = "" // unknown: sudoGated fails closed
	}
	u := upgrader{root: installRoot, owner: 0, group: 0, sudoUID: -1, gated: sudoGated(self, os.Getenv, os.Lstat),
		running: version.Version, goarch: runtime.GOARCH, stdout: stdout,
		self: func() (fs.FileInfo, error) { return os.Stat("/proc/self/exe") }}
	if n, err := strconv.Atoi(os.Getenv("SUDO_UID")); err == nil {
		u.sudoUID = n
	}
	return u
}

// upgradeCommand parses `upgrade [-allow-downgrade] FILE` and runs it.
func upgradeCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	flags.SetOutput(stderr)
	allowDowngrade := flags.Bool("allow-downgrade", false,
		"install an older signed build (root only, not through the sudoers rule)")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("%w: upgrade takes one staged file", errUsage)
	}
	if os.Geteuid() != 0 {
		return errors.New("upgrade replaces the root-owned " + installedBinary + "; run it through sudo")
	}
	return newUpgrader(stdout).upgrade(flags.Arg(0), *allowDowngrade)
}

// upgrade verifies staged and installs it as the tracer or the agent jar,
// whichever its signed name says. Nothing under root is written before the
// signature, the artifact name and the version all passed.
func (u upgrader) upgrade(staged string, allowDowngrade bool) error {
	if allowDowngrade && u.gated {
		return fmt.Errorf("%w: -allow-downgrade is not allowed through the sudoers rule; run it as root directly",
			errUsage)
	}
	if err := u.checkLayout(); err != nil {
		return fmt.Errorf("upgrade: install layout: %w", err)
	}
	key, err := release.ReadKeyFile(filepath.Join(u.root, pinnedKeyRel), u.owner)
	if err != nil {
		return fmt.Errorf("upgrade: %w", err)
	}
	data, m, err := u.verifyStaged(key, staged)
	if err != nil {
		return fmt.Errorf("upgrade: %w", err)
	}
	agent := m.File == release.AgentFile
	if agent && allowDowngrade {
		return fmt.Errorf("%w: -allow-downgrade applies to the tracer only; an agent jar must match it", errUsage)
	}
	// One lock on bin/ serializes every upgrade, tracer or jar: a jar's
	// version is checked against the installed tracer, which must not
	// change under the check.
	unlock, err := u.lock()
	if err != nil {
		return fmt.Errorf("upgrade: %w", err)
	}
	defer unlock()
	if agent {
		return u.lockedInstallAgent(m, data)
	}
	return u.lockedInstall(m, data, allowDowngrade)
}

// lock takes an exclusive flock on bin/, held until the returned func
// runs: two upgrades (two rule calls, or one stopped with ^Z while root
// upgrades) check and rename one after the other, never interleaved.
func (u upgrader) lock() (func(), error) {
	d, err := os.Open(filepath.Dir(u.bin()))
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(d.Fd()), syscall.LOCK_EX); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("lock %s: %w", d.Name(), err)
	}
	return func() { _ = d.Close() }, nil // closing releases the lock
}

// lockedInstall is the version check and the install, under lock. The
// version compared is the running binary's, so through the rule the
// installed binary must still be that very file (same device and inode):
// if another upgrade replaced it since this process started, its version
// is unknown here and the call is refused, to be rerun by the new binary.
func (u upgrader) lockedInstall(m release.Meta, data []byte, allowDowngrade bool) error {
	if err := u.checkInstalled(); err != nil {
		return fmt.Errorf("upgrade: install layout: %w", err)
	}
	same, err := u.checkVersion(m, data, allowDowngrade)
	if err != nil || same {
		if same {
			_, _ = fmt.Fprintf(u.stdout, "iormesh: %s %s is already installed\n", m.File, m.Version)
		}
		return err
	}
	if err := installFile(u.bin(), data, u.owner, u.group, 0o755); err != nil {
		return fmt.Errorf("upgrade: %w", err)
	}
	_, _ = fmt.Fprintf(u.stdout, "iormesh: upgraded %s from %s to %s\n", u.bin(), u.running, m.Version)
	if m.Version != u.running {
		return u.dropAgent(m.Version)
	}
	return nil
}

func (u upgrader) bin() string { return filepath.Join(u.root, binRel) }

// checkLayout requires the install docs/security.md describes, before
// anything is read or written: installRoot's parent (followed: /opt may
// be a link on image-based systems), installRoot, bin/ and etc/ (not
// followed) are directories the owner owns and nobody else may write, and
// the installed binary is such a regular file — a symlink there is
// refused, never replaced, since sudoGated already treats it as tampering.
func (u upgrader) checkLayout() error {
	if err := u.checkDir(filepath.Dir(u.root), os.Stat); err != nil {
		return err
	}
	for _, d := range []string{u.root, filepath.Dir(u.bin()), filepath.Dir(filepath.Join(u.root, pinnedKeyRel))} {
		if err := u.checkDir(d, os.Lstat); err != nil {
			return err
		}
	}
	_, err := u.installedInfo()
	return err
}

// installedInfo is the installed binary's lstat, which must be a regular
// file (not a symlink) the owner owns and nobody else may write.
func (u upgrader) installedInfo() (fs.FileInfo, error) {
	info, err := os.Lstat(u.bin())
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file (mode %v)", release.ErrUnsafeFile, u.bin(), info.Mode())
	}
	return info, release.OwnedBy(u.owner)(u.bin(), info)
}

// checkInstalled re-checks the installed binary under the lock and,
// through the rule, that it is still this process's executable.
func (u upgrader) checkInstalled() error {
	info, err := u.installedInfo()
	if err != nil || !u.gated {
		return err
	}
	self, err := u.self()
	if err != nil {
		return fmt.Errorf("own executable: %w", err)
	}
	if !os.SameFile(info, self) {
		return fmt.Errorf("%s changed since this upgrade started (another upgrade ran); rerun it", u.bin())
	}
	return nil
}

func (u upgrader) checkDir(path string, stat func(string) (fs.FileInfo, error)) error {
	info, err := stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s is not a directory (mode %v)", release.ErrUnsafeFile, path, info.Mode())
	}
	return release.OwnedBy(u.owner)(path, info)
}

// verifyStaged reads staged and its signature once and verifies them;
// the returned bytes are the ones that verified.
func (u upgrader) verifyStaged(key minisign.PublicKey, staged string) ([]byte, release.Meta, error) {
	data, err := release.ReadFile(staged, release.MaxBinarySize, u.stagedCheck)
	if err != nil {
		return nil, release.Meta{}, err
	}
	sig, err := release.ReadFile(staged+release.SigSuffix, release.MaxSigSize, u.stagedCheck)
	if err != nil {
		return nil, release.Meta{}, err
	}
	m, err := release.Verify(key, data, sig)
	if err != nil {
		return nil, release.Meta{}, fmt.Errorf("%s: %w", staged, err)
	}
	if want := release.TracerFile(u.goarch); m.File != want && m.File != release.AgentFile {
		return nil, release.Meta{}, fmt.Errorf("%s is signed as %s; this host takes %s or %s", staged, m.File,
			want, release.AgentFile)
	}
	return data, m, nil
}

// stagedCheck admits a staged file the sudo caller owns — through the
// rule only theirs, since root reading root's files (or another user's)
// on their behalf is not what the rule grants, even if a signature over
// them could never verify. Root directly may stage its own files too.
func (u upgrader) stagedCheck(path string, info fs.FileInfo) error {
	uid, ok := release.Owner(info)
	if ok && (uid == u.sudoUID || (uid == u.owner && !u.gated)) {
		return nil
	}
	return fmt.Errorf("%w: %s is owned by uid %d, not by the caller", release.ErrUnsafeFile, path, uid)
}

// checkVersion refuses a signed version older than the running binary's
// unless allowDowngrade. same reports that the very bytes are installed
// already (deploy re-pushes the current build), so nothing is rewritten;
// through the rule the same version with other bytes is refused — a
// release bumps Version, and without the bump a rebuild could stand in
// for a fix. Through the rule the running binary is the installed one
// (checkInstalled), so this compares installed and staged; root running
// another binary compares against that one, which is root's own choice.
func (u upgrader) checkVersion(m release.Meta, data []byte, allowDowngrade bool) (same bool, err error) {
	cmp, err := version.Compare(m.Version, u.running)
	if err != nil {
		return false, fmt.Errorf("upgrade: %w", err)
	}
	if cmp < 0 && !allowDowngrade {
		return false, fmt.Errorf("upgrade: refusing to downgrade from %s to %s (root may pass -allow-downgrade)",
			u.running, m.Version)
	}
	if cmp != 0 {
		return false, nil
	}
	installed, err := release.ReadFile(u.bin(), release.MaxBinarySize, release.OwnedBy(u.owner))
	if err != nil {
		return false, fmt.Errorf("upgrade: %w", err)
	}
	if bytes.Equal(installed, data) {
		return true, nil
	}
	if u.gated {
		return false, fmt.Errorf("upgrade: %s is installed with other bytes; through the sudoers rule a new "+
			"build must carry a new version", m.Version)
	}
	return false, nil
}

// installFile writes data as a new file next to dst and renames it over
// dst, so a crash leaves the old file or the new one, never half of each,
// and a running tracer (or a JVM that opened the old jar) keeps its own
// inode. dst's directory is writable by its owner only (checkLayout,
// agentDir), so the temp name cannot be planted; CreateTemp's O_EXCL
// refuses one anyway. The file is chowned to uid:gid (root:root) on its
// descriptor before it gets mode, so it never carries the mode with
// another owner: 0755 for the tracer, 0644 for the jar, each what the
// layout check requires of it next time.
func installFile(dst string, data []byte, uid, gid int, mode os.FileMode) (err error) {
	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, ".iormesh-upgrade-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	if err := writeSynced(tmp, data, uid, gid, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return err
	}
	return syncPath(dir)
}

// writeSynced writes data to f, chowns it to uid:gid, gives it mode,
// fsyncs and closes it.
func writeSynced(f *os.File, data []byte, uid, gid int, mode os.FileMode) error {
	_, err := f.Write(data)
	if err == nil {
		err = f.Chown(uid, gid)
	}
	if err == nil {
		err = f.Chmod(mode)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// syncPath fsyncs dir so the rename is durable: without it a crash can
// bring back the old directory entry.
func syncPath(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
