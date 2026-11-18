package gatecmd

import (
	"fmt"
	"strconv"
	"strings"
)

// The argv, the gate and the closing summary of `mage integrationTest` (M1
// step 11). Adapted from ior internal/gatecmd (gatecmd.go RootLinkTests*,
// unexercisedskips.go) @ b21afc50328c. Changes: ior runs its integration
// binary without -test.v and believes the exit status plus a skip summary;
// iormesh runs it with -test.v and requires every listed test to report
// PASS, like the root BPF tests (rootbpf.go), and no test or subtest to
// report SKIP (SkippedTests) - a skip, for root missing or for a case no
// run exercised, fails the target after the summary says why. Only the unexercised-case marker is lifted; ior's fold and row
// skips have no iormesh counterpart yet.

// IntegrationTestBinary is where `mage integrationTest` compiles the test
// binary of ./integrationtests: the repository root, as in ior, so its
// absolute path can match a scoped sudoers rule.
const IntegrationTestBinary = "integrationtests.test"

// IntegrationParallelEnv is the knob for `go test -parallel` of the
// parallel target (ior's INTEGRATION_PARALLEL). IntegrationParallelFlag is
// the test binary's own flag (integrationtests/helpers_test.go) that makes
// the tests call t.Parallel; the serial target leaves it off and every test
// runs alone. ior passes that switch as an environment variable; a flag
// travels in the argv, which `sudo -n -E` cannot filter and this package
// pins.
const (
	IntegrationParallelEnv  = "INTEGRATION_PARALLEL"
	IntegrationParallelFlag = "-iormesh.parallel"
)

// UnexercisedSkipMarker begins the line a test prints when it skips because
// none of its runs exercised the case it tests (every run lost records in
// the kernel, so a golden miss could not be told from loss); the rest of
// the line names the test and why.
const UnexercisedSkipMarker = "iormesh-integration: test skipped, case not exercised: "

// IntegrationTests are the top-level tests of ./integrationtests. They are
// listed rather than matched so a renamed test fails a gate
// (internal/buildgate) instead of silently no longer running.
func IntegrationTests() []string {
	return []string{
		"TestWalkingSkeleton",
		"TestFileBasic",
		"TestUntracedProcessStaysOut",
		"TestRestartPattern",
		"TestSecondRunNormalizesIdentically",
		"TestDiffNormalized",
		"TestParseKernelLoss",
		"TestFirstRunWithoutKernelLoss",
		"TestTracepointExists",
		"TestKernelProbeFindsTheM1Tracepoints",
	}
}

// IntegrationTestArgs are the arguments of the integration test binary.
// parallel > 0 adds -test.parallel and IntegrationParallelFlag; 0 runs
// serially. failfast stops at the first failure, as in ior; -test.v is
// what IntegrationTestsNotPassed reads. The timeouts are ior's.
func IntegrationTestArgs(parallel int) []string {
	timeout := "90m"
	if parallel > 0 {
		timeout = "30m"
	}
	args := []string{
		"-test.run", "^(" + strings.Join(IntegrationTests(), "|") + ")$",
		"-test.failfast",
		"-test.timeout=" + timeout,
		"-test.count=1",
		"-test.v",
	}
	if parallel > 0 {
		args = append(args, "-test.parallel", strconv.Itoa(parallel), IntegrationParallelFlag)
	}
	return args
}

// IntegrationTestsNotPassed returns the tests of IntegrationTests that
// output does not report as passed (see notPassed).
func IntegrationTestsNotPassed(output string) []string {
	return notPassed(output, IntegrationTests())
}

// SkippedTests returns the name of every test output reports as skipped,
// top-level or subtest: the "--- SKIP: " verdict at any indentation. The
// gate needs both: Go prints "--- PASS" for a parent test whose subtests
// all skipped, so IntegrationTestsNotPassed alone would believe a parent
// that checked nothing.
func SkippedTests(output string) []string {
	var skipped []string
	for line := range strings.SplitSeq(output, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimLeft(line, " \t"), "--- SKIP: "); ok {
			name, _, _ := strings.Cut(rest, " ")
			skipped = append(skipped, name)
		}
	}
	return skipped
}

// SkipSummary returns the lines `mage integrationTest` prints after the
// run about the tests that skipped because no run exercised their case:
// none when there are none, else a count and one line per test.
func SkipSummary(output string) []string {
	var skipped []string
	for line := range strings.SplitSeq(output, "\n") {
		if rest, ok := strings.CutPrefix(line, UnexercisedSkipMarker); ok {
			skipped = append(skipped, rest)
		}
	}
	if len(skipped) == 0 {
		return nil
	}
	lines := []string{fmt.Sprintf("%d test(s) SKIPPED because no run exercised the case they test "+
		"(what they check went unchecked on this host):", len(skipped))}
	for _, test := range skipped {
		lines = append(lines, "  "+test)
	}
	return lines
}
