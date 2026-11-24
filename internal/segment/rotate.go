package segment

import (
	"errors"
	"path/filepath"
	"strconv"
	"time"

	"iormesh/internal/schema"
)

// errClosed is returned by every call after Close.
var errClosed = errors.New("segment: writer closed")

// withDefaults validates cfg and fills in its zero fields.
func withDefaults(cfg Config) (Config, error) {
	if cfg.Dir == "" {
		return cfg, errors.New("segment: no directory")
	}
	if err := errors.Join(validName("fqdn", cfg.FQDN, true),
		validName("session", cfg.Session, false), validName("project", cfg.Project, true)); err != nil {
		return cfg, err
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = DefaultMaxBytes
	}
	if cfg.CheckEvery <= 0 {
		cfg.CheckEvery = DefaultCheckEvery
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return cfg, nil
}

// usable is the error a call must return before touching the segment.
func (w *Writer) usable() error {
	if w.closed {
		return errClosed
	}
	return w.err
}

// fail records the writer's first error; every later call returns it.
func (w *Writer) fail(err error) error {
	if w.err == nil {
		w.err = err
	}
	return err
}

// writeRow appends one table row, opening the table's file in the active
// segment on its first row. A generic function rather than a method, as
// Go methods take no type parameters.
func writeRow[T any](w *Writer, t *table[T], row T) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.usable(); err != nil {
		return err
	}
	if t.s == nil {
		s, err := openStream[T](w.activePath(t.name), w.codec)
		if err != nil {
			return w.fail(err)
		}
		t.s = s
		w.open = append(w.open, t)
	}
	if err := t.s.write([]T{row}); err != nil {
		return w.fail(err)
	}
	return nil
}

// rotate publishes the active segment and opens the next. The disk is
// checked first (docs/milestones.md: "before every segment rotation"); over
// the limit, nothing rotates and the tracer ends the trace. A segment
// without a single row is not published: an idle session would
// otherwise leave an empty file in ready/ every minute. It restarts its
// clock instead, so <start-ts> stays when its first row's minute began.
func (w *Writer) rotate(now time.Time) error {
	if err := w.checkDisk(); err != nil {
		return err
	}
	if w.empty() {
		w.start, w.rotateAt = now, now.Add(w.cfg.Interval)
		return nil
	}
	if err := w.publish(); err != nil {
		return err
	}
	w.seq++
	return w.openSegment(now)
}

// openSegment opens the active segment's events file at now.
func (w *Writer) openSegment(now time.Time) error {
	s, err := openEventStream(w.activePath("events"), w.codec, w.footer)
	if err != nil {
		return w.fail(err)
	}
	w.events, w.start, w.rotateAt = s, now, now.Add(w.cfg.Interval)
	return nil
}

// publish moves the active segment into ready/: its tables, then its
// events (see Writer), then an fsync of ready/ so the renames are
// durable before the next segment starts. Any error breaks the writer.
func (w *Writer) publish() error {
	ready := filepath.Join(w.cfg.Dir, ReadyDir)
	for i, p := range w.open {
		name := TableName(w.cfg.FQDN, w.cfg.Session, w.start, w.seq, p.tableName())
		if err := p.publish(filepath.Join(ready, name), w.meta(p.tableName())); err != nil {
			w.open = w.open[i+1:]
			return w.fail(errors.Join(err, w.abortSegment()))
		}
	}
	if len(w.open) > 0 {
		// The table renames must be durable before the events rename:
		// a crash in between may then leave tables without their events
		// (harmless rows), never events without their tables.
		if err := syncDir(ready); err != nil {
			return w.fail(err)
		}
	}
	w.open = w.open[:0]
	name := Name(w.cfg.FQDN, w.cfg.Session, w.start, w.seq)
	events := w.events
	w.events = nil
	if err := events.publish(filepath.Join(ready, name), w.meta("events")); err != nil {
		return w.fail(err)
	}
	if err := syncDir(ready); err != nil {
		return w.fail(err)
	}
	return nil
}

// abortSegment removes the active segment's files.
func (w *Writer) abortSegment() error {
	var errs []error
	for _, p := range w.open {
		errs = append(errs, p.abort())
	}
	w.open = w.open[:0]
	if w.events != nil {
		errs = append(errs, w.events.abort())
		w.events = nil
	}
	return errors.Join(errs...)
}

// empty reports whether the active segment holds no row at all.
func (w *Writer) empty() bool {
	return len(w.open) == 0 && (w.events == nil || w.events.rows == 0)
}

func (w *Writer) checkDisk() error {
	if w.cfg.Watchdog == nil {
		return nil
	}
	return w.cfg.Watchdog.Check()
}

// activePath is the file of the active segment's events or a table. The
// session is in the name, so even a writer that somehow bypassed the
// lock could never open another run's file.
func (w *Writer) activePath(what string) string {
	return filepath.Join(w.cfg.Dir, activePrefix+w.cfg.Session+"-"+what+Ext)
}

// meta is a segment file's footer key-value metadata: what the name
// encodes plus what it cannot (project, schema version, the exact start),
// so a file renamed or copied loose still says where it came from.
func (w *Writer) meta(what string) map[string]string {
	return map[string]string{
		"iormesh.schema_version": strconv.Itoa(schema.Version),
		"iormesh.project":        w.cfg.Project,
		"iormesh.fqdn":           w.cfg.FQDN,
		"iormesh.session":        w.cfg.Session,
		"iormesh.seq":            strconv.Itoa(w.seq),
		"iormesh.start":          w.start.UTC().Format(time.RFC3339Nano),
		"iormesh.content":        what,
	}
}

// publish and abort make a table a tableFile; both leave it without a
// file, ready for the next segment's first row.
func (t *table[T]) publish(readyPath string, meta map[string]string) error {
	s := t.s
	t.s = nil
	return s.publish(readyPath, meta)
}

func (t *table[T]) abort() error {
	s := t.s
	t.s = nil
	if s == nil {
		return nil
	}
	return s.abort()
}

func (t *table[T]) tableName() string { return t.name }
