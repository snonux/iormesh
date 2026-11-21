package buildgate

import (
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// testOnlyPkgs are packages no production code may import: they exist for
// tests alone. internal/corereloc relocates the BPF object against a
// foreign kernel BTF and silences libbpf's output while it does (task of4);
// in the tracer that would hide a real load's verifier log.
var testOnlyPkgs = []string{"iormesh/internal/corereloc"}

// TestTestOnlyPackagesOnlyInTests (task bg4) lists every package's
// non-test imports - go list's .Imports leaves out _test.go files - and
// fails on any that names a test-only package. The toolchain answers, so
// no import spelling or build constraint is missed (TestMageTagHidesNoFile
// says why that matters); the mage tag is set so Magefile.go counts too.
func TestTestOnlyPackagesOnlyInTests(t *testing.T) {
	cmd := exec.Command("go", "list", "-e", "-tags", "mage", "-f",
		"{{.ImportPath}}\t{{join .Imports \" \"}}", "./...")
	cmd.Dir = repoRoot(t)
	// go list needs the cgo environment for the libbpfgo-importing
	// packages, exactly as the Mage targets do; `mage test` exports it.
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("go list failed (cgo environment not exported?): %v", err)
	}
	checked := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pkg, imports, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		checked++
		for _, imp := range strings.Fields(imports) {
			if slices.Contains(testOnlyPkgs, imp) {
				t.Errorf("%s imports %s outside its _test.go files; only tests may", pkg, imp)
			}
		}
	}
	if checked == 0 {
		t.Fatal("go list named no package")
	}
}
