package gatecmd

import (
	"fmt"
	"strings"
)

// KindTestsVerdict judges the `go test -v` output of a kind acceptance run
// (mage k8sAcceptance, k8sDebugRestart: magefile_k8sacceptance.go) by what
// it reported, as `mage integrationTest` does (runIntegrationTestBinary):
// every test of tests must report a top-level PASS (notPassed), and no test
// or subtest may report SKIP (SkippedTests). go test's exit status is 0 for
// both, so without this gate a run whose checks skipped - an existing
// cluster without a node or a crictl prefix to kill the app container's
// init or read the tracer's exit from - did setup, deploy and teardown,
// checked nothing and was green (task si4). The caller checks the exit
// status first; nil means every test passed and nothing skipped.
func KindTestsVerdict(output string, tests ...string) error {
	if missing := notPassed(output, tests); len(missing) != 0 {
		return fmt.Errorf("go test did not report these kind tests as passed: %s",
			strings.Join(missing, ", "))
	}
	// Go reports a parent as passed when all its subtests skipped, so
	// every skip, at any level, fails the run too.
	if skipped := SkippedTests(output); len(skipped) != 0 {
		return fmt.Errorf("these kind tests or subtests skipped, so what they check went unchecked: %s",
			strings.Join(skipped, ", "))
	}
	return nil
}
