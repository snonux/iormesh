package routemap

import (
	"net/netip"
	"strings"
	"testing"
)

const (
	uid1 = "0b6e7a52-1111-4c3e-9a63-000000000001"
	uid2 = "0b6e7a52-1111-4c3e-9a63-000000000002"
	uidT = "0b6e7a52-1111-4c3e-9a63-0000000000ff"
)

// TestReusedPodIP: the map lists 10.244.1.5 as web-abc (UID 1), but the
// traced server end is pod-<UID 2> — web-xyz, which got the address
// after web-abc went. The address must not name web-abc: the end is not
// listed, and the Service is flagged stale. An endpoint without a UID
// still matches by address.
func TestReusedPodIP(t *testing.T) {
	tbl := Compile(mustParse(t, `services:
  - service: prod/web
    cluster_ips: [10.96.0.20]
    ports: [{port: 80}]
    endpoints: [{addr: 10.244.1.5, pod: prod/web-abc, uid: `+uid1+`}, {addr: 10.244.1.6}]
`), nil, nil)
	flow := func(host, addr string) Flow {
		return Flow{From: "a", Endpoint: netip.MustParseAddrPort("10.96.0.20:80"), Fused: true,
			ServerHost: host, ServerAddr: netip.MustParseAddrPort(addr)}
	}
	r := tbl.Join([]Flow{flow("pod-"+uid2, "10.244.1.5:80")}, nil)
	if len(r.Edges) != 1 || r.Edges[0].Pod != "pod-"+uid2 {
		t.Errorf("reused IP named %+v", r.Edges)
	}
	if len(r.Flags) != 1 || r.Flags[0].Kind != FlagStaleService {
		t.Errorf("reused IP flags %+v, want stale_service", r.Flags)
	}
	r = tbl.Join([]Flow{flow("pod-"+uid1, "10.244.1.5:80"), flow("pod-"+uid2, "10.244.1.6:80"),
		flow("hostB", "10.244.1.5:80")}, nil)
	if len(r.Flags) != 0 {
		t.Errorf("listed ends flagged: %+v", r.Flags)
	}
}

// ingressMap routes the shop FQDN through Traefik, whose own Service is
// named on the route.
const ingressMap = `routes:
  - {fqdn: shop.example.org, ingress: traefik, service: prod/backend, ingress_service: kube-system/traefik}
services:
  - {service: prod/backend, cluster_ips: [10.96.0.20], ports: [{port: 80}],
     endpoints: [{addr: 10.244.1.5, uid: ` + uid1 + `}]}
`

const traefikService = `  - {service: kube-system/traefik, cluster_ips: [10.96.0.100], ports: [{port: 80}],
     endpoints: [{addr: 10.244.9.1, uid: ` + uidT + `}]}
`

