package federate

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"iormesh/internal/schema"
)

// Trace-directory file names (docs/schema.md "Files").
const (
	eventsFile = "events.ndjson"
	graphFile  = "graph.json"
)

// maxLine bounds one NDJSON line, as schema.ReadNDJSON does: generous for
// any event, and a corrupt file is never read into memory whole.
const maxLine = 1 << 20

// Source is one host session's collected trace, whatever its format. The
// merge reads every source through this interface only, so the NDJSON
// trace directory of today and the parquet segments of the M4 segment
// writer (task 8b4) are interchangeable inputs.
type Source interface {
	// Name identifies the source in errors and orders the sources, so a
	// merge reads them in the same order every time.
	Name() string
	// Graph is the host's own graph (the correlator's export).
	Graph(ctx context.Context) (schema.Graph, error)
	// Events calls fn for every event in seq order. fn borrows the event
	// for the call only (schema.Sink's rule); a non-nil error from fn
	// stops the iteration and is returned.
	Events(ctx context.Context, fn func(*schema.Event) error) error
}

// NDJSONDir is a trace directory as `iormesh run -out DIR` writes it:
// events.ndjson and graph.json.
type NDJSONDir struct{ Dir string }

var _ Source = NDJSONDir{}

// Name is the directory path.
func (d NDJSONDir) Name() string { return d.Dir }

// Graph reads and validates graph.json strictly (schema.DecodeGraph).
func (d NDJSONDir) Graph(_ context.Context) (schema.Graph, error) {
	data, err := os.ReadFile(filepath.Join(d.Dir, graphFile))
	if err != nil {
		return schema.Graph{}, fmt.Errorf("federate: %w", err)
	}
	g, err := schema.DecodeGraph(data)
	if err != nil {
		return schema.Graph{}, fmt.Errorf("federate: %s: %w", d.Dir, err)
	}
	return g, nil
}

// Events streams events.ndjson line by line, each decoded strictly
// (schema.DecodeEvent): a trace may hold hours of events, so it is never
// read whole. The context is checked once per line, cheap next to the
// decode.
func (d NDJSONDir) Events(ctx context.Context, fn func(*schema.Event) error) error {
	f, err := os.Open(filepath.Join(d.Dir, eventsFile))
	if err != nil {
		return fmt.Errorf("federate: %w", err)
	}
	defer func() { _ = f.Close() }() // read-only: a close error loses nothing
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), maxLine)
	for line := 1; sc.Scan(); line++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		ev, err := schema.DecodeEvent(sc.Bytes())
		if err != nil {
			return fmt.Errorf("federate: %s line %d: %w", d.Dir, line, err)
		}
		if err := fn(&ev); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("federate: %s: %w", d.Dir, err)
	}
	return nil
}
