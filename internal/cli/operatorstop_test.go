package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestStopMarksOperatorStop (task zh4): `iormesh stop` records in
// state.json that the operator ended the session, so iormeshctl ensure
// can tell it from an end it should undo — a reboot's SIGTERM ends a run
// with the same "stop" reason. The mark is written once the unit is
// stopped, over the run's own end record; never while the unit is live,
// when the run may be writing the file itself.
func TestStopMarksOperatorStop(t *testing.T) {
	traces := t.TempDir()
	dir := filepath.Join(traces, "shop")
	mustWriteState(t, dir, liveState("shop", "inv1"))
	before, err := os.ReadFile(filepath.Join(dir, StateFile))
	if err != nil {
		t.Fatal(err)
	}
	sys := newFakeSystemd()
	sys.units["iormesh-shop.service"] = running
	sys.onStop = func() {
		if now, err := os.ReadFile(filepath.Join(dir, StateFile)); err != nil || string(now) != string(before) {
			t.Errorf("state.json written while the unit was live:\n%s", now)
		}
		o := &segOutput{dir: dir, session: "1a2b3c4d"}
		if err := o.markEnded("stop", time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)); err != nil {
			t.Error(err)
		}
	}
	c, _ := testControl(sys, nil)
	if err := c.stop(context.Background(), []string{"-project", "shop", "-trace-dir", traces}); err != nil {
		t.Fatal(err)
	}
	st, err := readState(dir)
	if err != nil || st.State != stateEnded || st.OperatorStop == nil || !*st.OperatorStop {
		t.Fatalf("after stop: %+v, %v", st, err)
	}
}

