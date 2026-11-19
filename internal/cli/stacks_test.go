package cli

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOpenStacksOffRemovesStale: a run without -native-stacks leaves no
// stacks.ndjson of an earlier run next to its events, and its nil output
// is inert.
func TestOpenStacksOffRemovesStale(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, StacksFile)
	if err := os.WriteFile(stale, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := openStacks(runConfig{out: dir}, newFakeTracer())
	if err != nil || st != nil {
		t.Fatalf("openStacks = %v, %v; want nil, nil", st, err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stale %s kept: %v", StacksFile, err)
	}
	if st.ids() != nil || st.close() != nil || st.summary() != "" {
		t.Fatal("a nil stack output is not inert")
	}
}

// TestOpenStacksOn: -native-stacks creates the table file and reports its
// counts on the summary line.
func TestOpenStacksOn(t *testing.T) {
	dir := t.TempDir()
	st, err := openStacks(runConfig{out: dir, nativeStacks: true}, newFakeTracer())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ids().ID(1, 1, -14); err != nil {
		t.Fatal(err)
	}
	if err := st.close(); err != nil {
		t.Fatal(err)
	}
	if got := st.summary(); !strings.Contains(got, "native stacks 0 (uncaptured 1)") {
		t.Fatalf("summary %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, StacksFile)); err != nil {
		t.Fatal(err)
	}
}
