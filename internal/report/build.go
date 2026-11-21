package report

import (
	"cmp"
	"maps"
	"net/netip"
	"slices"
	"strconv"

	"iormesh/internal/federate"
	"iormesh/internal/servicedb"
)

// Input is what Build needs besides the collector: the merge's conn-id
// renames (so connections speak the merged trace's ids, as query does)
// and the service database's endpoints (so a server address reads as
// its service).
type Input struct {
	Renames   map[federate.LocalConn]string
	Endpoints []servicedb.Endpoint
}

// end is one connection end, indexed by merged conn id to find peers.
type end struct {
	acc   *connAcc
	label string // "project/host/comm[pid]"
	role  string
}

// builder is one Build's lookups.
type builder struct {
	c       *Collector
	in      Input
	ends    map[string][]end // merged conn id → its ends in the rollup
	labels  map[string]string
	servers map[string]string // merged conn id → server address
}

// Build turns the collected state into the Report: processes sorted by
// (project, host, pid, incarnation), each list in first-seen order and
// cut at its cap.
func Build(c *Collector, in Input) Report {
	b := builder{c: c, in: in, ends: map[string][]end{}, labels: endpointLabels(in.Endpoints),
		servers: map[string]string{}}
	keys := slices.SortedFunc(maps.Keys(c.procs), func(x, y procKey) int {
		px, py := c.procs[x].p, c.procs[y].p
		return cmp.Or(cmp.Compare(px.Project, py.Project), cmp.Compare(px.Host, py.Host),
			cmp.Compare(px.PID, py.PID), cmp.Compare(px.Incarnation, py.Incarnation),
			cmp.Compare(x.host, y.host))
	})
	for _, k := range keys {
		b.index(k)
	}
	r := Report{Untracked: c.untracked}
	for i, k := range keys {
		if i == MaxProcesses {
			r.MoreProcesses = len(keys) - i
			break
		}
		r.Processes = append(r.Processes, b.process(k))
	}
	return r
}

// index records every connection end of process k under its merged id,
// and the server address each connection was seen at: the client's
// remote address when its client was traced (what the client app talks
// to, as the service database decides too), else the server's own.
func (b *builder) index(k procKey) {
	p := b.c.procs[k]
	for _, a := range p.conns {
		id := b.merged(p.hostID, a.local)
		b.ends[id] = append(b.ends[id], end{acc: a, label: procLabel(p), role: a.role})
		switch {
		case a.role == RoleClient && a.addrR != "":
			b.servers[id] = a.addrR
		case a.role == RoleServer && a.addrL != "" && b.servers[id] == "":
			b.servers[id] = a.addrL
		}
	}
}

// merged is a host-local conn id's id in the merged trace.
func (b *builder) merged(hostID, local string) string {
	if id, ok := b.in.Renames[federate.LocalConn{Host: hostID, ConnID: local}]; ok {
		return id
	}
	return local
}

// process renders one process: its lists cut at their caps.
func (b *builder) process(k procKey) Process {
	p := b.c.procs[k]
	out := p.p
	out.Argv = nonNil(out.Argv)
	out.Files, out.MoreFiles = topFiles(p.files)
	serves := map[string]bool{}
	for i, a := range p.conns {
		c := b.conn(p, a)
		if c.Role == RoleServer && c.Service != "" {
			serves[c.Service] = true
		}
		if i < MaxConns {
			out.Conns = append(out.Conns, c)
		} else {
			out.MoreConns++
		}
	}
	out.Serves = slices.Sorted(maps.Keys(serves))
	out.Threads, out.MoreThreads = b.threads(p)
	out.Methods, out.MoreMethods = topMethods(p.methods)
	out.Files, out.Conns, out.Serves = nonNil(out.Files), nonNil(out.Conns), nonNil(out.Serves)
	return out
}

// topFiles lists a process's files, cut at MaxFiles. Within the cap the
// list is every file in first-seen order. Past it, which files stay is
// decided by the work done on them — first the files written or changed
// (a write open, written bytes, a file-system change that succeeded),
// then those read from, then those opened without a byte read, last
// those only failed on (the dynamic loader's ENOENT probes) — earlier
// first-seen first within a class, and the kept ones are listed in
// first-seen order. A process traced from its exec opens a hundred
// library and JDK paths before its own work (task vg4); a first-seen
// cut would list just those and hide its config and its records. What
// is cut is counted (MoreFiles) either way.
func topFiles(files []*fileAcc) ([]File, int) {
	keep := make([]int, len(files))
	for i := range keep {
		keep[i] = i
	}
	more := 0
	if len(files) > MaxFiles {
		slices.SortStableFunc(keep, func(a, b int) int { return cmp.Compare(fileRank(files[a]), fileRank(files[b])) })
		more, keep = len(files)-MaxFiles, keep[:MaxFiles]
		slices.Sort(keep)
	}
	out := make([]File, 0, len(keep))
	for _, i := range keep {
		f := files[i].File
		f.Ops, f.Methods = nonNil(f.Ops), nonNil(f.Methods)
		out = append(out, f)
	}
	return out, more
}

