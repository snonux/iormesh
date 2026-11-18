// Adapted from ior internal/buildgate/buildgate_test.go @ b21afc50328c.
// Changes: kept the module, lint-config, argv and no-nolint gates; dropped
// the Magefile AST, World, ioworkload-exclusion and planted-defect tests.

package buildgate

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"iormesh/internal/gatecmd"
)

// repoRoot returns the repository root, resolved from this test's own location
// so the test does not depend on the working directory `go test` happens to use.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := filepath.Join(wd, "..", "..")
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("go.mod not found at resolved repo root %s: %v", root, err)
	}
	return root
}

// TestMageTagHidesNoFile underwrites Lint running a single pass. `mage lint`
// passes --build-tags mage, which covers every package plus Magefile.go only
// while nothing is excluded *by* that tag; a file that the default build
// includes and the tagged build drops would never be linted at all.
//
// The two package lists are compared rather than the constraints parsed. Three
// rounds of regex each missed a spelling the toolchain accepts (`!mage`, then
// `!(mage)` and `! mage`, then `!(linux && mage)`), and parsing //go:build in
// turn missed a file carrying only the older `// +build` form. Asking `go
// list` twice cannot miss a spelling, because it is the toolchain answering.
func TestMageTagHidesNoFile(t *testing.T) {
	root := repoRoot(t)
	plain := goFilesByPackage(t, root)
	tagged := goFilesByPackage(t, root, "-tags", "mage")
	for pkg, files := range plain {
		for _, f := range files {
			if !slices.Contains(tagged[pkg], f) {
				t.Errorf("%s (package %s) is in the default build but not the -tags mage build, so `mage lint`'s single pass never sees it", f, pkg)
			}
		}
	}
}

// goFilesByPackage returns the Go files of every package in the module, as the
// toolchain resolves them under the given extra flags.
func goFilesByPackage(t *testing.T, root string, flags ...string) map[string][]string {
	t.Helper()
	args := append([]string{"list", "-e"}, flags...)
	args = append(args, "-f", "{{.ImportPath}}\t{{range .GoFiles}}{{.}} {{end}}{{range .TestGoFiles}}{{.}} {{end}}", "./...")
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	// go list needs the cgo environment for the libbpfgo-importing packages,
	// exactly as the Mage targets do; `mage test` has already exported it.
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("go list %v failed (cgo environment not exported?): %v", flags, err)
	}
	byPkg := map[string][]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pkg, files, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		byPkg[pkg] = strings.Fields(files)
	}
	return byPkg
}

// TestNoNestedModules underwrites every `./...` in this repository. A go.mod
// below the root cuts its whole subtree out of the main module, so `mage
// lint`, `mage vet` and `mage test` all stop seeing it at once and all three
// keep reporting success.
func TestNoNestedModules(t *testing.T) {
	root := repoRoot(t)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "vendor" || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "go.mod" || filepath.Dir(path) == root {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		t.Errorf("%s is a nested module; everything under %s drops out of ./... , so lint, vet and test all stop covering it while still reporting success", rel, filepath.Dir(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}

// golangciConfig models .golangci.yml as raw maps on purpose, all the way
// down. Typed fields would let yaml.Unmarshal silently drop any key the struct
// does not name, and every way this gate has been found to be defeatable was a
// *different* key from the one the tests were watching: `linters.disable`
// beats `enable`, `exclusions.paths` and `.presets` beat `exclusions.rules`,
// `settings.errcheck.exclude-functions` exempts a function module-wide, and
// `run.issues-exit-code: 0` / `run.tests: false` silence the gate from a
// section none of it appears in. So the tests below work from the whole
// document and treat an unreviewed key as a failure.
type golangciConfig map[string]any

// section returns cfg[key] as a mapping, or nil when it is absent. It fails
// the test when the key is present but not a mapping this code can walk.
//
// That case is not hypothetical: yaml.v3 decodes a mapping into the named type
// golangciConfig only while every key in it is a string. A single non-string
// key - `true: 1` is enough - makes it a map[any]any instead, and a silent
// type assertion would hand back nil, which assertOnlyKnownKeys would then
// walk to a clean pass. One stray key would blind the check on that whole
// section.
func (c golangciConfig) section(t *testing.T, key string) golangciConfig {
	t.Helper()
	raw, present := c[key]
	if !present || raw == nil {
		return nil
	}
	m, ok := raw.(golangciConfig)
	if !ok {
		t.Fatalf("%q in .golangci.yml is %T, not a mapping this test can inspect; a non-string key inside it would otherwise silently disable every assertion about that section", key, raw)
	}
	return m
}

// mapStrings returns m[key] as a []string, tolerating a scalar.
func mapStrings(m golangciConfig, key string) []string {
	switch v := m[key].(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			out = append(out, fmt.Sprint(item))
		}
		return out
	case nil:
		return nil
	default:
		return []string{fmt.Sprint(v)}
	}
}

// assertOnlyKnownKeys fails for any key in m that is not in known. This is the
// deny-by-default rule the config assertions rest on.
func assertOnlyKnownKeys(t *testing.T, where string, m golangciConfig, known ...string) {
	t.Helper()
	for key := range m {
		if !slices.Contains(known, key) {
			t.Errorf("unreviewed %s key %q in .golangci.yml. Keys here can silence the gate without touching anything the other tests watch (e.g. linters.disable, linters.exclusions.paths, run.issues-exit-code). Review what it does, then add it to the known set in %s.", where, key, "buildgate_test.go")
		}
	}
}

// configNames are every filename golangci-lint will load as its configuration.
// It prefers .golangci.yaml/.toml/.json over the .golangci.yml this suite
// asserts on, so an added sibling would shadow the reviewed config entirely
// while every assertion below kept passing against a file no longer in use.
var configNames = []string{
	".golangci.yml", ".golangci.yaml", ".golangci.toml", ".golangci.json",
}

// TestOnlyOneLintConfig fails if a second configuration file appears beside
// the reviewed one.
func TestOnlyOneLintConfig(t *testing.T) {
	root := repoRoot(t)
	for _, name := range configNames {
		if name == ".golangci.yml" {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			t.Errorf("%s exists and shadows .golangci.yml, which every other assertion in this package reads; golangci-lint would use %s instead", name, name)
		}
	}
}

func loadGolangciConfig(t *testing.T) golangciConfig {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), ".golangci.yml"))
	if err != nil {
		t.Fatalf("read .golangci.yml: %v", err)
	}
	var cfg golangciConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse .golangci.yml: %v", err)
	}
	return cfg
}

