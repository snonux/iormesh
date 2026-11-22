package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestStopMarksOperatorStop (task zh4): `iormesh stop` records in
// state.json that the operator ended the session, so iormeshctl ensure
// can tell it from an end it should undo — a reboot's SIGTERM ends a run
// with the same "stop" reason. The mark is written before the unit is
// stopped and kept by the run's own end record (markEnded re-reads the
// file), and written again after it.
func TestStopMarksOperatorStop(t *testing.T) {
	traces := t.TempDir()
	dir := filepath.Join(traces, "shop")
	mustWriteState(t, dir, liveState("shop", "inv1"))
	sys := newFakeSystemd()
	sys.units["iormesh-shop.service"] = running
	var markedBefore bool
	sys.onStop = func() {
		st, err := readState(dir)
		markedBefore = err == nil && st.OperatorStop != nil && *st.OperatorStop
		o := &segOutput{dir: dir, session: "1a2b3c4d"}
		if err := o.markEnded("stop", time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)); err != nil {
			t.Error(err)
		}
	}
	c, _ := testControl(sys, nil)
	if err := c.stop(context.Background(), []string{"-project", "shop", "-trace-dir", traces}); err != nil {
		t.Fatal(err)
	}
	if !markedBefore {
		t.Error("state.json not marked before the unit was stopped")
	}
	st, err := readState(dir)
	if err != nil || st.State != stateEnded || st.OperatorStop == nil || !*st.OperatorStop {
		t.Fatalf("after stop: %+v, %v", st, err)
	}
}

// TestStopMarksUncleanEnd: a stop whose run never recorded its end
// (SIGKILLed after the stop timeout) still fails, but the operator's
// intent is on record: ensure must not restart it either.
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
