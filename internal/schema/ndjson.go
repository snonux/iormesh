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
// Keys come out in struct order, which encoding/json guarantees, so two
// writes of the same event are byte-identical.
type NDJSONSink struct {
	w      *bufio.Writer
	closer io.Closer
	err    error // sticky: once a write failed, every later call fails
}

// NewNDJSONSink buffers writes to w. If w is an io.Closer, Close closes it
// after flushing.
func NewNDJSONSink(w io.Writer) *NDJSONSink {
	s := &NDJSONSink{w: bufio.NewWriter(w)}
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
	line, err := json.Marshal(ev)
	if err != nil {
		s.err = fmt.Errorf("ndjson: encode seq %d: %w", ev.Seq, err)
		return s.err
	}
	if _, err := s.w.Write(append(line, '\n')); err != nil {
		s.err = fmt.Errorf("ndjson: write seq %d: %w", ev.Seq, err)
	}
	return s.err
}

// Close implements Sink: it flushes, then closes the underlying writer if
// it is one. A flush error is returned even when closing succeeds.
func (s *NDJSONSink) Close() error {
	err := s.err
	if ferr := s.w.Flush(); ferr != nil && err == nil {
		err = fmt.Errorf("ndjson: flush: %w", ferr)
	}
	if s.closer != nil {
		err = errors.Join(err, s.closer.Close())
	}
	return err
}

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
// line by line; the order makes them byte-stable.
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
// rsync) sees the old file or the complete new one, never half a graph.
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
	if err := WriteGraph(tmp, g); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("graph: %w", err)
	}
	return os.Rename(tmp.Name(), path)
}
