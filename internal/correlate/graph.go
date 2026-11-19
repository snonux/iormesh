package correlate

import (
	"maps"
	"slices"

	"iormesh/internal/schema"
)

// graphBuilder accumulates the per-host graph as events are emitted.
// Nodes and edges are keyed by id so repeats collapse; build sorts them.
// Attributes stay minimal on purpose: raw pids or ports in attrs would
// survive normalization and make two runs' graphs differ.
type graphBuilder struct {
	hostID string
	nodes  map[string]schema.Node
	edges  map[[3]string]schema.Edge
}

func newGraphBuilder(hostID string) *graphBuilder {
	g := &graphBuilder{hostID: hostID, nodes: map[string]schema.Node{},
		edges: map[[3]string]schema.Edge{}}
	host := schema.HostNodeID(hostID)
	g.nodes[host] = schema.Node{Type: schema.NodeHost, ID: host}
	return g
}

// process adds a process node and its runs_on edge.
func (g *graphBuilder) process(pid uint32, inc uint64, comm string) {
	id := schema.ProcessNodeID(g.hostID, pid, inc)
	node := schema.Node{Type: schema.NodeProcess, ID: id}
	if comm != "" {
		node.Attrs = map[string]string{"comm": comm}
	}
	g.nodes[id] = node
	g.edge(schema.Edge{Type: schema.EdgeRunsOn, From: id, To: schema.HostNodeID(g.hostID)})
}

// renamed sets a process node's comm attr to the name the process got
// from an exec. The node is the process incarnation, which an exec does
// not change, so there is one node and one comm: the newest, which is
// the program the process runs for the rest of its life (a forked child
// that execs is known by its new program, not by its parent's name) and
// what ps shows. The events keep each moment's comm, so the old name is
// not lost. The node exists: proc() added it.
func (g *graphBuilder) renamed(pid uint32, inc uint64, comm string) {
	id := schema.ProcessNodeID(g.hostID, pid, inc)
	node := g.nodes[id]
	node.Attrs = map[string]string{"comm": comm}
	g.nodes[id] = node
}

// incarnation adds the app node and its incarnation edge to one process
// (M2 step 3). The process node itself comes from process().
func (g *graphBuilder) incarnation(app string, pid uint32, inc uint64) {
	g.nodes[app] = schema.Node{Type: schema.NodeApp, ID: app}
	g.edge(schema.Edge{Type: schema.EdgeIncarnation, From: app,
		To: schema.ProcessNodeID(g.hostID, pid, inc)})
}

// forked adds the edge from a parent process to the child it forked (M2
// step 6). Both process nodes exist: newTask registered both.
func (g *graphBuilder) forked(parent uint32, parentInc uint64, child uint32, childInc uint64) {
	g.edge(schema.Edge{Type: schema.EdgeForked,
		From: schema.ProcessNodeID(g.hostID, parent, parentInc),
		To:   schema.ProcessNodeID(g.hostID, child, childInc)})
}

// signaled adds the edge from a traced sender to the traced process it
// signalled (M2 step 7). Both process nodes exist: proc() registered both.
func (g *graphBuilder) signaled(sender uint32, senderInc uint64, target uint32, targetInc uint64) {
	g.edge(schema.Edge{Type: schema.EdgeSignaled,
		From: schema.ProcessNodeID(g.hostID, sender, senderInc),
		To:   schema.ProcessNodeID(g.hostID, target, targetInc)})
}

// opened adds a file node and the process's opened edge. The file id uses
// the path exactly as the open event reports it, so the normalizer's
// bracket pruning finds the node from the event (internal/normalize).
func (g *graphBuilder) opened(pid uint32, inc uint64, path string) {
	file := schema.FileNodeID(g.hostID, path)
	g.nodes[file] = schema.Node{Type: schema.NodeFile, ID: file}
	g.edge(schema.Edge{Type: schema.EdgeOpened,
		From: schema.ProcessNodeID(g.hostID, pid, inc), To: file})
}

// connected adds a socket node for connID and the process's connected
// edge, fused when both ends were traced on this host.
func (g *graphBuilder) connected(pid uint32, inc uint64, connID string, fused bool) {
	sock := schema.SocketNodeID(connID)
	g.nodes[sock] = schema.Node{Type: schema.NodeSocket, ID: sock}
	state := schema.StateUnresolved
	if fused {
		state = schema.StateFused
	}
	g.edge(schema.Edge{Type: schema.EdgeConnected,
		From: schema.ProcessNodeID(g.hostID, pid, inc), To: sock, State: state})
}

func (g *graphBuilder) edge(e schema.Edge) {
	g.edges[[3]string{e.Type, e.From, e.To}] = e
}

func (g *graphBuilder) build() schema.Graph {
	gr := schema.Graph{SchemaVersion: schema.Version,
		Nodes: slices.Collect(maps.Values(g.nodes)),
		Edges: slices.Collect(maps.Values(g.edges))}
	gr.Sort()
	return gr
}
