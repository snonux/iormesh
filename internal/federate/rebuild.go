package federate

import (
	"cmp"
	"maps"
	"slices"
	"strings"

	"iormesh/internal/schema"
)

// Decision #3: the server keeps no graph — a segment holds events and
// tables only — so the controller rebuilds each host session's graph
// from its events. The rebuild applies the correlator's own rules
// (internal/correlate/graph.go) to what the events carry, which is most
// of what the correlator knew, not all of it:
//
//   - host, process (comm: the newest the events show, an exec's new
//     name), runs_on;
//   - opened: every successful open, openat, openat2 and creat with a
//     path, and the new name of a rename that moved a file the process
//     held open — the open fds are followed through the events (open,
//     close, dup, dup2, dup3, fcntl F_DUPFD, rename, unlink, and an
//     exec closing the close-on-exec ones);
//   - socket and connected for every event with a conn_id, fused when the
//     host's correlator fused the connection itself: a pipe or socketpair
//     (both ends are created together), or a connection whose connect and
//     accept both happened on this host (a loopback pair);
//   - signaled: a delivered signal event whose sender is a traced
//     process.
//
// Not rebuilt, because no event says it: app nodes and incarnation edges
// (the session's pattern is not in the trace), forked edges (no event
// records a clone), the open fds a child inherited (so a rename moves
// only the files a process opened or copied itself), and close-on-exec
// changes by fcntl F_SETFD (the record has no argument). A graph.json written by
// `iormesh run -out` has them; a build from segments does not, and says
// so (docs/implementation.md M4 "Build").

// Open-flag and fcntl constants the fd tracking reads (Linux, the same
// on amd64 and arm64).
const (
	oCloexec      = 0o2000000
	fDupFD        = 0
	fDupFDCloexec = 1030
)

// fdKey names one process incarnation's fd number.
type fdKey struct {
	pid uint32
	inc uint64
	fd  int32
}

// connEnds is what a host saw of one conn_id: whether it was created as
// a pipe or socketpair, and whether a connect and an accept named it.
type connEnds struct{ pair, connect, accept bool }

