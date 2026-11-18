package normalize

import (
	"iormesh/internal/schema"
)

// prune keeps the part of the graph the bracketed events account for. The
// graph has no times, so without this a JVM startup file opened outside
// the bracket would still satisfy a declared edge or trip must_not_appear,
// and entities the scenario never touched would take run-dependent
// ordinals. Kept: processes with events in the bracket, their runs_on
// edges, opened edges whose file a bracketed event of that process opened
// (path or new_path), and connected edges whose conn_id a bracketed event
// of that process carries; then exactly the nodes those edges and
// processes name. The correlator must therefore set conn_id on the socket
// events of a connection (docs/implementation.md M1 step 6). An
// incarnation edge (app → process, M2 step 3) is kept when its process
// has events in the bracket: the app node never has events of its own, so
// testing From like the other edges would always drop it, and restart's
// golden could never pass. A forked edge (M2 step 6) is kept when both
// processes have events in the bracket: a child the cut left out must not
// bring its node back.
func prune(g schema.Graph, events []schema.Event) schema.Graph {
	seen := touched(events)
	keep := func(e schema.Edge) bool {
		switch e.Type {
		case schema.EdgeIncarnation:
			return seen[e.To]
		case schema.EdgeForked:
			return seen[e.From] && seen[e.To]
		}
		if !seen[e.From] {
			return false
		}
		switch e.Type {
		case schema.EdgeOpened, schema.EdgeConnected:
			return seen[e.From+"\x00"+e.To]
		}
		return true
	}
	out := schema.Graph{SchemaVersion: g.SchemaVersion}
	nodes := map[string]bool{}
	for _, e := range g.Edges {
		if keep(e) {
			out.Edges = append(out.Edges, e)
			nodes[e.From], nodes[e.To] = true, true
		}
	}
	for _, nd := range g.Nodes {
		if nodes[nd.ID] || (nd.Type == schema.NodeProcess && seen[nd.ID]) {
			out.Nodes = append(out.Nodes, nd)
		}
	}
	return out
}

// touched collects the process node ids of the bracketed events and,
// keyed by process id + "\x00" + node id, the files and connections each
// of them touched.
func touched(events []schema.Event) map[string]bool {
	seen := map[string]bool{}
	for _, e := range events {
		if e.PID == 0 {
			continue
		}
		p := processKey(e)
		seen[p] = true
		for _, path := range []*string{e.Args.Path, e.Args.NewPath} {
			if path != nil {
				seen[p+"\x00"+schema.FileNodeID(e.HostID, *path)] = true
			}
		}
		if e.ConnID != "" {
			seen[p+"\x00"+schema.SocketNodeID(e.ConnID)] = true
		}
	}
	return seen
}
