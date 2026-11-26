package release

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"aead.dev/minisign"
)

func TestIsArtifact(t *testing.T) {
	for name, want := range map[string]bool{
		"iormesh-linux-amd64":         true,
		"iormesh-linux-arm64":         true,
		"iormeshctl-linux-amd64":      true,
		"iormeshctl-darwin-arm64":     true,
		"iormesh-agent.jar":           true,
		"LICENSE":                     false,
		"CHANGELOG.md":                false,
		"SHA256SUMS":                  false,
		"iormesh-linux-amd64.minisig": false,
		"iormesh-linux-":              false,
		"iormesh-linux-AMD64":         false,
		"iormesh-linux-amd64.bak":     false,
		"iormeshctl-windows-amd64":    false,
		"iormesh-darwin-arm64":        false,
	} {
		if got := IsArtifact(name); got != want {
			t.Errorf("IsArtifact(%q) = %v, want %v", name, got, want)
		}
	}
	for _, arch := range []string{"amd64", "arm64"} {
		for _, n := range Artifacts(arch) {
			if !IsArtifact(n) {
				t.Errorf("Artifacts(%s) lists %s, which IsArtifact refuses", arch, n)
			}
		}
	}
}

// unpackedRelease writes what an operator has after tar -xzf: one arch's
// four stamped artifacts at ver next to files that are not artifacts.
func unpackedRelease(t *testing.T, ver string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range Artifacts("amd64") {
		data := []byte(n + stampOf(ver))
		if n == AgentFile {
			data = jarWith(t, agentManifest(ver))
		}
		if err := os.WriteFile(filepath.Join(dir, n), data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"LICENSE", "CHANGELOG.md", "SHA256SUMS", ".hidden"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestSignRelease signs an unpacked release: exactly its artifacts, each
// verifying with the key's public half as its own name at the version,
// and with no other key.
func TestSignRelease(t *testing.T) {
	pub, priv := newKey(t)
	otherPub, _ := newKey(t)
	dir := unpackedRelease(t, "0.3.0")
	signed, err := SignRelease(priv, dir, "0.3.0", testTime)
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, n := range Artifacts("amd64") {
		want = append(want, filepath.Join(dir, n))
	}
	slices.Sort(want)
	if !slices.Equal(signed, want) {
		t.Fatalf("SignRelease = %v, want %v", signed, want)
	}
	for _, path := range signed {
		m, err := VerifyFile(pub, path)
		if err != nil || m != (Meta{File: filepath.Base(path), Version: "0.3.0"}) {
			t.Errorf("%s: VerifyFile = %+v, %v", path, m, err)
		}
		if _, err := VerifyFile(otherPub, path); !errors.Is(err, ErrBadSignature) {
			t.Errorf("%s with another key: err = %v, want ErrBadSignature", path, err)
		}
	}
	for _, n := range []string{"LICENSE", "CHANGELOG.md", "SHA256SUMS", ".hidden"} {
		if _, err := os.Stat(filepath.Join(dir, n+SigSuffix)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s was signed: %v", n, err)
		}
	}
}

// TestSignReleaseReplacesPlantedSymlinks: an untrusted archive may carry
// NAME.minisig as a symlink (to a victim file, or dangling). Signing must
// replace the link with a regular file and never write through it.
func TestSignReleaseReplacesPlantedSymlinks(t *testing.T) {
	pub, priv := newKey(t)
	dir := unpackedRelease(t, "0.3.0")
	victim := filepath.Join(t.TempDir(), "bashrc")
	if err := os.WriteFile(victim, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	tracer := filepath.Join(dir, TracerFile("amd64"))
	jar := filepath.Join(dir, AgentFile)
	if err := os.Symlink(victim, tracer+SigSuffix); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(t.TempDir(), "nowhere", "x")
	if err := os.Symlink(dangling, jar+SigSuffix); err != nil {
		t.Fatal(err)
	}
	if _, err := SignRelease(priv, dir, "0.3.0", testTime); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(victim); err != nil || string(data) != "keep me" {
		t.Errorf("victim = %q, %v; want it untouched", data, err)
	}
	if _, err := os.Lstat(dangling); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("dangling target was created: %v", err)
	}
	for _, p := range []string{tracer, jar} {
		info, err := os.Lstat(p + SigSuffix)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o644 {
			t.Errorf("%s: %v, %v; want a regular 0644 file", p+SigSuffix, info, err)
		}
		if _, err := VerifyFile(pub, p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".*.tmp*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

// A release at another version than the one asked for is refused whole,
// and a directory without artifacts is an error, not a silent no-op.
func TestSignReleaseRefuses(t *testing.T) {
	_, priv := newKey(t)
	dir := unpackedRelease(t, "0.2.0")
	if signed, err := SignRelease(priv, dir, "0.3.0", testTime); !errors.Is(err, ErrStaleBuild) || len(signed) != 0 {
		t.Fatalf("stale release: SignRelease = %v, %v; want ErrStaleBuild", signed, err)
	}
	if sigs, _ := filepath.Glob(filepath.Join(dir, "*"+SigSuffix)); len(sigs) != 0 {
		t.Fatalf("stale release left signatures %v", sigs)
	}
	if _, err := SignRelease(priv, t.TempDir(), "0.3.0", testTime); err == nil {
		t.Fatal("empty directory: SignRelease succeeded")
	}
}

// encryptedKeyFile writes priv encrypted with password at path, mode.
func encryptedKeyFile(t *testing.T, priv minisign.PrivateKey, password, path string, mode os.FileMode) {
	t.Helper()
	data, err := minisign.EncryptKey(password, priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestReadSecretKey(t *testing.T) {
	_, priv, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "iormesh.key")
	encryptedKeyFile(t, priv, "pw", path, 0o600)
	got, err := ReadSecretKey(path, "pw", os.Getuid())
	if err != nil || got.ID() != priv.ID() {
		t.Fatalf("ReadSecretKey = %X, %v", got.ID(), err)
	}
	if _, err := ReadSecretKey(path, "wrong", os.Getuid()); err == nil {
		t.Error("wrong password: ReadSecretKey succeeded")
	}
	if _, err := ReadSecretKey(path, "pw", os.Getuid()+1); !errors.Is(err, ErrUnsafeFile) {
		t.Errorf("foreign owner: err = %v, want ErrUnsafeFile", err)
	}
	link := filepath.Join(dir, "link.key")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSecretKey(link, "pw", os.Getuid()); !errors.Is(err, ErrUnsafeFile) {
		t.Errorf("symlinked key: err = %v, want ErrUnsafeFile", err)
	}
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o620} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadSecretKey(path, "pw", os.Getuid()); !errors.Is(err, ErrUnsafeFile) {
			t.Errorf("mode %v: err = %v, want ErrUnsafeFile", mode, err)
		}
	}
}
