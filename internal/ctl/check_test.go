package ctl

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"iormesh/internal/schema"
)

// TestChildChecker: the segment check runs in a child of this binary
// (TestMain), returns the summary for a good segment, and fails the
// segment — not the build — when the child runs out of its address
// space or the file is no segment.
func TestChildChecker(t *testing.T) {
	f := newBuildFixture(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, err := childChecker(exe, checkAddressSpace)(ctx, f.app1Files[0])
	if err != nil || s.HostID != "hostA" || s.Events == 0 || s.Meta.Project != "shop" {
		t.Fatalf("good segment: %+v, %v", s, err)
	}
	garbage := filepath.Join(t.TempDir(), "x.parquet")
	if err := os.WriteFile(garbage, []byte("not parquet at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := childChecker(exe, checkAddressSpace)(ctx, garbage); err == nil ||
		!strings.Contains(err.Error(), "check failed") {
		t.Errorf("garbage: %v", err)
	}
	if runtime.GOOS != "linux" {
		t.Skip("RLIMIT_AS is enforced on Linux only")
	}
	// 64 KiB of address space beyond the start: any real decode needs
	// more, and the child dies of it.
	if _, err := childChecker(exe, 64<<10)(ctx, f.app1Files[0]); err == nil {
		t.Error("a check without memory passed")
	}
}

// TestHostPins: the first build pins each host directory to the host it
// saw; a later session in app1 that claims another host is left out
// (it is a bad segment), and the pin file is not rewritten.
func TestHostPins(t *testing.T) {
	f := newBuildFixture(t)
	f.mustRun(t, "build")
	pinsPath := filepath.Join(filepath.Dir(f.db), hostPinsFileName)
	pins, err := os.ReadFile(pinsPath)
	if err != nil || !strings.Contains(string(pins), `"app1": {`) ||
		!strings.Contains(string(pins), `"host_id": "hostA"`) {
		t.Fatalf("pins: %s, %v", pins, err)
	}
	// app1's server now plants a session of its own under another
	// identity: a new machine id and FQDN in a fresh session.
	forged := &hostTrace{host: "hostZ", project: "shop"}
	forged.header()
	forged.add(100, schema.KindRead, 3, 1, 10, "", schema.Args{}).Bytes = 1
	writeStoreSegments(t, f.store, "app1", forged, 100)
	if code := f.run("build"); code != exitError ||
		!strings.Contains(f.stdout.String(), "app1 is pinned to host_id hostA") ||
		!strings.Contains(f.stdout.String(), "2 hosts, 2 sessions") {
		t.Fatalf("exit %d:\n%s\n%s", code, f.stdout, f.stderr)
	}
	again, err := os.ReadFile(pinsPath)
	if err != nil || string(again) != string(pins) {
		t.Errorf("pins rewritten: %s", again)
	}
}

// TestBuildsDirRefused: a builds directory others can write is refused.
func TestBuildsDirRefused(t *testing.T) {
	f := newBuildFixture(t)
	if err := os.MkdirAll(f.builds, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.builds, 0o777); err != nil {
		t.Fatal(err)
	}
	if code := f.run("build"); code != exitError || !strings.Contains(f.stderr.String(), "writable by others") {
		t.Errorf("exit %d: %s", code, f.stderr)
	}
}
