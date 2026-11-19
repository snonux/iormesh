package correlate

import (
	"testing"
	"time"
)

// TestBootClockNeverAhead: the estimate never runs ahead of the boot
// clock it extrapolates (read before and after it), and it reads that
// clock once per resync interval, not per call.
func TestBootClockNeverAhead(t *testing.T) {
	reads := 0
	c := newBootClock(func() uint64 { reads++; return BootNow() })
	for range 1000 {
		got := c.now()
		if after := BootNow(); got > after {
			t.Fatalf("estimate %d ahead of the boot clock %d", got, after)
		}
	}
	// Two reads at most: a host pause longer than bootResync (a loaded
	// CI box) may make the loop span one resync.
	if reads < 1 || reads > 2 {
		t.Errorf("%d boot clock reads in well under %v, want 1 (2 after a pause)", reads, bootResync)
	}
}

// TestBootClockResyncs: once the interval has passed, the next call reads
// the boot clock again, so a suspend's lag lasts one interval at most.
func TestBootClockResyncs(t *testing.T) {
	base := uint64(1_000_000)
	c := newBootClock(func() uint64 { base += 1_000_000_000; return base })
	first := c.now()
	c.at = c.at.Add(-bootResync) // as if a second had passed
	if second := c.now(); second != base || second == first {
		t.Fatalf("after the interval: %d, want a fresh read %d (first %d)", second, base, first)
	}
	if d := c.now() - base; d > uint64(time.Second) {
		t.Errorf("extrapolated %d ns past the fresh read", d)
	}
}
