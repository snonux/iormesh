package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"

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
	for _, c := range []struct {
		edit func(*sessionState)
		want string
	}{
		{func(*sessionState) {}, "running s1\n"},
		{func(s *sessionState) { s.State, s.EndReason = stateEnded, "disk_limit" }, "ended disk_limit\n"},
		{func(s *sessionState) { s.PIDStartNS++ }, "dead\n"},
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
