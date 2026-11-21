package routemap

import (
	"cmp"
	"maps"
	"net/netip"
	"slices"
	"strings"
)

// Bases: what an Edge stands on.
const (
	// BasisFused: both ends were traced and the merge fused them by the
	// ISN pair; the map only names the Service the client dialed and the
	// pod that answered is the traced server end.
	BasisFused = "fused"
	// BasisService: the client dialed a Service's ClusterIP and the pod
	// behind it was not traced; the edge stops at the Service and its
	// workload, at a pod only when the Service has exactly one endpoint.
	BasisService = "service"
	// BasisRoute: the client asked for an FQDN the map routes through an
	// ingress; the ingress ended the connection, so the edge is the
	// route's, never the traces'. It names a pod only when the Service
	// has exactly one endpoint and that pod was traced (Join's traced).
	BasisRoute = "route"
)

// Flag kinds: what the map and the traces disagree on, or what the map
// could not say.
const (
	// FlagStaleService: a fused connection through a Service's ClusterIP
	// reached a pod that is not one of the Service's listed endpoints —
	// the map is older than the pods. The fused fact stands.
	FlagStaleService = "stale_service"
	// FlagUnknownService: a connection to an address inside a declared
	// Service CIDR that no Service of the map holds.
	FlagUnknownService = "unknown_service"
	// FlagContradicted: an observed HTTP host's route names another
	// Service than the one the connection reached. Observations never
	// change a route (decision #15); they flag it.
	FlagContradicted = "route_contradicted"
	// FlagShadowed: a discovered entry the static map overrode.
	FlagShadowed = "shadowed"
	// FlagAmbiguousRoute: discovery found one host routed to several
	// Services (by path, or by two objects); no route is made for it.
	FlagAmbiguousRoute = "ambiguous_route"
	// FlagAmbiguousWorkload: a discovered Service's endpoints belong to
	// more than one workload, or to one of another kind; none is named.
	FlagAmbiguousWorkload = "ambiguous_workload"
)

// Flow is one connection as the build saw it, from its client's side:
// the client must have been traced, since only the client knows what it
// dialed and asked for.
type Flow struct {
	From     string         // the client's label (service, app or process name)
	Endpoint netip.AddrPort // the address the client dialed
	// Hostname is the name the client asked for: its HTTP host, else its
	// DNS lookup of the endpoint (servicedb.Observation.Hostname).
	Hostname string
	// HTTPHosts are the hosts of the client's own HTTP requests on the
	// connection: observations, checked against the routes.
	HTTPHosts []string
	// Fused says the merge joined both ends; ServerHost and ServerAddr
	// are then the server end's host id ("pod-<uid>" in a pod) and the
	// address it accepted on.
	Fused       bool
	ServerHost  string
	ServerAddr  netip.AddrPort
	First, Last uint64 // wall ns
}

// Edge is one routed edge: client → (FQDN → ingress →) Service →
// workload (→ pod), aggregated over the flows that share it.
type Edge struct {
	From     string `json:"from"`
	FQDN     string `json:"fqdn,omitempty"`
	Ingress  string `json:"ingress,omitempty"`
	Service  string `json:"service"`
	Workload string `json:"workload,omitempty"`
	Pod      string `json:"pod,omitempty"`
	Basis    string `json:"basis"`
	// Endpoints is how many endpoints the map lists for the Service: the
	// reader's measure of what a BasisService edge without a pod left
	// open.
	Endpoints int    `json:"endpoints"`
	Origin    string `json:"origin"` // OriginStatic or OriginDiscovered
	Conns     int    `json:"conns"`
	First     uint64 `json:"first"`
	Last      uint64 `json:"last"`
}

// Flag is one disagreement or gap, aggregated by (Kind, Subject, Detail).
type Flag struct {
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
	Detail  string `json:"detail"`
	Conns   int    `json:"conns"`
}

// Result is what Join made of the flows: edges sorted by their fields in
// order, flags by (Kind, Subject, Detail).
type Result struct {
	Edges []Edge `json:"edges"`
	Flags []Flag `json:"flags"`
}

// Join routes the flows. A flow to a ClusterIP is matched by the
// address it dialed (a fact of the trace) before any route by name (the
// client's word); a flow the map names nothing about is left out, to
// stay the plain unresolved edge it was. traced holds the host ids of
// the build's traced sessions ("pod-<uid>" for an in-pod tracer): a
// route edge names its Service's sole endpoint only when that pod is
// among them (addRoute). nil means none.
func (t *Table) Join(flows []Flow, traced map[string]bool) Result {
	j := joiner{t: t, traced: traced, edges: map[edgeKey]*Edge{}, flags: map[Flag]int{}}
	if t != nil {
		for _, f := range t.flags {
			j.flag(f.Kind, f.Subject, f.Detail, 0)
		}
	}
	for i := range flows {
		j.flow(&flows[i])
	}
	return j.result()
}

