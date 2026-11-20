package federate

import (
	"cmp"
	"maps"
	"net/netip"
	"slices"
	"strings"

	"iormesh/internal/schema"
)

// Decision #3: the server keeps no graph — a segment holds events and
// tables only — so the controller rebuilds each host session's graph
// from its events. The rebuild applies the correlator's own rules
// (internal/correlate/graph.go) to what the events carry, and since task
// ze4 the events carry everything the correlator's graph rests on, so a
// single-host run's rebuilt graph equals its graph.json (the integration
// suite checks every run):
//
//   - host, process (comm: the newest the events show, an exec's new
//     name), runs_on — for every process an event names, including an
//     attach (a target that never made a traced call) and a fork's
//     parent;
//   - app and incarnation: an attach with args.pattern;
//   - forked: a fork with its parent, which is also where the child
//     inherits a copy of the parent's open fds;
//   - opened: every successful open, openat, openat2 and creat with a
//     path, and the new name of a rename that moved a file the process
//     held open — the open fds are followed through the events (open,
//     close, dup, dup2, dup3, fcntl F_DUPFD, F_DUPFD_CLOEXEC, F_SETFD and
//     F_GETFD, fork, the fd-creating socket, pipe and epoll calls,
//     rename, unlink, and an exec closing the close-on-exec ones);
//   - socket and connected for every event with a conn_id, fused when the
//     host's correlator fused the connection itself: a pipe or socketpair
//     (both ends are created together), a connection whose connect and
//     accept both happened on this host (a loopback pair), or one whose
//     events show both ends' mirrored addresses (a loopback UDP flow);
//   - signaled: a delivered signal event whose sender is a traced
//     process.
//
// What stays out of reach is what the correlator itself cannot see: the
// fds of an untraced close_range, a cwd-relative rename (both alike in
// either graph).

// Open-flag and fcntl constants the fd tracking reads (Linux, the same
// on amd64 and arm64).
const (
	oCloexec      = 0o2000000
	fdCloexec     = 1 // FD_CLOEXEC, F_SETFD's and F_GETFD's flag
	fDupFD        = 0
	fGetFD        = 1
	fSetFD        = 2
	fDupFDCloexec = 1030
	eBADF         = 9
)

// fdKey names one process incarnation's fd number.
type fdKey struct {
	pid uint32
	inc uint64
	fd  int32
}

// connEnds is what a host saw of one conn_id: whether it was created as
// a pipe or socketpair, whether a connect and an accept named it, and
// the (local, remote) address pairs its events carried.
type connEnds struct {
	pair, connect, accept bool
	tuples                map[[2]netip.AddrPort]bool
}

// graphRebuilder accumulates one host session's graph from its events,
// fed in seq order.
type graphRebuilder struct {
	nodes map[string]schema.Node
	edges map[[3]string]schema.Edge
	// paths are the open fds' file names, for renames.
	paths map[fdKey]string
	// cloexec is each fd's known close-on-exec state, which decides what
	// an exec closes (absent: unknown, as in the correlator).
	cloexec map[fdKey]bool
	conns   map[string]*connEnds
	// incs is each pid's newest incarnation: a signal names its sender by
	// pid only.
	incs map[uint32]uint64
}

func newGraphRebuilder() *graphRebuilder {
	return &graphRebuilder{nodes: map[string]schema.Node{}, edges: map[[3]string]schema.Edge{},
		paths: map[fdKey]string{}, cloexec: map[fdKey]bool{}, conns: map[string]*connEnds{}, incs: map[uint32]uint64{}}
}

// RebuildGraph rebuilds one host session's graph from its events, in seq
// order, by the correlator's rules (decision #3): what the build does
// for a session's segments, for a caller holding the events already —
// the integration suite compares it with the run's graph.json.
func RebuildGraph(events []schema.Event) schema.Graph {
	g := newGraphRebuilder()
	for i := range events {
		g.add(&events[i])
	}
	return g.graph()
}

