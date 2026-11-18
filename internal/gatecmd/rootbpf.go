package gatecmd

import "strings"

// RootBPFTests are the tests of internal/sock_root_test.go and
// internal/loader_root_test.go: they load the real BPF object, attach the
// TCP programs or the loader's default probe set, and read the ring buffer
// (M1 steps 4 and 5). They skip for anybody but root, so `mage test` passes them by, and
// `mage bpfRootTest` is the one place that runs them. The names are listed
// rather than matched by a pattern so that a renamed test fails a gate
// (internal/buildgate) instead of silently no longer running. Adapted from
// ior's RootLinkTests (internal/gatecmd/gatecmd.go @ b21afc50328c).
func RootBPFTests() []string {
	return []string{
		"TestSockLoopbackConnectTuple",
		"TestSockNonTargetPidNoEvents",
		"TestSockFailedConnectUnregisters",
		"TestSockResetBeforeAcceptNotRegistered",
		"TestLoaderDeliversRecords",
		"TestLoaderTargetsOnlyTheMap",
		"TestLoaderCloseEndsRecords",
		"TestLoaderRefusesEmptyAttach",
	}
}

// RootBPFTestArgs are the arguments the test binary of ./internal gets to
// run RootBPFTests, and nothing else, as root. -test.v is not for the reader
// alone: RootBPFTestsNotPassed reads the verdicts it prints.
func RootBPFTestArgs() []string {
	return []string{
		"-test.run", "^(" + strings.Join(RootBPFTests(), "|") + ")$",
		"-test.timeout=5m",
		"-test.count=1",
		"-test.v",
	}
}

// RootBPFTestsNotPassed returns the tests of RootBPFTests that output, the
// -test.v output of the test binary, does not report as passed.
//
// The exit status cannot say so: a binary whose -test.run matches nothing,
// or whose tests all skipped (what they do for anybody but root), exits 0.
// A verdict counts only at the start of a line, where the testing package
// prints that of a top-level test; a subtest's is indented.
func RootBPFTestsNotPassed(output string) []string {
	passed := map[string]bool{}
	for line := range strings.SplitSeq(output, "\n") {
		if rest, ok := strings.CutPrefix(line, "--- PASS: "); ok {
			name, _, _ := strings.Cut(rest, " ")
			passed[name] = true
		}
	}
	var missing []string
	for _, name := range RootBPFTests() {
		if !passed[name] {
			missing = append(missing, name)
		}
	}
	return missing
}
