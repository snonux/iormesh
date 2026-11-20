package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"iormesh/internal/k8s"
)

// TestReadyRoundTrip: `iormesh ready -project P` streams the project's
// segments, k8s.Unpack (the controller's side) takes them, and
// `-remove -- NAME` deletes them on the tracer.
func TestReadyRoundTrip(t *testing.T) {
	root := t.TempDir()
	ready := filepath.Join(root, "shop", "ready")
	if err := os.MkdirAll(ready, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ready, "h-s-1-0.parquet"), []byte("seg"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stream, errOut bytes.Buffer
	if err := readyIn(root, []string{"-project", "shop"}, &stream, &errOut); err != nil {
		t.Fatal(err)
	}
	local := t.TempDir()
	taken, err := k8s.Unpack(&stream, local)
	if err != nil || !slices.Equal(taken, []string{"h-s-1-0.parquet"}) {
		t.Fatalf("Unpack = %q, %v", taken, err)
	}
	if b, err := os.ReadFile(filepath.Join(local, "h-s-1-0.parquet")); err != nil || string(b) != "seg" {
		t.Errorf("unpacked = %q, %v", b, err)
	}
	args := append([]string{"-project", "shop", "-remove", "--"}, taken...)
	if err := readyIn(root, args, &stream, &errOut); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(ready); len(entries) != 0 {
		t.Errorf("ready/ still holds %v", entries)
	}
}
