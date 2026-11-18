package normalize

import (
	"path/filepath"
	"testing"

	"iormesh/internal/schema"
)

// restartTrace builds a raw restart trace: two incarnations of one app
// (run's server and client stand in for them), each bracketing itself,
// and a graph with the app node, both incarnation edges and a third
// incarnation that never entered the scenario.
func restartTrace(r run) Trace {
	b := &traceBuilder{r: r}
	state := filepath.Join(r.tmp, "restart.state")
	one, two := r.server, r.client
	b.jvmNoise(one)
	b.marker(one, "restart", "begin")
	b.add(one, one.tid, schema.KindOpenat, schema.NoFD, 0, func(e *schema.Event) {
		e.Ret, e.Err = -2, "ENOENT"
		e.Args.Path, e.Args.Flags = strp(state), u32p(0)
	})
	b.open(one, one.tid, state, oWronlyCreatTrunc, 5, 1)
	b.io(one, one.tid, schema.KindWrite, 5, 1, 8)
	b.close(one, 5, 1)
	b.marker(one, "restart", "end")
	b.jvmNoise(two)
	b.marker(two, "restart", "begin")
	b.open(two, two.tid, state, 0, 5, 1)
	b.io(two, two.tid, schema.KindRead, 5, 1, 8)
	b.close(two, 5, 1)
	b.open(two, two.tid, state, oWronlyCreatTrunc, 5, 2)
	b.io(two, two.tid, schema.KindWrite, 5, 2, 8)
	b.close(two, 5, 2)
	b.marker(two, "restart", "end")
	return Trace{Events: b.events, Graph: restartGraph(r, state, one, two)}
}

// restartGraph is what the M2 correlator should export for restart: one
// app node linking every incarnation the pattern matched.
func restartGraph(r run, state string, procs ...proc) schema.Graph {
	g := schema.Graph{SchemaVersion: schema.Version}
	app, file := "app:demo/*RestartApp*", schema.FileNodeID(r.host, state)
	g.Nodes = append(g.Nodes, schema.Node{Type: "app", ID: app},
		schema.Node{Type: schema.NodeFile, ID: file})
	// A third incarnation outside the bracket: its edge must be pruned.
	stray := schema.ProcessNodeID(r.host, 99, 1)
	g.Nodes = append(g.Nodes, schema.Node{Type: schema.NodeProcess, ID: stray})
	g.Edges = append(g.Edges, schema.Edge{Type: edgeIncarnation, From: app, To: stray})
	for _, p := range procs {
		id := schema.ProcessNodeID(r.host, p.pid, p.inc)
		g.Nodes = append(g.Nodes, schema.Node{Type: schema.NodeProcess, ID: id})
		g.Edges = append(g.Edges,
			schema.Edge{Type: schema.EdgeOpened, From: id, To: file},
			schema.Edge{Type: edgeIncarnation, From: app, To: id})
	}
	g.Sort()
	return g
}

// TestRestartGoldenEndToEnd runs restart's shipped golden through
// Normalize, prune included: the incarnation edges must survive the
// bracket cut, and the stray incarnation must not.
func TestRestartGoldenEndToEnd(t *testing.T) {
	for _, r := range []run{runA(), runB()} {
		got := normalized(t, restartTrace(r), r, "restart")
		if vs := Check(got, golden(t, "restart")); len(vs) > 0 {
			t.Fatalf("violations: %v", vs)
		}
		n := 0
		for _, e := range got.Graph.Edges {
			if e.Type == edgeIncarnation {
				n++
			}
		}
		if n != 2 {
			t.Fatalf("want 2 incarnation edges after pruning, got %d: %v", n, got.Graph.Edges)
		}
	}
}

// TestRestartTwoApps: incarnations under two different app nodes are
// not one app identity.
func TestRestartTwoApps(t *testing.T) {
	r := runA()
	raw := restartTrace(r)
	for i, e := range raw.Graph.Edges {
		if e.Type == edgeIncarnation && e.To == schema.ProcessNodeID(r.host, r.client.pid, r.client.inc) {
			raw.Graph.Edges[i].From = "app:demo/*Other*"
		}
	}
	raw.Graph.Nodes = append(raw.Graph.Nodes, schema.Node{Type: "app", ID: "app:demo/*Other*"})
	raw.Graph.Sort()
	vs := Check(normalized(t, raw, r, "restart"), golden(t, "restart"))
	if len(vs) != 1 || vs[0].Kind != ViolationEdge {
		t.Fatalf("want one edge violation, got %v", vs)
	}
}
