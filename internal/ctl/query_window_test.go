package ctl

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"iormesh/internal/schema"
	"iormesh/internal/segment"
)

// TestQueryAnchorFirstReadable (review of task ik4): the default query
// places a time of day on the earliest READABLE segment's date, as it
// did before: an unreadable segment named 1999-12-31 next to app2's
// first one is left out, named, and moves nothing — the window golden's
// events are written unchanged (exit 1 for the bad segment). Build's
// anchor (the earliest listed segment) is task 1l4's, not changed here.
func TestQueryAnchorFirstReadable(t *testing.T) {
	f := newBuildFixture(t)
	name := segment.FileName{FQDN: "hostB.example.org", Session: "deadbeef",
		Start: time.Date(1999, 12, 31, 0, 0, 0, 0, time.UTC)}.Base() + segment.Ext
	bad := filepath.Join(filepath.Dir(f.app2Files[0]), name)
	if err := os.WriteFile(bad, []byte("not parquet"), 0o600); err != nil {
		t.Fatal(err)
	}
	code := f.run(slices.Concat([]string{"query"}, queryProjects,
		[]string{"-from", "00:53:20.001", "-to", "00:53:20.002"})...)
	if code != exitError || !strings.Contains(f.stderr.String(), name) {
		t.Fatalf("exit %d, stderr %s", code, f.stderr)
	}
	golden(t, "query_window.golden", f.stdout.String())
}

// TestQueryWindowErrorsAsBefore (review of task ik4): the default query
// reports what it reported before — a store part with no readable
// segment is "no readable segment" even with a time window, and an empty
// window is refused after the load, in the filter's words.
func TestQueryWindowErrorsAsBefore(t *testing.T) {
	f := newBuildFixture(t)
	if err := os.MkdirAll(filepath.Join(f.store, "ghost", "hostX"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		args []string
		code int
		msg  string
	}{
		{[]string{"-project", "ghost", "-from", "00:53:20"}, exitError, "query: no readable segment for ghost in "},
		{slices.Concat(queryProjects, []string{"-from", "00:53:21", "-to", "00:53:20"}), exitUsage,
			"usage: invalid filter: the window -from 1763600001000000000 -to 1763600000000000000 is empty"},
	} {
		if code := f.run(append([]string{"query"}, c.args...)...); code != c.code ||
			!strings.Contains(f.stderr.String(), c.msg) {
			t.Errorf("%v: exit %d, want %d with %q; stderr %s", c.args, code, c.code, c.msg, f.stderr)
		}
	}
}

// Task ik4: with -window-segments query's time window selects the
// segments it opens, as build's does (loadSelection, federate.Window);
// by default it still decodes the whole store and cuts the events
// afterwards, exactly as before.

// minuteHost adds host app3 (project shop) to f: one segment a minute,
// each named by the minute its event was written at (writeStoreSegments,
// one event per segment), so the clocks agree with the names — the
// session record at t0, then at minute m (1..11) an openat of /data/m
// stamped a millisecond later. Two are long calls, stamped before the
// segment they were written into: minute 7's at t0+6m40s (written up to
// 20 s after a -to of t0+6m50s: inside build's slack) and minute 9's at
// t0+6m45s (written over two minutes after it: outside the slack).
func minuteHost(t *testing.T, f *buildFixture) {
	t.Helper()
	h := &hostTrace{host: "app3", project: "shop"}
	h.header()
	for m := 1; m <= 11; m++ {
		at := uint64(time.Duration(m)*time.Minute/time.Microsecond) + 1000
		switch m {
		case 7:
			at = uint64((6*time.Minute + 40*time.Second) / time.Microsecond)
		case 9:
			at = uint64((6*time.Minute + 45*time.Second) / time.Microsecond)
		}
		ev := h.add(300, schema.KindOpenat, 3, 3, at, "", schema.Args{Path: bptr(fmt.Sprintf("/data/%d", m))})
		ev.FDGen = uint32(m)
	}
	writeStoreSegments(t, f.store, h.host, h, 1)
}

// countChecks makes f's env count the segment files query decodes.
func countChecks(f *buildFixture) func() int {
	var mu sync.Mutex
	n := 0
	f.e.checked = func(string) {
		mu.Lock()
		defer mu.Unlock()
		n++
	}
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		defer func() { n = 0 }()
		return n
	}
}

// at is t0 plus d in RFC 3339.
func at(d time.Duration) string { return windowT0.Add(d).Format(time.RFC3339Nano) }

