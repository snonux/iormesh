package integrationtests

// Adapted from ior integrationtests/unexercised.go @ b21afc50328c.
// Changes: the marker is iormesh's (gatecmd.UnexercisedSkipMarker).

import (
	"fmt"
	"io"

	"iormesh/internal/gatecmd"
)

// skipVerdict is what SkipUnexercised needs of a test (*testing.T).
type skipVerdict interface {
	Helper()
	Name() string
	Skipf(format string, args ...any)
}

// SkipUnexercised ends a test none of whose runs could judge what it is
// there to check: every run lost records in the kernel, so a missing
// golden event could be loss rather than a bug. It SKIPS - a pass would
// claim a check that never ran - and first prints
// gatecmd.UnexercisedSkipMarker with the test and why to out (the test
// binary's stdout), so `mage integrationTest` lists it in its closing
// summary (gatecmd.SkipSummary) before its gate fails on the "--- SKIP"
// line - a top-level test's or a subtest's alike (gatecmd.SkippedTests):
// a parent whose subtests all skipped still prints "--- PASS".
func SkipUnexercised(t skipVerdict, out io.Writer, why string) {
	t.Helper()
	_, _ = fmt.Fprintf(out, "%s%s: %s\n", gatecmd.UnexercisedSkipMarker, t.Name(), why)
	t.Skipf("%s", why)
}
