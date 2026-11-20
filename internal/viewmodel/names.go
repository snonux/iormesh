package viewmodel

import (
	"cmp"
	"strconv"

	"iormesh/internal/federate"
	"iormesh/internal/schema"
	"iormesh/internal/servicedb"
)

// namer names a conversation's lifelines the way the service database
// names the services and edges, so the sequence view and `show edges`
// agree: a server request's lifeline is the endpoint it was made to (a
// service name from the naming rules, else hostname:port, else
// ip:port); a client's is its process — the service the process serves
// when the rules give it exactly one, else its app, else its comm
// (servicedb.Interactions' rule).
type namer struct {
	procs  map[string]proc // process node id → app, comm
	served map[string]string
	// byMerged and byLocal map a connection to its observation: by the
	// merged id when fused, by the anchor's host-local id otherwise.
	byMerged map[string]servicedb.Observation
	byLocal  map[federate.LocalConn]servicedb.Observation
	labels   map[epKey]string
}

// proc is what the merged graph says about a process.
type proc struct{ app, comm string }

// epKey is an endpoint as the database keys it.
type epKey struct {
	hostname, ip string
	port         uint16
}

// newNamer indexes the graph, the build's observations and the
// database's endpoints.
func newNamer(g schema.Graph, obs []servicedb.Observation, eps []servicedb.Endpoint) namer {
	n := namer{procs: map[string]proc{}, served: servicedb.AppServices(eps),
		byMerged: map[string]servicedb.Observation{},
		byLocal:  map[federate.LocalConn]servicedb.Observation{}, labels: map[epKey]string{}}
	for _, nd := range g.Nodes {
		if nd.Type == schema.NodeProcess {
			n.procs[nd.ID] = proc{comm: nd.Attrs["comm"]}
		}
	}
	for _, e := range g.Edges {
		if e.Type == schema.EdgeIncarnation {
			p := n.procs[e.To]
			p.app = e.From
			n.procs[e.To] = p
		}
	}
	for _, o := range obs {
		if o.MergedID != "" {
			n.byMerged[o.MergedID] = o
		}
		n.byLocal[federate.LocalConn{Host: o.Host, ConnID: o.ConnID}] = o
	}
	for _, e := range eps {
		n.labels[epKey{e.Hostname, e.IP, e.Port}] = e.Label()
	}
	return n
}

// process names a process lifeline.
func (n namer) process(th threadKey) string {
	p := n.procs[schema.ProcessNodeID(th.host, th.pid, th.inc)]
	return n.named(p.app, p.comm, "pid "+strconv.FormatUint(uint64(th.pid), 10))
}

// named is the service an app serves, else the app, else the comm,
// else fallback.
func (n namer) named(app, comm, fallback string) string {
	return cmp.Or(n.served[app], app, comm, fallback)
}

// observation finds q's connection as the build observed it.
func (n namer) observation(q request) (servicedb.Observation, bool) {
	if o, ok := n.byMerged[q.conn]; ok && q.conn != "" {
		return o, true
	}
	o, ok := n.byLocal[q.local]
	return o, ok && q.conn != ""
}

// endpoint names the endpoint q went to (or came in on); "" when the
// build observed no endpoint for its connection.
func (n namer) endpoint(q request) string {
	o, ok := n.observation(q)
	if !ok || !o.Endpoint.IsValid() {
		return ""
	}
	k := epKey{o.Hostname, o.Endpoint.Addr().String(), o.Endpoint.Port()}
	if l, ok := n.labels[k]; ok {
		return l
	}
	return servicedb.Endpoint{Hostname: o.Hostname, IP: k.ip, Port: k.port}.Label()
}

// server is a server request's lifeline: its endpoint, else its
// process.
func (n namer) server(q request) string {
	return cmp.Or(n.endpoint(q), n.process(q.th))
}

// peer is the far end of an unpaired client request: its endpoint, else
// the connection's address, else "(unknown)".
func (n namer) peer(q request) string {
	if e := n.endpoint(q); e != "" {
		return e
	}
	if o, ok := n.observation(q); ok && o.Endpoint.IsValid() {
		return o.Endpoint.String()
	}
	return "(unknown)"
}

// caller names the client of a server request no client end was joined
// to: the connection's traced client process, else servicedb.Untraced.
func (n namer) caller(q request) string {
	o, ok := n.observation(q)
	if !ok || o.Client.Host == "" {
		return servicedb.Untraced
	}
	return n.named(o.Client.App, o.Client.Comm, servicedb.Untraced)
}