// fileRank is a file's class for topFiles: 0 written or changed, 1 read
// from, 2 opened only, 3 only failed on.
func fileRank(f *fileAcc) int {
	switch {
	case f.WriteOpens > 0 || f.WriteBytes > 0 || f.changed:
		return 0
	case f.ReadBytes > 0:
		return 1
	case f.ReadOpens > 0:
		return 2
	}
	return 3
}

// conn renders one connection end.
func (b *builder) conn(p *procAcc, a *connAcc) Conn {
	id := b.merged(p.hostID, a.local)
	c := Conn{ID: id, FD: a.fd.fd, Role: a.role, Local: a.addrL, Remote: a.addrR, Service: b.service(id),
		SentBytes: a.sent, RecvBytes: a.recv, Requests: nonNil(a.requests), MoreRequests: a.moreRequests}
	c.Peers = b.peers(id, a)
	c.PeerState = PeerUntraced
	if len(c.Peers) > 0 {
		c.PeerState = PeerTraced
	}
	return c
}

// peers names the other ends of a merged connection: every end but this
// one (a process talking to itself over loopback, or through a pipe,
// holds both ends — each its own entry — and is its own peer).
func (b *builder) peers(id string, self *connAcc) []string {
	out := []string{}
	for _, e := range b.ends[id] {
		if e.acc == self {
			continue
		}
		if !slices.Contains(out, e.label) {
			out = append(out, e.label)
		}
	}
	slices.Sort(out)
	return out
}

// service labels a connection's server address through the service
// database: its service name, else hostname:port, else the address.
func (b *builder) service(id string) string {
	addr := b.servers[id]
	if l, ok := b.labels[addr]; ok {
		return l
	}
	return addr
}

// threads renders a process's threads with their calls resolved: the
// direction from the connection's role in this process, the merged id,
// the server's label and the peers.
func (b *builder) threads(p *procAcc) ([]Thread, int) {
	out := []Thread{}
	for i, t := range p.threads {
		if i == MaxThreads {
			return out, len(p.threads) - i
		}
		th := t.Thread
		th.Name = threadName(t)
		th.Calls = []Call{}
		for _, ca := range t.calls {
			id := b.merged(p.hostID, ca.end.local)
			th.Calls = append(th.Calls, Call{Dir: direction(ca.end.role), Method: ca.method,
				Scheme: ca.scheme, Path: ca.path, Status: ca.status, Conn: id, Service: b.service(id),
				Peers: b.peers(id, ca.end), Carrier: ca.carrier})
		}
		out = append(out, th)
	}
	return out, 0
}

// direction is a call's direction for the role its process had on the
// connection.
func direction(role string) string {
	switch role {
	case RoleClient:
		return DirOut
	case RoleServer:
		return DirIn
	}
	return ""
}

// topMethods sorts methods by events, then bytes, then name, and cuts
// them at MaxMethods.
func topMethods(ms map[string]*MethodStat) ([]MethodStat, int) {
	out := make([]MethodStat, 0, len(ms))
	for _, m := range ms {
		out = append(out, *m)
	}
	slices.SortFunc(out, func(x, y MethodStat) int {
		return cmp.Or(cmp.Compare(y.Events, x.Events), cmp.Compare(y.Bytes, x.Bytes),
			cmp.Compare(x.Method, y.Method))
	})
	if len(out) > MaxMethods {
		return out[:MaxMethods], len(out) - MaxMethods
	}
	return out, 0
}

// endpointLabels maps each endpoint's ip:port to its label.
func endpointLabels(eps []servicedb.Endpoint) map[string]string {
	out := map[string]string{}
	for _, e := range eps {
		key := e.IP + ":" + strconv.Itoa(int(e.Port))
		if a, err := netip.ParseAddr(e.IP); err == nil {
			key = netip.AddrPortFrom(a, e.Port).String()
		}
		out[key] = e.Label()
	}
	return out
}

// procLabel names a process for a peer list as its block's header
// does: "project/host/comm[pid]".
func procLabel(p *procAcc) string {
	return p.p.Project + "/" + p.p.Host + "/" + p.p.Comm + "[" + itoa(p.p.PID) + "]"
}

// nonNil turns a nil slice into an empty one, so the JSON says [] and
// not null: an empty list is data.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
