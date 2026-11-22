package buildgate

import (
	"strings"
	"testing"

	"iormesh/internal/gatecmd"
)

// TestKindTestsVerdict: the kind targets (mage k8sAcceptance,
// k8sDebugRestart) judge `go test -v` by what it reported, as `mage
// integrationTest` does: a test that skipped - top-level, or a subtest
// under a parent Go reports as passed - or that never reported PASS fails
// the target although go test exited 0 (task si4). Without the gate an
// existing cluster without a crictl prefix ran setup, deploy and teardown,
// checked nothing and was green.
func TestKindTestsVerdict(t *testing.T) {
	cases := map[string]struct {
		output string
		tests  []string
		want   string // "" for no error, else a substring of the error
	}{
		"pass": {"=== RUN   TestKindDebugRestart\n--- PASS: TestKindDebugRestart (181.00s)\nPASS\nok",
			[]string{"TestKindDebugRestart"}, ""},
		"top-level skip": {"--- SKIP: TestKindDebugRestart (12.00s)\n" +
			"    restart_test.go:202: IORMESH_KIND_NODE unset\nPASS\nok",
			[]string{"TestKindDebugRestart"}, "TestKindDebugRestart"},
		"subtest skip under parent pass": {"    --- SKIP: TestKindAcceptance/sigterm (0.00s)\n" +
			"--- PASS: TestKindAcceptance (104.00s)\nPASS\nok",
			[]string{"TestKindAcceptance"}, "TestKindAcceptance/sigterm"},
		"not reported": {"testing: warning: no tests to run\nPASS\nok",
			[]string{"TestKindClusterIP"}, "TestKindClusterIP"},
		"one of two not reported": {"--- PASS: TestKindAcceptance (1s)\n",
			[]string{"TestKindAcceptance", "TestKindClusterIP"}, "TestKindClusterIP"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := gatecmd.KindTestsVerdict(tc.output, tc.tests...)
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("KindTestsVerdict() = %v, want nil", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("KindTestsVerdict() = %v, want an error naming %s", err, tc.want)
			}
		})
	}
}
