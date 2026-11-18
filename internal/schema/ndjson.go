package schema

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// NDJSONSink writes one event per line, the live and golden format
// (docs/schema.md). Every event is validated first: a producer bug fails
// the write instead of reaching a file a strict reader would reject.
// Lines are encoded by AppendEvent, byte-identical to json.Marshal (keys
// in struct order), so two writes of the same event are byte-identical;
// it appends straight into the bufio buffer, so a line costs no
// allocation (decision #25's per-event budget, task wc4).
type NDJSONSink struct {
	w      *bufio.Writer
	closer io.Closer
	err    error // sticky: once a write failed, every later call fails
}

// sinkBuffer is the sink's write buffer: one write(2) per ~200 burst
// events instead of per ~12 with bufio's 4 KiB default, which was a tenth
// of the tracer's per-event CPU (task wc4). A crash loses at most this
// much of the stream — and the end event, which tells a reader so.
// lineRoom is the free space below which the buffer is flushed before a
// line is encoded: a typical event line is ~330 bytes.
const (
	sinkBuffer = 64 << 10
	lineRoom   = 1 << 10
)

// NewNDJSONSink buffers writes to w. If w is an io.Closer, Close closes it
// after flushing.
func NewNDJSONSink(w io.Writer) *NDJSONSink {
	s := &NDJSONSink{w: bufio.NewWriterSize(w, sinkBuffer)}
	if c, ok := w.(io.Closer); ok {
		s.closer = c
	}
	return s
}

// Event implements Sink.
func (s *NDJSONSink) Event(ev Event) error {
	if s.err != nil {
		return s.err
	}
	if err := ev.Validate(); err != nil {
		return fmt.Errorf("ndjson: event seq %d: %w", ev.Seq, err)
	}
	// A nearly full buffer is flushed first, so the line is encoded in
	// place: AvailableBuffer is empty with the buffer's free capacity, and
	// Write of a slice of it only advances the buffer. A line longer than
	// the room left (a long argv, say) grows a copy, which Write flushes
	// through.
	if s.w.Available() < lineRoom && s.w.Buffered() > 0 {
		if err := s.w.Flush(); err != nil {
			s.err = fmt.Errorf("ndjson: write seq %d: %w", ev.Seq, err)
			return s.err
		}
	}
	line := append(AppendEvent(s.w.AvailableBuffer(), &ev), '\n')
	if _, err := s.w.Write(line); err != nil {
		s.err = fmt.Errorf("ndjson: write seq %d: %w", ev.Seq, err)
	}
	return s.err
}

// Close implements Sink: it flushes, then closes the underlying writer if
// it is one. A flush error is returned even when closing succeeds.
func (s *NDJSONSink) Close() error {
	if s.err == errSinkClosed {
		return nil
	}
	err := s.err
	if ferr := s.w.Flush(); ferr != nil && err == nil {
		err = fmt.Errorf("ndjson: flush: %w", ferr)
	}
	if s.closer != nil {
		err = errors.Join(err, s.closer.Close())
	}
	// Later calls fail instead of buffering into a closed writer, and a
	// second Close does not close the underlying writer twice.
	s.closer, s.err = nil, errSinkClosed
	return err
}

var errSinkClosed = errors.New("ndjson: sink closed")

// maxLine bounds one NDJSON line. An event is a few hundred bytes; paths
// are at most PATH_MAX (4096), so 1 MiB is generous and still stops a
// corrupt file from being read into memory whole.
const maxLine = 1 << 20

// ReadNDJSON decodes a whole event stream strictly (DecodeEvent per
// line). Blank lines are not part of the format and are rejected too.
func ReadNDJSON(r io.Reader) ([]Event, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), maxLine)
	var events []Event
	for line := 1; sc.Scan(); line++ {
		ev, err := DecodeEvent(sc.Bytes())
		if err != nil {
			return events, fmt.Errorf("ndjson line %d: %w", line, err)
		}
		events = append(events, ev)
	}
	if err := sc.Err(); err != nil {
		return events, fmt.Errorf("ndjson: %w", err)
	}
	return events, nil
}

// WriteGraph writes g as indented JSON, nodes and edges in canonical
// order, after validating it. Indentation makes graph goldens diffable
// line by line; the order makes them byte-stable. It sorts g's slices in
// place (Graph.Sort), so the caller's graph ends up sorted too.
func WriteGraph(w io.Writer, g Graph) error {
	g.Sort()
	if err := g.Validate(); err != nil {
		return fmt.Errorf("graph: %w", err)
	}
	data, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return fmt.Errorf("graph: encode: %w", err)
	}
	if _, err := w.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("graph: write: %w", err)
	}
	return nil
}

// WriteGraphFile writes the graph export to path atomically: a reader (or
// rsync) sees the old file or the complete new one, never half a graph,
// and the data is synced before the rename so a crash cannot leave the
// new name on an empty file. The mode is 0644 (CreateTemp's 0600 would
// keep an unprivileged collector out).
func WriteGraphFile(path string, g Graph) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("graph: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	if err := writeSynced(tmp, g); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("graph: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("graph: %w", err)
	}
	return nil
}

func writeSynced(f *os.File, g Graph) error {
	if err := WriteGraph(f, g); err != nil {
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		return fmt.Errorf("graph: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("graph: %w", err)
	}
	return nil
}
