package correlate

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"

	"iormesh/internal/schema"
)

// Clock returns the current boot-clock time in ns, the clock BPF records
// carry. Session, drop and end events are stamped with it: they describe
// the tracer, not a syscall, so no record time fits them.
type Clock func() uint64

// Errors of the host-level event API. They are ordering mistakes of the
// caller, never of the traced processes.
var (
	// ErrEnded: an event was offered after End. The end event must stay
	// the last one, or a reader would take a truncated trace for whole.
	ErrEnded = errors.New("correlate: trace already ended")
	// ErrSessionNotFirst: Session was called after other events were
	// numbered, or twice. The header is seq 1 or absent, never in between.
	ErrSessionNotFirst = errors.New("correlate: session header must be the first event")
)

// BootNow reads CLOCK_BOOTTIME, the default Config.Clock. A failing read
// (impossible on Linux ≥ 2.6.39) yields 0 rather than an error: a host
// event with a zero timestamp is still ordered by its seq.
func BootNow() uint64 {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts); err != nil {
		return 0
	}
	return uint64(ts.Nano())
}

// Session queues the session header. Call it before the first Handle, so
// the header is seq 1 and every reader meets the host description first.
func (c *Correlator) Session(s schema.Session) error {
	if c.ended {
		return ErrEnded
	}
	if c.seq != 0 {
		return ErrSessionNotFirst
	}
	ev := c.hostEvent(schema.KindSession)
	ev.Session = &s
	c.enqueue(ev, nil)
	return c.release(false)
}

// RecordLosses takes the tracer's cumulative loss count (bpf.Losses.Total)
// and queues a drop event for the growth since the last call, so a gap
// shows up in the stream near where it happened instead of only in a
// summary. No growth means no event: a drop of 0 would be noise.
func (c *Correlator) RecordLosses(total uint64) error {
	return c.recordGrowth(total, &c.lost, nil)
}

// RecordSkips takes the tracer's cumulative count of program runs the
// kernel skipped (bpf.Losses.Missed) and queues a skipped_runs drop for
// the growth since the last call. It is kept apart from RecordLosses
// because the count is host-wide - the skip comes before the target
// filter - so it bounds the target records lost instead of counting them
// (task vd4): mixing it in would inflate the trace's loss.
func (c *Correlator) RecordSkips(total uint64) error {
	reason := schema.DropSkippedRuns
	return c.recordGrowth(total, &c.skipped, &reason)
}

// recordGrowth queues a host-level drop (reason nil for plain loss) for
// the growth of a cumulative counter over *seen, the part already
// reported. A lower value than before (impossible unless the caller
// swapped tracers) is ignored rather than turned into a negative drop.
func (c *Correlator) recordGrowth(total uint64, seen *uint64, reason *string) error {
	if c.ended {
		return ErrEnded
	}
	if total <= *seen {
		return nil
	}
	lost := total - *seen
	*seen = total
	ev := c.hostEvent(schema.KindDrop)
	ev.Args.Lost, ev.Args.DropReason = &lost, reason
	c.enqueue(ev, nil)
	return c.release(false)
}

// End queues the end event with reason, one of the schema.End* values.
// It is numbered after every event already queued, including socket
// events still waiting for their conn id, so it is the last event once
// Close flushes. Handle, RecordLosses, RecordSkips and Session fail
// after End.
func (c *Correlator) End(reason string) error {
	if c.ended {
		return ErrEnded
	}
	if !schema.KnownEndReason(reason) {
		// Checked here, not only by the sink: a rejected end would leave
		// the trace without one, which reads as "crashed".
		return fmt.Errorf("correlate: end reason %q: %w", reason, schema.ErrInvalid)
	}
	c.ended = true
	ev := c.hostEvent(schema.KindEnd)
	ev.Args.EndReason = &reason
	c.enqueue(ev, nil)
	return c.release(false)
}

// hostEvent builds a host-level event: pid, tid and incarnation 0 mark it
// as belonging to no process, which the normalizer relies on to keep it
// out of process renumbering and inside the marker bracket.
func (c *Correlator) hostEvent(kind string) schema.Event {
	now := c.cfg.Clock()
	return schema.Event{
		SchemaVersion: schema.Version, Project: c.cfg.Host.Project, HostID: c.cfg.Host.ID,
		BootNS: now, WallNS: c.wall(now), Event: kind, FD: schema.NoFD,
		Attribution: schema.AttributionNone,
	}
}