// add folds one event in; it keeps no reference to ev.
func (g *graphRebuilder) add(ev *schema.Event) {
	host := schema.HostNodeID(ev.HostID)
	g.nodes[host] = schema.Node{Type: schema.NodeHost, ID: host}
	if ev.PID == 0 { // host-level records: session, drop, end
		return
	}
	proc := g.process(ev)
	g.file(proc, ev)
	switch {
	case ev.ConnID != "":
		g.connected(proc, ev)
	case ev.Event == schema.KindSignal:
		g.signaled(proc, ev)
	case ev.Event == schema.KindFork:
		g.forked(proc, ev)
	case ev.Event == schema.KindAttach && ev.Args.Pattern != nil:
		app := schema.AppNodeID(ev.Project, *ev.Args.Pattern)
		g.nodes[app] = schema.Node{Type: schema.NodeApp, ID: app}
		g.edge(schema.Edge{Type: schema.EdgeIncarnation, From: app, To: proc})
	}
}

// process adds the event's process node and runs_on edge and returns
// its id. The comm is overwritten by every later event: the node is
// named after the newest name, as the correlator's is.
func (g *graphRebuilder) process(ev *schema.Event) string {
	id := g.processNode(ev.HostID, ev.PID, ev.PIDInc)
	n := g.nodes[id]
	if ev.Comm != "" && n.Attrs["comm"] != ev.Comm {
		n.Attrs = map[string]string{"comm": ev.Comm}
	}
	g.nodes[id] = n
	g.incs[ev.PID] = ev.PIDInc
	return id
}

// processNode adds a process node (without a comm if it is new) and its
// runs_on edge, and returns its id.
func (g *graphRebuilder) processNode(hostID string, pid uint32, inc uint64) string {
	id := schema.ProcessNodeID(hostID, pid, inc)
	if _, ok := g.nodes[id]; !ok {
		g.nodes[id] = schema.Node{Type: schema.NodeProcess, ID: id}
	}
	g.edge(schema.Edge{Type: schema.EdgeRunsOn, From: id, To: schema.HostNodeID(hostID)})
	return id
}

// connected adds the socket node and the process's connected edge; the
// edge's state is settled in graph, once every end is known.
func (g *graphRebuilder) connected(proc string, ev *schema.Event) {
	sock := schema.SocketNodeID(ev.ConnID)
	g.nodes[sock] = schema.Node{Type: schema.NodeSocket, ID: sock}
	g.edge(schema.Edge{Type: schema.EdgeConnected, From: proc, To: sock})
	c := g.conns[ev.ConnID]
	if c == nil {
		c = &connEnds{tuples: map[[2]netip.AddrPort]bool{}}
		g.conns[ev.ConnID] = c
	}
	switch ev.Event {
	case schema.KindPipe, schema.KindPipe2, schema.KindSocketpair:
		c.pair = true
	case schema.KindConnect:
		c.connect = true
	case schema.KindAccept, schema.KindAccept4:
		c.accept = true
	}
	if t, ok := eventTuple(&ev.Args); ok {
		c.tuples[t] = true
	}
}

// fused reports whether the host's correlator fused c: created as a pair,
// both a connect and an accept, or both ends' mirrored address pairs.
func (c *connEnds) fused() bool {
	if c.pair || (c.connect && c.accept) {
		return true
	}
	for t := range c.tuples {
		if c.tuples[[2]netip.AddrPort{t[1], t[0]}] {
			return true
		}
	}
	return false
}

// eventTuple is an event's (local, remote) address pair, when it has
// both ports and a remote address. A missing local address is the
// wildcard; towards a loopback peer it is keyed as the peer's address,
// the address the route sends from (the correlator's flowLocal), so the
// two sides of a loopback flow mirror each other.
func eventTuple(a *schema.Args) ([2]netip.AddrPort, bool) {
	if a.LocalPort == nil || a.RemoteAddr == nil || a.RemotePort == nil {
		return [2]netip.AddrPort{}, false
	}
	remote, err := netip.ParseAddr(*a.RemoteAddr)
	if err != nil {
		return [2]netip.AddrPort{}, false
	}
	local := remote
	if a.LocalAddr != nil {
		if local, err = netip.ParseAddr(*a.LocalAddr); err != nil {
			return [2]netip.AddrPort{}, false
		}
	} else if !remote.IsLoopback() {
		return [2]netip.AddrPort{}, false
	}
	return [2]netip.AddrPort{netip.AddrPortFrom(local, *a.LocalPort),
		netip.AddrPortFrom(remote, *a.RemotePort)}, true
}

