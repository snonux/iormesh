package integrationtests

// The M4 tracer lifecycle on the real systemd (task 9b4, decision #19):
// `iormesh start` runs the tracer as the transient unit
// iormesh-<project>.service with its cgroup caps and runtime bound,
// status reads the run's state.json, stop ends it with SIGTERM and the
// last segment is published. Host safety: the project (and so the unit)
// name is unique to the run, the unit is stopped and reset in t.Cleanup
// whatever happens, and nothing is enabled, written to /etc or reloaded
// — a transient unit lives in /run and is gone after its stop. start is
// confined to /opt/iormesh/traces, so the trace goes there; its project
// directory, and the directories above it the test created, are removed
// again.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
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
	project, unit, traces := uniqueUnit(t)
	sleeper := exec.Command("sleep", "600")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() })
	// A -trace-dir elsewhere is refused before anything is launched.
	if out, err := exec.Command(bin, "start", "-pid", strconv.Itoa(sleeper.Process.Pid), "-project", project,
		"-trace-dir", t.TempDir()).CombinedOutput(); err == nil || !strings.Contains(string(out), "not allowed") {
		t.Fatalf("start with another -trace-dir: %v: %s", err, out)
	}
	// The trace is a few KiB on the pinned dir's partition, which may be
	// over the 90% default on a developer's host: the percentage limit is
	// off, the free-bytes floor (256 MiB) still guards the partition.
	out := iormesh(t, bin, "start", "-pid", strconv.Itoa(sleeper.Process.Pid), "-project", project,
		"-duration", "10m", "-wait", "90s", "-disk-limit", "0")
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

// pinnedTraceDir is the trace dir start is confined to, /opt/iormesh/traces
// (no other is accepted), and arranges for the test's trace in it to go
// when the test ends — and /opt/iormesh/traces and /opt/iormesh too if
// they did not exist before, each only if empty by then (os.Remove).
func pinnedTraceDir(t *testing.T, project string) string {
	t.Helper()
	const root, traces = "/opt/iormesh", "/opt/iormesh/traces"
	var created []string
	for _, d := range []string{traces, root} {
		if _, err := os.Stat(d); errors.Is(err, os.ErrNotExist) {
			created = append(created, d)
		}
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(filepath.Join(traces, project)); err != nil {
			t.Errorf("remove the test's trace: %v", err)
		}
		for _, d := range created {
			_ = os.Remove(d)
		}
	})
	return traces
}

// uniqueUnit names a project, and its unit, no other run or host session
// uses (with the pinned trace dir its trace goes to, pinnedTraceDir),
// uses, and makes sure the unit is gone when the test ends: stopped (the
// tracer publishes and exits) and its failed state, if any, reset.
func uniqueUnit(t *testing.T) (project, unit, traces string) {
	t.Helper()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	project = "itest-" + hex.EncodeToString(b[:])
	unit = "iormesh-" + project + ".service"
	// Registered first, so it runs last: the unit is gone before its
	// trace is removed.
	traces = pinnedTraceDir(t, project)
	t.Cleanup(func() {
		_ = exec.Command("systemctl", "stop", "--", unit).Run()
		_ = exec.Command("systemctl", "reset-failed", "--", unit).Run()
	})
	return project, unit, traces
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
