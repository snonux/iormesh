package release

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"aead.dev/minisign"
)

// ErrNoPassword is an empty password for a new secret key: the signers
// read only a password-encrypted key (ReadSecretKey), so a key without
// one could sign nothing, and an empty one protects nothing.
var ErrNoPassword = errors.New("empty password")

// WriteKeyPair makes the operator's minisign key pair (decision #38):
// the secret key, encrypted with password exactly as `minisign -G` does
// (scrypt, the library's sensitive limits), at secretPath, and its public
// half, in minisign's text format, at pubPath. Both files are new and
// 0600: an existing file or symlink at either path is refused before
// anything is written (fs.ErrExist), and each is created
// O_CREAT|O_EXCL|O_NOFOLLOW, so a name that appears meanwhile is refused
// too rather than written through. A missing directory above either file
// is made 0700, and each key's directory must then be uid's and writable
// by nobody else, the rule ReadKeyFile holds the pinned key's directory
// to. When the public key cannot be written, the secret key just made is
// removed again, so a failed run leaves no half pair. The password is the
// caller's and never part of an error.
func WriteKeyPair(secretPath, pubPath, password string, uid int) (minisign.PublicKey, error) {
	if password == "" {
		return minisign.PublicKey{}, fmt.Errorf("keygen: %w: the secret key must be password-encrypted", ErrNoPassword)
	}
	for _, p := range []string{secretPath, pubPath} {
		if _, err := os.Lstat(p); err == nil {
			return minisign.PublicKey{}, fmt.Errorf("keygen: %s: %w; keygen never replaces a key", p, fs.ErrExist)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return minisign.PublicKey{}, fmt.Errorf("keygen: %w", err)
		}
	}
	for _, p := range []string{secretPath, pubPath} {
		if err := makeKeyDir(filepath.Dir(p), uid); err != nil {
			return minisign.PublicKey{}, err
		}
	}
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		return minisign.PublicKey{}, fmt.Errorf("keygen: %w", err)
	}
	secret, err := minisign.EncryptKey(password, priv)
	if err != nil {
		return minisign.PublicKey{}, fmt.Errorf("keygen: encrypt: %w", err)
	}
	pubText, err := pub.MarshalText()
	if err != nil {
		return minisign.PublicKey{}, fmt.Errorf("keygen: %w", err)
	}
	if err := writeNew(secretPath, secret); err != nil {
		return minisign.PublicKey{}, err
	}
	if err := writeNew(pubPath, append(pubText, '\n')); err != nil {
		_ = os.Remove(secretPath)
		return minisign.PublicKey{}, err
	}
	return pub, nil
}

// makeKeyDir makes dir and any missing parent 0700, then requires dir
// (followed if a symlink, as ReadKeyFile does) to be uid's and not
// writable by group or other.
func makeKeyDir(dir string, uid int) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("keygen: %w", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("keygen: %w", err)
	}
	if err := OwnedBy(uid)(dir, info); err != nil {
		return fmt.Errorf("keygen: key directory: %w", err)
	}
	return nil
}

// writeNew writes data to path as a new 0600 file (createNew) and syncs
// it; on any error the partly written file is removed.
func writeNew(path string, data []byte) (err error) {
	f, err := createNew(path, 0o600)
	if err != nil {
		return fmt.Errorf("keygen: %w", err)
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(path)
		}
	}()
	if _, err = f.Write(data); err != nil {
		return fmt.Errorf("keygen: %s: %w", path, err)
	}
	if err = f.Sync(); err != nil {
		return fmt.Errorf("keygen: %s: %w", path, err)
	}
	return f.Close()
}
