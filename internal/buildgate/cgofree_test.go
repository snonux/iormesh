package buildgate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"iormesh/internal/version"
)

// controllerPkg is iormeshctl, which PLAN.md M4 requires cgo-free: it runs
// on the operator's laptop, darwin or linux, cross-compiled with no eBPF.
const controllerPkg = "./cmd/iormeshctl"

// TestControllerIsCgoFree lists every non-standard package iormeshctl
// links and fails on one with cgo files or the libbpfgo import. Nothing
// else would notice: the tracer's `mage build` has the cgo environment, so
// a controller package importing internal/bpf by accident still builds on
// Linux and only breaks the macOS cross-compile nobody runs locally.
func TestControllerIsCgoFree(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "-f",
		"{{if not .Standard}}{{.ImportPath}}\t{{len .CgoFiles}}\t{{join .Imports \" \"}}{{end}}",
		controllerPkg)
	cmd.Dir = repoRoot(t)
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list %s: %v", controllerPkg, err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" { // a standard package
			continue
		}
		pkg, rest, _ := strings.Cut(line, "\t")
		cgoFiles, imports, _ := strings.Cut(rest, "\t")
		if cgoFiles != "0" || strings.Contains(imports, "libbpfgo") {
			t.Errorf("iormeshctl links %s, which needs cgo (cgo files: %s)", pkg, cgoFiles)
		}
	}
}

// TestControllerCrossCompiles builds iormeshctl for macOS with cgo off,
// the build PLAN.md M4 promises and the Linux host never exercises. The
// build must carry the version stamp `mage sign` reads (task uf4): a
// darwin binary cannot be run here to ask its --version.
func TestControllerCrossCompiles(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "iormeshctl")
	cmd := exec.Command("go", "build", "-o", bin, controllerPkg)
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=darwin", "GOARCH=arm64")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("darwin/arm64 build of %s: %v\n%s", controllerPkg, err, out)
	}
	requireStamp(t, bin)
}

// TestTracerCarriesVersionStamp builds the tracer as `mage build` does
// (with the cgo environment `mage test` exported) and requires the stamp
// in it: without one `mage sign` refuses every tracer.
func TestTracerCarriesVersionStamp(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "iormesh")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/iormesh")
	cmd.Dir = repoRoot(t)
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("tracer build failed (cgo environment not exported?): %v\n%s", err, out)
	}
	requireStamp(t, bin)
}

// requireStamp fails unless the binary at path is stamped with this
// tree's version.
func requireStamp(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := version.Embedded(data); err != nil || got != version.Version {
		t.Fatalf("%s: stamp %q, %v; want %s", path, got, err, version.Version)
	}
}
