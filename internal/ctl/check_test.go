package ctl

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"iormesh/internal/schema"
)

// TestChildChecker: the segment check runs in a child of this binary
// (TestMain), returns the summary for a good segment, and fails the
// segment — not the build — when the file is no segment. Running out
// of the address space is TestCheckCap's.
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
}

// TestCheckCap (task 6p4): a check child that needs more address space
// than its cap dies of it, and the segment fails. A real decode of the
// fixture's small segment cannot show it: the cap counts the unused
// rest of the reserved heap arena (capMemory), the decode fits there,
// and under a 64 KiB cap the child died only when the runtime happened
// to map new metadata or a thread stack — so it now and then passed.
// The child here allocates a known size instead (allocEnv), 256 MiB:
// four arenas past that slack, untouched, so it costs no RAM.
func TestCheckCap(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("RLIMIT_AS is enforced on Linux only")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	const alloc = 256 << 20
	for _, c := range []struct {
		name         string
		space, alloc int64
		fail         bool
	}{
		{"fits the build's cap", checkAddressSpace, alloc, false},
		{"past the build's cap", checkAddressSpace, 2 * checkAddressSpace, true},
		{"past a 64 KiB cap", 64 << 10, alloc, true},
	} {
		t.Setenv(allocEnv, strconv.FormatInt(c.alloc, 10))
		_, err := childChecker(exe, c.space)(context.Background(), "unused.parquet")
		if c.fail != (err != nil) || err != nil && !strings.Contains(err.Error(), "check failed") {
			t.Errorf("%s: %v", c.name, err)
		}
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

// TestBuildHostileClockSegment (ib4's second review): a segment from
// app1's own host whose http event's latency overflows wall_ns + latency
// and whose path sets the terminal title is a bad segment — the build
// writes the outputs of the rest, nothing on stdout or stderr carries a
// raw control byte, and the database holds none of its requests.
func TestBuildHostileClockSegment(t *testing.T) {
	f := newBuildFixture(t)
	evil := &hostTrace{host: "hostA", project: "shop", session: "e1e2e3e4"}
	evil.header()
	evil.http(100, 50, "c9", "orders.example", "/\x1b]0;pwned\a")
	evil.events[len(evil.events)-1].LatencyNS = math.MaxUint64 - 5
	files := writeStoreSegments(t, f.store, "app1", evil, 100)
	if code := f.run("build"); code != exitError {
		t.Fatalf("exit %d:\n%s\n%s", code, f.stdout, f.stderr)
	}
	out := f.stdout.String() + f.stderr.String()
	if strings.ContainsAny(out, "\x1b\a") {
		t.Errorf("raw control bytes printed:\n%q", out)
	}
	if !strings.Contains(f.stdout.String(), "bad        shop/app1/"+filepath.Base(files[0])) ||
		!strings.Contains(f.stdout.String(), "latency_ns") {
		t.Errorf("stdout:\n%s", f.stdout)
	}
	if !strings.Contains(f.output(t, modelFileName), `"name": "orders.example:8080"`) {
		t.Error("the rest of the mesh was not written")
	}
	if n := f.requestCount(t); n != 3 {
		t.Errorf("%d requests, want the genuine 3", n)
	}
}

// TestErrorsAreTerminalSafe: an error carrying server text (ssh's
// stderr, a request path) is printed quoted, never raw.
func TestErrorsAreTerminalSafe(t *testing.T) {
	var stderr strings.Builder
	exitCodeOf(errors.New("ssh: \x1b]0;pwned\a"), &stderr)
	if strings.ContainsAny(stderr.String(), "\x1b\a") || !strings.Contains(stderr.String(), `\x1b`) {
		t.Errorf("stderr %q", stderr.String())
	}
}
