package ctl

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aead.dev/minisign"

	"iormesh/internal/release"
)

// TestVerify signs a fake dist/ with throwaway keys (generated per run,
// never committed), pins one in a temp config dir, and checks good files
// pass while tampered, foreign-key and unsigned ones fail.
func TestVerify(t *testing.T) {
	pub, priv := genKey(t)
	_, other := genKey(t)
	cfg, dist := t.TempDir(), t.TempDir()
	keyPath := filepath.Join(cfg, keyFileName)
	text, _ := pub.MarshalText()
	writeFile(t, keyPath, text, 0o600)
	path := func(n string) string { return filepath.Join(dist, n) }
	signed(t, priv, path("iormesh-linux-amd64"))
	signed(t, priv, path("iormesh-linux-arm64"))
	signed(t, priv, path("tampered"))
	writeFile(t, path("tampered"), []byte("changed"), 0o755)
	signed(t, other, path("foreign"))
	writeFile(t, path("unsigned"), []byte("x"), 0o755)

	var stdout, stderr bytes.Buffer
	e := env{stdout: &stdout, stderr: &stderr, releaseKey: func() (string, error) { return keyPath, nil }}
	good := []string{"verify", path("iormesh-linux-amd64"), path("iormesh-linux-arm64")}
	if code := run(good, e); code != exitOK || !strings.Contains(stdout.String(), "OK, signed iormesh-linux-arm64 0.4.0") {
		t.Fatalf("good files: exit %d, %s %s", code, stdout.String(), stderr.String())
	}
	for _, bad := range []string{"tampered", "foreign", "unsigned"} {
		stderr.Reset()
		if code := run([]string{"verify", path(bad)}, e); code != exitError || !strings.Contains(stderr.String(), "FAILED") {
			t.Errorf("%s: exit %d, %s", bad, code, stderr.String())
		}
	}
	// A key file others may write pins nothing.
	if err := os.Chmod(keyPath, 0o666); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	if code := run(good, e); code != exitError || !strings.Contains(stderr.String(), "unsafe file") {
		t.Errorf("writable key: exit %d, %s", code, stderr.String())
	}
}

func genKey(t *testing.T) (minisign.PublicKey, minisign.PrivateKey) {
	t.Helper()
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

// signed writes a file named after itself and its 0.4.0 signature by key.
func signed(t *testing.T, key minisign.PrivateKey, path string) {
	t.Helper()
	name := filepath.Base(path)
	writeFile(t, path, []byte(name), 0o755)
	sig, err := release.Sign(key, []byte(name), release.Meta{File: name, Version: "0.4.0"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path+release.SigSuffix, sig, 0o644)
}

func writeFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
