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
	// ClockWindow is how much traffic one clock bound covers; zero or
	// less means DefaultClockWindow.
	ClockWindow time.Duration
	// DriftPPM is the assumed worst rate at which two hosts' clocks move
	// apart, in parts per million: each clock bound is widened by it over
	// its window (ClockBound.Drift). Zero or less means DefaultDriftPPM.
	DriftPPM float64
}

// Result is a merged mesh: one graph over every source, the fusion that
// joined it, and the conn-id renames that turn a host's events into the
// merged graph's connections.
type Result struct {
	Graph schema.Graph
	Fusion
	// Renames maps each host-local conn id the merge changed to its id in
	// Graph: both ends of a fused connection, an unfused id two hosts
	// happened to share (requalified, so two connections never pose as
	// one), and the further ids one socket got from further sessions of
	// its host (Half.Aliases, joined to its kept id). Ids not listed are
	// unchanged.
	Renames map[LocalConn]string
	// Clocks bounds each traced host pair's clock offset per window, from
	// the fused connections' send→receive edges (docs/milestones.md M4 "Clock
	// reconciliation"), sorted by (HostA, HostB, Start). A pair without a
	// fused connection has none: no traffic, no estimate. An empty bound
	// is kept and reported (ClockBound.Empty). NewTimeline orders
	// cross-host instants by them. The build's view model summarizes them
	// per host pair (internal/viewmodel Clock), and its summary prints
	// them.
	Clocks []ClockBound
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
// edges (docs/milestones.md M4 "Build").
//
// The clock bounds are computed after fusing, from the fused connections
// only, and never feed back into Fuse: a bound proves offsets only
// because its connections are facts, so letting it pick partners would
// argue in a circle. Fuse's window stays a coarse guard against ISN-clock
// wrap; hosts skewed beyond it fuse nothing and so get no bound either.
func Merge(ctx context.Context, sources []Source, opt Options) (Result, error) {
	window, clockWindow, drift := opt.resolve()
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
	r.Clocks = col.clocks(r.Conns, clockWindow, drift)
	requalify(traces, r.Renames)
	renameAliases(r.Fusion, r.Renames)
	r.Graph = union(traces, r.Renames, fused)
	if err := r.Graph.Validate(); err != nil {
		return Result{}, fmt.Errorf("federate: merged graph: %w", err)
	}
	return r, nil
}

// ClockBounds is the clock half of Merge for events alone, with no graph:
// it fuses the events' halves and bounds every host pair's clock offset
// from the fused connections, exactly as Merge's Result.Clocks. Like
// Halves, it lets a test split one host's real trace into pretend hosts
// and check the bounds the merge would draw (task xe4).
func ClockBounds(events []schema.Event, opt Options) []ClockBound {
	window, clockWindow, drift := opt.resolve()
	col := newCollector()
	for i := range events {
		col.add(&events[i])
	}
	return col.clocks(Fuse(col.halves(), window).Conns, clockWindow, drift)
}

// resolve returns the options with their defaults filled in: Fuse's
// window, the clock window and the drift allowance per bound side.
func (o Options) resolve() (window, clockWindow time.Duration, drift int64) {
	window = o.Window
	if window <= 0 { // a negative window would admit every candidate
		window = DefaultWindow
	}
	clockWindow = cmp.Or(max(o.ClockWindow, 0), DefaultClockWindow)
	drift = driftAllowance(clockWindow, cmp.Or(max(o.DriftPPM, 0), DefaultDriftPPM))
	return window, clockWindow, drift
}

// clocks bounds the clock offsets from the messages of the fused conns.
func (c *collector) clocks(conns []Conn, clockWindow time.Duration, drift int64) []ClockBound {
	var msgs []message
	for _, conn := range conns {
		msgs = append(msgs, c.messages(conn)...)
	}
	return clockBounds(msgs, clockWindow, drift)
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
// observed. Only ids shared across hosts count: two sessions on one host
// give one socket two ids, each hashing its own session's establish
// stamp, and Fuse folded those into one half whose aliases
// renameAliases joins afterwards.
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

// renameAliases sends every alias of a folded half (foldSessions) where
// its kept conn id goes: the fused id, the requalified one, or the kept
// id itself. It runs after requalify, so an alias follows the kept id's
// final name and never a requalified alias of its own: the sessions'
// socket nodes and events become one.
func renameAliases(f Fusion, renames map[LocalConn]string) {
	alias := func(h Half) {
		to := h.ConnID
		if r, ok := renames[LocalConn{h.Host, h.ConnID}]; ok {
			to = r
		}
		for _, a := range h.Aliases {
			renames[LocalConn{h.Host, a}] = to
		}
	}
	for _, c := range f.Conns {
		alias(c.Client)
		alias(c.Server)
	}
	for _, u := range f.Unresolved {
		alias(u.Half)
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