// edgeKey is an Edge without its counts and window.
type edgeKey struct {
	from, fqdn, ingress, service, workload, pod, basis, origin string
	endpoints                                                  int
}

type joiner struct {
	t      *Table
	traced map[string]bool // host ids with a traced session
	edges  map[edgeKey]*Edge
	flags  map[Flag]int // Conns zeroed in the key; the value counts them
}

// flow routes one flow and checks its HTTP hosts. A flow that reached
// its hostname's ingress — the route's ingress Service by ClusterIP, or
// fused to one of that Service's pods — is the route's edge: an
// in-cluster client calling the FQDN through Traefik's Service is the
// ingress hop, not a contradiction. An address in a Service CIDR that no
// Service holds is flagged and the route still tried: the ingress's own
// Service may simply be missing from the map.
func (j *joiner) flow(f *Flow) {
	s, viaService := j.t.ServiceAt(f.Endpoint)
	if r, origin, ok := j.t.Route(stripPort(f.Hostname)); ok && j.reachedIngress(f, r, s, viaService) {
		j.addRoute(f, r, origin)
		return
	}
	switch {
	case viaService:
		j.service(f, s)
	case f.Endpoint.IsValid() && j.t.inServiceRange(f.Endpoint.Addr()):
		j.flag(FlagUnknownService, f.Endpoint.String(), "inside a service CIDR, no service of the map holds it", 1)
		if !f.Fused {
			j.route(f)
		}
	case !f.Fused:
		j.route(f)
	}
	j.checkHosts(f, s, viaService)
}

// reachedIngress reports whether the flow ended on r's ingress: its
// ClusterIP dialed, or — fused — one of its listed pods answered.
func (j *joiner) reachedIngress(f *Flow, r Route, reached Service, viaService bool) bool {
	if r.IngressService == "" {
		return false
	}
	if viaService {
		return reached.Service == r.IngressService
	}
	ing, ok := j.t.Service(r.IngressService)
	if !ok || !f.Fused {
		return false
	}
	_, listed := endpointOf(ing, f.ServerHost, f.ServerAddr)
	return listed
}

// service adds a flow that dialed s's ClusterIP: the Service named, the
// pod either the fused server end (checked against the endpoints) or,
// unfused, the sole endpoint.
func (j *joiner) service(f *Flow, s Service) {
	k := edgeKey{from: f.From, service: s.Service, workload: s.Workload, basis: BasisService,
		origin: j.t.services[s.Service].origin, endpoints: len(s.Endpoints)}
	if f.Fused {
		k.basis = BasisFused
		ep, listed := endpointOf(s, f.ServerHost, f.ServerAddr)
		k.pod = cmp.Or(ep.Pod, f.ServerHost)
		if !listed && len(s.Endpoints) > 0 {
			j.flag(FlagStaleService, s.Service, "reached "+cmp.Or(f.ServerHost, f.ServerAddr.String())+
				", not one of its endpoints", 1)
		}
	} else if len(s.Endpoints) == 1 {
		k.pod = cmp.Or(s.Endpoints[0].Pod, s.Endpoints[0].Addr)
	}
	j.add(k, f)
}

// route adds an unfused flow whose hostname the map routes.
func (j *joiner) route(f *Flow) {
	if r, origin, ok := j.t.Route(stripPort(f.Hostname)); ok {
		j.addRoute(f, r, origin)
	}
}

// addRoute adds f as r's edge: FQDN → ingress → Service → workload, and
// → pod by the ClusterIP join's single-endpoint rule (service): a
// Service that lists exactly one endpoint names it (task ff4). A route
// edge adds one condition, that the pod was traced (its "pod-<uid>"
// host id in traced): the ingress ended the client's connection, so no
// trace of the flow ever reached the backend — its own trace is the
// evidence that the map's one endpoint was the pod serving then, not a
// stale entry. Two endpoints, an endpoint without a UID or an untraced
// pod name none.
func (j *joiner) addRoute(f *Flow, r Route, origin string) {
	k := edgeKey{from: f.From, fqdn: r.FQDN, ingress: r.Ingress, service: r.Service,
		workload: r.Workload, basis: BasisRoute, origin: origin}
	if s, ok := j.t.Service(r.Service); ok {
		k.workload, k.endpoints = cmp.Or(r.Workload, s.Workload), len(s.Endpoints)
		if len(s.Endpoints) == 1 {
			if ep := s.Endpoints[0]; ep.UID != "" && j.traced["pod-"+ep.UID] {
				k.pod = cmp.Or(ep.Pod, "pod-"+ep.UID)
			}
		}
	}
	j.add(k, f)
}

