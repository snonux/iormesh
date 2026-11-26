package servicedb

import (
	"cmp"
	"maps"
	"math"
	"net/netip"
	"slices"

	"iormesh/internal/federate"
	"iormesh/internal/pathtmpl"
	"iormesh/internal/schema"
)

// traffic is what Observe reads from the events beyond the merge: per
// host-local connection its records' window, the processes that
// connected and accepted it, its HTTP requests; per process its DNS
// lookups.
type traffic struct {
	conns map[federate.LocalConn]*connAcc
	dns   map[string][]lookup // by process node id
	// source names the source whose events are being read: requests are
	// counted per source, so a sum over sources stays exact whichever of
	// them a later build still has.
	source string
	// paths templates each request's path before it is counted.
	paths *pathtmpl.Rules
}

// connAcc accumulates one (host, conn_id) across every source.
type connAcc struct {
	project         string
	first, last     uint64
	clients, server map[string]bool // process ids: connect, accept (or an initial snapshot's role)
	peer, local     netip.AddrPort  // connect's remote, accept's local (likewise)
	reqs            map[reqKey]*Request
	// seen is each source's window of the connection's records, so the
	// database knows which sources a forgotten segment leaves (task gk4).
	seen map[string]*SourceSpan
}

// reqKey groups a connection's requests per source. Host is set only on
// a client's event (docs/schema.md "HTTP events"), which is how the two
// ends' views of one request — both on one conn id for a same-host pair
// — stay apart: the reads count one view, never both.
type reqKey struct{ source, method, scheme, host, path string }

// lookup is one DNS mapping a process recorded.
type lookup struct {
	wall  uint64
	name  string
	addrs []netip.Addr
}

func newTraffic() *traffic {
	return &traffic{conns: map[federate.LocalConn]*connAcc{}, dns: map[string][]lookup{}}
}

// add folds one event in. It is a federate.Source callback, so it only
// borrows ev: everything kept is copied (strings are immutable, the DNS
// addresses are parsed into values).
func (t *traffic) add(ev *schema.Event) error {
	pid := schema.ProcessNodeID(ev.HostID, ev.PID, ev.PIDInc)
	if ev.Event == schema.KindDNS && ev.DNS != nil {
		t.dns[pid] = append(t.dns[pid], newLookup(ev))
		return nil
	}
	if ev.ConnID == "" {
		return nil
	}
	a := t.acc(federate.LocalConn{Host: ev.HostID, ConnID: ev.ConnID}, ev)
	// An http event starts at wall_ns and ends latency_ns later.
	a.first, a.last = min(a.first, ev.WallNS), max(a.last, endOf(ev))
	a.saw(t.source, ev.WallNS, endOf(ev))
	switch ev.Event {
	case schema.KindConnect:
		a.clients[pid] = true
		if p, ok := addrPort(ev.Args.RemoteAddr, ev.Args.RemotePort); ok {
			a.peer = p
		}
	case schema.KindAccept, schema.KindAccept4:
		a.server[pid] = true
		if l, ok := addrPort(ev.Args.LocalAddr, ev.Args.LocalPort); ok {
			a.local = l
		}
	case schema.KindTCPState:
		a.initial(pid, ev)
	case schema.KindHTTP:
		a.request(t.source, t.paths, ev)
	}
	return nil
}

// initial takes the role of a socket open before the session from its
// initial snapshot (decision #35), which stands in for the connect or
// accept the trace never saw: the client end's remote address is the
// peer, the server end's local address the endpoint. A connect or accept
// of the same conn id, had there been one, sets the same.
func (a *connAcc) initial(pid string, ev *schema.Event) {
	switch ev.InitialRole() {
	case RoleClient:
		a.clients[pid] = true
		if p, ok := addrPort(ev.Args.RemoteAddr, ev.Args.RemotePort); ok {
			a.peer = p
		}
	case RoleServer:
		a.server[pid] = true
		if l, ok := addrPort(ev.Args.LocalAddr, ev.Args.LocalPort); ok {
			a.local = l
		}
	}
}

