package federate

import (
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
