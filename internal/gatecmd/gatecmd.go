// Adapted from ior internal/gatecmd/gatecmd.go @ b21afc50328c.
// Changes: no unsafeptr exemption (iormesh has no ioworkload), so vet is one
// pass over ./...; the integration-test root-link argv is not lifted yet.

// Package gatecmd holds the command lines of the repository's static-analysis
// gates as data.
//
// Magefile.go is behind `//go:build mage`, so nothing can import it and any
// test of what `mage lint` or `mage vet` actually runs would have to work from
// the source text. ior tried that for several rounds and lost repeatedly: a
// target whose body merely mentions the right strings, a flag routed through
// a constant, a helper wrapping the invocation, a guard clause returning
// early - each read correctly and ran something else, or nothing.
//
// Keeping the argv here instead makes it ordinary Go that internal/buildgate
// imports, asserts on, and executes against a package with a planted defect.
// The Mage targets become the thin wrappers that supply the environment and
// check the error.
package gatecmd

// GolangciLintBin is the linter binary the lint gate runs, and GolangciLintPkg
// is the module path `mage lint` names when it is missing from PATH.
const (
	GolangciLintBin = "golangci-lint"
	GolangciLintPkg = "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest"
)

// LintConfigVerify is the pre-flight that rejects a configuration `run` would
// otherwise half-ignore: golangci-lint silently drops keys it does not
// recognize, so a typo reports "0 issues" from a config nobody reviewed.
func LintConfigVerify() []string {
	return []string{GolangciLintBin, "config", "verify"}
}

// LintRun is the lint gate itself.
//
// The mage build tag is passed so Magefile.go is linted too: the default build
// ignores `//go:build mage`, which would leave the one file that defines the
// gate as the one file outside it. A single pass suffices only while no file
// is excluded *by* that tag, which internal/buildgate asserts against the
// toolchain's own package lists.
func LintRun() []string {
	return []string{GolangciLintBin, "run", "--build-tags", "mage", "./..."}
}

// VetAll vets the whole module with the full analyzer set.
func VetAll() []string {
	return []string{"go", "vet", "./..."}
}

// CleanTestCache drops cached test results so Test always really runs.
func CleanTestCache() []string {
	return []string{"go", "clean", "-testcache"}
}

// TestAll runs the whole suite. The timeout is ior's; the C harness tests in
// internal/generate compile and run many small programs.
func TestAll() []string {
	return []string{"go", "test", "./...", "-failfast", "-timeout=90m"}
}
