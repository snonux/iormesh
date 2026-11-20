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
//     (release.TracerFile) — not iormeshctl, not the other arch;
//   - the signed version may not be older than the running binary's,
//     unless root itself, not the rule, passes -allow-downgrade: an old
//     signed build may carry a fixed hole;
//   - verification happens on bytes read into memory once, and exactly
//     those bytes are installed: the staged file is the caller's and may
//     change after the check;
//   - the install is a fresh root-owned 0755 regular file renamed over
//     installedBinary in its root-only directory — never through a
//     symlink, and only into the layout docs/security.md requires.
//
// Running sessions keep the old binary (their inode); the next start runs
// the new one.
const (
	// pinnedKeyRel is the pinned release key under installRoot: the
	// root-owned server copy of the controller's key (decision #28).
	pinnedKeyRel = "etc/minisign.pub"
	// binRel is the installed binary under installRoot.
	binRel = "bin/iormesh"
)

// upgrader is one upgrade, its environment injected so tests run on a
// temporary layout their own uid owns.
type upgrader struct {
	// root is installRoot; owner the uid that must own it (0).
	root  string
	owner int
	// sudoUID is the sudo caller (-1 without sudo), who must own the
	// staged file (stagedCheck).
	sudoUID int
	// gated is sudoGated: this run came through the sudoers rule.
	gated bool
	// running is this binary's version, goarch its arch.
	running, goarch string
	stdout          io.Writer
}

// newUpgrader is the production upgrader for this process.
func newUpgrader(stdout io.Writer) upgrader {
	self, err := os.Executable()
	if err != nil {
		self = "" // unknown: sudoGated fails closed
	}
	u := upgrader{root: installRoot, owner: 0, sudoUID: -1, gated: sudoGated(self, os.Getenv, os.Lstat),
		running: version.Version, goarch: runtime.GOARCH, stdout: stdout}
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
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("%w: upgrade takes one staged file", errUsage)
	}
	if os.Geteuid() != 0 {
		return errors.New("upgrade replaces the root-owned " + installedBinary + "; run it through sudo")
	}
	return newUpgrader(stdout).upgrade(flags.Arg(0), *allowDowngrade)
}

// upgrade verifies staged and installs it. Nothing under root is written
// before the signature, the artifact name and the version all passed.
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
	same, err := u.checkVersion(m, data, allowDowngrade)
	if err != nil || same {
		if same {
			_, _ = fmt.Fprintf(u.stdout, "iormesh: %s %s is already installed\n", m.File, m.Version)
		}
		return err
	}
	if err := u.install(data); err != nil {
		return fmt.Errorf("upgrade: %w", err)
	}
	_, _ = fmt.Fprintf(u.stdout, "iormesh: upgraded %s from %s to %s\n", u.bin(), u.running, m.Version)
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
	info, err := os.Lstat(u.bin())
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is not a regular file (mode %v)", release.ErrUnsafeFile, u.bin(), info.Mode())
	}
	return release.OwnedBy(u.owner)(u.bin(), info)
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
	if want := release.TracerFile(u.goarch); m.File != want {
		return nil, release.Meta{}, fmt.Errorf("%s is signed as %s; this host runs %s", staged, m.File, want)
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
// already (deploy re-pushes the current build), so nothing is rewritten.
// Under the sudoers rule the running binary is the installed one, so this
// compares installed and staged; root running another binary compares
// against that one, which is root's own choice.
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
	return bytes.Equal(installed, data), nil
}

// install writes data as a new file in bin/ and renames it over the
// installed binary, so a crash leaves the old binary or the new one,
// never half of each, and a running tracer keeps its own inode. bin/ is
// writable by its owner only (checkLayout), so the temp name cannot be
// planted; CreateTemp's O_EXCL refuses one anyway. The file is created by
// this process — root in production — so it is root-owned; 0755 matches
// what the layout check requires of it next time.
func (u upgrader) install(data []byte) (err error) {
	dir := filepath.Dir(u.bin())
	tmp, err := os.CreateTemp(dir, ".iormesh-upgrade-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	if err := writeSynced(tmp, data); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), u.bin()); err != nil {
		return err
	}
	return syncPath(dir)
}

// writeSynced writes data to f, makes it 0755, fsyncs and closes it.
func writeSynced(f *os.File, data []byte) error {
	_, err := f.Write(data)
	if err == nil {
		err = f.Chmod(0o755)
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
