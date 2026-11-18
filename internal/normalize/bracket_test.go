package normalize

import (
	"testing"

	"iormesh/internal/schema"
)

// TestBracketKeepsForkedChildren: a process without markers that a
// bracketed process forked inside its bracket — and its own child — is cut
// to the parent's window (M2 step 6, fd-lifecycle's child JVM). A child
// whose first event precedes the parent's begin marker was forked before
// the scenario and stays out, as does a process no forked edge links; the
// pruned graph keeps the forked edges of the kept children only.
func TestBracketKeepsForkedChildren(t *testing.T) {
	r := runA()
	b := &traceBuilder{r: r}
	parent := r.server
	child, grandchild := proc{5000, 50, 5000}, proc{5001, 51, 5001}
	early, stranger := proc{5002, 52, 5002}, proc{5003, 53, 5003}
	b.io(early, early.tid, schema.KindWrite, 1, 0, 1)
	b.marker(parent, "s", "begin")
	b.io(child, child.tid, schema.KindRead, 0, 0, 2)
	b.io(grandchild, grandchild.tid, schema.KindRead, 0, 0, 3)
	b.io(early, early.tid, schema.KindWrite, 1, 0, 4)
	b.io(stranger, stranger.tid, schema.KindWrite, 1, 0, 5)
	b.marker(parent, "s", "end")
	b.io(child, child.tid, schema.KindWrite, 1, 0, 6)
	g := r.graph([]proc{parent}, []string{"x.dat"}, false)
	for _, p := range []proc{child, grandchild, early, stranger} {
		g.Nodes = append(g.Nodes, schema.Node{Type: schema.NodeProcess, ID: procID(r, p)})
	}
	g.Edges = append(g.Edges, forkedEdge(r, parent, child), forkedEdge(r, child, grandchild),
		forkedEdge(r, parent, early))
	g.Sort()
	got, err := Normalize(Trace{Events: b.events, Graph: g}, Options{Bracket: "s"})
	if err != nil {
		t.Fatal(err)
	}
	var bytes []uint64
	for _, e := range got.Events {
		if e.Event != schema.KindMarker {
			bytes = append(bytes, e.Bytes)
		}
	}
	if len(bytes) != 2 || bytes[0] != 2 || bytes[1] != 3 {
		t.Fatalf("kept the events of %v bytes, want the child's 2 and the grandchild's 3", bytes)
	}
	var forked int
	for _, e := range got.Graph.Edges {
		if e.Type == schema.EdgeForked {
			forked++
		}
	}
	if forked != 2 {
		t.Fatalf("pruned graph has %d forked edges, want 2: %+v", forked, got.Graph.Edges)
	}
}

func procID(r run, p proc) string { return schema.ProcessNodeID(r.host, p.pid, p.inc) }

func forkedEdge(r run, from, to proc) schema.Edge {
	return schema.Edge{Type: schema.EdgeForked, From: procID(r, from), To: procID(r, to)}
}
