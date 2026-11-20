package federate

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"iormesh/internal/schema"
)

// Options tune a merge.
type Options struct {
	// Window is Fuse's candidate window; zero or less means
	// DefaultWindow.
	Window time.Duration
}

// Result is a merged mesh: one graph over every source, the fusion that
// joined it, and the conn-id renames that turn a host's events into the
// merged graph's connections.
type Result struct {
	Graph schema.Graph
	Fusion
	// Renames maps each host-local conn id the merge changed to its id in
	// Graph: both ends of a fused connection, and an unfused id two hosts
	// happened to share (requalified, so two connections never pose as
	// one). Ids not listed are unchanged.
	Renames map[LocalConn]string
}

// hostGraph is one source's graph with the host it describes, which
// scopes its socket node ids.
type hostGraph struct {
	host string
	g    schema.Graph
}

// Merge reads every source and merges them into one graph. Sources are
// read in Name order and every map is sorted before export, so the same
// traces always merge into the same bytes. The sources may belong to
// several projects: the join is the same across projects, so building
// project A with project B fuses A's calls into B into cross-project
// edges (PLAN.md M4 "Build").
func Merge(ctx context.Context, sources []Source, opt Options) (Result, error) {
	window := opt.Window
	if window <= 0 { // a negative window would admit every candidate
		window = DefaultWindow
	}
	sorted := slices.SortedFunc(slices.Values(sources), func(a, b Source) int {
		return cmp.Compare(a.Name(), b.Name())
	})
	col := newCollector()
	traces := make([]hostGraph, 0, len(sorted))
	for _, s := range sorted {
		hg, err := readSource(ctx, s, col)
		if err != nil {
			return Result{}, err
		}
		traces = append(traces, hg)
	}
	r := Result{Fusion: Fuse(col.halves(), window), Renames: map[LocalConn]string{}}
	fused := map[string]bool{}
	for _, c := range r.Conns {
		r.Renames[LocalConn{c.Client.Host, c.Client.ConnID}] = c.ID
		r.Renames[LocalConn{c.Server.Host, c.Server.ConnID}] = c.ID
		fused[c.ID] = true
	}
	requalify(traces, r.Renames)
	r.Graph = union(traces, r.Renames, fused)
	if err := r.Graph.Validate(); err != nil {
		return Result{}, fmt.Errorf("federate: merged graph: %w", err)
	}
	return r, nil
}

// readSource reads one source's graph and feeds its events to col. A
// source is one host session: events of two hosts in it would leave its
// socket ids unscoped, so that is an error, not a merge.
func readSource(ctx context.Context, s Source, col *collector) (hostGraph, error) {
	g, err := s.Graph(ctx)
	if err != nil {
		return hostGraph{}, err
	}
	host := ""
	err = s.Events(ctx, func(ev *schema.Event) error {
		switch {
		case host == "":
			host = ev.HostID
		case ev.HostID != host:
			return fmt.Errorf("federate: %s: events of two hosts (%s, %s)",
				s.Name(), host, ev.HostID)
		}
		col.add(ev)
		return nil
	})
	if err != nil {
		return hostGraph{}, err
	}
	if host == "" {
		if host, err = graphHost(s.Name(), g); err != nil {
			return hostGraph{}, err
		}
	}
	return hostGraph{host: host, g: g}, nil
}

// graphHost names the host of a source without events from its graph's
// one host node. Its socket ids still need a host to be scoped by, so a
// graph with sockets but no single host is rejected; an empty graph
// scopes nothing and needs none.
func graphHost(name string, g schema.Graph) (string, error) {
	var hosts []string
	sockets := false
	for _, n := range g.Nodes {
		switch n.Type {
		case schema.NodeHost:
			hosts = append(hosts, strings.TrimPrefix(n.ID, schema.HostNodeID("")))
		case schema.NodeSocket:
			sockets = true
		}
	}
	switch {
	case len(hosts) == 1:
		return hosts[0], nil
	case len(hosts) > 1:
		return "", fmt.Errorf("federate: %s: no events and %d host nodes", name, len(hosts))
	case sockets:
		return "", fmt.Errorf("federate: %s: no events and no host node to scope its sockets", name)
	}
	return "", nil
}

// requalify renames every unfused socket id that sources of two
// different hosts both use. Host-local ids hash the 4-tuple and a boot
// time, so a clash is unlikely — but two private networks reuse
// addresses, and one shared node would claim a connection nobody
// observed. Two sessions on one host share ids legitimately: they saw
// the same socket.
func requalify(traces []hostGraph, renames map[LocalConn]string) {
	hosts := map[string]map[string]bool{}
	for _, t := range traces {
		for _, id := range socketIDs(t.g) {
			if hosts[id] == nil {
				hosts[id] = map[string]bool{}
			}
			hosts[id][t.host] = true
		}
	}
	for id, hs := range hosts {
		if len(hs) < 2 {
			continue
		}
		for h := range hs {
			k := LocalConn{h, id}
			if _, done := renames[k]; !done {
				renames[k] = hashID("host|" + h + "|" + id)
			}
		}
	}
}

// socketIDs returns the conn ids of g's socket nodes.
func socketIDs(g schema.Graph) []string {
	var ids []string
	for _, n := range g.Nodes {
		if id, ok := strings.CutPrefix(n.ID, schema.SocketNodeID("")); ok && n.Type == schema.NodeSocket {
			ids = append(ids, id)
		}
	}
	return ids
}

// union merges the host graphs, renaming socket nodes and the connected
// edges into them. Process, file and host ids carry their host already,
// and app ids are meant to be shared across hosts, so only socket ids
// need scoping. A node seen twice keeps its first (Name-order) form; an
// edge seen twice keeps its first form unless the later one is fused.
func union(traces []hostGraph, renames map[LocalConn]string, fused map[string]bool) schema.Graph {
	nodes := map[string]schema.Node{}
	edges := map[[3]string]schema.Edge{}
	for _, t := range traces {
		rename := func(id string) string {
			conn, ok := strings.CutPrefix(id, schema.SocketNodeID(""))
			if to, found := renames[LocalConn{t.host, conn}]; ok && found {
				return schema.SocketNodeID(to)
			}
			return id
		}
		for _, n := range t.g.Nodes {
			if n.Type == schema.NodeSocket {
				n.ID = rename(n.ID)
			}
			if _, seen := nodes[n.ID]; !seen {
				nodes[n.ID] = n
			}
		}
		for _, e := range t.g.Edges {
			addEdge(edges, e, rename, fused)
		}
	}
	g := schema.Graph{SchemaVersion: schema.Version,
		Nodes: slices.Collect(maps.Values(nodes)), Edges: slices.Collect(maps.Values(edges))}
	g.Sort()
	return g
}

// addEdge adds one host edge to the union: a connected edge follows its
// socket's rename and becomes fused when the merge fused the socket.
func addEdge(edges map[[3]string]schema.Edge, e schema.Edge,
	rename func(string) string, fused map[string]bool) {
	if e.Type == schema.EdgeConnected {
		e.To = rename(e.To)
		conn, _ := strings.CutPrefix(e.To, schema.SocketNodeID(""))
		if fused[conn] {
			e.State = schema.StateFused
		}
	}
	k := [3]string{e.Type, e.From, e.To}
	if old, seen := edges[k]; seen && (old.State == schema.StateFused || e.State != schema.StateFused) {
		return
	}
	edges[k] = e
}
