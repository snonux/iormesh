package release

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// keygenPassword is the throwaway password of the keys these tests make.
const keygenPassword = "keygen-pw-vm4"

// keygenPaths are a secret key two missing directories deep and a public
// key in a missing directory of its own, as a fresh laptop has neither
// ~/.minisign nor ~/.config/iormesh.
func keygenPaths(t *testing.T) (secret, pub string) {
	t.Helper()
	root := t.TempDir()
	return filepath.Join(root, "home", ".minisign", "iormesh.key"),
		filepath.Join(root, "config", "minisign.pub")
}

// TestKeygenPairSignsAndVerifies (task vm4): the pair WriteKeyPair makes
// is what the signers and the verifiers read — ReadSecretKey decrypts the
// secret half with the password, ReadKeyFile pins the public half, and a
// signature by one verifies under the other, with the minisign CLI too
// when it is installed. Both files are 0600, every directory it made 0700.
func TestKeygenPairSignsAndVerifies(t *testing.T) {
	secret, pubPath := keygenPaths(t)
	pub, err := WriteKeyPair(secret, pubPath, keygenPassword, os.Getuid())
	if err != nil {
		t.Fatalf("WriteKeyPair: %v", err)
	}
	key, err := ReadSecretKey(secret, keygenPassword, os.Getuid())
	if err != nil {
		t.Fatalf("ReadSecretKey of the new key: %v", err)
	}
	pinned, err := ReadKeyFile(pubPath, os.Getuid())
	if err != nil {
		t.Fatalf("ReadKeyFile of the new public key: %v", err)
	}
	if !pinned.Equal(pub) || key.ID() != pub.ID() {
		t.Fatalf("public key on disk %X, returned %X, secret key %X", pinned.ID(), pub.ID(), key.ID())
	}
	data := []byte("iormesh-linux-amd64\x00iormesh-version:0.1.0\x00")
	sig, err := Sign(key, data, Meta{File: "iormesh-linux-amd64", Version: "0.1.0"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(pinned, data, sig); err != nil {
		t.Fatalf("own signature does not verify: %v", err)
	}
	for _, p := range []string{secret, pubPath} {
		assertMode(t, p, 0o600)
	}
	for _, d := range []string{filepath.Dir(secret), filepath.Dir(filepath.Dir(secret)), filepath.Dir(pubPath)} {
		assertMode(t, d, fs.ModeDir|0o700)
	}
	minisignVerifies(t, pubPath, data, sig)
}

// minisignVerifies checks sig with the minisign CLI (`minisign -Vm`)
// where it is installed: the files must be the CLI's format, not just the
// library's reading of it.
func minisignVerifies(t *testing.T, pubPath string, data, sig []byte) {
	t.Helper()
	bin, err := exec.LookPath("minisign")
	if err != nil {
		t.Log("minisign CLI not installed; the library's verify stands in for it")
		return
	}
	dir := t.TempDir()
	msg := filepath.Join(dir, "iormesh-linux-amd64")
	if err := os.WriteFile(msg, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(msg+SigSuffix, sig, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(bin, "-Vm", msg, "-p", pubPath).CombinedOutput(); err != nil {
		t.Fatalf("minisign -Vm: %v: %s", err, out)
	}
}

func assertMode(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode() != want {
		t.Errorf("%s has mode %v, want %v", path, info.Mode(), want)
	}
}

// TestKeygenRefusesExisting: an existing secret or public key file is
// never overwritten — that would lose the key every host pins — and a
// refused run leaves no half of a new pair behind.
func TestKeygenRefusesExisting(t *testing.T) {
	for _, which := range []string{"secret", "public"} {
		t.Run(which, func(t *testing.T) {
			secret, pubPath := keygenPaths(t)
			existing := secret
			if which == "public" {
				existing = pubPath
			}
			if err := os.MkdirAll(filepath.Dir(existing), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(existing, []byte("old key"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := WriteKeyPair(secret, pubPath, keygenPassword, os.Getuid())
			if !errors.Is(err, fs.ErrExist) {
				t.Fatalf("WriteKeyPair over an existing %s key: %v, want ErrExist", which, err)
			}
			if got, _ := os.ReadFile(existing); !bytes.Equal(got, []byte("old key")) {
				t.Errorf("existing %s key changed to %q", which, got)
			}
			for _, p := range []string{secret, pubPath} {
				if p == existing {
					continue
				}
				if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("refused run left %s: %v", p, err)
				}
			}
		})
	}
}

// TestKeygenRefusesSymlink: a key path that is a symlink — to a file or
// dangling — is refused, and nothing is written through it.
func TestKeygenRefusesSymlink(t *testing.T) {
	for _, dangling := range []bool{false, true} {
		secret, pubPath := keygenPaths(t)
		if err := os.MkdirAll(filepath.Dir(secret), 0o700); err != nil {
			t.Fatal(err)
		}
		victim := filepath.Join(t.TempDir(), "victim")
		if !dangling {
			if err := os.WriteFile(victim, []byte("victim"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(victim, secret); err != nil {
			t.Fatal(err)
		}
		if _, err := WriteKeyPair(secret, pubPath, keygenPassword, os.Getuid()); err == nil {
			t.Fatalf("dangling=%v: WriteKeyPair through a symlink succeeded", dangling)
		}
		got, err := os.ReadFile(victim)
		if dangling && !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("dangling symlink: the target was created: %v", err)
		}
		if !dangling && !bytes.Equal(got, []byte("victim")) {
			t.Errorf("symlink target changed to %q", got)
		}
		if _, err := os.Lstat(pubPath); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("dangling=%v: refused run left the public key: %v", dangling, err)
		}
	}
}

// TestKeygenRefusesEmptyPassword: no unencrypted key is made, and an
// empty password is refused before anything is written. ReadSecretKey,
// and so both signers, read only a password-encrypted key.
func TestKeygenRefusesEmptyPassword(t *testing.T) {
	secret, pubPath := keygenPaths(t)
	_, err := WriteKeyPair(secret, pubPath, "", os.Getuid())
	if !errors.Is(err, ErrNoPassword) {
		t.Fatalf("WriteKeyPair with an empty password: %v, want ErrNoPassword", err)
	}
	if _, err := os.Lstat(filepath.Dir(secret)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("refused run made the key directory: %v", err)
	}
}

// TestKeygenRefusesUnsafeDir: a key directory others may write is
// refused, as ReadKeyFile refuses the pinned key's directory.
func TestKeygenRefusesUnsafeDir(t *testing.T) {
	secret, pubPath := keygenPaths(t)
	dir := filepath.Dir(secret)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	_, err := WriteKeyPair(secret, pubPath, keygenPassword, os.Getuid())
	if !errors.Is(err, ErrUnsafeFile) || !strings.Contains(err.Error(), dir) {
		t.Fatalf("WriteKeyPair into a 0777 directory: %v, want ErrUnsafeFile naming it", err)
	}
	if _, err := os.Lstat(secret); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("refused run wrote the secret key: %v", err)
	}
}