// graphRebuilder accumulates one host session's graph from its events,
// fed in seq order.
type graphRebuilder struct {
	nodes map[string]schema.Node
	edges map[[3]string]schema.Edge
	// paths are the open fds' file names, for renames.
	paths map[fdKey]string
	// cloexec says which fds an exec closes.
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

// add folds one event in; it keeps no reference to ev.
func (g *graphRebuilder) add(ev *schema.Event) {
	host := schema.HostNodeID(ev.HostID)
	g.nodes[host] = schema.Node{Type: schema.NodeHost, ID: host}
	if ev.PID == 0 { // host-level records: session, drop, end
		return
	}
	proc := g.process(ev)
	switch {
	case ev.ConnID != "":
		g.connected(proc, ev)
	case ev.Event == schema.KindSignal:
		g.signaled(proc, ev)
	default:
		g.file(proc, ev)
	}
}

// process adds the event's process node and runs_on edge and returns
// its id. The comm is overwritten by every later event: the node is
// named after the newest name, as the correlator's is.
func (g *graphRebuilder) process(ev *schema.Event) string {
	id := schema.ProcessNodeID(ev.HostID, ev.PID, ev.PIDInc)
	n, ok := g.nodes[id]
	if !ok {
		n = schema.Node{Type: schema.NodeProcess, ID: id}
	}
	if ev.Comm != "" && n.Attrs["comm"] != ev.Comm {
		n.Attrs = map[string]string{"comm": ev.Comm}
	}
	g.nodes[id] = n
	g.incs[ev.PID] = ev.PIDInc
	g.edge(schema.Edge{Type: schema.EdgeRunsOn, From: id, To: schema.HostNodeID(ev.HostID)})
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
		c = &connEnds{}
		g.conns[ev.ConnID] = c
	}
	switch ev.Event {
	case schema.KindPipe, schema.KindSocketpair:
		c.pair = true
	case schema.KindConnect:
		c.connect = true
	case schema.KindAccept, schema.KindAccept4:
		c.accept = true
	}
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

// file follows the open fds and adds the opened edges.
func (g *graphRebuilder) file(proc string, ev *schema.Event) {
	k := fdKey{ev.PID, ev.PIDInc, ev.FD}
	switch ev.Event {
	case schema.KindOpen, schema.KindOpenat, schema.KindOpenat2, schema.KindCreat:
		if ev.Ret >= 0 && ev.Args.Path != nil {
			g.opened(proc, ev.HostID, *ev.Args.Path)
			g.paths[k] = *ev.Args.Path
			g.cloexec[k] = ev.Args.Flags != nil && *ev.Args.Flags&oCloexec != 0
		}
	case schema.KindDup, schema.KindDup2, schema.KindDup3:
		g.dup(k, ev, ev.Event == schema.KindDup3 && ev.Args.Flags != nil && *ev.Args.Flags&oCloexec != 0)
	case schema.KindFcntl:
		if c := ev.Args.Cmd; c != nil && (*c == fDupFD || *c == fDupFDCloexec) {
			g.dup(k, ev, *c == fDupFDCloexec)
		}
	case schema.KindExec:
		g.exec(ev.PID, ev.PIDInc)
	case schema.KindClose:
		delete(g.paths, k)
		delete(g.cloexec, k)
	case schema.KindUnlink, schema.KindUnlinkat, schema.KindRmdir:
		if ev.Ret == 0 && ev.Args.Path != nil {
			g.unlinked(trimSlash(*ev.Args.Path))
		}
	case schema.KindRename, schema.KindRenameat, schema.KindRenameat2:
		if ev.Ret == 0 && ev.Args.Path != nil && ev.Args.NewPath != nil {
			g.renamed(ev.HostID, trimSlash(*ev.Args.Path), trimSlash(*ev.Args.NewPath))
		}
	}
}

// dup makes the returned fd a copy of the event's fd (docs/schema.md
// "fd lifecycle"): same file, its own close-on-exec state. Whatever the
// new number held before is replaced.
func (g *graphRebuilder) dup(old fdKey, ev *schema.Event, cloexec bool) {
	if ev.Ret < 0 {
		return
	}
	nk := fdKey{old.pid, old.inc, int32(ev.Ret)}
	delete(g.paths, nk)
	g.cloexec[nk] = cloexec
	if p, ok := g.paths[old]; ok {
		g.paths[nk] = p
	}
}

// exec closes the process's close-on-exec fds: a rename after the exec
// no longer moves them.
func (g *graphRebuilder) exec(pid uint32, inc uint64) {
	for k, ce := range g.cloexec {
		if ce && k.pid == pid && k.inc == inc {
			delete(g.paths, k)
			delete(g.cloexec, k)
		}
	}
}

func (g *graphRebuilder) opened(proc, hostID, path string) {
	file := schema.FileNodeID(hostID, path)
	g.nodes[file] = schema.Node{Type: schema.NodeFile, ID: file}
	g.edge(schema.Edge{Type: schema.EdgeOpened, From: proc, To: file})
}

// unlinked forgets the fds of a removed name: a later rename onto the
// same name must not move them.
func (g *graphRebuilder) unlinked(name string) {
	if !strings.HasPrefix(name, "/") {
		return
	}
	maps.DeleteFunc(g.paths, func(_ fdKey, p string) bool { return p == name })
}

// renamed moves every open fd at or below old to new, and gives its
// process an opened edge to the new name (correlate's renamed). Relative
// names cannot be matched against the absolute paths fds keep.
func (g *graphRebuilder) renamed(hostID, old, new string) {
	if old == new || !strings.HasPrefix(old, "/") || !strings.HasPrefix(new, "/") {
		return
	}
	g.unlinked(new)
	for _, k := range slices.SortedFunc(maps.Keys(g.paths), compareFD) {
		p := g.paths[k]
		if !below(p, old) {
			continue
		}
		moved := new + p[len(old):]
		g.paths[k] = moved
		g.opened(schema.ProcessNodeID(hostID, k.pid, k.inc), hostID, moved)
	}
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
			if c := g.conns[conn]; c != nil && (c.pair || (c.connect && c.accept)) {
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

// below: path is name or lies under it (correlate's below).
func below(path, name string) bool {
	return path == name ||
		(len(path) > len(name) && strings.HasPrefix(path, name) && path[len(name)] == '/')
}

// trimSlash drops a trailing slash ("dir/" names dir) except of "/".
func trimSlash(path string) string {
	if len(path) > 1 {
		return strings.TrimSuffix(path, "/")
	}
	return path
}
