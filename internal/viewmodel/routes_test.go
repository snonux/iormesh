package viewmodel

import (
	"net/netip"
	"testing"

	"iormesh/internal/routemap"
	"iormesh/internal/schema"
	"iormesh/internal/servicedb"
)

// routeUID is the backend pod's UID in TestRoutesNameTracedPod.
const routeUID = "0b6e7a52-1111-4c3e-9a63-000000000001"

// TestRoutesNameTracedPod: a routed call through the ingress names the
// Service's sole endpoint once the build traced that pod — here by the
// server end of the ingress's own connection to it, as on kind (task
// ff4) — and names none without it.
func TestRoutesNameTracedPod(t *testing.T) {
	m, err := routemap.Parse("routes", []byte(`routes:
  - {fqdn: shop.example.org, ingress: traefik, service: prod/inventory, ingress_service: traefik/traefik}
services:
  - {service: traefik/traefik, cluster_ips: [10.96.0.100], ports: [{port: 80}]}
  - {service: prod/inventory, cluster_ips: [10.96.0.20], ports: [{port: 80}],
     endpoints: [{addr: 10.244.1.5, pod: prod/inventory-abc, uid: `+routeUID+`}]}
`))
	if err != nil {
		t.Fatal(err)
	}
	tbl := routemap.Compile(m, nil, nil)
	gateway := servicedb.Observation{Host: "pod-gw", Role: servicedb.RoleClient, State: schema.StateUnresolved,
		Client:   servicedb.End{Host: "pod-gw", Comm: "gateway"},
		Endpoint: netip.MustParseAddrPort("10.96.0.100:80"), Hostname: "shop.example.org"}
	backend := servicedb.Observation{Host: "pod-" + routeUID, Role: servicedb.RoleServer,
		State: schema.StateUnresolved, Server: servicedb.End{Host: "pod-" + routeUID, Comm: "java"},
		Endpoint: netip.MustParseAddrPort("10.244.1.5:80")}
	for name, c := range map[string]struct {
		obs []servicedb.Observation
		pod string
	}{
		"backend traced":   {[]servicedb.Observation{gateway, backend}, "prod/inventory-abc"},
		"backend untraced": {[]servicedb.Observation{gateway}, ""},
	} {
		rs, _ := routes(tbl, c.obs, nil, nil, tracedHosts(nil, c.obs))
		if len(rs) != 1 || rs[0].Basis != routemap.BasisRoute || rs[0].Pod != c.pod {
			t.Errorf("%s: routes %+v, want pod %q", name, rs, c.pod)
		}
	}
}

// TestTracedHosts: the tally's hosts and every traced end of an
// observation, never an empty one.
func TestTracedHosts(t *testing.T) {
	tally := NewTally()
	tally.fqdns["pod-a"] = "a.prod.00000000"
	got := tracedHosts(tally, []servicedb.Observation{
		{Host: "pod-b", Client: servicedb.End{Host: "pod-b"}},
		{Host: "pod-c", Server: servicedb.End{Host: "pod-c"}},
	})
	if len(got) != 3 || !got["pod-a"] || !got["pod-b"] || !got["pod-c"] || got[""] {
		t.Errorf("tracedHosts = %v", got)
	}
}
