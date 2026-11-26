package release

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"aead.dev/minisign"
)

// ErrUnsafeFile is a key, binary or signature file, or a directory above a
// key, that is not what it must be: a symlink, not a regular file, owned
// by someone else, or writable by others.
var ErrUnsafeFile = errors.New("unsafe file")

// FileCheck vets an opened file's fstat before a byte of it is read.
type FileCheck func(path string, info fs.FileInfo) error

// ReadFile reads at most max bytes of path. A final-component symlink is
// refused, the file must be regular and pass check, and all of it is
// judged on the very file that is then read, so a swap after the check
// reads nothing new. On Linux the file is not even opened for reading
// before those checks (openChecked), so a root caller never opens a
// device or FIFO a path names.
func ReadFile(path string, max int64, check FileCheck) ([]byte, error) {
	f, err := openChecked(path, check)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%w: %s is over %d bytes", ErrUnsafeFile, path, max)
	}
	return data, nil
}

// vet applies ReadFile's rules to info, the fstat of path.
func vet(path string, info fs.FileInfo, check FileCheck) error {
	if info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s is a symlink", ErrUnsafeFile, path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is not a regular file", ErrUnsafeFile, path)
	}
	if check != nil {
		return check(path, info)
	}
	return nil
}

// OwnedBy is a FileCheck (and a directory check) requiring the owner uid
// and no group or other write permission: whoever else could write a
// pinned key could pin their own.
func OwnedBy(uid int) FileCheck {
	return ownedWithout(uid, 0o022, "group or other may write")
}

// ownedWithout is a FileCheck requiring the owner uid and none of the
// permission bits in mask; why names what a set bit allows.
func ownedWithout(uid int, mask fs.FileMode, why string) FileCheck {
	return func(path string, info fs.FileInfo) error {
		if perm := info.Mode().Perm(); perm&mask != 0 {
			return fmt.Errorf("%w: %s has mode %v (%s)", ErrUnsafeFile, path, perm, why)
		}
		if got, ok := Owner(info); !ok || got != uid {
			return fmt.Errorf("%w: %s is owned by uid %d, want %d", ErrUnsafeFile, path, got, uid)
		}
		return nil
	}
}

// Owner is info's owning uid; ok is false where the platform has none.
func Owner(info fs.FileInfo) (uid int, ok bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return -1, false
	}
	return int(st.Uid), true
}

// ReadKeyFile reads the pinned public key at path, which must be a
// regular file owned by uid and writable by nobody else, in a directory
// that is the same (followed if a symlink: dotfile managers link the
// controller's config directory). The controller pins its key in its
// config this way; the tracer checks its whole root-owned layout itself
// before it calls this.
func ReadKeyFile(path string, uid int) (minisign.PublicKey, error) {
	dir := filepath.Dir(path)
	info, err := os.Stat(dir)
	if err != nil {
		return minisign.PublicKey{}, fmt.Errorf("key directory: %w", err)
	}
	if err := OwnedBy(uid)(dir, info); err != nil {
		return minisign.PublicKey{}, err
	}
	data, err := ReadFile(path, MaxKeySize, OwnedBy(uid))
	if err != nil {
		return minisign.PublicKey{}, fmt.Errorf("pinned key: %w", err)
	}
	return ParsePublicKey(data)
}

// ReadSecretKey reads the operator's minisign secret key at path and
// decrypts it with password: the key `mage sign` and `iormeshctl sign`
// sign with (decision #32). The file must be a regular file, not a
// symlink, owned by uid, with no group or other permission at all — a
// secret key others may read is as good as published, so it is refused
// (chmod 600) rather than used. Only scrypt-encrypted keys (minisign -G
// with a password) decrypt; the password is the caller's and never part
// of an error.
func ReadSecretKey(path, password string, uid int) (minisign.PrivateKey, error) {
	data, err := ReadFile(path, MaxKeySize, ownedWithout(uid, 0o077, "group or other may access it; chmod 600"))
	if err != nil {
		return minisign.PrivateKey{}, fmt.Errorf("secret key: %w", err)
	}
	key, err := minisign.DecryptKey(password, data)
	if err != nil {
		return minisign.PrivateKey{}, fmt.Errorf("secret key %s: %w (wrong password, or not a password-encrypted minisign key)",
			path, err)
	}
	return key, nil
}

// VerifyFile verifies the release artifact at path against path+SigSuffix
// with the pinned key, and requires the signed file name to be path's own
// name: a renamed artifact (the arm64 tracer as amd64's) is refused. This
// is the controller's check before it pushes a binary (decision #28).
func VerifyFile(key minisign.PublicKey, path string) (Meta, error) {
	_, _, m, err := ReadVerified(key, path)
	return m, err
}

