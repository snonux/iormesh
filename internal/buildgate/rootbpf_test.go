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

// rootBPFTestFiles hold the tests `mage bpfRootTest` runs as root: the
// socket tests (M1 step 4) and their tcp_sock fields test (task ve4), the
// loader tests (step 5) and the process-tree and exec-watch tests (M2
// steps 1–2), the Java mark test (task td4) and the data calls' tcp_seq
// tests (task xe4), the tracer in a PID namespace (task 4f4), the stale
// socket owner and pod-mode connect tests (task if4), the process
// exit's group_dead (task nf4), a call on fd -1 across an exec (task
// mf4) and the key an RST abort's CLOSE rebuilds (task qf4), all in the
// one test binary of ./internal.
var rootBPFTestFiles = []string{"internal/sock_root_test.go", "internal/sockseq_root_test.go",
	"internal/loader_root_test.go", "internal/follow_root_test.go",
	"internal/javamark_root_test.go", "internal/dispatch_root_test.go",
	"internal/streamseq_root_test.go", "internal/pidns_root_test.go",
	"internal/sockreuse_root_test.go", "internal/exit_root_test.go",
	"internal/pidnsconnect_root_test.go", "internal/badfd_root_test.go",
	"internal/sockclose_root_test.go"}

// testFunctionsIn returns the names of the top-level Test functions of the Go
// file at the repository path rel. Adapted from ior
// internal/buildgate/rootlink_test.go @ b21afc50328c. Changes: TestMain is
// left out — it runs the tests, it is none (integrationtests' stands in
// for the eval's model, task rb4).
func testFunctionsIn(t *testing.T, rel string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(repoRoot(t), rel), nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	var names []string
	for _, declaration := range file.Decls {
		decl, ok := declaration.(*ast.FuncDecl)
		if ok && decl.Recv == nil && strings.HasPrefix(decl.Name.Name, "Test") && decl.Name.Name != "TestMain" {
			names = append(names, decl.Name.Name)
		}
	}
	return names
}

// TestRootBPFTestsAreTheTestsOfTheFile: a root test added to or renamed in
// the files without updating gatecmd.RootBPFTests would never run, because
// every other `go test` skips it for not being root.
func TestRootBPFTestsAreTheTestsOfTheFile(t *testing.T) {
	listed := gatecmd.RootBPFTests()
	var declared []string
	for _, f := range rootBPFTestFiles {
		declared = append(declared, testFunctionsIn(t, f)...)
	}
	slices.Sort(listed)
	slices.Sort(declared)
	if len(declared) == 0 || !slices.Equal(listed, declared) {
		t.Errorf("gatecmd.RootBPFTests() = %v, but %v declare %v: the two must name the same tests",
			listed, rootBPFTestFiles, declared)
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
