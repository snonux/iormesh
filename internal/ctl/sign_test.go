package ctl

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aead.dev/minisign"

	"iormesh/internal/release"
	"iormesh/internal/version"
)

// testPassword is the throwaway key's password; no output may hold it.
const testPassword = "s3cret-pw-5m4"

// signFixture is an unpacked release for amd64 at ver, a password-
// encrypted throwaway secret key (0600) and its public key pinned in a
// config dir, as an operator has them before `iormeshctl sign`.
type signFixture struct {
	dir, secret, pubPath string
	pub                  minisign.PublicKey
}

func newSignFixture(t *testing.T, ver string) signFixture {
	t.Helper()
	pub, priv := genKey(t)
	f := signFixture{dir: t.TempDir(), pub: pub}
	keys := t.TempDir()
	f.secret = filepath.Join(keys, "iormesh.key")
	enc, err := minisign.EncryptKey(testPassword, priv)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, f.secret, enc, 0o600)
	f.pubPath = filepath.Join(keys, keyFileName)
	text, _ := pub.MarshalText()
	writeFile(t, f.pubPath, text, 0o600)
	for _, n := range release.Artifacts("amd64") {
		data := []byte(n + "\x00iormesh-version:" + ver + "\x00")
		if n == release.AgentFile {
			data = agentJar(t, ver)
		}
		writeFile(t, filepath.Join(f.dir, n), data, 0o755)
	}
	writeFile(t, filepath.Join(f.dir, "LICENSE"), []byte("MIT"), 0o644)
	return f
}

