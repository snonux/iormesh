package servicedb

import (
	"context"
	"net/netip"
	"reflect"
	"testing"

	"iormesh/internal/schema"
)

// localConn is a fused connection on host between two apps of project,
// to ep: both ends on the one host, as a loopback connection is.
func localConn(host, id, client, server string, ep netip.AddrPort, at uint64) Observation {
	app := func(name string) string { return schema.AppNodeID("p", name) }
	return Observation{Host: host, ConnID: id, Role: RoleClient, State: schema.StateFused,
		MergedID: host + "/" + id, Project: "p",
		Client:   End{Project: "p", Host: host, ConnID: id, App: app(client), Comm: client},
		Server:   End{Project: "p", Host: host, ConnID: id + "s", App: app(server), Comm: server},
		Endpoint: ep, TLS: -1, First: at, Last: at}
}

// TestLocalEndpointsPerHost: the same loopback (or link-local) ip:port
// on two hosts is two endpoints — each host's own local service — and
// the two clients reach two different targets. A routable address
// shared by two hosts' clients stays one endpoint.
func TestLocalEndpointsPerHost(t *testing.T) {
	ctx := context.Background()
	d, _ := openTemp(t)
	lo := netip.MustParseAddrPort("127.0.0.1:5432")
	ll := netip.MustParseAddrPort("[fe80::1]:6379")
	shared := netip.MustParseAddrPort("10.0.0.9:443")
	importSnap(t, d, Snapshot{Conns: []Observation{
		localConn("hostA", "c1", "orders", "orders-db", lo, wall0+1),
		localConn("hostA", "c2", "orders", "orders-cache", ll, wall0+2),
		localConn("hostA", "c3", "orders", "api", shared, wall0+3),
		localConn("hostB", "c1", "billing", "billing-db", lo, wall0+4),
		localConn("hostB", "c2", "billing", "billing-cache", ll, wall0+5),
		localConn("hostB", "c3", "billing", "api", shared, wall0+6),
	}})
	eps, err := d.Endpoints(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		host, ip string
		port     uint16
		conns    int64
		clients  []string
	}
	var got []row
	for _, e := range eps {
		got = append(got, row{e.Host, e.IP, e.Port, e.Conns, e.Clients})
	}
	orders, billing := schema.AppNodeID("p", "orders"), schema.AppNodeID("p", "billing")
	want := []row{
		{"", "10.0.0.9", 443, 2, []string{billing, orders}},
		{"hostA", "127.0.0.1", 5432, 1, []string{orders}},
		{"hostB", "127.0.0.1", 5432, 1, []string{billing}},
		{"hostA", "fe80::1", 6379, 1, []string{orders}},
		{"hostB", "fe80::1", 6379, 1, []string{billing}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("endpoints:\n got %+v\nwant %+v", got, want)
	}
	edges, err := d.Interactions(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	from := map[string][]string{}
	for _, in := range edges {
		from[in.To] = append(from[in.To], in.From)
	}
	for to, clients := range from {
		if len(clients) > 1 && to != "10.0.0.9:443" {
			t.Errorf("target %q shared by %v: one host's local service drawn as another's", to, clients)
		}
	}
	if len(edges) != 6 {
		t.Errorf("edges %+v, want 6 (two shared, four local)", edges)
	}
}
