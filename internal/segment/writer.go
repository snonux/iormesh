package segment

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"iormesh/internal/schema"
)

// Rotation defaults (decision #16): a segment is published after a
// minute or at 64 MiB, whichever comes first, so one never outgrows what
// a collect moves comfortably. The disk is checked every five seconds:
// at decision #25's reference rate (18 MB a minute) that is 1.5 MB a
// check, far inside the free-bytes floor.
const (
	DefaultInterval   = time.Minute
	DefaultMaxBytes   = 64 << 20
	DefaultCheckEvery = 5 * time.Second
)

// eventBatch is how many events Writer collects before handing them to
// the parquet library in one Write: its per-call overhead (schema lookup,
// row-group bookkeeping) is paid once per batch instead of per event.
const eventBatch = 256

// Config describes a Writer. Dir, FQDN, Session and Project are
// required; zero durations and sizes take the defaults.
type Config struct {
	// Dir is the project directory, <trace-dir>/<project>.
	Dir string
	// FQDN and Session name the segments (Name); Session is one run's id
	// and must not contain a dash.
	FQDN, Session string
	// Project is recorded in every file's footer.
	Project    string
	Interval   time.Duration
	MaxBytes   int64
	CheckEvery time.Duration
	// Owner, if set, owns ready/ (-ready-owner).
	Owner *Owner
	// Watchdog, if set, is checked by New, every CheckEvery in Tick, and
	// before every rotation.
	Watchdog *Watchdog
	// Now is the clock New opens the first segment by; nil is time.Now.
	Now func() time.Time
}

// Writer is a schema.Sink that writes a trace as parquet segments (PLAN.md
// M4 "Trace files", decision #16). Only the active segment's files are
// ever open; a finished segment is renamed into ready/, which makes it
// immutable and safe to rsync while the session runs.
//
// A segment is the events file plus a file per table (methods, threads,
// classes, stacks) that got rows while it was active, so the union of a
// session's segments holds its whole tables. Tables are published before
// their segment's events: a collector that has an events segment also
// has every Java table row its events reference (the join writes those
// rows before it releases the event). Stack rows come from the stack
// table's worker and may land one segment later than the first event
// naming them.
//
// Event, the table methods, Tick and Close are safe for concurrent use:
// the stack table writes from its own goroutine.
type Writer struct {
	mu        sync.Mutex
	cfg       Config
	codec     *zstdCodec
	seq       int
	start     time.Time // the active segment was opened; names it
	rotateAt  time.Time
	nextCheck time.Time
	events    *stream[schema.Event]
	batch     []schema.Event
	methods   table[schema.Method]
	threads   table[schema.Thread]
	classes   table[schema.Class]
	stacks    table[schema.Stack]
	// open lists the tables with a file in the active segment, in the
	// order their first row came.
	open   []tableFile
	err    error // sticky: the first write or publish error
	closed bool
}

var _ schema.Sink = (*Writer)(nil)

// table is one table's file in the active segment, nil until a row
// comes.
type table[T any] struct {
	name string
	s    *stream[T]
}

// New prepares cfg.Dir (ready/ owned by cfg.Owner, stale active files
// removed), checks the disk — a partition already over the limit fails
// here, before the trace starts — and opens segment 0.
func New(cfg Config) (*Writer, error) {
	cfg, err := withDefaults(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.Watchdog != nil {
		if err := cfg.Watchdog.Check(); err != nil {
			return nil, err
		}
	}
	if err := prepareDir(cfg.Dir, cfg.Owner); err != nil {
		return nil, err
	}
	if err := sweepActive(cfg.Dir); err != nil {
		return nil, err
	}
	w := &Writer{cfg: cfg, codec: newZstdCodec(), batch: make([]schema.Event, 0, eventBatch),
		methods: table[schema.Method]{name: TableMethods},
		threads: table[schema.Thread]{name: TableThreads},
		classes: table[schema.Class]{name: TableClasses},
		stacks:  table[schema.Stack]{name: TableStacks}}
	now := cfg.Now()
	w.nextCheck = now.Add(cfg.CheckEvery)
	if err := w.openSegment(now); err != nil {
		return nil, err
	}
	return w, nil
}

// Event implements schema.Sink. The event is validated (readers are
// strict; a writer must not produce what they reject) and copied into
// the batch: a shallow copy, as the Java join's queue keeps, since the
// Sink contract only lets the caller reuse the Event itself.
func (w *Writer) Event(ev *schema.Event) error {
	if err := ev.Validate(); err != nil {
		return fmt.Errorf("segment: event seq %d: %w", ev.Seq, err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.usable(); err != nil {
		return err
	}
	w.batch = append(w.batch, *ev)
	if len(w.batch) < eventBatch {
		return nil
	}
	return w.flushBatch()
}

// Method adds a Java method row to the active segment's methods table,
// validated as the NDJSON tables are; Thread, Class and Stack do the
// same for theirs.
func (w *Writer) Method(m schema.Method) error {
	if err := m.Validate(); err != nil {
		return fmt.Errorf("segment: %w", err)
	}
	return writeRow(w, &w.methods, m)
}

// Thread adds a Java thread row (see Method).
func (w *Writer) Thread(t schema.Thread) error {
	if err := t.Validate(); err != nil {
		return fmt.Errorf("segment: %w", err)
	}
	return writeRow(w, &w.threads, t)
}

// Class adds a Java class row (see Method).
func (w *Writer) Class(c schema.Class) error {
	if err := c.Validate(); err != nil {
		return fmt.Errorf("segment: %w", err)
	}
	return writeRow(w, &w.classes, c)
}

// Stack adds a native stack row (see Method); the stack table's worker
// goroutine calls it.
func (w *Writer) Stack(s schema.Stack) error {
	if err := s.Validate(); err != nil {
		return fmt.Errorf("segment: %w", err)
	}
	return writeRow(w, &w.stacks, s)
}

// Tick is the writer's clock, called from the tracer's loop (every
// 200 ms): it checks the disk every CheckEvery and rotates once the
// segment is a minute old or over the size cap. Rotation lives here, not
// in Event, so the per-event path reads no clock, and an idle stream
// still publishes its last events on time.
//
// ErrDiskLimit is returned (wrapped) but does not break the writer: the
// tracer ends the trace on it, writes the end event, and Close publishes
// the last segment.
func (w *Writer) Tick(now time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.usable(); err != nil {
		return err
	}
	if !now.Before(w.nextCheck) {
		w.nextCheck = now.Add(w.cfg.CheckEvery)
		if err := w.checkDisk(); err != nil {
			return err
		}
	}
	if now.Before(w.rotateAt) && w.events.size() < w.cfg.MaxBytes {
		return nil
	}
	return w.rotate(now)
}

// Close implements schema.Sink: it publishes the active segment (an
// empty one is removed instead) and releases the encoder. After a
// write error nothing more is published — the broken segment is removed
// and the error returned.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	defer w.codec.release()
	if w.err != nil {
		return errors.Join(w.err, w.abortSegment())
	}
	if w.empty() {
		return w.abortSegment()
	}
	return w.publish()
}
