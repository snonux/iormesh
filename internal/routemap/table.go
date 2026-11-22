package routemap

import (
	"net/netip"
	"strings"

	"iormesh/internal/pathtmpl"
)

// Origins: where an entry of the compiled table came from.
const (
	OriginStatic     = "static"
	OriginDiscovered = "discovered"
)

// Table is the maps compiled for lookups: the static map over the
// discovered one. Build it with Compile; the zero Table names nothing.
type Table struct {
	routes   map[string]entry[Route] // by normalized FQDN, "*." wildcards included
	services map[string]entry[Service]
	byAddr   map[netip.AddrPort]string // ClusterIP:port → Service ref
	cidrs    []netip.Prefix
	// flags are what compiling found worth the operator's eye: carried
	// over from the discovery snapshot (its ambiguous routes and
	// workloads), and discovered entries a static one shadowed.
	flags []Flag
	// paths are the static map's path templates, nil without any.
	paths *pathtmpl.Rules
}

// entry is one compiled route or Service and its origin.
type entry[T any] struct {
	v      T
	origin string
}

// Compile builds the table from the static map and the discovered one,
// either possibly nil. Precedence (decision #15): a static route wins
// over a discovered one for the same FQDN, a static Service over a
// discovered one of the same name — and over one claiming the same
// ClusterIP and port, which can only mean one of the two maps is out of
// date. Discovery fills only what the file lacks. Both maps were
// validated on load, so each is free of duplicates of its own. Path
// templates come from the static map alone: they are the operator's
// rules, not something a cluster's objects say.
func Compile(static, discovered *Map, discoveryFlags []Flag) *Table {
	t := &Table{routes: map[string]entry[Route]{}, services: map[string]entry[Service]{},
		byAddr: map[netip.AddrPort]string{}}
	t.flags = append(t.flags, discoveryFlags...)
	if static != nil {
		// Validate refused a bad template on load; a map built in code
		// without it gets no rules rather than half of them.
		t.paths, _ = pathtmpl.Compile(static.PathTemplates)
	}
	for _, src := range []struct {
		m      *Map
		origin string
	}{{static, OriginStatic}, {discovered, OriginDiscovered}} {
		if src.m != nil {
			t.add(src.m, src.origin)
		}
	}
	return t
}

// add folds one map in; static is added first, so anything already
// present wins.
func (t *Table) add(m *Map, origin string) {
	for _, r := range m.Routes {
		if _, ok := t.routes[r.FQDN]; ok {
			t.shadowed(origin, "route "+r.FQDN)
			continue
		}
		t.routes[r.FQDN] = entry[Route]{r, origin}
	}
	for _, s := range m.Services {
		if t.claimed(s) {
			t.shadowed(origin, "service "+s.Service)
			continue
		}
		t.services[s.Service] = entry[Service]{s, origin}
		for _, a := range s.addrs() {
			t.byAddr[a] = s.Service
		}
	}
	for _, c := range m.ServiceCIDRs {
		if p, err := netip.ParsePrefix(c); err == nil {
			t.cidrs = append(t.cidrs, p)
		}
	}
}

// claimed reports whether s's name or any of its addresses is taken.
func (t *Table) claimed(s Service) bool {
	if _, ok := t.services[s.Service]; ok {
		return true
	}
	for _, a := range s.addrs() {
		if _, ok := t.byAddr[a]; ok {
			return true
		}
	}
	return false
}

// shadowed notes a discovered entry the static map overrode: expected
// when the operator pins a route by hand, and worth a line, since the
// two disagreeing is how a hand-kept file goes stale.
func (t *Table) shadowed(origin, what string) {
	if origin == OriginDiscovered {
		t.flags = append(t.flags, Flag{Kind: FlagShadowed, Subject: what,
			Detail: "the static map's entry wins over the discovered one"})
	}
}

// Paths are the operator's path templates; nil (the built-in folding
// alone) for a nil table or a static map without any.
func (t *Table) Paths() *pathtmpl.Rules {
	if t == nil {
		return nil
	}
	return t.paths
}

// Empty reports whether the table names nothing: no route, Service or
// Service range (path templates name no edge).
func (t *Table) Empty() bool {
	return t == nil || (len(t.routes) == 0 && len(t.services) == 0 && len(t.cidrs) == 0)
}

// Route returns the route for a hostname: the exact FQDN, else a "*."
// wildcard for its parent (one label, as Kubernetes matches Ingress
// hosts).
func (t *Table) Route(host string) (Route, string, bool) {
	if t == nil {
		return Route{}, "", false
	}
	host = NormalizeFQDN(host)
	if e, ok := t.routes[host]; ok {
		return e.v, e.origin, true
	}
	if _, parent, ok := strings.Cut(host, "."); ok && strings.Contains(parent, ".") {
		if e, ok := t.routes["*."+parent]; ok {
			return e.v, e.origin, true
		}
	}
	return Route{}, "", false
}

// ServiceAt returns the Service whose ClusterIP and port a client
// dialed.
func (t *Table) ServiceAt(a netip.AddrPort) (Service, bool) {
	if t == nil {
		return Service{}, false
	}
	ref, ok := t.byAddr[netip.AddrPortFrom(a.Addr().Unmap(), a.Port())]
	if !ok {
		return Service{}, false
	}
	return t.services[ref].v, true
}

// Service returns a Service by "<namespace>/<service>".
func (t *Table) Service(ref string) (Service, bool) {
	if t == nil {
		return Service{}, false
	}
	e, ok := t.services[ref]
	return e.v, ok
}

// inServiceRange reports whether a is inside a declared Service CIDR.
func (t *Table) inServiceRange(a netip.Addr) bool {
	if t == nil {
		return false
	}
	for _, p := range t.cidrs {
		if p.Contains(a.Unmap()) {
			return true
		}
	}
	return false
}
