package ctl

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"iormesh/internal/viewmodel"
)

// The fixture's segments are named by the writer's clock (writeStoreSegments):
// both hosts' seq 0 at t0, seq 1 at t0+3m, seq 2 at t0+6m, and app1's
// seq 3 and 4 at t0+9m and t0+12m — eight segments.
var windowT0 = time.Unix(0, int64(buildWall0)).UTC()

// windowedRun reads the run section of the windowed build's model.
func windowedRun(t *testing.T, f *buildFixture) viewmodel.Run {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.builds, "billing+shop~window", modelFileName))
	if err != nil {
		t.Fatal(err)
	}
	var m viewmodel.Model
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m.Run
}

// TestBuildWindow (task yh4): -from, -to and -last select segments by
// the time range their names give — a segment is taken when its range
// (its start, a minute before, to its interval and a minute after)
// overlaps the window — echo the resolved window, and write into
// <projects>~window, leaving the full build as it was.
func TestBuildWindow(t *testing.T) {
	f := newBuildFixture(t)
	f.e.now = func() time.Time { return windowT0.Add(8 * time.Minute) }
	f.mustRun(t, "build")
	full := f.output(t, graphFileName)
	from := windowT0.Add(5 * time.Minute).Format(time.RFC3339)
	cases := []struct {
		args []string
		want int    // segments built
		echo string // the window line
	}{
		// From t0+5m: seq 1 (range ends at t0+5m) is out, seq 2 on in.
		{[]string{"-from", from}, 4, "window     " + from + " .. (open)"},
		// A time of day lands on the first segment's day.
		{[]string{"-from", windowT0.Add(5 * time.Minute).Format(time.TimeOnly)}, 4, "window     " + from},
		// -last 3m is -from now-3m.
		{[]string{"-last", "3m"}, 4, "window     " + from + " .. (open)"},
		// Before t0+4m: seq 0 and 1 of each host (seq 2's range starts
		// at t0+5m).
		{[]string{"-to", windowT0.Add(4 * time.Minute).Format(time.RFC3339)}, 4,
			"window     (open) .. " + windowT0.Add(4*time.Minute).Format(time.RFC3339)},
	}
	for _, c := range cases {
		out := f.mustRun(t, append([]string{"build"}, c.args...)...)
		if !strings.Contains(out, c.echo) {
			t.Errorf("build %v: no %q in\n%s", c.args, c.echo, out)
		}
		if run := windowedRun(t, f); run.Segments != c.want {
			t.Errorf("build %v: %d segments, want %d", c.args, run.Segments, c.want)
		}
	}
	if again := f.output(t, graphFileName); again != full {
		t.Errorf("a windowed build changed the full build's graph")
	}
	if out := f.mustRun(t, "show", "-windowed"); !strings.Contains(out, "segments") {
		t.Errorf("show -windowed:\n%s", out)
	}
}

// TestBuildWindowChecksUnpinnedWhole: before a host directory is pinned
// a windowed build checks all of its segments — the host vetting judges
// a directory on every session, as a full build does — so a damaged
// segment outside the window is still reported; once a full build has
// pinned the directories, the window's segments are all it reads.
func TestBuildWindowChecksUnpinnedWhole(t *testing.T) {
	f := newBuildFixture(t)
	bad := f.app2Files[0] // seq 0 of app2, at t0: outside -from t0+5m
	data, err := os.ReadFile(bad)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, data[:len(data)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	from := windowT0.Add(5 * time.Minute).Format(time.RFC3339)
	if code := f.run("build", "-from", from); code != exitError ||
		!strings.Contains(f.stdout.String(), filepath.Base(bad)) {
		t.Fatalf("unpinned windowed build: exit %d, stdout\n%s", code, f.stdout)
	}
	if code := f.run("build"); code != exitError { // pins both hosts
		t.Fatalf("full build: exit %d", code)
	}
	out := f.mustRun(t, "build", "-from", from)
	if strings.Contains(out, filepath.Base(bad)) {
		t.Errorf("pinned windowed build read a segment outside its window:\n%s", out)
	}
	if run := windowedRun(t, f); run.Segments != 4 {
		t.Errorf("%d segments, want 4", run.Segments)
	}
}

// TestBuildWindowRefusals: -last with -from or -to, a -last that is not
// positive, a malformed or empty window are usage errors before
// anything is read; show -windowed of a window never built says so.
func TestBuildWindowRefusals(t *testing.T) {
	f := newBuildFixture(t)
	at := func(m int) string { return windowT0.Add(time.Duration(m) * time.Minute).Format(time.RFC3339) }
	for _, args := range [][]string{
		{"-last", "1h", "-from", at(0)},
		{"-last", "1h", "-to", at(0)},
		{"-last", "0s"},
		{"-last", "-1h"},
		{"-from", "yesterday"},
		{"-from", at(5), "-to", at(5)},
		{"-from", at(6), "-to", at(5)},
	} {
		if code := f.run(append([]string{"build"}, args...)...); code != exitUsage {
			t.Errorf("build %v: exit %d, want %d (%s)", args, code, exitUsage, f.stderr)
		}
	}
	// A window no segment overlaps builds nothing and says so.
	if code := f.run("build", "-from", at(60)); code == exitOK ||
		!strings.Contains(f.stderr.String(), "no readable segment") {
		t.Errorf("empty window: exit %d, %s", code, f.stderr)
	}
	if code := f.run("show", "-windowed"); code == exitOK ||
		!strings.Contains(f.stderr.String(), "billing+shop~window") {
		t.Errorf("show of an unbuilt window: exit %d, %s", code, f.stderr)
	}
}
