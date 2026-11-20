package servicedb

import (
	"cmp"
	"maps"
	"net/netip"
	"slices"

	"iormesh/internal/federate"
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
}

// connAcc accumulates one (host, conn_id) across every source.
type connAcc struct {
	project         string
	first, last     uint64
	clients, server map[string]bool // process ids: connect, accept
	peer, local     netip.AddrPort  // connect's remote, accept's local
	reqs            map[reqKey]*Request
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
	a.first, a.last = min(a.first, ev.WallNS), max(a.last, ev.WallNS+ev.LatencyNS)
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
	case schema.KindHTTP:
		a.request(t.source, ev)
	}
	return nil
}

// acc returns k's accumulator, creating it from its first event.
func (t *traffic) acc(k federate.LocalConn, ev *schema.Event) *connAcc {
	a := t.conns[k]
	if a == nil {
		a = &connAcc{project: ev.Project, first: ev.WallNS, last: ev.WallNS,
			clients: map[string]bool{}, server: map[string]bool{}, reqs: map[reqKey]*Request{}}
		t.conns[k] = a
	}
	return a
}

// request counts one http event of source.
func (a *connAcc) request(source string, ev *schema.Event) {
	h := ev.HTTP
	if h == nil {
		return
	}
	k := reqKey{source, h.Method, h.Scheme, h.Host, h.Path}
	end := ev.WallNS + ev.LatencyNS
	r := a.reqs[k]
	if r == nil {
		r = &Request{Source: source, Method: h.Method, Scheme: h.Scheme, Host: h.Host,
			Path: h.Path, First: ev.WallNS, Last: end}
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
