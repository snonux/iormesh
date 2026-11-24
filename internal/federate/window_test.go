package federate

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"iormesh/internal/segment"
)

// TestWindowOverlaps (task yh4): a segment's range is its named start
// widened by a minute before and its interval plus a minute after; the
// window takes it when the two overlap, each open side unbounded, and a
// nil window takes everything.
func TestWindowOverlaps(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	f := segment.FileName{FQDN: "h", Session: "s", Start: t0}
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	cases := []struct {
		w    *Window
		want bool
	}{
		{nil, true},
		{&Window{}, true},
		{&Window{From: at(2)}, false}, // the range ends at t0+2m, exclusive
		{&Window{From: at(1)}, true},  // inside the trailing slack
		{&Window{To: at(-1)}, false},  // the range starts at t0-1m
		{&Window{To: at(0)}, true},    // inside the leading slack
		{&Window{From: at(-30), To: at(-2)}, false},
		{&Window{From: at(-30), To: at(30)}, true},
	}
	for _, c := range cases {
		if got := c.w.Overlaps(f); got != c.want {
			t.Errorf("%+v.Overlaps(t0) = %v, want %v", c.w, got, c.want)
		}
	}
}

// TestStartRange (task 1l4): the earliest and the latest start the
// events segments of the projects name, from the listings alone — a
// stray file and a project left out count for nothing — and an error
// when none is listed.
func TestStartRange(t *testing.T) {
	store := t.TempDir()
	at := func(d, h int) time.Time { return time.Date(2026, 11, d, h, 0, 0, 0, time.UTC) }
	touch := func(proj, host, name string) {
		t.Helper()
		dir := filepath.Join(store, proj, host)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	seg := func(start time.Time) string {
		return segment.FileName{FQDN: "h.example.org", Session: "s1", Start: start}.Base() + segment.Ext
	}
	touch("shop", "app1", seg(at(21, 3)))
	touch("shop", "app1", seg(at(20, 23)))
	touch("shop", "app2", seg(at(22, 1)))
	touch("shop", "app2", "notes.txt")
	touch("billing", "app3", seg(at(19, 0)))
	first, last, err := StartRange(store, []string{"shop"})
	if err != nil || !first.Equal(at(20, 23)) || !last.Equal(at(22, 1)) {
		t.Errorf("StartRange(shop) = %v, %v, %v; want %v .. %v", first, last, err, at(20, 23), at(22, 1))
	}
	if first, _, err = StartRange(store, nil); err != nil || !first.Equal(at(19, 0)) {
		t.Errorf("StartRange(every project) starts %v (%v), want %v", first, err, at(19, 0))
	}
	if err := os.MkdirAll(filepath.Join(store, "ghost", "app9"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := StartRange(store, []string{"ghost"}); err == nil {
		t.Error("StartRange of a project without segments: no error")
	}
}
