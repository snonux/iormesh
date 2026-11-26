package dist

import (
	"archive/zip"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestJarDate(t *testing.T) {
	// An odd second and a non-UTC zone: jar --date gets UTC, to the second.
	at := time.Date(2026, 11, 26, 9, 19, 15, 0, time.FixedZone("CET", 3600))
	if got, want := JarDate(at), "2026-11-26T08:19:15Z"; got != want {
		t.Errorf("JarDate = %q, want %q", got, want)
	}
}

// writeZip writes a zip whose entries carry the given mtimes. Go's writer
// adds an extended timestamp (one-second resolution) to the DOS one the
// jar tool writes alone; JarDated must accept both.
func writeZip(t *testing.T, path string, mtimes ...time.Time) {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for i, m := range mtimes {
		h := &zip.FileHeader{Name: string(rune('a'+i)) + ".class", Method: zip.Deflate,
			Modified: m.UTC()}
		if _, err := w.CreateHeader(h); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestJarDated(t *testing.T) {
	at := time.Date(2026, 11, 26, 8, 19, 15, 0, time.UTC)
	dir := t.TempDir()
	tests := []struct {
		name   string
		mtimes []time.Time
		want   bool
	}{
		// The odd second is stored rounded down; it still matches.
		{"all at the date", []time.Time{at, at.Add(-time.Second)}, true},
		{"one entry later", []time.Time{at, at.Add(2 * time.Second)}, false},
		{"built before the commit", []time.Time{at.Add(-time.Hour), at.Add(-time.Hour)}, false},
		{"no entries", nil, false},
	}
	for i, tt := range tests {
		path := filepath.Join(dir, string(rune('0'+i))+".jar")
		writeZip(t, path, tt.mtimes...)
		got, err := JarDated(path, at)
		if err != nil || got != tt.want {
			t.Errorf("%s: JarDated = %v, %v; want %v", tt.name, got, err, tt.want)
		}
	}
	if _, err := JarDated(filepath.Join(dir, "missing.jar"), at); err == nil {
		t.Error("JarDated of a missing jar: no error")
	}
	notZip := filepath.Join(dir, "not.jar")
	if err := os.WriteFile(notZip, []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := JarDated(notZip, at); err == nil {
		t.Error("JarDated of a non-zip: no error")
	}
}

// TestJarToolReproducible packs the same tree twice with the JDK's jar
// tool and JarDate, the way magefiles/magefile_agent.go packs the agent
// jar, its files written in different orders and at different times: the
// bytes must match and JarDated must hold (task bm4). It skips without a
// jar tool on PATH; CI has one (setup-java).
func TestJarToolReproducible(t *testing.T) {
	jarTool, err := exec.LookPath("jar")
	if err != nil {
		t.Skip("no jar tool on PATH")
	}
	at := time.Date(2026, 11, 26, 8, 19, 15, 0, time.UTC)
	dir := t.TempDir()
	build := func(name string, order []string, mtime time.Time) []byte {
		tree := filepath.Join(dir, name)
		for _, f := range order {
			p := filepath.Join(tree, f)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(f), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(p, mtime, mtime); err != nil {
				t.Fatal(err)
			}
		}
		jar := filepath.Join(dir, name+".jar")
		out, err := exec.Command(jarTool, "--create", "--date", JarDate(at),
			"--file", jar, "-C", tree, ".").CombinedOutput()
		if err != nil {
			t.Fatalf("jar: %v: %s", err, out)
		}
		if ok, err := JarDated(jar, at); err != nil || !ok {
			t.Errorf("%s: JarDated = %v, %v; want true", name, ok, err)
		}
		b, err := os.ReadFile(jar)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	files := []string{"q/Q.class", "a/A.class", "m.txt", "a/B.class", "z.tsv"}
	reversed := []string{"z.tsv", "a/B.class", "m.txt", "a/A.class", "q/Q.class"}
	one := build("one", files, time.Now().Add(-time.Hour))
	two := build("two", reversed, time.Now())
	if !bytes.Equal(one, two) {
		t.Error("two jars of the same tree differ")
	}
}