// TestLintConfigHasNoUnreviewedSections walks the whole document and fails on
// any key that has not been looked at. `run` in particular contains
// issues-exit-code (which makes the linter exit 0 with findings present) and
// tests (which removes every _test.go file from the gate).
func TestLintConfigHasNoUnreviewedSections(t *testing.T) {
	cfg := loadGolangciConfig(t)
	assertOnlyKnownKeys(t, "top-level", cfg, "version", "linters", "issues")

	linters := cfg.section(t, "linters")
	settings := linters.section(t, "settings")
	exclusions := linters.section(t, "exclusions")
	assertOnlyKnownKeys(t, "linters", linters, "default", "enable", "settings", "exclusions")
	assertOnlyKnownKeys(t, "linters.settings", settings, "staticcheck")
	// One level further down: `checks` is the key that decides which analyses
	// actually run, and `checks: [SA4006]` leaves staticcheck enabled while
	// switching off everything the fixture does not happen to trigger.
	assertOnlyKnownKeys(t, "linters.settings.staticcheck", settings.section(t, "staticcheck"), "checks")
	// No `rules`: iormesh has no exclusion rule yet, and ior's one (for its
	// stimulus binary) was not lifted. Adding one is a reviewed change here.
	assertOnlyKnownKeys(t, "linters.exclusions", exclusions, "generated")
	// The value matters, not just the key. Under golangci-lint's default
	// `lax`, prefixing any file with "// Code generated by hand. DO NOT EDIT."
	// removes it from the gate exactly the way //nolint does - and //nolint is
	// banned outright for that reason. `strict` does not help: it matches that
	// header too. Only `disable` closes it, and it costs nothing, because the
	// repository's two generated Go files have no findings under any setting.
	if generated, _ := exclusions["generated"].(string); generated != "disable" {
		t.Errorf("linters.exclusions.generated is %q, want \"disable\". Any other value lets a hand-written \"Code generated\" header take a file out of the gate.", generated)
	}
	assertOnlyKnownKeys(t, "issues", cfg.section(t, "issues"), "max-issues-per-linter", "max-same-issues")
}

