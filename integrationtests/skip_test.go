package integrationtests

import (
	"errors"
	"os/exec"
	"testing"
	"time"
)

// TestSkipCodeIsSkip: a workload exiting with the skip code 3 (a host
// that cannot run its scenario, tcp-ipv6 without IPv6) becomes
// ErrWorkloadSkipped, which traceJudged turns into t.Skip; any other
// failure stays a failure. Needs no root: the workloads are shells.
func TestSkipCodeIsSkip(t *testing.T) {
	for _, tt := range []struct {
		code string
		skip bool
	}{{"3", true}, {"1", false}} {
		p, err := startProcess("workload", exec.Command("sh", "-c", "exit "+tt.code))
		if err != nil {
			t.Fatal(err)
		}
		w := &Workload{Scenario: "tcp-ipv6", proc: p}
		err = skipped(w, p.wait(10*time.Second))
		if err == nil || errors.Is(err, ErrWorkloadSkipped) != tt.skip {
			t.Errorf("exit %s: err %v, want skip %v", tt.code, err, tt.skip)
		}
	}
}
