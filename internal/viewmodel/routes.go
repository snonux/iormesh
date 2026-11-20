package viewmodel

import (
	"cmp"
	"slices"
	"time"

	"iormesh/internal/federate"
	"iormesh/internal/routemap"
	"iormesh/internal/schema"
	"iormesh/internal/servicedb"
	"iormesh/internal/termsafe"
)

// The routed edges (PLAN.md M7 "Service-NAT correlation", "Ingress route
// mapping", decision #15): what the route map adds to the edges the
// traces made. A connection through a ClusterIP gets the Service named
// (and, fused, the pod that answered); one to an untraced Service or an
// FQDN on an ingress is completed to the Service and its workload. The
// map never changes a fused edge; where it and the traces disagree, a
// RouteFlag says so.

// Route is one routed edge: From → (FQDN → Ingress →) Service →
// Workload (→ Pod). Basis is routemap's: "fused" (both ends traced, the
// Service named from the ClusterIP dialed), "service" (the pod behind
// the ClusterIP untraced) or "route" (the FQDN's route through an
// ingress).
type Route struct {
	From      string    `json:"from"`
	FQDN      string    `json:"fqdn"`
	Ingress   string    `json:"ingress"`
	Service   string    `json:"service"`
	Workload  string    `json:"workload"`
	Pod       string    `json:"pod"`
	Basis     string    `json:"basis"`
	Endpoints int       `json:"endpoints"`
	Origin    string    `json:"origin"`
	Conns     int       `json:"conns"`
	First     time.Time `json:"first"`
	Last      time.Time `json:"last"`
}

// RouteFlag is one disagreement between the route map and the traces,
// or a gap of the map (routemap.Flag).
type RouteFlag struct {
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
	Detail  string `json:"detail"`
	Conns   int    `json:"conns"`
}

// routes joins the build's client-side observations with the route map.
// Only connections whose client was traced take part: only the client
// knows what it dialed and asked for. Labels follow the edges' rule
// (servicedb Interactions): the service a client app serves, else its
// app, else its process name.
func routes(t *routemap.Table, obs []servicedb.Observation, conns []federate.Conn,
	eps []servicedb.Endpoint) ([]Route, []RouteFlag) {
	outR, outF := []Route{}, []RouteFlag{}
	if t.Empty() {
		return outR, outF
	}
	servers := map[string]federate.Half{}
	for _, c := range conns {
		servers[c.ID] = c.Server
	}
	served := servicedb.AppServices(eps)
	var flows []routemap.Flow
	for _, o := range obs {
		if o.Role != servicedb.RoleClient {
			continue
		}
		f := routemap.Flow{From: cmp.Or(served[o.Client.App], o.Client.App, o.Client.Comm),
			Endpoint: o.Endpoint, Hostname: o.Hostname, HTTPHosts: clientHosts(o.Requests),
			Fused: o.State == schema.StateFused, First: o.First, Last: o.Last}
		if s, ok := servers[o.MergedID]; ok && f.Fused {
			f.ServerHost, f.ServerAddr = s.Host, s.Local
		}
		flows = append(flows, f)
	}
	res := t.Join(flows)
	for _, e := range res.Edges {
		outR = append(outR, Route{From: e.From, FQDN: e.FQDN, Ingress: e.Ingress, Service: e.Service,
			Workload: e.Workload, Pod: e.Pod, Basis: e.Basis, Endpoints: e.Endpoints, Origin: e.Origin,
			Conns: e.Conns, First: wallTime(e.First), Last: wallTime(e.Last)})
	}
	for _, f := range res.Flags {
		outF = append(outF, RouteFlag(f))
	}
	return outR, outF
}

// clientHosts are the distinct hosts of a connection's requests as its
// client recorded them (only a client's view carries the host).
func clientHosts(reqs []servicedb.Request) []string {
	var out []string
	for _, r := range reqs {
		if r.Host != "" {
			out = append(out, r.Host)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// safeRoutes makes the routed edges and flags terminal-safe. The map's
// own names passed Kubernetes' rules on load, but the labels come from
// the servers, and a flag's detail quotes a server end's host id.
func safeRoutes(rs []Route, fs []RouteFlag) ([]Route, []RouteFlag) {
	s := termsafe.String
	rs, fs = slices.Clone(rs), slices.Clone(fs)
	for i := range rs {
		x := &rs[i]
		x.From, x.FQDN, x.Ingress, x.Service = s(x.From), s(x.FQDN), s(x.Ingress), s(x.Service)
		x.Workload, x.Pod, x.Basis, x.Origin = s(x.Workload), s(x.Pod), s(x.Basis), s(x.Origin)
	}
	for i := range fs {
		x := &fs[i]
		x.Kind, x.Subject, x.Detail = s(x.Kind), s(x.Subject), s(x.Detail)
	}
	return rs, fs
}
