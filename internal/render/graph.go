package render

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	"iormesh/internal/schema"
)

// Graph layout: one column per host (host ids sorted), its host node on
// top, then its processes, then its files; apps, which span hosts, in a
// column on the left; connections, which join hosts, on the right.
// Within a column nodes keep the graph's (Type, ID) order.
const (
	colWidth   = 300
	boxWidth   = 260
	rowHeight  = 26
	boxHeight  = 20
	topMargin  = 40
	leftMargin = 20
	// maxColumnNodes caps one column: a host with thousands of files
	// would make an unreadable, huge picture. The rest is counted in a
	// "+N more" box; edges to them are not drawn.
	maxColumnNodes = 200
)

// graphStyle styles graph.svg: unresolved connections dashed, fused ones
// solid (PLAN.md: an unresolved edge is never drawn as a fact).
const graphStyle = `rect{fill:#fff;stroke:#333}rect.host{fill:#e8eef8}rect.app{fill:#eef8e8}` +
	`rect.socket{fill:#f8f0e8}rect.more{fill:#f4f4f4;stroke-dasharray:3 3}` +
	`line{stroke:#888}line.unresolved{stroke:#c33;stroke-dasharray:6 4}` +
	`line.fused{stroke:#36c;stroke-width:1.5}text.title{font-weight:bold}`

// column is one column of the layout.
type column struct {
	title string
	nodes []schema.Node
	more  int
}

// pos is a drawn node's box.
type pos struct{ x, y int }

// Graph renders a normalized graph (schema.Graph.Sort order) as SVG,
// hosts and processes labeled by names (NamesOf the build's model; the
// zero Names draws the graph's own ids).
func Graph(g schema.Graph, names Names) []byte {
	cols := graphColumns(g, names)
	at := map[string]pos{}
	height := topMargin
	for i, c := range cols {
		for j, n := range c.nodes {
			at[n.ID] = pos{leftMargin + i*colWidth, topMargin + j*rowHeight}
		}
		height = max(height, topMargin+(len(c.nodes)+1)*rowHeight)
	}
	var s svg
	s.open(leftMargin*2+max(len(cols), 1)*colWidth, height+rowHeight, graphStyle)
	drawEdges(&s, g.Edges, at)
	for i, c := range cols {
		x := leftMargin + i*colWidth
		s.text("title", x, topMargin-14, "start", short(c.title))
		for _, n := range c.nodes {
			p := at[n.ID]
			s.rect(n.Type, p.x, p.y, boxWidth, boxHeight)
			s.text("label", p.x+6, p.y+14, "start", short(nodeLabel(n, names)))
		}
		if c.more > 0 {
			y := topMargin + len(c.nodes)*rowHeight
			s.rect("more", x, y, boxWidth, boxHeight)
			s.text("label", x+6, y+14, "start", "+"+strconv.Itoa(c.more)+" more")
		}
	}
	return s.close()
}

// graphColumns sorts the nodes into columns: apps, each host's, sockets.
// Host columns are ordered by host name, then id: a host id is a random
// machine id, so named hosts keep their order from run to run.
func graphColumns(g schema.Graph, names Names) []column {
	apps := column{title: "apps"}
	socks := column{title: "connections"}
	hosts := map[string]*column{}
	var order []string
	for _, n := range g.Nodes {
		switch n.Type {
		case schema.NodeApp:
			apps.nodes = append(apps.nodes, n)
		case schema.NodeSocket:
			socks.nodes = append(socks.nodes, n)
		default:
			h := nodeHost(n)
			if hosts[h] == nil {
				hosts[h] = &column{title: "host " + names.host(h)}
				order = append(order, h)
			}
			hosts[h].nodes = append(hosts[h].nodes, n)
		}
	}
	slices.SortFunc(order, func(a, b string) int {
		return cmp.Or(cmp.Compare(names.host(a), names.host(b)), cmp.Compare(a, b))
	})
	var cols []column
	if len(apps.nodes) > 0 {
		cols = append(cols, apps)
	}
	for _, h := range order {
		c := hosts[h]
		slices.SortStableFunc(c.nodes, func(a, b schema.Node) int { return typeRank(a) - typeRank(b) })
		cols = append(cols, *c)
	}
	if len(socks.nodes) > 0 {
		cols = append(cols, socks)
	}
	for i := range cols {
		if n := len(cols[i].nodes); n > maxColumnNodes {
			cols[i].more, cols[i].nodes = n-maxColumnNodes, cols[i].nodes[:maxColumnNodes]
		}
	}
	return cols
}

// drawEdges draws the edges between drawn nodes, in the graph's order:
// from the right side of the left box to the left side of the right one.
func drawEdges(s *svg, edges []schema.Edge, at map[string]pos) {
	for _, e := range edges {
		a, okA := at[e.From]
		b, okB := at[e.To]
		if !okA || !okB {
			continue
		}
		class := e.Type
		if e.State != "" {
			class = e.State
		}
		x1, x2 := a.x+boxWidth, b.x
		switch {
		case a.x == b.x: // same column: along the left edge
			x1, x2 = a.x, b.x
		case a.x > b.x:
			x1, x2 = a.x, b.x+boxWidth
		}
		s.line(class, x1, a.y+boxHeight/2, x2, b.y+boxHeight/2, true)
	}
}

// nodeHost is the host id a host, process or file node belongs to.
func nodeHost(n schema.Node) string {
	id := n.ID
	switch n.Type {
	case schema.NodeHost:
		return strings.TrimPrefix(id, schema.HostNodeID(""))
	case schema.NodeProcess:
		h, _, _ := strings.Cut(strings.TrimPrefix(id, "proc:"), "/")
		return h
	case schema.NodeFile:
		h, _, _ := strings.Cut(strings.TrimPrefix(id, "file:"), ":")
		return h
	}
	return "?"
}

// typeRank orders a host's column: host, processes, files, the rest.
func typeRank(n schema.Node) int {
	switch n.Type {
	case schema.NodeHost:
		return 0
	case schema.NodeProcess:
		return 1
	case schema.NodeFile:
		return 2
	}
	return 3
}

// nodeLabel is what a box says: a process's service (when names has
// one), comm and pid, a file's path, an app's project/pattern, a
// connection's id, a host's name and id.
func nodeLabel(n schema.Node, names Names) string {
	switch n.Type {
	case schema.NodeProcess:
		_, pid, _ := strings.Cut(strings.TrimPrefix(n.ID, "proc:"), "/")
		return names.process(n.ID, strings.TrimSpace(n.Attrs["comm"]+" "+pid))
	case schema.NodeHost:
		return names.hostLabel(strings.TrimPrefix(n.ID, schema.HostNodeID("")))
	case schema.NodeFile:
		_, path, _ := strings.Cut(strings.TrimPrefix(n.ID, "file:"), ":")
		return path
	}
	_, rest, _ := strings.Cut(n.ID, ":")
	return rest
}
