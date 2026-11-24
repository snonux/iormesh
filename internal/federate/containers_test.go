package federate

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestReadContainers (task jk4): a missing map is an empty one, a good
// one reads back, and a JSON null is an empty map — never nil, which a
// caller would assign into.
func TestReadContainers(t *testing.T) {
	dir := t.TempDir()
	m, err := ReadContainers(dir)
	if err != nil || m == nil || len(m) != 0 {
		t.Fatalf("missing map: %v, %v; want an empty map", m, err)
	}
	id := strings.Repeat("ab", 32)
	writeMap(t, dir, `{"`+id+`": "app"}`)
	if m, err = ReadContainers(dir); err != nil || !maps.Equal(m, map[string]string{id: "app"}) {
		t.Errorf("good map: %v, %v", m, err)
	}
	writeMap(t, dir, `null`)
	if m, err = ReadContainers(dir); err != nil || m == nil || len(m) != 0 {
		t.Errorf("null map: %v, %v; want an empty, non-nil map", m, err)
	}
}

// TestReadContainersRefusals: a map that is not one is an error naming
// why — bad JSON, not an object, an id or a name no cluster issues, a
// file past the size bound — and so is a store entry that is no regular
// file: a symlink is not followed, and a FIFO is refused at once rather
// than hanging the build.
func TestReadContainersRefusals(t *testing.T) {
	id := strings.Repeat("ab", 32)
	for name, body := range map[string]string{
		"bad JSON":   `{`,
		"an array":   `["app"]`,
		"a bad id":   `{"` + strings.ToUpper(id) + `": "app"}`,
		"a bad name": `{"` + id + `": "App\u001b[31m"}`,
		"oversized":  `{"` + id + `": "app"` + strings.Repeat(" ", maxContainersSize) + `}`,
	} {
		dir := t.TempDir()
		writeMap(t, dir, body)
		if m, err := ReadContainers(dir); err == nil {
			t.Errorf("%s: read %v, want an error", name, m)
		}
	}
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "map.json")
	if err := os.WriteFile(target, []byte(`{"`+id+`": "app"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, ContainersFile)); err != nil {
		t.Fatal(err)
	}
	if m, err := ReadContainers(dir); err == nil {
		t.Errorf("symlink: read %v, want an error", m)
	}
	fifo := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(fifo, ContainersFile), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := ReadContainers(fifo); done <- err }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("FIFO: err %v, want not a regular file", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a FIFO hung ReadContainers")
	}
}

// writeMap writes dir's container map.
func writeMap(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ContainersFile), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
