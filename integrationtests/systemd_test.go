package integrationtests

// The M4 tracer lifecycle on the real systemd (task 9b4, decision #19):
// `iormesh start` runs the tracer as the transient unit
// iormesh-<project>.service with its cgroup caps and runtime bound,
// status reads the run's state.json, stop ends it with SIGTERM and the
// last segment is published. Host safety: the project (and so the unit)
// name is unique to the run, the unit is stopped and reset in t.Cleanup
// whatever happens, and nothing is enabled, written to /etc or reloaded
// — a transient unit lives in /run and is gone after its stop.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// unitStatus is the part of `iormesh status -json` the test reads.
type unitStatus struct {
	Project       string `json:"project"`
	UnitState     string `json:"unit_state"`
	State         string `json:"state"`
	ReadySegments int    `json:"ready_segments"`
	Session       struct {
		PID       int    `json:"pid"`
		EndReason string `json:"end_reason"`
	} `json:"session"`
}

// TestSystemdUnit: start a session tracing a sleeping process, check the
// unit's properties and the running status, refuse a second start, stop
// it and find the end recorded (reason stop) with a published segment.
func TestSystemdUnit(t *testing.T) {
	enableParallelIfRequested(t)
	requireTracepoints(t, m1Tracepoints...)
	bin := absPath(t, tracerBinaryDefault)
	project, unit := uniqueUnit(t)
	sleeper := exec.Command("sleep", "600")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() })
	traces := t.TempDir()
	out := iormesh(t, bin, "start", "-pid", strconv.Itoa(sleeper.Process.Pid), "-project", project,
		"-trace-dir", traces, "-duration", "10m", "-wait", "90s")
	if !strings.Contains(out, "started "+unit) {
		t.Fatalf("start: %s", out)
	}
	checkUnitProperties(t, unit)
	if st := unitStatusOf(t, bin, project, traces); st.State != "running" || st.UnitState != "active/running" {
		t.Fatalf("status while running: %+v", st)
	}
	if out, err := exec.Command(bin, "start", "-pid", strconv.Itoa(sleeper.Process.Pid), "-project", project,
		"-trace-dir", traces).CombinedOutput(); err == nil || !strings.Contains(string(out), "already active") {
		t.Fatalf("second start: %v: %s", err, out)
	}
	if out := iormesh(t, bin, "stop", "-project", project, "-trace-dir", traces); !strings.Contains(out, "ended (stop)") {
		t.Fatalf("stop: %s", out)
	}
	st := unitStatusOf(t, bin, project, traces)
	if st.State != "ended" || st.Session.EndReason != "stop" || st.UnitState != "not loaded" || st.ReadySegments < 1 {
		t.Fatalf("status after stop: %+v", st)
	}
	if active, _ := filepath.Glob(filepath.Join(traces, project, ".active-*")); len(active) != 0 {
		t.Fatalf("active files left after the stop: %v", active)
	}
}

// uniqueUnit names a project, and its unit, no other run or host session
// uses, and makes sure the unit is gone when the test ends: stopped (the
// tracer publishes and exits) and its failed state, if any, reset.
func uniqueUnit(t *testing.T) (project, unit string) {
	t.Helper()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	project = "itest-" + hex.EncodeToString(b[:])
	unit = "iormesh-" + project + ".service"
	t.Cleanup(func() {
		_ = exec.Command("systemctl", "stop", "--", unit).Run()
		_ = exec.Command("systemctl", "reset-failed", "--", unit).Run()
	})
	return project, unit
}

// iormesh runs the tracer binary and fails the test on an error.
func iormesh(t *testing.T, bin string, args ...string) string {
	t.Helper()
	out, err := exec.Command(bin, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("iormesh %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func unitStatusOf(t *testing.T, bin, project, traces string) unitStatus {
	t.Helper()
	out, err := exec.Command(bin, "status", "-project", project, "-trace-dir", traces, "-json").Output()
	if err != nil {
		t.Fatalf("status: %v: %s", err, out)
	}
	var reports []unitStatus
	if err := json.Unmarshal(out, &reports); err != nil || len(reports) != 1 {
		t.Fatalf("status output %q: %v", out, err)
	}
	return reports[0]
}

// checkUnitProperties: the cgroup caps and the runtime bound start set
// (1 core, 1 GiB, -duration plus 5 minutes) are what systemd applies.
func checkUnitProperties(t *testing.T, unit string) {
	t.Helper()
	out, err := exec.Command("systemctl", "show", "--property=CPUQuotaPerSecUSec,MemoryMax,RuntimeMaxUSec,"+
		"KillSignal,TimeoutStopUSec", "--", unit).Output()
	if err != nil {
		t.Fatalf("systemctl show: %v", err)
	}
	for _, want := range []string{"CPUQuotaPerSecUSec=1s", "MemoryMax=1073741824", "RuntimeMaxUSec=15min",
		"KillSignal=15", "TimeoutStopUSec=30s"} {
		if !strings.Contains(string(out), want+"\n") {
			t.Errorf("unit property %s missing in:\n%s", want, out)
		}
	}
}
