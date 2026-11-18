// Adapted from ior internal/generate/docs_drift_test.go @ b21afc50328c.
// Changes: only the repo-file readers are kept; the drift checks against
// ior's docs/output.md were not lifted (iormesh has no such document).

package generate

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// readCSource returns a hand-written C file from internal/c so tests can
// assert that the generated handlers and the shared BPF helpers they call stay
// consistent.
func readCSource(name string) (string, error) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	content, err := os.ReadFile(filepath.Join(repoRoot, "internal", "c", name))
	if err != nil {
		return "", err
	}
	return string(content), nil
}

// readRepoFile returns a file of the repository by its path parts.
func readRepoFile(parts ...string) (string, error) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	content, err := os.ReadFile(filepath.Join(append([]string{repoRoot}, parts...)...))
	if err != nil {
		return "", err
	}
	return string(content), nil
}

// readGeneratedTracepointsC returns the committed BPF C artifact so tests can
// assert invariants over the generated handlers, not just over freshly
// generated output.
func readGeneratedTracepointsC() (string, error) {
	return readRepoFile("internal", "c", "generated_tracepoints.c")
}
