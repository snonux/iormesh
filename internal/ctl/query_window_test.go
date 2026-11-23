package ctl

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"iormesh/internal/schema"
)

// Task ik4: query's time window selects the segments it opens, as
// build's does (loadSelection, federate.Window), instead of decoding the
// whole store and cutting the events afterwards.

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
// a windowed query decodes only the segments whose range (their start,
// a minute before, to their interval and a minute after) overlaps the
// window — not the whole store, which a query without a window still
// reads.
func TestQueryWindowOpensItsSegments(t *testing.T) {
	f := newBuildFixture(t)
	minuteHost(t, f)
	f.mustRun(t, "build") // pins the three host directories
	count := countChecks(f)
	f.mustRun(t, slices.Concat([]string{"query"}, queryProjects)...)
	if n := count(); n != 20 { // app1 5, app2 3, app3 12
		t.Fatalf("a query without a window decoded %d segments, want all 20", n)
	}
	// [t0+5m, t0+7m): app3's segments t0+4m..t0+7m, and the t0+6m one
	// of app1 and of app2.
	f.mustRun(t, slices.Concat([]string{"query"}, queryProjects,
		[]string{"-from", at(5 * time.Minute), "-to", at(7 * time.Minute)})...)
	if n := count(); n != 6 {
		t.Errorf("a query of [t0+5m, t0+7m) decoded %d segments, want 6", n)
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

// TestQueryWindowEquivalent: for events written within the slack of
// their stamp, a windowed query writes exactly what the whole store's
// query, cut to the window afterwards, holds — the narrowing loses
// nothing inside the window.
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
		{nil, "00:58:20", "00:59:20", 5 * time.Minute, 6 * time.Minute}, // t0 is 00:53:20
		{[]string{"-host", "app3"}, "", at(2 * time.Minute), 0, 2 * time.Minute},
		{[]string{"-file", "/data/*"}, at(3 * time.Minute), at(6 * time.Minute), 3 * time.Minute,
			6 * time.Minute},
	} {
		base := slices.Concat([]string{"query"}, queryProjects, c.filter)
		whole, _ := queryLines(t, f.mustRun(t, base...))
		want := inWindow(whole, c.lo, c.hi)
		args := base
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

// TestQueryWindowLongCall: a call stamped inside the window but written
// after its end is still found when it was written within the slack of
// that end (the segment it lies in starts less than a minute after -to)
// and missed when written later — build's guarantee, no more.
func TestQueryWindowLongCall(t *testing.T) {
	f := newBuildFixture(t)
	minuteHost(t, f)
	f.mustRun(t, "build")
	evs, _ := queryLines(t, f.mustRun(t, "query", "-project", "shop", "-host", "app3",
		"-from", at(6*time.Minute+30*time.Second), "-to", at(6*time.Minute+50*time.Second)))
	var paths []string
	for _, ev := range evs {
		paths = append(paths, *ev.Args.Path)
	}
	if strings.Join(paths, " ") != "/data/7" {
		t.Errorf("found %v, want minute 7's call (written within the slack) and not minute 9's", paths)
	}
}