// signaled adds the edge from a traced sender to the process a signal
// was delivered to. A sender the trace never saw has no node.
func (g *graphRebuilder) signaled(proc string, ev *schema.Event) {
	s := ev.Args.SenderPID
	if s == nil || *s == ev.PID {
		return
	}
	inc, traced := g.incs[*s]
	if !traced {
		return
	}
	g.edge(schema.Edge{Type: schema.EdgeSignaled,
		From: schema.ProcessNodeID(ev.HostID, *s, inc), To: proc})
}

// forked starts a fork's child with a copy of its parent's open fds and
// adds the forked edge (correlate's forked and inherit). Whatever the
// child's key held belongs to a predecessor, as in the correlator; a
// fork whose parent the record could not name inherits nothing.
func (g *graphRebuilder) forked(proc string, ev *schema.Event) {
	g.dropFDs(ev.PID, ev.PIDInc, func(fdKey) bool { return true })
	a := ev.Args
	if a.ParentPID == nil || a.ParentInc == nil {
		return
	}
	parent := g.processNode(ev.HostID, *a.ParentPID, *a.ParentInc)
	if _, seen := g.incs[*a.ParentPID]; !seen {
		g.incs[*a.ParentPID] = *a.ParentInc
	}
	g.edge(schema.Edge{Type: schema.EdgeForked, From: parent, To: proc})
	// Keys added while ranging are the child's, which the filter skips
	// (a fork names no parent of the child's own pid).
	for k, ce := range g.cloexec {
		if k.pid == *a.ParentPID && k.inc == *a.ParentInc {
			g.cloexec[fdKey{ev.PID, ev.PIDInc, k.fd}] = ce
		}
	}
	for k, p := range g.paths {
		if k.pid == *a.ParentPID && k.inc == *a.ParentInc {
			g.paths[fdKey{ev.PID, ev.PIDInc, k.fd}] = p
		}
	}
}

// dropFDs forgets the fds of one process incarnation that drop selects.
func (g *graphRebuilder) dropFDs(pid uint32, inc uint64, drop func(fdKey) bool) {
	mine := func(k fdKey) bool { return k.pid == pid && k.inc == inc && drop(k) }
	maps.DeleteFunc(g.paths, func(k fdKey, _ string) bool { return mine(k) })
	maps.DeleteFunc(g.cloexec, func(k fdKey, _ bool) bool { return mine(k) })
}

func (g *graphRebuilder) edge(e schema.Edge) {
	g.edges[[3]string{e.Type, e.From, e.To}] = e
}

// graph returns the sorted graph, the connected edges' states settled.
func (g *graphRebuilder) graph() schema.Graph {
	edges := make([]schema.Edge, 0, len(g.edges))
	for _, e := range g.edges {
		if e.Type == schema.EdgeConnected {
			e.State = schema.StateUnresolved
			conn, _ := strings.CutPrefix(e.To, schema.SocketNodeID(""))
			if c := g.conns[conn]; c != nil && c.fused() {
				e.State = schema.StateFused
			}
		}
		edges = append(edges, e)
	}
	gr := schema.Graph{SchemaVersion: schema.Version,
		Nodes: slices.Collect(maps.Values(g.nodes)), Edges: edges}
	gr.Sort()
	return gr
}

// compareFD orders fd keys, so renames visit fds in a fixed order (the
// result does not depend on it; the order only keeps runs identical).
func compareFD(a, b fdKey) int {
	return cmp.Or(cmp.Compare(a.pid, b.pid), cmp.Compare(a.inc, b.inc), cmp.Compare(a.fd, b.fd))
}
