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
		{IP: "127.0.0.1", Port: 9000, Conns: 1, TLS: "plain", First: wall0 + 50, Last: wall0 + 560,
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
