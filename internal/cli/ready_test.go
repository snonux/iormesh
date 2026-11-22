package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"iormesh/internal/k8s"
)

// TestReadyRoundTrip: `iormesh ready -project P` streams the project's
// segments, k8s.Unpack (the controller's side) takes them, and
// `-remove -- NAME` deletes them on the tracer.
func TestReadyRoundTrip(t *testing.T) {
	root := t.TempDir()
	ready := filepath.Join(root, "shop", "ready")
	if err := os.MkdirAll(ready, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ready, "h-s-1-0.parquet"), []byte("seg"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stream, errOut bytes.Buffer
	if err := readyIn(root, []string{"-project", "shop"}, &stream, &errOut); err != nil {
		t.Fatal(err)
	}
	local := t.TempDir()
	taken, err := k8s.Unpack(&stream, local)
	if err != nil || !slices.Equal(taken, []string{"h-s-1-0.parquet"}) {
		t.Fatalf("Unpack = %q, %v", taken, err)
	}
	if b, err := os.ReadFile(filepath.Join(local, "h-s-1-0.parquet")); err != nil || string(b) != "seg" {
		t.Errorf("unpacked = %q, %v", b, err)
	}
	args := append([]string{"-project", "shop", "-remove", "--"}, taken...)
	if err := readyIn(root, args, &stream, &errOut); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(ready); len(entries) != 0 {
		t.Errorf("ready/ still holds %v", entries)
	}
}

// TestReadyMissingProject: a project that never ran here (no trace dir)
// fails, unlike one whose ready/ is not there yet, which is an empty
// stream — a project mismatch must not look like an empty collect.
func TestReadyMissingProject(t *testing.T) {
	root := t.TempDir()
	var stream, errOut bytes.Buffer
	if err := readyIn(root, []string{"-project", "shop"}, &stream, &errOut); err == nil || stream.Len() != 0 {
		t.Fatalf("no trace dir: err = %v, %d bytes streamed", err, stream.Len())
	}
	if err := os.Mkdir(filepath.Join(root, "shop"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := readyIn(root, []string{"-project", "shop"}, &stream, &errOut); err != nil {
		t.Fatalf("no ready/ yet: %v", err)
	}
	if taken, err := k8s.Unpack(&stream, t.TempDir()); err != nil || len(taken) != 0 {
		t.Errorf("no ready/ yet: Unpack = %q, %v", taken, err)
	}
}

// TestReadyStatus: `iormesh ready -project P -status` is what iormeshctl
// status reads from a pod (task ph4), where `iormesh status` cannot run
// (no systemd in the tracer container): status's JSON report for the
// project, without a unit — the state, the session with its version, and
// the ready/ segments awaiting collection. No state.json yet is a report
// without a session; a corrupt one fails, and -status takes neither
// -state, -remove nor arguments.
func TestReadyStatus(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "shop")
	answer := func() statusReport {
		t.Helper()
		var out, errOut bytes.Buffer
		if err := readyIn(root, []string{"-project", "shop", k8s.ReadyStatusFlag}, &out, &errOut); err != nil {
			t.Fatal(err)
		}
		var r statusReport
		if err := json.Unmarshal(out.Bytes(), &r); err != nil {
			t.Fatalf("%q: %v", out.String(), err)
		}
		return r
	}
	if err := os.MkdirAll(filepath.Join(dir, "ready"), 0o700); err != nil {
		t.Fatal(err)
	}
	if r := answer(); r.Project != "shop" || r.State != "" || r.Session != nil || r.Unit != "" {
		t.Errorf("no state.json: %+v", r)
	}
	for _, name := range []string{"h-s-1-0.parquet", "h-s-1-0.files.parquet", "h-s-1-1.parquet"} {
		if err := os.WriteFile(filepath.Join(dir, "ready", name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pid := os.Getpid()
	st := sessionState{Project: "shop", Session: "s1", PID: pid, PIDStartNS: processStart(uint32(pid)),
		State: stateEnded, EndReason: "duration", Version: "0.9.1", Argv: []string{"iormesh", "run"}}
	if err := writeState(dir, st); err != nil {
		t.Fatal(err)
	}
	r := answer()
	if r.State != stateEnded || r.Session == nil || r.Session.Version != "0.9.1" ||
		r.Session.EndReason != "duration" || r.ReadySegments != 2 {
		t.Errorf("ended: %+v", r)
	}
	var out, errOut bytes.Buffer
	for _, extra := range [][]string{{"-state"}, {"-remove"}, {"x"}} {
		args := append([]string{"-project", "shop", k8s.ReadyStatusFlag}, extra...)
		if err := readyIn(root, args, &out, &errOut); err == nil {
			t.Errorf("%q accepted", args)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, StateFile), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := readyIn(root, []string{"-project", "shop", k8s.ReadyStatusFlag}, &out, &errOut); err == nil {
		t.Errorf("corrupt state.json: %q", out.String())
	}
}

// TestReadyState: `iormesh ready -project P -state` is what deploy's
// wait reads (task ef4): pending before state.json (with or without the
// project dir), then running with the session id, ended with the
// reason, dead for a running state whose process is gone; a corrupt
// state.json fails, and -state takes no -remove.
func TestReadyState(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "shop")
	answer := func() string {
		t.Helper()
		var out, errOut bytes.Buffer
		if err := readyIn(root, []string{"-project", "shop", "-state"}, &out, &errOut); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if got := answer(); got != "pending\n" {
		t.Errorf("no project dir: %q", got)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := answer(); got != "pending\n" {
		t.Errorf("no state.json: %q", got)
	}
	pid := os.Getpid()
	st := sessionState{Project: "shop", Session: "s1", PID: pid, PIDStartNS: processStart(uint32(pid)),
		State: stateRunning}
	// A state with a StartedAt ends with it (task eh4), in UTC whatever
	// zone wrote it; one without (an older writer) prints none.
	started := time.Date(2026, 11, 21, 23, 4, 5, 123456789, time.FixedZone("x", 3600))
	const at = " started=2026-11-21T22:04:05.123456789Z\n"
	for _, c := range []struct {
		edit func(*sessionState)
		want string
	}{
		{func(*sessionState) {}, "running s1\n"},
		{func(s *sessionState) { s.State, s.EndReason = stateEnded, "disk_limit" }, "ended disk_limit\n"},
		{func(s *sessionState) { s.PIDStartNS++ }, "dead\n"},
		{func(s *sessionState) { s.StartedAt = started }, "running s1" + at},
		{func(s *sessionState) { s.StartedAt, s.State, s.EndReason = started, stateEnded, "stop" }, "ended stop" + at},
		{func(s *sessionState) { s.StartedAt, s.State = started, stateEnded }, "ended" + at},
		{func(s *sessionState) { s.StartedAt, s.PIDStartNS = started, s.PIDStartNS+1 }, "dead" + at},
	} {
		s := st
		c.edit(&s)
		if err := writeState(dir, s); err != nil {
			t.Fatal(err)
		}
		if got := answer(); got != c.want {
			t.Errorf("state %+v: %q, want %q", s, got, c.want)
		}
	}
	// With a valid state.json, so only the flag check can refuse these.
	var out, errOut bytes.Buffer
	for _, extra := range [][]string{{"-remove"}, {"-remove", "--", "x.parquet"}, {"x"}} {
		args := append([]string{"-project", "shop", "-state"}, extra...)
		if err := readyIn(root, args, &out, &errOut); err == nil {
			t.Errorf("%q accepted", args)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, StateFile), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := readyIn(root, []string{"-project", "shop", "-state"}, &out, &errOut); err == nil {
		t.Errorf("corrupt state.json: %q", out.String())
	}
}
