package servicedb

import (
	"cmp"
	"net/netip"
	"slices"

	"iormesh/internal/federate"
	"iormesh/internal/schema"
)

// builder turns the merge's verdicts plus the traffic into observations.
type builder struct {
	t       *traffic
	procs   map[string]proc
	renames map[federate.LocalConn]string
}

// fused observes a connection the merge fused: anchored at the client,
// with both ends known.
func (b builder) fused(c federate.Conn) Observation {
	o := Observation{Host: c.Client.Host, ConnID: c.Client.ConnID, Role: RoleClient,
		State: schema.StateFused, MergedID: c.ID, Project: c.Client.Project,
		Client: b.end(c.Client), Server: b.end(c.Server), Endpoint: c.Client.Peer}
	if !o.Endpoint.IsValid() {
		o.Endpoint = c.Server.Local
	}
	ca, sa := b.acc(c.Client), b.acc(c.Server)
	o.fill(c.Client.Established, ca, sa)
	o.Hostname = b.name(c.Client.Procs, o)
	return o
}

// unresolved observes a half the merge left alone: anchored at itself.
// A server half names its own local address as the endpoint; a client
// half — and one with no role, whose far end may be anything — names
// its peer.
func (b builder) unresolved(u federate.Unresolved) Observation {
	h := u.Half
	o := Observation{Host: h.Host, ConnID: h.ConnID, State: schema.StateUnresolved,
		Reason: u.Reason, Project: h.Project, Endpoint: h.Peer}
	a := b.acc(h)
	switch h.Role {
	case federate.RoleServer:
		o.Role, o.Server, o.Endpoint = RoleServer, b.end(h), h.Local
	case federate.RoleClient:
		o.Role, o.Client = RoleClient, b.end(h)
	default:
		o.Role, o.Client = RoleUnknown, b.end(h)
	}
	o.fill(h.Established, a)
	if o.Role == RoleClient {
		o.Hostname = b.name(h.Procs, o)
	}
	return o
}

// localPairs observes the same-host TCP pairs: a conn id one host both
// connected and accepted, which its correlator joined already, so the
// merge never sees a half. A pair without a peer address (a unix stream
// socket) is no TCP endpoint and is skipped.
func (b builder) localPairs() []Observation {
	var out []Observation
	for _, k := range b.t.sortedConns() {
		a := b.t.conns[k]
		if len(a.clients) == 0 || len(a.server) == 0 || !a.peer.IsValid() {
			continue
		}
		cp, sp := sortedKeys(a.clients), sortedKeys(a.server)
		merged := cmp.Or(b.renames[k], k.ConnID)
		o := Observation{Host: k.Host, ConnID: k.ConnID, Role: RoleClient,
			State: schema.StateFused, MergedID: merged, Project: a.project,
			Client:   b.procEnd(k, a.project, cp),
			Server:   b.procEnd(k, a.project, sp),
			Endpoint: a.peer}
		o.fill(a.first, a)
		o.Hostname = b.name(cp, o)
		out = append(out, o)
	}
	return out
}

// acc is h's accumulated traffic, nil when the sources never named it
// (Observe's caller passed other sources than the merge read).
func (b builder) acc(h federate.Half) *connAcc {
	return b.t.conns[federate.LocalConn{Host: h.Host, ConnID: h.ConnID}]
}

// end describes a half as one end of a connection.
func (b builder) end(h federate.Half) End {
	return b.procEnd(federate.LocalConn{Host: h.Host, ConnID: h.ConnID}, h.Project, h.Procs)
}

// procEnd names an end by its processes (sorted): the first with an app
// gives the app, the first gives the comm, so the choice is stable.
func (b builder) procEnd(k federate.LocalConn, project string, procs []string) End {
	e := End{Project: project, Host: k.Host, ConnID: k.ConnID}
	for _, p := range procs {
		info := b.procs[p]
		e.Comm = cmp.Or(e.Comm, info.comm)
		e.App = cmp.Or(e.App, info.app)
	}
	return e
}

// name is the hostname the client asked for: its DNS lookup of the
// endpoint's address, else the host of its HTTP requests when that is a
// name rather than an address literal.
func (b builder) name(procs []string, o Observation) string {
	if n := b.t.hostname(procs, o.Endpoint.Addr(), o.First); n != "" {
		return n
	}
	for _, r := range o.Requests {
		if _, err := netip.ParseAddr(r.Host); r.Host != "" && err != nil {
			return r.Host
		}
	}
	return ""
}

// fill sets the window, the requests and the TLS hint from the ends'
// traffic, anchor first. established stands in for the window when no
// end has traffic.
func (o *Observation) fill(established uint64, accs ...*connAcc) {
	o.First, o.Last, o.TLS = established, established, -1
	seen := false
	for _, a := range accs {
		if a == nil {
			continue
		}
		if !seen {
			o.First, o.Last, seen = a.first, a.last, true
		}
		o.First, o.Last = min(o.First, a.first), max(o.Last, a.last)
	}
	o.Requests = requestsOf(accs)
	o.TLS = tlsOf(o.Requests)
}

// requestsOf picks one end's view of the requests: the client's events
// (they carry the host) when any end has them, else the server's — never
// both, which would count each request twice.
func requestsOf(accs []*connAcc) []Request {
	for _, client := range []bool{true, false} {
		var out []Request
		for _, a := range accs {
			if a == nil {
				continue
			}
			for _, r := range a.reqs {
				if (r.Host != "") == client {
					out = append(out, *r)
				}
			}
			if len(out) > 0 {
				return sortRequests(out)
			}
		}
	}
	return nil
}

func sortRequests(rs []Request) []Request {
	slices.SortFunc(rs, func(a, b Request) int {
		return cmp.Or(cmp.Compare(a.Method, b.Method), cmp.Compare(a.Scheme, b.Scheme),
			cmp.Compare(a.Host, b.Host), cmp.Compare(a.Path, b.Path))
	})
	return rs
}

// tlsOf is the TLS hint: 1 when a request went over https, 0 when they
// all were plain http, -1 when there is no request to tell.
func tlsOf(rs []Request) int {
	tls := -1
	for _, r := range rs {
		switch r.Scheme {
		case "https":
			return 1
		case "http":
			tls = 0
		}
	}
	return tls
}
