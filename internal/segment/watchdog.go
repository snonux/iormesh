package segment

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// Watchdog defaults (decision #17: both a percentage and a free-bytes
// floor). 90% is PLAN.md's -disk-limit default. The floor is four
// segments at the size cap (decision #16's 64 MB): on a large partition
// 10% is plenty, but on a small one 10% may be less than the last
// segment, the end event and the collector's next run need.
const (
	DefaultMaxUsedPercent = 90
	DefaultMinFreeBytes   = 4 * DefaultMaxBytes
)

// ErrDiskLimit is what Check (and so Writer.Tick and New) returns, wrapped
// with the numbers, when the trace directory's partition is over a limit.
// The tracer ends the trace with end reason disk_limit on it.
var ErrDiskLimit = errors.New("disk limit reached")

// Usage is a partition's space as an unprivileged writer sees it, the
// view df prints: root's reserved blocks count as neither used nor
// available, so the tracer (root) stops before eating them.
type Usage struct {
	Used  uint64 // bytes in use
	Avail uint64 // bytes available to unprivileged users
}

// Watchdog bounds tracing by disk (PLAN.md M4 "Disk watchdog"): it
// reports ErrDiskLimit when the partition holding Dir is more than
// MaxUsedPercent used or has less than MinFreeBytes available. Either
// limit 0 is off.
type Watchdog struct {
	Dir            string
	MaxUsedPercent float64
	MinFreeBytes   uint64
	// Statfs reads the partition; nil is the real statfs(2). Tests fake it
	// to trip the limit without filling a disk.
	Statfs func(dir string) (Usage, error)
}

// Check statfs's the trace directory and compares it with the limits.
func (w Watchdog) Check() error {
	statfs := w.Statfs
	if statfs == nil {
		statfs = Statfs
	}
	u, err := statfs(w.Dir)
	if err != nil {
		return fmt.Errorf("disk watchdog: %w", err)
	}
	pct := u.UsedPercent()
	if w.MaxUsedPercent > 0 && pct >= w.MaxUsedPercent {
		return fmt.Errorf("%w: %s is %.1f%% used (limit %g%%)", ErrDiskLimit, w.Dir, pct,
			w.MaxUsedPercent)
	}
	if w.MinFreeBytes > 0 && u.Avail < w.MinFreeBytes {
		return fmt.Errorf("%w: %s has %d bytes free (floor %d)", ErrDiskLimit, w.Dir, u.Avail,
			w.MinFreeBytes)
	}
	return nil
}

// UsedPercent is used / (used + available), df's "Use%"; an empty or
// zero-sized filesystem is 0% used.
func (u Usage) UsedPercent() float64 {
	total := u.Used + u.Avail
	if total == 0 {
		return 0
	}
	return float64(u.Used) * 100 / float64(total)
}

// Statfs is the real partition reader for Watchdog.Statfs.
func Statfs(dir string) (Usage, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return Usage{}, fmt.Errorf("statfs %s: %w", dir, err)
	}
	bs := uint64(st.Bsize)
	return Usage{Used: (st.Blocks - st.Bfree) * bs, Avail: st.Bavail * bs}, nil
}
