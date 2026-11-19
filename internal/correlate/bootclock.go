package correlate

import "time"

// bootResync is how often a bootClock reads CLOCK_BOOTTIME itself; in
// between it extrapolates on Go's monotonic clock.
const bootResync = time.Second

// bootClock reads the boot clock cheaply, for a check made once per
// event. unix.ClockGettime is a real syscall — Go's vDSO path serves only
// CLOCK_MONOTONIC (and REALTIME) — and JavaJoin.Event's "is a spool scan
// due?" read of it was a fifth of the tracer's CPU per event on the S3
// burst with -spool (task ud4, docs/spikes/s3-overhead.md). This clock
// reads CLOCK_BOOTTIME once a second and adds the monotonic time elapsed
// since (time.Since, a vDSO read).
//
// It never runs ahead of the boot clock: the boot read is taken before
// the monotonic one, and the two clocks differ only by time spent
// suspended, which only the boot clock counts — so after a suspend the
// estimate lags until the next resync, never leads. A lagging "now" only
// delays a scan; a leading one could claim a scan covered a moment it
// did not. Not safe for concurrent use: one per JavaJoin, like its owner.
type bootClock struct {
	read func() uint64 // the precise boot clock (BootNow)
	base uint64        // read() at at
	at   time.Time     // taken right after base, carries a monotonic reading
}

func newBootClock(read func() uint64) *bootClock {
	return &bootClock{read: read}
}

// now returns the estimated boot time in ns.
func (c *bootClock) now() uint64 {
	d := time.Since(c.at)
	if c.at.IsZero() || d >= bootResync || d < 0 {
		c.base = c.read()
		c.at = time.Now()
		d = 0
	}
	return c.base + uint64(d)
}
