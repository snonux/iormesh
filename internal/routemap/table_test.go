package routemap

import (
	"net/netip"
	"testing"
)

func mustParse(t *testing.T, doc string) *Map {
	t.Helper()
	m, err := Parse(t.Name(), []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestCompilePrecedence: a static route and Service win over discovered
// ones of the same FQDN, name or ClusterIP, each shadowing flagged;
// discovery fills what the file lacks.
func TestCompilePrecedence(t *testing.T) {
	static := mustParse(t, goodMap)
	disc := mustParse(t, `routes:
  - {fqdn: shop.example.org, ingress: ingress/prod/web, service: prod/web}
  - {fqdn: api.example.org, ingress: ingress/prod/api, service: prod/api}
services:
  - {service: prod/backend, cluster_ips: [10.96.0.21], ports: [{port: 80}]}
  - {service: prod/other, cluster_ips: [10.96.0.20], ports: [{port: 80}]}
  - {service: prod/api, cluster_ips: [10.96.0.22], ports: [{port: 80}]}
`)
	tbl := Compile(static, disc, []Flag{{Kind: FlagAmbiguousRoute, Subject: "x.example.org"}})
	if r, origin, _ := tbl.Route("shop.example.org"); r.Service != "prod/backend" || origin != OriginStatic {
		t.Errorf("shop route = %+v from %s, want the static one", r, origin)
	}
	if r, origin, _ := tbl.Route("API.example.org."); r.Service != "prod/api" || origin != OriginDiscovered {
		t.Errorf("api route = %+v from %s, want the discovered one", r, origin)
	}
	if s, _ := tbl.ServiceAt(netip.MustParseAddrPort("10.96.0.20:80")); s.Service != "prod/backend" {
		t.Errorf("10.96.0.20:80 = %s, want the static prod/backend", s.Service)
	}
	if _, ok := tbl.ServiceAt(netip.MustParseAddrPort("10.96.0.21:80")); ok {
		t.Error("the shadowed discovered prod/backend's ClusterIP still resolves")
	}
	flags := tbl.Join(nil, nil).Flags
	want := map[string]bool{"route shop.example.org": true, "service prod/backend": true,
		"service prod/other": true, "x.example.org": true}
	if len(flags) != len(want) {
		t.Fatalf("flags %+v", flags)
	}
	for _, f := range flags {
		if !want[f.Subject] {
			t.Errorf("unexpected flag %+v", f)
		}
	}
}

// TestRouteWildcard: "*.example.org" matches one label, never two, and
// an exact route wins over it.
func TestRouteWildcard(t *testing.T) {
	tbl := Compile(mustParse(t, `routes:
  - {fqdn: "*.example.org", ingress: traefik, service: prod/web}
  - {fqdn: api.example.org, ingress: traefik, service: prod/api}
`), nil, nil)
	for host, want := range map[string]string{"shop.example.org": "prod/web", "api.example.org": "prod/api",
		"a.b.example.org": "", "example.org": ""} {
		r, _, _ := tbl.Route(host)
		if r.Service != want {
			t.Errorf("%s routed to %q, want %q", host, r.Service, want)
		}
	}
}

// TestJoinNegative: what the map does not name stays out — an unmatched
// address, a fused connection the map knows nothing of, a nil table —
// and a Service without endpoints is never called stale.
func TestJoinNegative(t *testing.T) {
	flows := []Flow{
		{From: "a", Endpoint: netip.MustParseAddrPort("198.51.100.7:80")},
		{From: "a", Endpoint: netip.MustParseAddrPort("10.0.2.2:8080"), Fused: true, ServerHost: "hostB",
			Hostname: "shop.example.org"},
	}
	if r := (*Table)(nil).Join(flows, nil); len(r.Edges) != 0 || len(r.Flags) != 0 {
		t.Errorf("nil table joined %+v", r)
	}
	tbl := Compile(mustParse(t, goodRoutes), nil, nil)
	if r := tbl.Join(flows, nil); len(r.Edges) != 0 || len(r.Flags) != 0 {
		t.Errorf("unnamed flows joined %+v", r)
	}
	bare := Compile(mustParse(t, "services:\n  - {service: prod/x, cluster_ips: [10.96.0.7], ports: [{port: 80}]}\n"),
		nil, nil)
	r := bare.Join([]Flow{{From: "a", Endpoint: netip.MustParseAddrPort("10.96.0.7:80"), Fused: true,
		ServerHost: "pod-x", ServerAddr: netip.MustParseAddrPort("10.244.0.9:80")}}, nil)
	if len(r.Flags) != 0 || len(r.Edges) != 1 || r.Edges[0].Pod != "pod-x" {
		t.Errorf("endpoint-less service: %+v", r)
	}
}

// TestJoinDirectContradiction: a client fused straight to a pod (no
// ClusterIP) while asking for a host whose route's Service does not
// list that pod: the route is flagged, and no routed edge replaces the
// fused fact. A Host with a port is the same host.
func TestJoinDirectContradiction(t *testing.T) {
	tbl := Compile(mustParse(t, goodMap), nil, nil)
	r := tbl.Join([]Flow{{From: "a", Endpoint: netip.MustParseAddrPort("10.244.7.7:8080"), Fused: true,
		ServerHost: "pod-other", ServerAddr: netip.MustParseAddrPort("10.244.7.7:8080"),
		HTTPHosts: []string{"shop.example.org:8080"}}}, nil)
	if len(r.Edges) != 0 || len(r.Flags) != 1 || r.Flags[0].Kind != FlagContradicted {
		t.Errorf("got %+v", r)
	}
	// Reaching a listed endpoint by address agrees with the route.
	r = tbl.Join([]Flow{{From: "a", Endpoint: netip.MustParseAddrPort("10.244.2.6:8080"), Fused: true,
		ServerHost: "hostB", ServerAddr: netip.MustParseAddrPort("10.244.2.6:8080"),
		HTTPHosts: []string{"shop.example.org"}}}, nil)
	if len(r.Flags) != 0 {
		t.Errorf("listed endpoint flagged: %+v", r.Flags)
	}
}
