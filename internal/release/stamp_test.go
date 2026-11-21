package release

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stampOf is a binary's version stamp (version.Stamp's shape) at v.
func stampOf(v string) string { return "\x00iormesh-version:" + v + "\x00" }

// jarWith is a jar whose manifest is manifest (none when empty).
func jarWith(t *testing.T, manifest string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if manifest != "" {
		w, err := zw.Create(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(manifest)); err != nil {
			t.Fatal(err)
		}
	}
	w, err := zw.Create("iormesh/agent/Agent.class")
	if err != nil {
		t.Fatal(err)
	}
	// A stamp-like string inside a class must not count for a jar.
	if _, err := w.Write([]byte(stampOf("0.3.0"))); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// agentManifest is the manifest `mage agentBuild` writes, at v.
func agentManifest(v string) string {
	return "Manifest-Version: 1.0\r\nPremain-Class: iormesh.agent.Agent\r\n" +
		"Implementation-Title: iormesh-agent\r\nImplementation-Version: " + v + "\r\n\r\n" +
		"Name: x\r\nImplementation-Version: 9.9.9\r\n"
}

func TestCheckStamp(t *testing.T) {
	tests := []struct {
		name, file string
		data       []byte
		ok         bool
	}{
		{"tracer at version", "iormesh-linux-arm64", []byte("\x7fELF" + stampOf("0.3.0")), true},
		{"stale tracer", "iormesh-linux-amd64", []byte("\x7fELF" + stampOf("0.2.0")), false},
		{"unstamped tracer", "iormeshctl-darwin-arm64", []byte("\xcf\xfa\xed\xfe"), false},
		{"jar at version", "iormesh-agent.jar", jarWith(t, agentManifest("0.3.0")), true},
		{"stale jar", "iormesh-agent.jar", jarWith(t, agentManifest("0.2.0")), false},
		// Only the main section counts, not a per-entry one.
		{"jar version only in an entry section", "iormesh-agent.jar",
			jarWith(t, "Manifest-Version: 1.0\n\nName: x\nImplementation-Version: 0.3.0\n"), false},
		{"jar without manifest", "iormesh-agent.jar", jarWith(t, ""), false},
		{"jar without version", "iormesh-agent.jar", jarWith(t, "Manifest-Version: 1.0\n"), false},
		{"jar continuation line", "iormesh-agent.jar",
			jarWith(t, "Manifest-Version: 1.0\nImplementation-Version: 0.\n 3.0\n"), true},
		{"not a jar", "iormesh-agent.jar", []byte("PK? no" + stampOf("0.3.0")), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckStamp(tt.file, tt.data, "0.3.0")
			if tt.ok != (err == nil) || (err != nil && !errors.Is(err, ErrStaleBuild)) {
				t.Fatalf("CheckStamp = %v, want ok %v", err, tt.ok)
			}
		})
	}
}

// One stale artifact in dist/ fails SignDir before anything is signed:
// no signature is written, not even for the current builds.
func TestSignDirRefusesStaleBuild(t *testing.T) {
	_, priv := newKey(t)
	dir := t.TempDir()
	files := map[string][]byte{
		"iormesh-linux-amd64": []byte("a" + stampOf("0.3.0")),
		"iormesh-linux-arm64": []byte("b" + stampOf("0.2.0")), // left over
		"iormesh-agent.jar":   jarWith(t, agentManifest("0.3.0")),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	signed, err := SignDir(priv, dir, "0.3.0", testTime)
	if !errors.Is(err, ErrStaleBuild) || !strings.Contains(err.Error(), "iormesh-linux-arm64 is stamped 0.2.0") {
		t.Fatalf("SignDir = %v, %v; want the arm64 build refused", signed, err)
	}
	sigs, _ := filepath.Glob(filepath.Join(dir, "*"+SigSuffix))
	if len(signed) != 0 || len(sigs) != 0 {
		t.Fatalf("signed %v, signatures %v; want none", signed, sigs)
	}
	// Rebuilt at the version, the directory signs.
	if err := os.WriteFile(filepath.Join(dir, "iormesh-linux-arm64"), []byte("b"+stampOf("0.3.0")), 0o755); err != nil {
		t.Fatal(err)
	}
	if signed, err := SignDir(priv, dir, "0.3.0", testTime); err != nil || len(signed) != 3 {
		t.Fatalf("SignDir after rebuild = %v, %v", signed, err)
	}
}
