package integrationtests

// Adapted from ior integrationtests/parse.go @ b21afc50328c (LoadTestResult).
// Changes: reads an iormesh trace directory - the NDJSON event stream and
// the graph export - instead of an .ior.zst recording, with the same strict
// readers `iormesh normalize` uses, so a malformed line fails the test
// rather than being skipped.

import (
	"fmt"
	"os"
	"path/filepath"

	"iormesh/internal/normalize"
	"iormesh/internal/schema"
)

// The files `iormesh run -out DIR` writes (internal/cli EventsFile and
// GraphFile; repeated here so the tests depend on the output contract, not
// on the CLI package).
const (
	eventsFile = "events.ndjson"
	graphFile  = "graph.json"
)

// ReadTrace reads the trace directory dir.
func ReadTrace(dir string) (normalize.Trace, error) {
	f, err := os.Open(filepath.Join(dir, eventsFile))
	if err != nil {
		return normalize.Trace{}, fmt.Errorf("read trace: %w", err)
	}
	defer func() { _ = f.Close() }()
	events, err := schema.ReadNDJSON(f)
	if err != nil {
		return normalize.Trace{}, fmt.Errorf("%s: %w", f.Name(), err)
	}
	data, err := os.ReadFile(filepath.Join(dir, graphFile))
	if err != nil {
		return normalize.Trace{}, fmt.Errorf("read trace: %w", err)
	}
	graph, err := schema.DecodeGraph(data)
	if err != nil {
		return normalize.Trace{}, fmt.Errorf("%s: %w", graphFile, err)
	}
	return normalize.Trace{Events: events, Graph: graph}, nil
}