// TestNoNolintDirectives enforces the rule in AGENTS.md ("Code Style"): a
// deliberately discarded error is written as an explicit `_ =`, and the single
// blanket exemption lives in .golangci.yml where it is stated once with its
// reasoning.
//
// This is the regression the lint gate was added to prevent, and the gate
// cannot catch it itself: golangci-lint honours //nolint by construction, so
// one comment silently removes a file from the gate while `mage lint` keeps
// reporting "0 issues" - which is exactly how 24 of them accumulated before.
func TestNoNolintDirectives(t *testing.T) {
	root := repoRoot(t)
	// Assembled rather than written out so this test does not report itself,
	// and skip this file outright: its documentation names the directive.
	// The spacing is deliberate: golangci-lint honours `// nolint` with any
	// run of spaces after the slashes exactly as it honours `//nolint`, so
	// matching the tight form alone left the ban one keystroke wide.
	// golangci-lint trims the cutset "/ " after the comment marker before it
	// looks for the directive, so `// /nolint` and `//  /  nolint` are honoured
	// exactly as `//nolint` is - and a pattern matching only spaces left the
	// ban one character wide for the second time. A tab is *not* trimmed, so
	// `//\tnolint` does not suppress anything; it is matched anyway, because a
	// directive that looks live and is not is worse than one that is banned.
	needle := regexp.MustCompile(`//[/ \t]*` + "nolint")
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller could not identify this test file")
	}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "vendor" || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || path == self {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		for i, line := range strings.Split(string(src), "\n") {
			if needle.MatchString(line) {
				t.Errorf("%s:%d has a %v directive: %s\nUse an explicit `_ =` for a deliberate discard, or add the case to .golangci.yml where the exemption is stated once with its reasoning (AGENTS.md, Code Style).", rel, i+1, needle, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}

// lintFixture is a package that violates exactly the two linters this gate
// exists to run: an unchecked error (errcheck) and a dead store (staticcheck
// SA4006). It is written to a temp directory rather than committed, so it
// cannot itself be caught by the gate it is used to test.
const lintFixture = `package fixture

import (
	"fmt"
	"os"
)

func Fixture() string {
	os.Remove("/nonexistent/lint-gate-fixture")
	s := fmt.Sprintf("dead %d", 1)
	s = fmt.Sprintf("live %d", 2)
	return s
}

func Printf(format string) {
	fmt.Printf(format)
}
`

// lintFixtureTest puts the same unchecked error in a test file.
const lintFixtureTest = `package fixture

import "os"

func helper() {
	os.Remove("/nonexistent/lint-gate-fixture-test")
}
`

// The gates' command lines live in internal/gatecmd rather than in
// Magefile.go, which is behind the mage build tag and so unreachable from any
// test. That put several rounds of review into a losing pattern: each
// assertion about what `mage lint` runs had to be made against the *source
// text* of a function nothing could call, and each was walked around - a body
// that merely mentions the right strings, a flag routed through a constant, a
// helper wrapping the invocation, a guard clause returning before the call.
//
// Asserting on data, and then running it, replaces all of that.

// TestLintArgvCoversTheWholeModule pins the lint command line itself.
func TestLintArgvCoversTheWholeModule(t *testing.T) {
	if got := gatecmd.GolangciLintBin; got != "golangci-lint" {
		t.Errorf("GolangciLintBin is %q; the gate runs something else", got)
	}
	assertArgvAllowed(t, "LintConfigVerify", gatecmd.LintConfigVerify(),
		[]string{"golangci-lint", "config", "verify"})
	assertArgvAllowed(t, "LintRun", gatecmd.LintRun(),
		[]string{"golangci-lint", "run", "--build-tags", "mage", "./..."})

	for _, want := range []string{"run", "--build-tags", "mage", "./..."} {
		if !slices.Contains(gatecmd.LintRun(), want) {
			t.Errorf("LintRun() omits %q; it must lint the whole module with the mage build tag (%v)", want, gatecmd.LintRun())
		}
	}
}

// TestVetArgvCoversTheWholeModule pins vet's single pass. An extra
// `-printf=false` here would switch an analyzer off module-wide while
// `mage vet` keeps exiting 0.
func TestVetArgvCoversTheWholeModule(t *testing.T) {
	assertArgvAllowed(t, "VetAll", gatecmd.VetAll(),
		[]string{"go", "vet", "./..."})
}

// TestTestArgvRunsEverything pins the suite that carries every assertion in
// this package. Narrowing it, or filtering it with -run, switches all of them
// off in one edit while `mage test` keeps reporting success.
func TestTestArgvRunsEverything(t *testing.T) {
	assertArgvAllowed(t, "TestAll", gatecmd.TestAll(),
		[]string{"go", "test", "./...", "-failfast", "-timeout=90m"})
	if !slices.Contains(gatecmd.TestAll(), "./...") {
		t.Errorf("TestAll() does not run ./... ; packages dropped from it leave mage test silently, internal/buildgate included (%v)", gatecmd.TestAll())
	}
	assertArgvAllowed(t, "CleanTestCache", gatecmd.CleanTestCache(),
		[]string{"go", "clean", "-testcache"})
}

// assertArgvAllowed compares an argv against the reviewed one exactly.
//
// An allow-list rather than a list of flags known to be bad, because
// blocklisting lost by one character twice: --issues-exit-code was banned and
// --issues-exit-code=0 walked past it; --config was banned while
// --new-from-rev, --new, -D and --enable-only were never on the list. Each
// makes a tool report findings and exit 0, or hides them outright.
func assertArgvAllowed(t *testing.T, name string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("gatecmd.%s() has changed.\n  is:   %v\n  want: %v\nArguments here can make a tool exit 0 with findings (--issues-exit-code=0), limit what it looks at (--new-from-rev, -run), turn a linter off (-D) or point it at another config (--config=...). Review the change, then update the expected argv.", name, got, want)
	}
}

// TestLintArgvRejectsAKnownDefect runs the lint gate's real command line - the
// argv `mage lint` runs, with the repository's own .golangci.yml - against a
// package containing an unchecked error and two dead stores, and requires all
// of them to be reported.
//
// This is the assertion the config- and argv-level ones exist to support: it
// asks what the gate *does*. A configuration that is plausible key by key and
// collectively inert fails here regardless of how it was spelled.
func TestLintArgvRejectsAKnownDefect(t *testing.T) {
	bin, err := exec.LookPath(gatecmd.GolangciLintBin)
	if err != nil {
		t.Skipf("%s not on PATH; `mage lint` would fail the same way", gatecmd.GolangciLintBin)
	}

	// The fixture is its own module so the linter does not try to load the
	// real one, which needs the libbpfgo cgo environment to typecheck.
	dir := writeLintFixtureModule(t)

	argv := gatecmd.LintRun()
	cmd := exec.Command(bin, append(argv[1:], "--output.text.path", "stdout")...)
	cmd.Dir = dir
	cmd.Env = privateLintLockEnv(t, os.Environ())
	out, runErr := cmd.CombinedOutput()
	report := string(out)

	if runErr == nil {
		t.Errorf("the lint gate accepted a package with an unchecked error and two dead stores; it reports nothing and would pass any tree.\n%s", report)
	}
	// Two different SA checks, because pinning one lets `checks: [SA4006]`
	// disable the rest of the family while this test still passes.
	for _, want := range []struct{ what, marker string }{
		{"errcheck", "fixture.go"},
		{"errcheck in a _test.go file", "fixture_test.go"},
		{"staticcheck SA4006", "SA4006"},
		{"staticcheck SA1006", "SA1006"},
	} {
		if !strings.Contains(report, want.marker) {
			t.Errorf("%s reported nothing for the fixture (looked for %q); it is enabled in name only.\n%s", want.what, want.marker, report)
		}
	}
}

// writeLintFixtureModule writes the lint fixture module (lintFixture,
// lintFixtureTest and the repository's own .golangci.yml) into a temporary
// directory and returns it.
func writeLintFixtureModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	// An old language version on purpose: the fixture needs nothing modern,
	// and naming the repository's own would break this test on any machine
	// whose golangci-lint was built with an older Go than go.mod requires.
	write("go.mod", "module lintfixture\n\ngo 1.21\n")
	write("fixture.go", lintFixture)
	// A defect in a _test.go file too, so `run.tests: false` - which drops
	// every test file in the repository, where most of what this gate has
	// caught lives - fails here as well.
	write("fixture_test.go", lintFixtureTest)

	cfg, err := os.ReadFile(filepath.Join(repoRoot(t), ".golangci.yml"))
	if err != nil {
		t.Fatalf("read .golangci.yml: %v", err)
	}
	write(".golangci.yml", string(cfg))
	return dir
}

// privateLintLockEnv returns env with TMPDIR pointed at a directory private
// to this test, so the golangci-lint it runs takes its own instance lock.
//
// golangci-lint serializes itself on $TMPDIR/golangci-lint.lock and, by
// default, gives up after five seconds with "parallel golangci-lint is
// running". Any concurrent `mage lint` - another shell, another agent, a
// parallel test binary - then made the two tests that run the real gate fail
// for a reason unrelated to the gate: the marker checks saw only that one line.
//
// Moving the lock is chosen over the alternatives because it leaves the
// command line alone. --allow-parallel-runners or --allow-serial-runners would
// have to be appended to the argv these tests exist to prove, and a retry
// loop keyed on the error text would still lose to a long enough concurrent
// run. The lint results cache
// (~/.cache/golangci-lint) is still shared: it is built on Go's build cache,
// which is safe for concurrent processes - the lock guards against running
// twice, not against the cache.
//
// os/exec keeps the last value of a duplicated variable, so appending is
// enough to override an inherited TMPDIR.
func privateLintLockEnv(t *testing.T, env []string) []string {
	t.Helper()
	return append(env, "TMPDIR="+t.TempDir())
}
