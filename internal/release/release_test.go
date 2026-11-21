package release

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"aead.dev/minisign"
)

// Every key here is generated per test run: no private key, throwaway or
// not, is ever committed.
func newKey(t *testing.T) (minisign.PublicKey, minisign.PrivateKey) {
	t.Helper()
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

var testTime = time.Unix(1_700_000_000, 0)

func TestSignVerify(t *testing.T) {
	pub, priv := newKey(t)
	otherPub, _ := newKey(t)
	data := []byte("\x7fELF a tracer build")
	m := Meta{File: TracerFile("amd64"), Version: "0.2.0"}
	sig, err := Sign(priv, data, m, testTime)
	if err != nil {
		t.Fatal(err)
	}
	tampered := append([]byte(nil), data...)
	tampered[5] ^= 1
	tests := []struct {
		name      string
		key       minisign.PublicKey
		data, sig []byte
		want      error
	}{
		{"valid", pub, data, sig, nil},
		{"tampered binary", pub, tampered, sig, ErrBadSignature},
		{"wrong key", otherPub, data, sig, ErrBadSignature},
		{"garbage signature", pub, data, []byte("untrusted comment: x\nAAAA\ntrusted comment: y\nAAAA\n"), ErrBadSignature},
		{"empty signature", pub, data, nil, ErrBadSignature},
		{"truncated binary", pub, data[:len(data)-1], sig, ErrBadSignature},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Verify(tt.key, tt.data, tt.sig)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Verify err = %v, want %v", err, tt.want)
			}
			if tt.want == nil && got != m {
				t.Fatalf("Verify = %+v, want %+v", got, m)
			}
		})
	}
}

// TestVerifyComment covers signatures that verify but whose trusted
// comment is not ours — minisign's own default, a doubled field — and a
// legacy (non-prehashed) signature that is.
func TestVerifyComment(t *testing.T) {
	pub, priv := newKey(t)
	data := []byte("binary")
	tests := []struct {
		name, comment string
		want          error
	}{
		{"legacy algorithm", "timestamp:1\tfile:iormesh-linux-arm64\tversion:1.2.3", nil},
		{"minisign default", "timestamp:1\tfile:iormesh-linux-arm64\thashed", ErrBadComment},
		{"no file", "version:1.2.3", ErrBadComment},
		{"doubled version", "file:a version:9.9.9 version:0.0.1", ErrBadComment},
		{"bad version", "file:a version:v1", ErrBadComment},
		{"path as file", "file:../a version:1.0.0", ErrBadComment},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sig := minisign.SignWithComments(priv, data, tt.comment, "test")
			_, err := Verify(pub, data, sig)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Verify err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestSignRejectsBadMeta(t *testing.T) {
	_, priv := newKey(t)
	for _, m := range []Meta{{"", "1.0.0"}, {"a b", "1.0.0"}, {"a/b", "1.0.0"}, {"a", "1.0"}} {
		if _, err := Sign(priv, nil, m, testTime); !errors.Is(err, ErrBadComment) {
			t.Errorf("Sign(%+v) err = %v, want ErrBadComment", m, err)
		}
	}
}

func TestSignDirVerifyFile(t *testing.T) {
	pub, priv := newKey(t)
	dir := t.TempDir()
	for _, name := range []string{"iormesh-linux-amd64", "iormesh-linux-arm64", ".hidden"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name+stampOf("0.3.0")), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	signed, err := SignDir(priv, dir, "0.3.0", testTime)
	if err != nil || len(signed) != 2 {
		t.Fatalf("SignDir = %v, %v; want the two binaries", signed, err)
	}
	amd := filepath.Join(dir, "iormesh-linux-amd64")
	if m, err := VerifyFile(pub, amd); err != nil || m.Version != "0.3.0" {
		t.Fatalf("VerifyFile = %+v, %v", m, err)
	}
	// Re-signing must not sign the signatures.
	if again, err := SignDir(priv, dir, "0.3.0", testTime); err != nil || len(again) != 2 {
		t.Fatalf("second SignDir = %v, %v", again, err)
	}
	// The arm64 build renamed (with its signature) to amd64's name.
	arm := filepath.Join(dir, "iormesh-linux-arm64")
	for _, s := range []string{"", SigSuffix} {
		if err := os.Rename(arm+s, amd+s); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := VerifyFile(pub, amd); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("renamed artifact: err = %v, want ErrBadSignature", err)
	}
}

func TestReadKeyFile(t *testing.T) {
	pub, _ := newKey(t)
	text, err := pub.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "minisign.pub")
	if err := os.WriteFile(path, append(text, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadKeyFile(path, os.Getuid())
	if err != nil || got.ID() != pub.ID() {
		t.Fatalf("ReadKeyFile = %X, %v", got.ID(), err)
	}
	if _, err := ReadKeyFile(path, os.Getuid()+1); !errors.Is(err, ErrUnsafeFile) {
		t.Errorf("foreign owner: err = %v, want ErrUnsafeFile", err)
	}
	link := filepath.Join(dir, "link.pub")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadKeyFile(link, os.Getuid()); !errors.Is(err, ErrUnsafeFile) {
		t.Errorf("symlinked key: err = %v, want ErrUnsafeFile", err)
	}
	if err := os.Chmod(path, 0o664); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadKeyFile(path, os.Getuid()); !errors.Is(err, ErrUnsafeFile) {
		t.Errorf("group-writable key: err = %v, want ErrUnsafeFile", err)
	}
}

func TestReadFileBounds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := os.WriteFile(path, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(path, 4, nil); !errors.Is(err, ErrUnsafeFile) {
		t.Errorf("oversized: err = %v, want ErrUnsafeFile", err)
	}
	if _, err := ReadFile(dir, 4, nil); err == nil {
		t.Error("a directory was read")
	}
	// Neither a device nor a FIFO is opened for reading: a FIFO without a
	// writer would block the open forever, so this would hang, not fail.
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{fifo, "/dev/null"} {
		if _, err := ReadFile(p, 4, nil); !errors.Is(err, ErrUnsafeFile) {
			t.Errorf("%s: err = %v, want ErrUnsafeFile", p, err)
		}
	}
}