// checkHosts flags each HTTP host whose route names another Service than
// the one the flow reached: by ClusterIP, or — fused straight to a pod —
// by the route's Service not listing that pod.
func (j *joiner) checkHosts(f *Flow, reached Service, viaService bool) {
	for _, h := range f.HTTPHosts {
		r, _, ok := j.t.Route(stripPort(h))
		if !ok || j.reachedIngress(f, r, reached, viaService) {
			continue // reaching the route's ingress is what the route says
		}
		switch {
		case viaService && reached.Service != r.Service:
			j.flag(FlagContradicted, r.FQDN, "routed to "+r.Service+", observed reaching "+reached.Service, 1)
		case !viaService && f.Fused:
			s, known := j.t.Service(r.Service)
			if _, listed := endpointOf(s, f.ServerHost, f.ServerAddr); known && len(s.Endpoints) > 0 && !listed {
				j.flag(FlagContradicted, r.FQDN, "routed to "+r.Service+", observed reaching "+
					cmp.Or(f.ServerHost, f.ServerAddr.String()), 1)
			}
		}
	}
}

// endpointOf finds the traced server end among s's endpoints: by pod UID
// first (an in-pod tracer's host id is "pod-<uid>"), else by the address
// it accepted on. For an in-pod end the address names only endpoints
// without a UID: pod IPs are reused, so an endpoint whose UID differs is
// another pod that held the address, and the end is not listed (the
// caller flags the map stale) rather than named after it.
func endpointOf(s Service, host string, addr netip.AddrPort) (Endpoint, bool) {
	uid, inPod := strings.CutPrefix(host, "pod-")
	for _, e := range s.Endpoints {
		if inPod && e.UID != "" && e.UID == uid {
			return e, true
		}
	}
	for _, e := range s.Endpoints {
		if inPod && e.UID != "" {
			continue
		}
		if a, err := netip.ParseAddr(e.Addr); err == nil && addr.IsValid() && a.Unmap() == addr.Addr().Unmap() {
			return e, true
		}
	}
	return Endpoint{}, false
}

// stripPort drops a ":port" suffix an HTTP Host may carry.
func stripPort(h string) string {
	if i := strings.LastIndexByte(h, ':'); i >= 0 && !strings.Contains(h[:i], ":") {
		return h[:i]
	}
	return h
}

// add aggregates f into k's edge.
func (j *joiner) add(k edgeKey, f *Flow) {
	e := j.edges[k]
	if e == nil {
		e = &Edge{From: k.from, FQDN: k.fqdn, Ingress: k.ingress, Service: k.service, Workload: k.workload,
			Pod: k.pod, Basis: k.basis, Endpoints: k.endpoints, Origin: k.origin, First: f.First, Last: f.Last}
		j.edges[k] = e
	}
	e.Conns++
	e.First, e.Last = min(e.First, f.First), max(e.Last, f.Last)
}

func (j *joiner) flag(kind, subject, detail string, conns int) {
	j.flags[Flag{Kind: kind, Subject: subject, Detail: detail}] += conns
}

// result sorts what was gathered.
func (j *joiner) result() Result {
	r := Result{Edges: []Edge{}, Flags: []Flag{}}
	for _, e := range j.edges {
		r.Edges = append(r.Edges, *e)
	}
	slices.SortFunc(r.Edges, func(a, b Edge) int {
		return cmp.Or(cmp.Compare(a.From, b.From), cmp.Compare(a.FQDN, b.FQDN), cmp.Compare(a.Service, b.Service),
			cmp.Compare(a.Basis, b.Basis), cmp.Compare(a.Pod, b.Pod), cmp.Compare(a.Ingress, b.Ingress),
			cmp.Compare(a.Workload, b.Workload), cmp.Compare(a.Origin, b.Origin))
	})
	for _, f := range slices.SortedFunc(maps.Keys(j.flags), func(a, b Flag) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Subject, b.Subject), cmp.Compare(a.Detail, b.Detail))
	}) {
		f.Conns = j.flags[f]
		r.Flags = append(r.Flags, f)
	}
	return r
}
