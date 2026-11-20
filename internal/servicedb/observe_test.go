package servicedb

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"iormesh/internal/federate"
	"iormesh/internal/schema"
)

// TestObserve checks every kind of connection the fixture holds: fused
// across hosts, unresolved client, unresolved server, no role, and a
// same-host pair.
func TestObserve(t *testing.T) {
	s := observeMesh(t)
	orders := End{Project: "shop", Host: "hA", App: schema.AppNodeID("shop", "orders"), Comm: "java"}
	inventory := End{Project: "inv", Host: "hB", App: schema.AppNodeID("inv", "inventory"), Comm: "java"}
	with := func(e End, conn string) End { e.ConnID = conn; return e }
	ap := netip.MustParseAddrPort
	want := []Observation{
		{Host: "hA", ConnID: "a1", Role: RoleClient, State: schema.StateFused, MergedID: "m1",
			Project: "shop", Client: with(orders, "a1"), Server: with(inventory, "b1"),
			Endpoint: ap("10.0.0.2:8443"), Hostname: "inventory.svc", TLS: 1,
			First: wall0 + 10, Last: wall0 + 535,
			Requests: []Request{{Source: "hA", Method: "GET", Scheme: "https", Host: "inventory.svc",
				Path: "/items", Count: 2, First: wall0 + 20, Last: wall0 + 530},
				{Source: "hB", Method: "GET", Scheme: "https", Path: "/items", Count: 2,
					First: wall0 + 25, Last: wall0 + 535}}},
		{Host: "hA", ConnID: "a2", Role: RoleClient, State: schema.StateUnresolved,
			Reason: federate.ReasonNoMatch, Project: "shop", Client: with(orders, "a2"),
			Endpoint: ap("203.0.113.9:5432"), TLS: -1, First: wall0 + 40, Last: wall0 + 40},
		{Host: "hA", ConnID: "lo", Role: RoleClient, State: schema.StateFused, MergedID: "lo",
			Project: "shop", Client: End{Project: "shop", Host: "hA", ConnID: "lo", Comm: "curl"},
			Server: with(orders, "lo"), Endpoint: ap("127.0.0.1:9000"), TLS: 0,
			First: wall0 + 50, Last: wall0 + 560,
			Requests: []Request{{Source: "hA", Method: "GET", Scheme: "http", Path: "/orders",
				Count: 1, First: wall0 + 60, Last: wall0 + 560},
				{Source: "hA", Method: "GET", Scheme: "http", Host: "127.0.0.1",
					Path: "/orders", Count: 1, First: wall0 + 60, Last: wall0 + 560}}},
		{Host: "hB", ConnID: "b2", Role: RoleServer, State: schema.StateUnresolved,
			Reason: federate.ReasonNoMatch, Project: "inv", Server: with(inventory, "b2"),
			Endpoint: ap("10.0.0.2:8443"), TLS: -1, First: wall0 + 70, Last: wall0 + 70},
		{Host: "hB", ConnID: "b3", Role: RoleUnknown, State: schema.StateUnresolved,
			Reason: federate.ReasonNoRole, Project: "inv", Client: with(inventory, "b3"),
			Endpoint: ap("10.0.0.7:6000"), TLS: -1, First: wall0 + 80, Last: wall0 + 80},
	}
	if len(s.Conns) != len(want) {
		t.Fatalf("%d observations, want %d: %+v", len(s.Conns), len(want), s.Conns)
	}
	for i := range want {
		if !reflect.DeepEqual(s.Conns[i], want[i]) {
			t.Errorf("observation %d:\n got %+v\nwant %+v", i, s.Conns[i], want[i])
		}
	}
}

// TestObserveDeterministic: source order changes nothing.
func TestObserveDeterministic(t *testing.T) {
	sources, r := mesh()
	first := observeMesh(t)
	rev := []federate.Source{sources[1], sources[0]}
	again, err := Observe(context.Background(), rev, r)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, again) {
		t.Errorf("source order changed the snapshot")
	}
}

// TestObserveDNSAfterConnect: a lookup recorded after the connection's
// first record did not lead to it and names nothing.
func TestObserveDNSAfterConnect(t *testing.T) {
	sources, r := mesh()
	a := sources[0].(memSource)
	a.events[0].WallNS = wall0 + 1000
	sources[0] = a
	s, err := Observe(context.Background(), sources, r)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Conns[0].Hostname; got != "inventory.svc" {
		// The client's https request still carries the name.
		t.Errorf("hostname %q, want the http host", got)
	}
	for i := range a.events {
		if a.events[i].HTTP != nil {
			a.events[i].HTTP.Host = "10.0.0.2"
		}
	}
	if s, err = Observe(context.Background(), sources, r); err != nil {
		t.Fatal(err)
	}
	if got := s.Conns[0].Hostname; got != "" {
		t.Errorf("hostname %q from a late lookup and an IP-literal host, want none", got)
	}
}

// TestObserveRejectsCorruptSource (negative): a trace that does not
// decode fails the observation instead of yielding a partial snapshot.
func TestObserveRejectsCorruptSource(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "events.ndjson"), []byte("{not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, r := mesh()
	if _, err := Observe(context.Background(), []federate.Source{federate.NDJSONDir{Dir: dir}}, r); err == nil {
		t.Fatal("corrupt events.ndjson observed without error")
	}
	boom := errors.New("boom")
	if _, err := Observe(context.Background(), []federate.Source{failing{boom}}, r); !errors.Is(err, boom) {
		t.Fatalf("source error %v, want %v", err, boom)
	}
}

// failing is a source whose events cannot be read.
type failing struct{ err error }

func (f failing) Name() string                                { return "failing" }
func (f failing) Graph(context.Context) (schema.Graph, error) { return schema.Graph{}, nil }
func (f failing) Events(context.Context, func(*schema.Event) error) error {
	return f.err
}
