package release

import (
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
// refused (O_NOFOLLOW) and a FIFO cannot block the open (O_NONBLOCK);
// the file must be regular and pass check, both judged on the descriptor
// that is then read, so a swap after the check reads nothing new.
func ReadFile(path string, max int64, check FileCheck) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%w: %s is a symlink", ErrUnsafeFile, path)
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrUnsafeFile, path)
	}
	if check != nil {
		if err := check(path, info); err != nil {
			return nil, err
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%w: %s is over %d bytes", ErrUnsafeFile, path, max)
	}
	return data, nil
}

// OwnedBy is a FileCheck (and a directory check) requiring the owner uid
// and no group or other write permission: whoever else could write a
// pinned key could pin their own.
func OwnedBy(uid int) FileCheck {
	return func(path string, info fs.FileInfo) error {
		if perm := info.Mode().Perm(); perm&0o022 != 0 {
			return fmt.Errorf("%w: %s has mode %v (group or other may write)", ErrUnsafeFile, path, perm)
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

// VerifyFile verifies the release artifact at path against path+SigSuffix
// with the pinned key, and requires the signed file name to be path's own
// name: a renamed artifact (the arm64 tracer as amd64's) is refused. This
// is the controller's check before it pushes a binary (decision #28).
func VerifyFile(key minisign.PublicKey, path string) (Meta, error) {
	data, err := ReadFile(path, MaxBinarySize, nil)
	if err != nil {
		return Meta{}, err
	}
	sig, err := ReadFile(path+SigSuffix, MaxSigSize, nil)
	if err != nil {
		return Meta{}, err
	}
	m, err := Verify(key, data, sig)
	if err != nil {
		return Meta{}, fmt.Errorf("%s: %w", path, err)
	}
	if m.File != filepath.Base(path) {
		return Meta{}, fmt.Errorf("%w: %s was signed as %s", ErrBadSignature, path, m.File)
	}
	return m, nil
}

// SignDir signs every release binary in dir — each regular file not
// itself a signature or hidden — as its own name at version, writing
// NAME.minisig next to it. It is `mage sign`'s work; the private key never
// leaves the caller.
func SignDir(key minisign.PrivateKey, dir, ver string, now time.Time) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}
	var signed []string
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || strings.HasSuffix(name, SigSuffix) || strings.HasPrefix(name, ".") {
			continue
		}
		path := filepath.Join(dir, name)
		if err := signFile(key, path, Meta{File: name, Version: ver}, now); err != nil {
			return signed, err
		}
		signed = append(signed, path)
	}
	return signed, nil
}

// signFile writes path's signature as m.
func signFile(key minisign.PrivateKey, path string, m Meta, now time.Time) error {
	data, err := ReadFile(path, MaxBinarySize, nil)
	if err != nil {
		return fmt.Errorf("sign: %w", err)
	}
	sig, err := Sign(key, data, m, now)
	if err != nil {
		return fmt.Errorf("sign %s: %w", path, err)
	}
	if err := os.WriteFile(path+SigSuffix, sig, 0o644); err != nil {
		return fmt.Errorf("sign: %w", err)
	}
	return nil
}