// endOf is when an event ends, wall_ns + latency_ns, saturated at int64
// (sqlite's integer): the segment reader refuses such clocks already,
// but a forged NDJSON trace or a future reader must not overflow the
// sum into a window Import rejects, failing the whole build.
func endOf(ev *schema.Event) uint64 {
	if ev.WallNS > math.MaxInt64 || ev.LatencyNS > math.MaxInt64-ev.WallNS {
		return math.MaxInt64
	}
	return ev.WallNS + ev.LatencyNS
}

// acc returns k's accumulator, creating it from its first event.
func (t *traffic) acc(k federate.LocalConn, ev *schema.Event) *connAcc {
	a := t.conns[k]
	if a == nil {
		a = &connAcc{project: ev.Project, first: ev.WallNS, last: ev.WallNS,
			clients: map[string]bool{}, server: map[string]bool{}, reqs: map[reqKey]*Request{},
			seen: map[string]*SourceSpan{}}
		t.conns[k] = a
	}
	return a
}

// saw widens source's window of the connection by one record.
func (a *connAcc) saw(source string, first, last uint64) {
	s := a.seen[source]
	if s == nil {
		a.seen[source] = &SourceSpan{Name: source, First: first, Last: last}
		return
	}
	s.First, s.Last = min(s.First, first), max(s.Last, last)
}

// request counts one http event of source under its path's template:
// one route's requests on different ids share a key.
func (a *connAcc) request(source string, paths *pathtmpl.Rules, ev *schema.Event) {
	h := ev.HTTP
	if h == nil {
		return
	}
	path := paths.Template(h.Path)
	k := reqKey{source, h.Method, h.Scheme, h.Host, path}
	end := endOf(ev)
	r := a.reqs[k]
	if r == nil {
		r = &Request{Source: source, Method: h.Method, Scheme: h.Scheme, Host: h.Host,
			Path: path, First: ev.WallNS, Last: end}
		a.reqs[k] = r
	}
	r.Count++
	r.First, r.Last = min(r.First, ev.WallNS), max(r.Last, end)
}

// newLookup copies a dns event's mapping; an address the JDK printed in
// a form netip cannot parse is skipped, never guessed.
func newLookup(ev *schema.Event) lookup {
	l := lookup{wall: ev.WallNS, name: ev.DNS.Host}
	for _, s := range ev.DNS.Addrs {
		if a, err := netip.ParseAddr(s); err == nil {
			l.addrs = append(l.addrs, a.Unmap())
		}
	}
	return l
}

// addrPort parses an address and port pair of an event's args.
func addrPort(addr *string, port *uint16) (netip.AddrPort, bool) {
	if addr == nil || port == nil {
		return netip.AddrPort{}, false
	}
	a, err := netip.ParseAddr(*addr)
	if err != nil {
		return netip.AddrPort{}, false
	}
	return netip.AddrPortFrom(a.Unmap(), *port), true
}

// hostname is the name procs looked up for addr: the latest of their
// DNS mappings recorded no later than the connection's first record
// (the lookup precedes the connect) that contains addr. Ties go to the
// smaller name, so the answer never depends on read order.
func (t *traffic) hostname(procs []string, addr netip.Addr, first uint64) string {
	var best *lookup
	for _, p := range procs {
		for i := range t.dns[p] {
			l := &t.dns[p][i]
			if l.wall > first || !slices.Contains(l.addrs, addr.Unmap()) {
				continue
			}
			if best == nil || l.wall > best.wall || (l.wall == best.wall && l.name < best.name) {
				best = l
			}
		}
	}
	if best == nil {
		return ""
	}
	return best.name
}

// sortedKeys returns the map's keys in order.
func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}

// sortedConns returns the accumulated connections in (host, conn) order.
func (t *traffic) sortedConns() []federate.LocalConn {
	return slices.SortedFunc(maps.Keys(t.conns), func(a, b federate.LocalConn) int {
		return cmp.Or(cmp.Compare(a.Host, b.Host), cmp.Compare(a.ConnID, b.ConnID))
	})
}
