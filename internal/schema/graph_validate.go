package schema

import (
	"reflect"
	"strings"
)

// edgeShape is what one edge type may connect, and whether it carries a
// state.
type edgeShape struct {
	from, to  string // node types
	wantState bool
}

// DecodeGraph parses a graph export strictly (exact-case keys, required
// keys present and non-null — nodes and edges are always arrays — no
// trailing data) and validates it.
func DecodeGraph(data []byte) (Graph, error) {
	var g Graph
	if err := checkKeys(data, reflect.TypeFor[Graph]()); err != nil {
		return Graph{}, err
	}
	if err := decodeStrict(data, &g); err != nil {
		return Graph{}, err
	}
	if err := g.Validate(); err != nil {
		return Graph{}, err
	}
	return g, nil
}

// Validate checks that the graph is canonical and closed: known types,
// ids that match their node type, sorted and duplicate-free nodes and
// edges, and every edge between existing nodes of the right types. A
// dangling edge would render as an arrow to nowhere.
func (g Graph) Validate() error {
	if g.SchemaVersion != Version {
		return invalidf("graph schema_version %d, want %d", g.SchemaVersion, Version)
	}
	types, err := g.validateNodes()
	if err != nil {
		return err
	}
	for i, e := range g.Edges {
		if i > 0 && compareEdges(g.Edges[i-1], e) >= 0 {
			return invalidf("edges not sorted or duplicated at %s %s→%s", e.Type, e.From, e.To)
		}
		if err := validateEdge(e, types); err != nil {
			return err
		}
	}
	return nil
}

// validateNodes checks every node and returns the node type per id, for
// the edge checks.
func (g Graph) validateNodes() (map[string]string, error) {
	types := make(map[string]string, len(g.Nodes))
	for i, n := range g.Nodes {
		if i > 0 && compareNodes(g.Nodes[i-1], n) >= 0 {
			return nil, invalidf("nodes not sorted or duplicated at %s %s", n.Type, n.ID)
		}
		if err := validateNodeID(n); err != nil {
			return nil, err
		}
		if _, dup := types[n.ID]; dup {
			return nil, invalidf("node id %q used twice", n.ID)
		}
		types[n.ID] = n.Type
	}
	return types, nil
}

// validateNodeID checks that a node's id carries its type's prefix and,
// for files and apps, the shape FileNodeID and AppNodeID build.
func validateNodeID(n Node) error {
	prefix, ok := nodeIDPrefix(n.Type)
	if !ok {
		return invalidf("unknown node type %q", n.Type)
	}
	rest, ok := strings.CutPrefix(n.ID, prefix)
	if !ok || rest == "" {
		return invalidf("%s node id %q lacks %q prefix", n.Type, n.ID, prefix)
	}
	switch n.Type {
	case NodeFile:
		host, path, ok := strings.Cut(rest, ":")
		if !ok || host == "" || path == "" {
			return invalidf("file node id %q, want file:<host>:<path>", n.ID)
		}
	case NodeApp:
		project, pattern, ok := strings.Cut(rest, "/")
		if !ok || project == "" || pattern == "" {
			return invalidf("app node id %q, want app:<project>/<pattern>", n.ID)
		}
	}
	return nil
}

// validateEdge checks one edge against its shape and the known nodes.
func validateEdge(e Edge, types map[string]string) error {
	shape, ok := edgeShapeOf(e.Type)
	if !ok {
		return invalidf("unknown edge type %q", e.Type)
	}
	if got := types[e.From]; got != shape.from {
		return invalidf("%s edge from %q: want a %s node", e.Type, e.From, shape.from)
	}
	if got := types[e.To]; got != shape.to {
		return invalidf("%s edge to %q: want a %s node", e.Type, e.To, shape.to)
	}
	switch {
	case shape.wantState && e.State != StateFused && e.State != StateUnresolved:
		return invalidf("%s edge state %q, want %q or %q",
			e.Type, e.State, StateFused, StateUnresolved)
	case !shape.wantState && e.State != "":
		return invalidf("%s edge carries state %q", e.Type, e.State)
	}
	return nil
}

// nodeIDPrefix maps a node type to the prefix its ids carry.
func nodeIDPrefix(nodeType string) (string, bool) {
	switch nodeType {
	case NodeHost:
		return prefixHost, true
	case NodeProcess:
		return prefixProcess, true
	case NodeFile:
		return prefixFile, true
	case NodeSocket:
		return prefixConn, true
	case NodeApp:
		return prefixApp, true
	}
	return "", false
}

// edgeShapeOf maps an edge type to the node types it connects.
func edgeShapeOf(edgeType string) (edgeShape, bool) {
	switch edgeType {
	case EdgeRunsOn:
		return edgeShape{from: NodeProcess, to: NodeHost}, true
	case EdgeOpened:
		return edgeShape{from: NodeProcess, to: NodeFile}, true
	case EdgeConnected:
		return edgeShape{from: NodeProcess, to: NodeSocket, wantState: true}, true
	case EdgeIncarnation:
		return edgeShape{from: NodeApp, to: NodeProcess}, true
	case EdgeForked, EdgeSignaled:
		return edgeShape{from: NodeProcess, to: NodeProcess}, true
	}
	return edgeShape{}, false
}