// TestStopDoesNotRollBackRunEnd (review of task zh4): the run ends by
// its duration at the moment of the stop and lands its end record while
// a write of stop's is in flight (between its read and its rename). Stop
// must not roll state.json back to running: the end would be lost, stop
// would report it unknown (rm then refuses) and the file would say
// running for good. The run writes only while its unit is live, so the
// stand-in fires only then; a run that already ended does not write
// again on SIGTERM.
func TestStopDoesNotRollBackRunEnd(t *testing.T) {
	traces := t.TempDir()
	dir := filepath.Join(traces, "shop")
	mustWriteState(t, dir, liveState("shop", "inv1"))
	sys := newFakeSystemd()
	sys.units["iormesh-shop.service"] = running
	runEnded := false
	// The run is another process: its write goes straight to state.json
	// (its own temporary, renamed), not through this process's
	// temporary name, which stop's write in flight holds.
	end := func(reason string) {
		runEnded = true
		st := liveState("shop", "inv1")
		st.OperatorStop = new(false)
		st.State, st.EndReason, st.EndedAt = stateEnded, reason, time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
		b, err := json.Marshal(st)
		if err == nil {
			err = os.WriteFile(filepath.Join(dir, StateFile), b, 0o644)
		}
		if err != nil {
			t.Error(err)
		}
	}
	beforeStateRename = func(string) {
		if _, live := sys.units["iormesh-shop.service"]; live && !runEnded {
			end("duration")
		}
	}
	t.Cleanup(func() { beforeStateRename = nil })
	sys.onStop = func() {
		if !runEnded {
			end("stop")
		}
	}
	c, _ := testControl(sys, nil)
	if err := c.stop(context.Background(), []string{"-project", "shop", "-trace-dir", traces}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	st, err := readState(dir)
	if err != nil || st.State != stateEnded || st.OperatorStop == nil || !*st.OperatorStop {
		t.Fatalf("after stop: %+v, %v", st, err)
	}
}

// TestStopMarkUnderTraceRoot: through the sudoers rule the mark is
// written when -trace-dir is the confined trace root, and no temporary
// is left behind.
func TestStopMarkUnderTraceRoot(t *testing.T) {
	traces := t.TempDir()
	dir := filepath.Join(traces, "shop")
	st := liveState("shop", "inv1")
	st.State, st.EndReason = stateEnded, "stop"
	mustWriteState(t, dir, st)
	sys := newFakeSystemd()
	sys.units["iormesh-shop.service"] = running
	c, _ := testControl(sys, map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"})
	c.confine.traceRoot = traces
	if err := c.stop(context.Background(), []string{"-project", "shop", "-trace-dir", traces}); err != nil {
		t.Fatal(err)
	}
	if got, err := readState(dir); err != nil || got.OperatorStop == nil || !*got.OperatorStop {
		t.Fatalf("not marked under the trace root: %+v, %v", got, err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".*.tmp")); len(left) != 0 {
		t.Fatalf("temporaries left: %v", left)
	}
}

// TestStopMarksUncleanEnd: a stop whose run never recorded its end
// (SIGKILLed after the stop timeout) still fails, but the operator's
// intent is on record — the mark after the stop finds the unit dead and
// the stale running state: ensure must not restart it either.
func TestStopMarksUncleanEnd(t *testing.T) {
	traces := t.TempDir()
	dir := filepath.Join(traces, "shop")
	mustWriteState(t, dir, liveState("shop", "inv1"))
	sys := newFakeSystemd()
	sys.units["iormesh-shop.service"] = running
	c, _ := testControl(sys, nil)
	if err := c.stop(context.Background(), []string{"-project", "shop", "-trace-dir", traces}); err == nil {
		t.Fatal("unclean stop succeeded")
	}
	st, err := readState(dir)
	if err != nil || st.State != stateRunning || st.OperatorStop == nil || !*st.OperatorStop {
		t.Fatalf("after unclean stop: %+v, %v", st, err)
	}
}

// TestStopMarkConfined is the negative side: through the sudoers rule
// stop takes any -trace-dir, but writes state.json only under the
// confined trace root — never as root into a directory the caller
// chose. The stop itself still happens.
func TestStopMarkConfined(t *testing.T) {
	traces := t.TempDir()
	dir := filepath.Join(traces, "shop")
	st := liveState("shop", "inv1")
	st.State, st.EndReason = stateEnded, "stop"
	mustWriteState(t, dir, st)
	before, err := os.ReadFile(filepath.Join(dir, StateFile))
	if err != nil {
		t.Fatal(err)
	}
	sys := newFakeSystemd()
	sys.units["iormesh-shop.service"] = running
	c, _ := testControl(sys, map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"})
	if err := c.stop(context.Background(), []string{"-project", "shop", "-trace-dir", traces}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(dir, StateFile))
	if err != nil || string(after) != string(before) {
		t.Fatalf("state.json outside the trace root rewritten through sudo:\n%s", after)
	}
	if !sys.called("systemctl", "stop", "--", "iormesh-shop.service") {
		t.Fatalf("no stop call: %v", sys.calls)
	}
}

// TestOperatorStopRecorded: a run writes operator_stop false from its
// start, so a reader can tell "not stopped by the operator" (false) from
// a tracer that predates the field (absent, nil), which still reads.
func TestOperatorStopRecorded(t *testing.T) {
	dir := t.TempDir()
	o := &segOutput{dir: dir, session: "1a2b3c4d", fqdn: "h"}
	if err := o.markRunning(runConfig{project: "p", duration: time.Hour}, "pid [1]", time.Now()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, StateFile))
	if err != nil || !strings.Contains(string(b), `"operator_stop": false`) {
		t.Fatalf("state.json %s, %v", b, err)
	}
	old := `{"project":"p","session":"1a2b3c4d","fqdn":"h","pid":1,"pid_start_ns":2,` +
		`"state":"ended","targets":"pid [1]","started_at":"2026-11-20T08:00:00Z",` +
		`"ends_by":"2026-11-20T09:00:00Z","end_reason":"stop"}`
	if err := os.WriteFile(filepath.Join(dir, StateFile), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if st, err := readState(dir); err != nil || st.OperatorStop != nil {
		t.Fatalf("old state %+v, %v", st, err)
	}
}
