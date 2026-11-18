package buildgate

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"iormesh/internal/gatecmd"
)

// rootBPFTestFile holds the tests `mage bpfRootTest` runs as root.
const rootBPFTestFile = "internal/sock_root_test.go"

// testFunctionsIn returns the names of the top-level Test functions of the Go
// file at the repository path rel. Adapted from ior
// internal/buildgate/rootlink_test.go @ b21afc50328c.
func testFunctionsIn(t *testing.T, rel string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(repoRoot(t), rel), nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	var names []string
	for _, declaration := range file.Decls {
		decl, ok := declaration.(*ast.FuncDecl)
		if ok && decl.Recv == nil && strings.HasPrefix(decl.Name.Name, "Test") {
			names = append(names, decl.Name.Name)
		}
	}
	return names
}

// TestRootBPFTestsAreTheTestsOfTheFile: a root test added to or renamed in
// the file without updating gatecmd.RootBPFTests would never run, because
// every other `go test` skips it for not being root.
func TestRootBPFTestsAreTheTestsOfTheFile(t *testing.T) {
	listed := gatecmd.RootBPFTests()
	declared := testFunctionsIn(t, rootBPFTestFile)
	slices.Sort(listed)
	slices.Sort(declared)
	if len(declared) == 0 || !slices.Equal(listed, declared) {
		t.Errorf("gatecmd.RootBPFTests() = %v, but %s declares %v: the two must name the same tests",
			listed, rootBPFTestFile, declared)
	}
}

// TestRootBPFTestsNotPassed: only a top-level "--- PASS" counts; a skip, a
// subtest's indented pass or a missing verdict leaves the test missing.
func TestRootBPFTestsNotPassed(t *testing.T) {
	names := gatecmd.RootBPFTests()
	if len(names) < 2 {
		t.Fatalf("gatecmd.RootBPFTests() = %v, want at least two to tell one missing from all", names)
	}
	var rest []string
	for _, name := range names[1:] {
		rest = append(rest, "--- PASS: "+name+" (0.50s)")
	}
	cases := map[string]struct {
		output string
		want   []string
	}{
		"all pass":        {"--- PASS: " + names[0] + " (1s)\n" + strings.Join(rest, "\n"), nil},
		"first skipped":   {"--- SKIP: " + names[0] + " (0s)\n" + strings.Join(rest, "\n"), names[:1]},
		"subtest only":    {"    --- PASS: " + names[0] + " (1s)\n" + strings.Join(rest, "\n"), names[:1]},
		"no tests to run": {"testing: warning: no tests to run\nPASS", names},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := gatecmd.RootBPFTestsNotPassed(tc.output); !slices.Equal(got, tc.want) {
				t.Errorf("RootBPFTestsNotPassed() = %v, want %v", got, tc.want)
			}
		})
	}
}