// agentJar is a jar whose manifest stamps it at ver, as `mage
// agentBuild` writes it.
func agentJar(t *testing.T, ver string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("META-INF/MANIFEST.MF")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("Manifest-Version: 1.0\r\nImplementation-Version: " + ver + "\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// run runs iormeshctl args with the password in its environment and
// fails the test if any output holds the password.
func (f signFixture) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv(signPasswordEnv, testPassword)
	var stdout, stderr bytes.Buffer
	e := env{stdout: &stdout, stderr: &stderr, releaseKey: func() (string, error) { return f.pubPath, nil }}
	code := run(args, e)
	if strings.Contains(stdout.String()+stderr.String(), testPassword) {
		t.Fatalf("%v printed the password", args)
	}
	return code, stdout.String(), stderr.String()
}

// TestSign signs an unpacked release and checks it the ways deploy and
// an operator do: iormeshctl verify -key, deploy's tracer and agent
// checks; a wrong key fails all of them.
func TestSign(t *testing.T) {
	f := newSignFixture(t, version.Version)
	code, stdout, stderr := f.run(t, "sign", "-key", f.secret, f.dir)
	if code != exitOK {
		t.Fatalf("sign: exit %d, %s%s", code, stdout, stderr)
	}
	var files []string
	for _, n := range release.Artifacts("amd64") {
		files = append(files, filepath.Join(f.dir, n))
		if !strings.Contains(stdout, "signed "+n+" "+version.Version) {
			t.Errorf("sign output does not report %s: %s", n, stdout)
		}
	}
	if _, err := os.Stat(filepath.Join(f.dir, "LICENSE"+release.SigSuffix)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("LICENSE was signed: %v", err)
	}
	code, stdout, stderr = f.run(t, append([]string{"verify", "-key", f.pubPath}, files...)...)
	if code != exitOK || strings.Count(stdout, ": OK, signed ") != len(files) {
		t.Fatalf("verify: exit %d, %s%s", code, stdout, stderr)
	}
	d := deployer{e: env{stdout: &bytes.Buffer{}}, key: f.pub, dist: f.dir}
	if _, ver, err := d.verify("amd64"); err != nil || ver != version.Version {
		t.Fatalf("deploy's verify = %s, %v", ver, err)
	}
	if _, err := d.verifyAgent(version.Version); err != nil {
		t.Fatalf("deploy's verifyAgent: %v", err)
	}

	// Another operator's key verifies none of it.
	other, _ := genKey(t)
	otherPath := filepath.Join(t.TempDir(), keyFileName)
	text, _ := other.MarshalText()
	writeFile(t, otherPath, text, 0o600)
	code, _, stderr = f.run(t, append([]string{"verify", "-key", otherPath}, files...)...)
	if code != exitError || strings.Count(stderr, "FAILED") != len(files) {
		t.Errorf("verify with another key: exit %d, %s", code, stderr)
	}
	d.key = other
	if _, _, err := d.verify("amd64"); !errors.Is(err, release.ErrBadSignature) {
		t.Errorf("deploy's verify with another key: %v, want ErrBadSignature", err)
	}
	if _, err := d.verifyAgent(version.Version); !errors.Is(err, release.ErrBadSignature) {
		t.Errorf("deploy's verifyAgent with another key: %v, want ErrBadSignature", err)
	}
}

// TestSignVersionFlag signs a release of another version than the
// controller's with -version, and refuses it without.
func TestSignVersionFlag(t *testing.T) {
	f := newSignFixture(t, "9.8.7")
	if code, stdout, stderr := f.run(t, "sign", "-key", f.secret, f.dir); code != exitError ||
		!strings.Contains(stderr, "is stamped 9.8.7") {
		t.Fatalf("sign without -version: exit %d, %s%s", code, stdout, stderr)
	}
	if code, stdout, stderr := f.run(t, "sign", "-key", f.secret, "-version", "9.8.7", f.dir); code != exitOK ||
		!strings.Contains(stdout, "signed "+release.TracerFile("amd64")+" 9.8.7") {
		t.Fatalf("sign -version 9.8.7: exit %d, %s%s", code, stdout, stderr)
	}
}

// TestSignRefuses: a wrong password, a key others may read, a symlinked
// key and a missing key or directory sign nothing.
func TestSignRefuses(t *testing.T) {
	f := newSignFixture(t, version.Version)
	link := filepath.Join(t.TempDir(), "link.key")
	if err := os.Symlink(f.secret, link); err != nil {
		t.Fatal(err)
	}
	t.Run("wrong password", func(t *testing.T) {
		t.Setenv(signPasswordEnv, "wrong")
		var stdout, stderr bytes.Buffer
		if code := run([]string{"sign", "-key", f.secret, f.dir}, env{stdout: &stdout, stderr: &stderr}); code != exitError ||
			!strings.Contains(stderr.String(), "wrong password") || strings.Contains(stderr.String(), "wrong\n") {
			t.Errorf("exit %d, %s", code, stderr.String())
		}
	})
	t.Run("symlinked key", func(t *testing.T) {
		if code, _, stderr := f.run(t, "sign", "-key", link, f.dir); code != exitError || !strings.Contains(stderr, "symlink") {
			t.Errorf("exit %d, %s", code, stderr)
		}
	})
	t.Run("readable key", func(t *testing.T) {
		if err := os.Chmod(f.secret, 0o644); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Chmod(f.secret, 0o600) }()
		if code, _, stderr := f.run(t, "sign", "-key", f.secret, f.dir); code != exitError || !strings.Contains(stderr, "chmod 600") {
			t.Errorf("exit %d, %s", code, stderr)
		}
	})
	for _, args := range [][]string{
		{"sign", f.dir},
		{"sign", "-key", f.secret},
		{"sign", "-key", f.secret, f.dir, f.dir},
	} {
		if code, _, _ := f.run(t, args...); code != exitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, exitUsage)
		}
	}
	if code, _, _ := f.run(t, "sign", "-key", f.secret, t.TempDir()); code != exitError {
		t.Errorf("empty directory: exit %d, want %d", code, exitError)
	}
	if sigs, _ := filepath.Glob(filepath.Join(f.dir, "*"+release.SigSuffix)); len(sigs) != 0 {
		t.Errorf("refused runs left signatures %v", sigs)
	}
}
