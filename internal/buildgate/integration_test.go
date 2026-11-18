package buildgate

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"iormesh/internal/gatecmd"
)

// TestIntegrationTestsAreTheTestsOfThePackage: `mage integrationTest` runs
// only the tests gatecmd.IntegrationTests names (-test.run) and requires
// each to pass, so a test added to or renamed in ./integrationtests without
// updating the list would never run, and a listed test that no longer
// exists would fail every run.
func TestIntegrationTestsAreTheTestsOfThePackage(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(repoRoot(t), "integrationtests", "*_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	var declared []string
	for _, f := range files {
		rel, err := filepath.Rel(repoRoot(t), f)
		if err != nil {
			t.Fatal(err)
		}
		declared = append(declared, testFunctionsIn(t, rel)...)
	}
	listed := gatecmd.IntegrationTests()
	slices.Sort(listed)
	slices.Sort(declared)
	if len(declared) == 0 || !slices.Equal(listed, declared) {
		t.Errorf("gatecmd.IntegrationTests() = %v, but integrationtests/*_test.go declare %v: the two must name the same tests",
			listed, declared)
	}
}

// TestIntegrationTestArgs pins what the gate relies on: -test.v (the gate
// reads the verdicts), a -test.run of exactly the listed tests, and the
// parallel switch only in parallel mode.
func TestIntegrationTestArgs(t *testing.T) {
	for _, parallel := range []int{0, 2} {
		args := gatecmd.IntegrationTestArgs(parallel)
		for _, want := range []string{"-test.v", "-test.count=1", "-test.failfast"} {
			if !slices.Contains(args, want) {
				t.Errorf("IntegrationTestArgs(%d) = %v lacks %s", parallel, args, want)
			}
		}
		i := slices.Index(args, "-test.run")
		if i < 0 || args[i+1] != "^("+strings.Join(gatecmd.IntegrationTests(), "|")+")$" {
			t.Errorf("IntegrationTestArgs(%d) = %v: -test.run must name exactly the listed tests", parallel, args)
		}
		if got := slices.Contains(args, gatecmd.IntegrationParallelFlag); got != (parallel > 0) {
			t.Errorf("IntegrationTestArgs(%d) has the parallel flag: %v", parallel, got)
		}
	}
}

// TestSkipSummary: only lines beginning with the marker are listed.
func TestSkipSummary(t *testing.T) {
	if got := gatecmd.SkipSummary("--- PASS: TestA (1s)\nok"); got != nil {
		t.Errorf("SkipSummary() without markers = %v, want nil", got)
	}
	output := gatecmd.UnexercisedSkipMarker + "TestA: lost\n  " + gatecmd.UnexercisedSkipMarker + "indented\n"
	got := gatecmd.SkipSummary(output)
	if len(got) != 2 || !strings.HasPrefix(got[0], "1 test(s)") || got[1] != "  TestA: lost" {
		t.Errorf("SkipSummary() = %q, want the count and TestA", got)
	}
}

// TestIntegrationTestsNotPassed: as for the root BPF tests, only a
// top-level PASS counts.
func TestIntegrationTestsNotPassed(t *testing.T) {
	names := gatecmd.IntegrationTests()
	var lines []string
	for _, name := range names[1:] {
		lines = append(lines, "--- PASS: "+name+" (2s)")
	}
	output := "--- SKIP: " + names[0] + " (0s)\n" + strings.Join(lines, "\n")
	if got := gatecmd.IntegrationTestsNotPassed(output); !slices.Equal(got, names[:1]) {
		t.Errorf("IntegrationTestsNotPassed() = %v, want %v", got, names[:1])
	}
}