// TestInClusterIngress: in-cluster clients asking for the FQDN through
// Traefik get the route's edge and no contradiction — (A) by Traefik's
// ClusterIP with its Service in the map, (B) by an address in the
// Service CIDR with Traefik's Service missing (flagged unknown, the
// route still tried), (C) fused to a traced Traefik pod, through its
// ClusterIP or straight to the pod.
func TestInClusterIngress(t *testing.T) {
	ask := func(endpoint string, fused bool, host, addr string) Flow {
		f := Flow{From: "checkout", Endpoint: netip.MustParseAddrPort(endpoint), Hostname: "shop.example.org",
			HTTPHosts: []string{"shop.example.org"}, Fused: fused, ServerHost: host}
		if addr != "" {
			f.ServerAddr = netip.MustParseAddrPort(addr)
		}
		return f
	}
	withTraefik := Compile(mustParse(t, ingressMap+traefikService), nil, nil)
	without := Compile(mustParse(t, ingressMap+"service_cidrs: [10.96.0.0/12]\n"), nil, nil)
	for name, c := range map[string]struct {
		tbl   *Table
		flow  Flow
		flags int
	}{
		"A: traefik clusterip":    {withTraefik, ask("10.96.0.100:80", false, "", ""), 0},
		"B: unmapped traefik":     {without, ask("10.96.0.100:80", false, "", ""), 1},
		"C: fused via clusterip":  {withTraefik, ask("10.96.0.100:80", true, "pod-"+uidT, "10.244.9.1:8000"), 0},
		"C: fused straight to it": {withTraefik, ask("10.244.9.1:8000", true, "pod-"+uidT, "10.244.9.1:8000"), 0},
	} {
		r := c.tbl.Join([]Flow{c.flow}, nil)
		if len(r.Edges) != 1 || r.Edges[0].Basis != BasisRoute || r.Edges[0].Ingress != "traefik" ||
			r.Edges[0].Service != "prod/backend" {
			t.Errorf("%s: edges %+v, want the route's", name, r.Edges)
		}
		if len(r.Flags) != c.flags || (c.flags == 1 && r.Flags[0].Kind != FlagUnknownService) {
			t.Errorf("%s: flags %+v", name, r.Flags)
		}
	}
	// Another pod than Traefik's, reached by the FQDN, still contradicts.
	r := withTraefik.Join([]Flow{ask("10.244.7.7:80", true, "pod-"+uid2, "10.244.7.7:80")}, nil)
	if len(r.Flags) != 1 || r.Flags[0].Kind != FlagContradicted {
		t.Errorf("non-ingress pod: %+v", r.Flags)
	}
	if _, err := Parse("bad", []byte("routes:\n  - {fqdn: a.example.org, ingress: t, service: p/b, "+
		"ingress_service: traefik}\n")); err == nil {
		t.Error("ingress_service without a namespace accepted")
	}
}

// TestRouteNamesSoleTracedPod: a routed edge names the pod by the
// ClusterIP join's single-endpoint rule when that pod was traced (task
// ff4) — its name, else its host id; two endpoints, an untraced pod or
// an endpoint without a UID name none.
func TestRouteNamesSoleTracedPod(t *testing.T) {
	ask := Flow{From: "gateway", Endpoint: netip.MustParseAddrPort("10.96.0.100:80"), Hostname: "shop.example.org"}
	one := ingressMap + traefikService
	named := strings.Replace(one, "endpoints: [{addr: 10.244.1.5, uid: "+uid1+"}]",
		"endpoints: [{addr: 10.244.1.5, pod: prod/backend-abc, uid: "+uid1+"}]", 1)
	two := strings.Replace(one, "endpoints: [{addr: 10.244.1.5, uid: "+uid1+"}]",
		"endpoints: [{addr: 10.244.1.5, uid: "+uid1+"}, {addr: 10.244.1.6, uid: "+uid2+"}]", 1)
	noUID := strings.Replace(one, "endpoints: [{addr: 10.244.1.5, uid: "+uid1+"}]",
		"endpoints: [{addr: 10.244.1.5}]", 1)
	both := map[string]bool{"pod-" + uid1: true, "pod-" + uid2: true}
	for name, c := range map[string]struct {
		m      string
		traced map[string]bool
		pod    string
		eps    int
	}{
		"one endpoint, traced":       {one, map[string]bool{"pod-" + uid1: true}, "pod-" + uid1, 1},
		"one named endpoint, traced": {named, map[string]bool{"pod-" + uid1: true}, "prod/backend-abc", 1},
		"two endpoints, traced":      {two, both, "", 2},
		"one endpoint, untraced":     {one, map[string]bool{"pod-" + uid2: true}, "", 1},
		"nothing traced":             {one, nil, "", 1},
		"one endpoint without uid":   {noUID, both, "", 1},
	} {
		r := Compile(mustParse(t, c.m), nil, nil).Join([]Flow{ask}, c.traced)
		if len(r.Edges) != 1 || r.Edges[0].Basis != BasisRoute || r.Edges[0].Pod != c.pod ||
			r.Edges[0].Endpoints != c.eps {
			t.Errorf("%s: edges %+v, want pod %q of %d endpoints", name, r.Edges, c.pod, c.eps)
		}
	}
}
