package normalize

import (
	"maps"
	"slices"
	"strconv"
	"strings"

	"iormesh/internal/schema"
)

// graph rewrites node ids and edge endpoints with the symbol table the
// events filled, so "file:<host>:<tmp>/a.dat" becomes "file:$h1:$tmp/a.dat"
// and a process node names the same ordinals as its events. The input is
// sorted (on a copy) first: entities the bracketed events never touched get
// their ordinals in canonical graph order, not in the producer's order.
// Attrs are copied, not rewritten.
func (n normalizer) graph(g schema.Graph) schema.Graph {
	out := schema.Graph{
		SchemaVersion: g.SchemaVersion,
		Nodes:         slices.Clone(g.Nodes),
		Edges:         slices.Clone(g.Edges),
	}
	out.Sort()
	for i, nd := range out.Nodes {
		nd.ID = n.nodeID(nd.ID)
		nd.Attrs = maps.Clone(nd.Attrs)
		out.Nodes[i] = nd
	}
	for i, e := range out.Edges {
		e.From, e.To = n.nodeID(e.From), n.nodeID(e.To)
		e.Attrs = maps.Clone(e.Attrs)
		out.Edges[i] = e
	}
	out.Sort()
	return out
}

// nodeID rewrites one id by its prefix (see schema's *NodeID helpers). An
// id it cannot parse is returned unchanged: Validate rejects those before
// a graph is exported, and the check will then simply not match it.
func (n normalizer) nodeID(id string) string {
	switch {
	case strings.HasPrefix(id, "host:"):
		return "host:" + n.host(strings.TrimPrefix(id, "host:"))
	case strings.HasPrefix(id, "file:"):
		host, path, ok := strings.Cut(strings.TrimPrefix(id, "file:"), ":")
		if !ok {
			return id
		}
		return schema.FileNodeID(n.host(host), n.path(path))
	case strings.HasPrefix(id, "conn:"):
		return schema.SocketNodeID(n.conn(strings.TrimPrefix(id, "conn:")))
	case strings.HasPrefix(id, "proc:"):
		return n.procID(id)
	}
	return id
}

// procID rewrites "proc:<host>/<pid>@<inc>" with the same ordinals the
// process's events got.
func (n normalizer) procID(id string) string {
	rest := strings.TrimPrefix(id, "proc:")
	at := strings.LastIndexByte(rest, '@')
	slash := strings.LastIndexByte(rest[:max(at, 0)], '/')
	if at < 0 || slash < 0 {
		return id
	}
	pid, err1 := strconv.ParseUint(rest[slash+1:at], 10, 32)
	inc, err2 := strconv.ParseUint(rest[at+1:], 10, 64)
	if err1 != nil || err2 != nil {
		return id
	}
	host := rest[:slash]
	p, i := n.process(schema.ProcessNodeID(host, uint32(pid), inc), uint32(pid))
	return schema.ProcessNodeID(n.host(host), p, i)
}
