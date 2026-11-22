package correlate

import (
	"fmt"
	"reflect"
	"testing"

	"iormesh/internal/schema"
)

// feedDistinct opens and closes n distinct files and runs one loopback
// connection: every call adds a node and an edge to a built graph.
func feedDistinct(t *testing.T, c *Correlator, n int) {
	now := uint64(0)
	f := feeder{t, c, 10, 11, &now}
	for i := range n {
		f.open(fmt.Sprintf("/var/spool/req-%d", i), 0, 5)
		f.close(5)
	}
	loopback(t, c, 1000)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestNoGraphHoldsNothing: with Config.NoGraph (a -trace-dir run without
// -out, task 4j4) the correlator keeps no graph however many distinct
// files and connections it sees, so a deployed tracer's memory does not
// grow per path, connection and process for a graph nobody writes; the
// events are the same as with the graph.
func TestNoGraphHoldsNothing(t *testing.T) {
	const n = 1000
	with, withSink := newTest(t)
	feedDistinct(t, with, n)
	if got := len(with.Graph().Nodes); got < n {
		t.Fatalf("graph on: %d nodes, want at least %d", got, n)
	}

	sink := &memSink{t: t}
	c := New(Config{Host: Host{ID: "h1", Project: "p"}, Sink: sink, NoGraph: true,
		ProcInfo: func(pid uint32) (string, uint64) { return fmt.Sprintf("proc%d", pid), 1000 + uint64(pid) }})
	feedDistinct(t, c, n)
	if c.graph != nil {
		t.Fatal("NoGraph: the correlator still holds a graph builder")
	}
	g := c.Graph()
	if len(g.Nodes) != 0 || len(g.Edges) != 0 || g.SchemaVersion != schema.Version {
		t.Fatalf("NoGraph: Graph() = %d nodes, %d edges, version %d; want an empty graph",
			len(g.Nodes), len(g.Edges), g.SchemaVersion)
	}
	if !reflect.DeepEqual(sink.events, withSink.events) {
		t.Fatal("NoGraph changed the events")
	}
}
