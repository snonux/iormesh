//go:build mage

// Adapted from ior Magefile.go @ b21afc50328c (IntegrationTest,
// IntegrationTestSerial, runIntegrationTests, compileIntegrationTestBinary,
// runIntegrationTestBinary, resolveIntegrationParallelism).
// Changes: the workload is the Java jar (WorkloadBuild) instead of ior's
// ioworkload; no root link step (BpfRootTest is its own target); the test
// binary runs with -test.v and every test of gatecmd.IntegrationTests must
// report PASS, where ior believes the exit status plus its skip summary;
// the parallel switch is a test-binary flag, not an environment variable.

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"

	"iormesh/internal/gatecmd"
)

// IntegrationTest builds the tracer, the workload jar and the integration
// test binary, then runs the end-to-end tests of ./integrationtests as root
// in parallel. INTEGRATION_PARALLEL tunes `-test.parallel` (default
// min(NumCPU, 2), as in ior).
func IntegrationTest() error {
	parallel, err := resolveIntegrationParallelism()
	if err != nil {
		return err
	}
	return runIntegrationTests(parallel)
}

// IntegrationTestSerial is IntegrationTest one test at a time, for
// debugging a failure without interleaved output.
func IntegrationTestSerial() error {
	return runIntegrationTests(0)
}

// runIntegrationTests builds everything the tests drive - the tests exec
// ./iormesh and `java -jar workload.jar` themselves, like ior's run its
// binaries - and runs the compiled test binary as root from
// ./integrationtests, so the tests find both at "../".
func runIntegrationTests(parallel int) error {
	mg.SerialDeps(Build, WorkloadBuild)
	if err := sh.RunWithV(goEnv(), "go", "test", "-c", "./integrationtests/",
		"-o", gatecmd.IntegrationTestBinary); err != nil {
		return err
	}
	if parallel > 0 {
		fmt.Printf("Running integration tests in parallel (requires root, parallel=%d)...\n", parallel)
	} else {
		fmt.Println("Running integration tests serially (requires root)...")
	}
	return runIntegrationTestBinary(gatecmd.IntegrationTestArgs(parallel))
}

// runIntegrationTestBinary runs the test binary as root and judges it by
// what it reported, not by its exit status: the skip summary first (why a
// test could not be judged on this host), then the gate that every listed
// test passed - a skipped or unmatched test exits 0 too.
func runIntegrationTestBinary(args []string) error {
	var output bytes.Buffer
	err := runRootTestBinary(gatecmd.IntegrationTestBinary, "integrationtests",
		io.MultiWriter(os.Stdout, &output), args...)
	for _, line := range gatecmd.SkipSummary(output.String()) {
		fmt.Println(line)
	}
	if err != nil {
		return err
	}
	if missing := gatecmd.IntegrationTestsNotPassed(output.String()); len(missing) != 0 {
		return fmt.Errorf("the integration test binary exited 0 but did not report these tests as passed: %s",
			strings.Join(missing, ", "))
	}
	return nil
}

// resolveIntegrationParallelism reads INTEGRATION_PARALLEL. The default is
// ior's conservative min(NumCPU, 2): every test starts a tracer that loads
// the BPF object and one or two JVMs, and more at once only adds load.
func resolveIntegrationParallelism() (int, error) {
	value := strings.TrimSpace(os.Getenv(gatecmd.IntegrationParallelEnv))
	if value == "" {
		return max(1, min(runtime.NumCPU(), 2)), nil
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s=%q: %w", gatecmd.IntegrationParallelEnv, value, err)
	}
	if n < 1 {
		return 0, fmt.Errorf("%s must be >= 1, got %d", gatecmd.IntegrationParallelEnv, n)
	}
	return n, nil
}
