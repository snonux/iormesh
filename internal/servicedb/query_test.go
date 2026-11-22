package servicedb

import (
	"context"
	"reflect"
	"testing"

	"iormesh/internal/schema"
)

// TestEndpoints checks one listing in full: counts, TLS hints, clients,
// server apps and requests per endpoint; the no-role half names none.
func TestEndpoints(t *testing.T) {
	ctx := context.Background()
	d, _ := openTemp(t)
	importSnap(t, d, observeMesh(t))
	eps, err := d.Endpoints(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	orders, inventory := schema.AppNodeID("shop", "orders"), schema.AppNodeID("inv", "inventory")
	want := []Endpoint{
		{IP: "10.0.0.2", Port: 8443, Conns: 1, Unresolved: 1, First: wall0 + 70, Last: wall0 + 70,
			Clients: []string{Untraced}, ServerApps: []string{inventory}},
		{IP: "127.0.0.1", Port: 9000, Host: "hA", Conns: 1, TLS: "plain", First: wall0 + 50, Last: wall0 + 560,
			Clients: []string{"curl"}, ServerApps: []string{orders},
			Requests: []EndpointRequest{{"GET", "/orders", 1}}},
		{IP: "203.0.113.9", Port: 5432, Conns: 1, Unresolved: 1, First: wall0 + 40, Last: wall0 + 40,
			Clients: []string{orders}},
		{Hostname: "inventory.svc", IP: "10.0.0.2", Port: 8443, Conns: 1, TLS: "tls",
			First: wall0 + 10, Last: wall0 + 535, Clients: []string{orders},
			ServerApps: []string{inventory}, Requests: []EndpointRequest{{"GET", "/items", 2}}},
	}
	if !reflect.DeepEqual(eps, want) {
		t.Errorf("endpoints:\n got %+v\nwant %+v", eps, want)
	}
	inv, err := d.Endpoints(ctx, Filter{Project: "inv"})
	if err != nil {
		t.Fatal(err)
	}
	if len(inv) != 2 || inv[0].Port != 8443 || inv[1].Hostname != "inventory.svc" {
		t.Errorf("project inv: %+v, want its two 8443 endpoints", inv)
	}
	// Several projects (a build of them together): every connection
	// either traced, each once.
	both, err := d.Endpoints(ctx, Filter{Projects: []string{"inv", "shop"}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(both, want) {
		t.Errorf("projects inv+shop:\n got %+v\nwant %+v", both, want)
	}
	none, err := d.Endpoints(ctx, Filter{Projects: []string{"nosuch"}})
	if err != nil || len(none) != 0 {
		t.Errorf("project nosuch: %+v, %v", none, err)
	}
}

// TestFilterHosts: Hosts narrows the views to the connections one of
// those traced hosts saw (either end), and ANDs with the projects — what
// a namespace-filtered build reads (task sh4).
func TestFilterHosts(t *testing.T) {
	ctx := context.Background()
	d, _ := openTemp(t)
	importSnap(t, d, observeMesh(t))
	ports := func(f Filter) []string {
		t.Helper()
		eps, err := d.Endpoints(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range eps {
			out = append(out, e.Hostname+"/"+e.IP)
		}
		return out
	}
	for name, c := range map[string]struct {
		f    Filter
		want []string
	}{
		"hB":         {Filter{Hosts: []string{"hB"}}, []string{"/10.0.0.2", "inventory.svc/10.0.0.2"}},
		"hA":         {Filter{Hosts: []string{"hA"}}, []string{"/127.0.0.1", "/203.0.113.9", "inventory.svc/10.0.0.2"}},
		"shop on hB": {Filter{Project: "shop", Hosts: []string{"hB"}}, []string{"inventory.svc/10.0.0.2"}},
		"nosuch":     {Filter{Hosts: []string{"nosuch"}}, nil},
	} {
		if got := ports(c.f); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: endpoints %v, want %v", name, got, c.want)
		}
	}
	in, err := d.Interactions(ctx, Filter{Hosts: []string{"nosuch"}})
	if err != nil || len(in) != 0 {
		t.Errorf("interactions of no host: %+v, %v", in, err)
	}
	in, err = d.Interactions(ctx, Filter{Hosts: []string{"hB"}})
	if err != nil || len(in) != 2 {
		t.Errorf("interactions of hB: %+v, %v; want its two", in, err)
	}
}

// TestInteractions: who talks to whom, by service name once the rules
// name the endpoints — and a client is named after the service it
// serves, so the edge reads "orders → inventory" (PLAN.md M4).
func TestInteractions(t *testing.T) {
	ctx := context.Background()
	d, _ := openTemp(t)
	importSnap(t, d, observeMesh(t))
	for _, r := range []Rule{{HostGlob: "inventory*", Port: 8443, Service: "inventory"},
		{HostGlob: "10.0.0.2", Port: 8443, Service: "inventory"},
		{HostGlob: "127.0.0.1", Port: 9000, Service: "orders"}} {
		if _, err := d.AddRule(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := d.Interactions(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	want := []Interaction{
		{From: Untraced, To: "inventory", Conns: 1, Unresolved: 1, First: wall0 + 70, Last: wall0 + 70},
		{From: "curl", To: "orders", Conns: 1, First: wall0 + 50, Last: wall0 + 560},
		{From: "orders", To: "203.0.113.9:5432", Conns: 1, Unresolved: 1, First: wall0 + 40, Last: wall0 + 40},
		{From: "orders", To: "inventory", Conns: 1, First: wall0 + 10, Last: wall0 + 535},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("interactions:\n got %+v\nwant %+v", got, want)
	}
}

// TestInteractionsUnnamedClient: an app whose endpoints are not all
// named the same keeps its app id rather than a guessed name.
func TestInteractionsUnnamedClient(t *testing.T) {
	ctx := context.Background()
	d, _ := openTemp(t)
	importSnap(t, d, observeMesh(t))
	got, err := d.Interactions(ctx, Filter{Project: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	orders := schema.AppNodeID("shop", "orders")
	var froms []string
	for _, in := range got {
		froms = append(froms, in.From)
	}
	if want := []string{orders, orders, "curl"}; !reflect.DeepEqual(froms, want) {
		t.Errorf("clients %v, want %v", froms, want)
	}
}
