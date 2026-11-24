package ctl

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"iormesh/internal/schema"
	"iormesh/internal/segment"
)

// Task 1l4: a bare time of day in -from/-to is placed on the one UTC
// date the store's listed segments start on, the same for build and
// query, and refused when they start on more than one — so pruning the
// oldest day never silently moves a window to a later day.

// nextDayHost adds host app3 (project shop) to f, a day after the
// fixture: its session record at t0+24h in a segment named then, and at
// t0+24h+1m+1ms (00:54:20.001 of the next day) an openat of /data/next
// in the next segment, named t0+24h+1m — the clocks agree with the
// names, so build's selection by name takes it too.
func nextDayHost(t *testing.T, f *buildFixture) {
	t.Helper()
	const day = uint64(24 * time.Hour / time.Microsecond)
	h := &hostTrace{host: "app3", project: "shop"}
	h.header()
	h.events[0].WallNS += day * uint64(time.Microsecond)
	h.events[0].BootNS += day * uint64(time.Microsecond)
	h.add(300, schema.KindOpenat, 3, 3, day+uint64(time.Minute/time.Microsecond)+1000,
		"", schema.Args{Path: bptr("/data/next")})
	writeStoreSegmentsAt(t, f.store, h.host, h, 1, windowT0.Add(24*time.Hour))
}

// windowEcho is the window line build (stdout) or query (stderr)
// printed: the text after "window", "" without one.
func windowEcho(out string) string {
	for _, l := range strings.Split(out, "\n") {
		if _, w, ok := strings.Cut(l, "window "); ok {
			w, _, _ = strings.Cut(strings.TrimSpace(w), " (")
			return w
		}
	}
	return ""
}

// timeOfDayRefusal is the line of stderr that refuses a time of day, "" without
// one (the usage text follows it; the default query names the bad
// segments it read before it).
func timeOfDayRefusal(stderr string) string {
	for _, l := range strings.Split(stderr, "\n") {
		if strings.Contains(l, "is a time of day") {
			return l
		}
	}
	return ""
}

// TestTimeOfDayAcrossPrune: on a store spanning 2025-11-20 and
// 2025-11-21 a bare time of day is refused by build and both queries,
// naming the dates, while a dated -from is taken; once prune deleted the
// first day the store lies within 2025-11-21 and the same command places
// the time of day there, as build and query echo. Before task 1l4 the
// command meant 2025-11-20 before the prune and 2025-11-21 after it,
// silently.
func TestTimeOfDayAcrossPrune(t *testing.T) {
	f := newBuildFixture(t)
	nextDayHost(t, f)
	window := []string{"-from", "00:54:20", "-to", "00:54:21"}
	for _, cmd := range [][]string{{"build"}, {"query"}, {"query", "-window-segments"}} {
		code := f.run(slices.Concat(cmd, window)...)
		if msg := timeOfDayRefusal(f.stderr.String()); code != exitUsage ||
			!strings.Contains(msg, "the store's segments start on 2025-11-20 .. 2025-11-21 (UTC)") ||
			!strings.Contains(msg, "-from 2025-11-21T00:54:20") {
			t.Errorf("%v on two days: exit %d, want %d; stderr %s", cmd, code, exitUsage, f.stderr)
		}
	}
	evs, _ := queryLines(t, f.mustRun(t, "query", "-from", "2025-11-21T00:54:20", "-to", "2025-11-21T00:54:21"))
	if len(evs) != 1 || *evs[0].Args.Path != "/data/next" {
		t.Fatalf("a dated window found %v, want /data/next", evs)
	}
	for _, p := range slices.Concat(f.app1Files, f.app2Files) { // prune's -apply
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	const want = "2025-11-21T00:54:20Z .. 2025-11-21T00:54:21Z"
	evs, _ = queryLines(t, f.mustRun(t, slices.Concat([]string{"query", "-project", "shop"}, window)...))
	if len(evs) != 1 || *evs[0].Args.Path != "/data/next" || windowEcho(f.stderr.String()) != want {
		t.Errorf("after the prune: %v, stderr %q, want /data/next in %s", evs, f.stderr, want)
	}
	out := f.mustRun(t, slices.Concat([]string{"build", "-project", "shop"}, window)...)
	if windowEcho(out) != want {
		t.Errorf("build after the prune echoes %q, want %s", windowEcho(out), want)
	}
}

// TestBuildAndQueryShareTheWindow: build, the default query and query
// -window-segments resolve -from/-to to the same window and echo it; a
// segment that does not read still counts for the date (all three take
// the listed segments, not what read), so one named 1999-12-31 makes
// all three refuse a time of day in the same words. Before task 1l4
// build placed it on 1999-12-31, the default query on 2025-11-20.
func TestBuildAndQueryShareTheWindow(t *testing.T) {
	f := newBuildFixture(t)
	window := []string{"-from", "00:58:20", "-to", "01:00:00"}
	const want = "2025-11-20T00:58:20Z .. 2025-11-20T01:00:00Z"
	if out := f.mustRun(t, append([]string{"build"}, window...)...); windowEcho(out) != want {
		t.Errorf("build echoes %q, want %s", windowEcho(out), want)
	}
	for _, cmd := range [][]string{{"query"}, {"query", "-window-segments"}} {
		f.mustRun(t, slices.Concat(cmd, window)...)
		if got := windowEcho(f.stderr.String()); got != want {
			t.Errorf("%v echoes %q, want %s", cmd, got, want)
		}
	}
	name := segment.FileName{FQDN: "hostB.example.org", Session: "deadbeef",
		Start: time.Date(1999, 12, 31, 0, 0, 0, 0, time.UTC)}.Base() + segment.Ext
	if err := os.WriteFile(filepath.Join(filepath.Dir(f.app2Files[0]), name), []byte("not parquet"),
		0o600); err != nil {
		t.Fatal(err)
	}
	var msgs []string
	for _, cmd := range [][]string{{"build"}, {"query"}, {"query", "-window-segments"}} {
		if code := f.run(slices.Concat(cmd, window)...); code != exitUsage {
			t.Errorf("%v with a 1999 segment: exit %d, want %d; stderr %s", cmd, code, exitUsage, f.stderr)
		}
		msgs = append(msgs, timeOfDayRefusal(f.stderr.String()))
	}
	if msgs[0] != msgs[1] || msgs[1] != msgs[2] ||
		!strings.Contains(msgs[0], "start on 1999-12-31 .. 2025-11-20 (UTC)") {
		t.Errorf("the refusals differ or miss the dates:\n%s", strings.Join(msgs, "\n"))
	}
}