// ReadVerified is VerifyFile that also returns the artifact's and the
// signature's bytes, exactly the ones it verified: iormeshctl bootstrap
// stages those bytes and checks the host's installed copy against their
// hash, so a file replaced in dist/ after the check cannot slip through
// (task qh4).
func ReadVerified(key minisign.PublicKey, path string) (data, sig []byte, m Meta, err error) {
	data, err = ReadFile(path, MaxBinarySize, nil)
	if err != nil {
		return nil, nil, Meta{}, err
	}
	sig, err = ReadFile(path+SigSuffix, MaxSigSize, nil)
	if err != nil {
		return nil, nil, Meta{}, err
	}
	m, err = Verify(key, data, sig)
	if err != nil {
		return nil, nil, Meta{}, fmt.Errorf("%s: %w", path, err)
	}
	if m.File != filepath.Base(path) {
		return nil, nil, Meta{}, fmt.Errorf("%w: %s was signed as %s", ErrBadSignature, path, m.File)
	}
	return data, sig, m, nil
}

// SignDir signs every release binary in dir — each regular file not
// itself a signature or hidden — as its own name at version, writing
// NAME.minisig next to it. It is `mage sign`'s work on dist/; the private
// key never leaves the caller. Every file must be stamped with ver
// (CheckStamp), and all are checked before the first is signed, so one
// stale build leaves dir unsigned rather than half signed at ver.
func SignDir(key minisign.PrivateKey, dir, ver string, now time.Time) ([]string, error) {
	paths, err := listDir(dir, func(name string) bool {
		return !strings.HasSuffix(name, SigSuffix) && !strings.HasPrefix(name, ".")
	})
	if err != nil {
		return nil, err
	}
	return signPaths(key, paths, ver, now)
}

// SignRelease signs the release artifacts (IsArtifact) in dir, an
// unpacked release archive, exactly as SignDir does: `iormeshctl sign`'s
// work (decision #32). The archive's other files — LICENSE, CHANGELOG.md,
// a SHA256SUMS copied next to them — are left alone, since they carry no
// version stamp and deploy reads none of them. A dir without any artifact
// is an error, so a wrong path is not reported as a signed release.
func SignRelease(key minisign.PrivateKey, dir, ver string, now time.Time) ([]string, error) {
	paths, err := listDir(dir, IsArtifact)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("sign: %s holds no release artifact (iormesh-linux-<arch>, %s, iormeshctl-*)",
			dir, AgentFile)
	}
	return signPaths(key, paths, ver, now)
}

// listDir is dir's regular files whose names keep accepts, in name
// order. A symlink is never listed: its target is not the operator's to
// vouch for, and ReadFile would refuse it anyway.
func listDir(dir string, keep func(name string) bool) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}
	var paths []string
	for _, e := range entries {
		if e.Type().IsRegular() && keep(e.Name()) {
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	return paths, nil
}

// signPaths checks every path's stamp, then signs each as its own name
// at ver; SignDir's and SignRelease's one implementation.
func signPaths(key minisign.PrivateKey, paths []string, ver string, now time.Time) ([]string, error) {
	for _, path := range paths {
		if _, err := readStamped(path, ver); err != nil {
			return nil, err
		}
	}
	var signed []string
	for _, path := range paths {
		if err := signFile(key, path, Meta{File: filepath.Base(path), Version: ver}, now); err != nil {
			return signed, err
		}
		signed = append(signed, path)
	}
	return signed, nil
}

// readStamped reads path and requires its stamp to be ver.
func readStamped(path, ver string) ([]byte, error) {
	data, err := ReadFile(path, MaxBinarySize, nil)
	if err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}
	if err := CheckStamp(filepath.Base(path), data, ver); err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}
	return data, nil
}

// signFile writes path's signature as m, checking the stamp again on the
// very bytes it signs.
func signFile(key minisign.PrivateKey, path string, m Meta, now time.Time) error {
	data, err := readStamped(path, m.Version)
	if err != nil {
		return err
	}
	sig, err := Sign(key, data, m, now)
	if err != nil {
		return fmt.Errorf("sign %s: %w", path, err)
	}
	if err := replaceFile(path+SigSuffix, sig); err != nil {
		return fmt.Errorf("sign: %w", err)
	}
	return nil
}

// replaceFile puts data at path as a fresh regular 0644 file without ever
// writing through what path names now: an unpacked, untrusted archive may
// carry NAME.minisig as a symlink to a victim file (or a dangling one),
// and os.WriteFile would follow it. The data goes to a new temporary file
// in the same directory, opened O_CREAT|O_EXCL|O_NOFOLLOW, is synced and
// then renamed over path; rename replaces a symlink itself, not its
// target. The temporary file is removed on any error.
func replaceFile(path string, data []byte) (err error) {
	tmp, f, err := createTemp(path)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	// The umask may have narrowed the mode; a signature is public.
	if err = f.Chmod(0o644); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// createTemp creates ".NAME.tmp-<random>" next to path, never following
// or reusing an existing name.
func createTemp(path string) (string, *os.File, error) {
	dir, base := filepath.Split(path)
	for range 16 {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", nil, err
		}
		tmp := filepath.Join(dir, "."+base+".tmp-"+hex.EncodeToString(b[:]))
		f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o644)
		if err == nil {
			return tmp, f, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", nil, err
		}
	}
	return "", nil, fmt.Errorf("%s: no free temporary name", path)
}