// TestQueryWindowOpensItsSegments: once the host directories are pinned,
// a -window-segments query decodes only the segments whose range (their
// start, a minute before, to their interval and a minute after) overlaps
// the window; a query without the flag, windowed or not, still decodes
// the whole store.
func TestQueryWindowOpensItsSegments(t *testing.T) {
	f := newBuildFixture(t)
	minuteHost(t, f)
	f.mustRun(t, "build") // pins the three host directories
	count := countChecks(f)
	window := []string{"-from", at(5 * time.Minute), "-to", at(7 * time.Minute)}
	for _, args := range [][]string{nil, window} {
		f.mustRun(t, slices.Concat([]string{"query"}, queryProjects, args)...)
		if n := count(); n != 20 { // app1 5, app2 3, app3 12
			t.Errorf("query %v decoded %d segments, want all 20", args, n)
		}
	}
	// [t0+5m, t0+7m): app3's segments t0+4m..t0+7m, and the t0+6m one
	// of app1 and of app2.
	f.mustRun(t, slices.Concat([]string{"query", "-window-segments"}, queryProjects, window)...)
	if n := count(); n != 6 {
		t.Errorf("a -window-segments query of [t0+5m, t0+7m) decoded %d segments, want 6", n)
	}
	if code := f.run("query", "-window-segments"); code != exitUsage ||
		!strings.Contains(f.stderr.String(), "-window-segments needs -from or -to") {
		t.Errorf("-window-segments without a window: exit %d, want %d; stderr %s", code, exitUsage, f.stderr)
	}
}

// TestQueryDefaultWindowExact: without -window-segments a windowed query
// keeps everything the whole store knows — app2's events inside the
// window carry the merged id of a connection whose app1 connect lies in
// a later segment — and returns the events -window-segments misses.
func TestQueryDefaultWindowExact(t *testing.T) {
	f := newBuildFixture(t)
	f.mustRun(t, "build")
	window := []string{"-from", "00:53:20.001", "-to", "00:53:20.002"}
	exact, _ := queryLines(t, f.mustRun(t, slices.Concat([]string{"query"}, queryProjects, window)...))
	narrow, _ := queryLines(t, f.mustRun(t,
		slices.Concat([]string{"query", "-window-segments"}, queryProjects, window)...))
	if len(exact) != 7 || len(narrow) >= len(exact) {
		t.Fatalf("exact %d events, -window-segments %d: want 7 and fewer", len(exact), len(narrow))
	}
	if exact[0].ConnID == "s1" || narrow[0].ConnID != "s1" {
		t.Errorf("conn ids: exact %q (want merged), -window-segments %q (want host-local s1)",
			exact[0].ConnID, narrow[0].ConnID)
	}
}

// inWindow keeps the events of evs whose wall_ns lies in [from, to); a
// zero side is open.
func inWindow(evs []schema.Event, from, to time.Duration) []schema.Event {
	var out []schema.Event
	for _, ev := range evs {
		w := time.Duration(ev.WallNS - buildWall0)
		if w >= from && (to == 0 || w < to) {
			out = append(out, ev)
		}
	}
	return out
}

// TestQueryWindowEquivalent: a windowed query (the default, no
// -window-segments) writes exactly what the whole store's query, cut to
// the window afterwards, holds; a time of day lands on t0's date, the
// first segment's.
func TestQueryWindowEquivalent(t *testing.T) {
	f := newBuildFixture(t)
	minuteHost(t, f)
	f.mustRun(t, "build")
	for _, c := range []struct {
		filter   []string
		from, to string
		lo, hi   time.Duration // the window, hi 0 for open
	}{
		{nil, at(5 * time.Minute), "", 5 * time.Minute, 0},
		{nil, "00:53:20.001", "00:53:20.002", time.Millisecond, 2 * time.Millisecond}, // the skewed mesh
		{nil, "00:58:20", "00:59:20", 5 * time.Minute, 6 * time.Minute},               // t0 is 00:53:20
		{[]string{"-host", "app3"}, "", at(2 * time.Minute), 0, 2 * time.Minute},
		{[]string{"-file", "/data/*"}, at(3 * time.Minute), at(6 * time.Minute), 3 * time.Minute,
			6 * time.Minute},
	} {
		base := slices.Concat([]string{"query"}, queryProjects, c.filter)
		whole, _ := queryLines(t, f.mustRun(t, base...))
		want := inWindow(whole, c.lo, c.hi)
		args := slices.Clone(base)
		if c.from != "" {
			args = append(args, "-from", c.from)
		}
		if c.to != "" {
			args = append(args, "-to", c.to)
		}
		got, _ := queryLines(t, f.mustRun(t, args...))
		if len(want) == 0 || !slices.EqualFunc(got, want, eventsEqual) {
			t.Errorf("%v: %d events, the whole store's cut to the window %d", args, len(got), len(want))
		}
	}
}

// eventsEqual compares two decoded events by their canonical encoding.
func eventsEqual(a, b schema.Event) bool {
	return string(schema.AppendEvent(nil, &a)) == string(schema.AppendEvent(nil, &b))
}

// TestQueryWindowLongCall: with -window-segments, a call stamped inside
// the window but written
// after its end is still found when it was written within the slack of
// that end (the segment it lies in starts less than a minute after -to)
// and missed when written later — build's guarantee, no more.
func TestQueryWindowLongCall(t *testing.T) {
	f := newBuildFixture(t)
	minuteHost(t, f)
	f.mustRun(t, "build")
	evs, _ := queryLines(t, f.mustRun(t, "query", "-window-segments", "-project", "shop", "-host", "app3",
		"-from", at(6*time.Minute+30*time.Second), "-to", at(6*time.Minute+50*time.Second)))
	var paths []string
	for _, ev := range evs {
		paths = append(paths, *ev.Args.Path)
	}
	if strings.Join(paths, " ") != "/data/7" {
		t.Errorf("found %v, want minute 7's call (written within the slack) and not minute 9's", paths)
	}
}
