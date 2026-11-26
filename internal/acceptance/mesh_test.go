package acceptance

import (
	"strconv"
	"strings"
	"testing"

	"iormesh/internal/viewmodel"
)

// loopbackModel is the refresh's model as the build labels it since task
// ai4: a loopback endpoint ends in "@host", and a named service keeps
// its name with the labelled endpoint behind it (the Rocky 9.9 run of
// task 3k4).
func loopbackModel(c Config) viewmodel.Model {
	ep := func(off int) string { return "127.0.0.1:" + strconv.Itoa(c.port(off)) + "@a425740679a84b37" }
	svcs := []viewmodel.Service{
		{Name: "m4-orders-svc", Named: true, Endpoints: []string{ep(portOrders)}, Conns: 1, TLS: "tls"},
		{Name: ep(portBilling), Endpoints: []string{ep(portBilling)}, Conns: 1, TLS: "tls"},
		{Name: ep(portInventory), Endpoints: []string{ep(portInventory)}, Conns: 2, TLS: "plain"},
	}
	var edges []viewmodel.Edge
	for _, s := range svcs {
		edges = append(edges, viewmodel.Edge{From: "gateway", To: s.Name, State: viewmodel.StateFused, Conns: s.Conns})
	}
	return viewmodel.Model{Services: svcs, Edges: edges}
}

// TestCheckMicroLoopbackHost pins M4-8 on ai4's labels: each loopback
// server is found by its port although its endpoint ends in "@host".
func TestCheckMicroLoopbackHost(t *testing.T) {
	c := Config{PortBase: 18200}
	got, err := checkMicro(loopbackModel(c), c)
	if err != nil {
		t.Fatalf("checkMicro: %v", err)
	}
	if !strings.Contains(got, "m4-orders-svc tls x1") {
		t.Errorf("checkMicro = %q, want the named orders service", got)
	}
}

// TestCheckMicroLoopbackEdgeNotFused is the negative: an edge to a
// loopback server that is not fused still fails M4-8, the "@host"
// label notwithstanding, and so does one to a named server.
func TestCheckMicroLoopbackEdgeNotFused(t *testing.T) {
	c := Config{PortBase: 18200}
	for _, i := range []int{0, 2} {
		m := loopbackModel(c)
		m.Edges[i].State = viewmodel.StateUnresolved
		if _, err := checkMicro(m, c); err == nil || !strings.Contains(err.Error(), "is unresolved") {
			t.Errorf("edge %d unresolved: err = %v, want it named", i, err)
		}
	}
}

// TestEndpointPort pins the label parsing: the port before an optional
// "@host", for IPv4, bracketed IPv6 and hostnames; no port, no match.
func TestEndpointPort(t *testing.T) {
	for _, tc := range []struct {
		label string
		port  int
		ok    bool
	}{
		{"10.9.0.2:18213", 18213, true},
		{"127.0.0.1:18213@a4257406", 18213, true},
		{"[::1]:443@h1", 443, true},
		{"[fe80::1]:8080", 8080, true},
		{"db.example:5432@h2", 5432, true},
		{"m4-orders-svc", 0, false},
		{"127.0.0.1:x@h1", 0, false},
		{"127.0.0.1:182130", 182130, true},
	} {
		port, ok := endpointPort(tc.label)
		if port != tc.port || ok != tc.ok {
			t.Errorf("endpointPort(%q) = %d, %t; want %d, %t", tc.label, port, ok, tc.port, tc.ok)
		}
	}
}
