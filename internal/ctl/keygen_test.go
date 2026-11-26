package ctl

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"iormesh/internal/version"
)

// keygenRun runs iormeshctl args with the release key default at pubPath
// and, unless password is nil, $IORMESH_MINISIGN_PASSWORD set to it. It
// fails the test if any output holds the password or a secret key line.
func keygenRun(t *testing.T, pubPath string, password *string, args ...string) (int, string, string) {
	t.Helper()
	if password != nil {
		t.Setenv(signPasswordEnv, *password)
	} else {
		// t.Setenv first so the variable is restored after the test.
		t.Setenv(signPasswordEnv, "")
		if err := os.Unsetenv(signPasswordEnv); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	e := env{stdout: &stdout, stderr: &stderr, releaseKey: func() (string, error) { return pubPath, nil }}
	code := run(args, e)
	out := stdout.String() + stderr.String()
	if (password != nil && *password != "" && strings.Contains(out, *password)) ||
		strings.Contains(out, "encrypted secret key") || strings.Contains(out, "RWRTY") {
		t.Fatalf("%v printed the password or the secret key: %s", args, out)
	}
	return code, stdout.String(), stderr.String()
}

// TestKeygen (task vm4): keygen makes the operator's key pair without a
// terminal, from $IORMESH_MINISIGN_PASSWORD, at -key and the default
// pinned key path; sign then signs a release with that secret key and the
// same password, and verify accepts the result under the new public key.
func TestKeygen(t *testing.T) {
	f := newSignFixture(t, version.Version)
	keys := t.TempDir()
	secret := filepath.Join(keys, "minisign", "iormesh.key")
	pubPath := filepath.Join(keys, "config", keyFileName)
	pw := testPassword
	code, stdout, stderr := keygenRun(t, pubPath, &pw, "keygen", "-key", secret)
	if code != exitOK {
		t.Fatalf("keygen: exit %d, %s%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, pubPath) || !strings.Contains(stdout, secret) {
		t.Errorf("keygen does not name both files: %s", stdout)
	}
	f.secret, f.pubPath = secret, pubPath
	if code, stdout, stderr := f.run(t, "sign", "-key", secret, f.dir); code != exitOK {
		t.Fatalf("sign with the new key: exit %d, %s%s", code, stdout, stderr)
	}
	files := []string{"verify", "-key", pubPath, filepath.Join(f.dir, "iormesh-linux-amd64")}
	if code, stdout, stderr := f.run(t, files...); code != exitOK || !strings.Contains(stdout, ": OK, signed ") {
		t.Fatalf("verify under the new key: exit %d, %s%s", code, stdout, stderr)
	}

	// -pub names another public key file; a second run over the same
	// secret key is refused and leaves it as it was.
	before, _ := os.ReadFile(secret)
	other := filepath.Join(keys, "other.pub")
	code, _, stderr = keygenRun(t, pubPath, &pw, "keygen", "-key", secret, "-pub", other)
	if code != exitError || !strings.Contains(stderr, "exists") {
		t.Errorf("keygen over an existing key: exit %d, %s", code, stderr)
	}
	if after, _ := os.ReadFile(secret); !bytes.Equal(before, after) {
		t.Error("refused keygen changed the secret key")
	}
	if _, err := os.Lstat(other); err == nil {
		t.Error("refused keygen wrote the -pub file")
	}
}

// TestKeygenRefuses: no password in the environment, an empty one, and
// bad arguments make no key.
func TestKeygenRefuses(t *testing.T) {
	keys := t.TempDir()
	secret := filepath.Join(keys, "iormesh.key")
	pubPath := filepath.Join(keys, keyFileName)
	empty := ""
	pw := testPassword
	if code, _, stderr := keygenRun(t, pubPath, nil, "keygen", "-key", secret); code != exitError ||
		!strings.Contains(stderr, signPasswordEnv) {
		t.Errorf("keygen without %s: exit %d, %s", signPasswordEnv, code, stderr)
	}
	if code, _, stderr := keygenRun(t, pubPath, &empty, "keygen", "-key", secret); code != exitError ||
		!strings.Contains(stderr, "empty") {
		t.Errorf("keygen with an empty password: exit %d, %s", code, stderr)
	}
	for _, args := range [][]string{
		{"keygen"},
		{"keygen", "-key", secret, "extra"},
	} {
		if code, _, _ := keygenRun(t, pubPath, &pw, args...); code != exitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, exitUsage)
		}
	}
	for _, p := range []string{secret, pubPath} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("refused runs left %s", p)
		}
	}
}
