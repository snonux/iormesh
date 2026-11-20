package acceptance

import (
	"fmt"
	"slices"
	"time"

	"iormesh/internal/schema"
	"iormesh/internal/viewmodel"
)

// model is the view model the fake build writes for projects.
func (w *fakeWorld) model(projects []string) viewmodel.Model {
	m := viewmodel.Model{Run: viewmodel.Run{Projects: projects, Hosts: []string{w.cfg.Host1, w.cfg.Host2},
		Segments: 3, FusedConns: 1}}
	switch {
	case slices.Equal(projects, []string{projShop}):
		m.Run.FusedConns, m.Run.UnresolvedConns = 0, 1
	case slices.Equal(projects, []string{projBilling, projShop}):
		m.Health.Clocks = []viewmodel.Clock{{HostA: "h1.test", HostB: "h2.test", Windows: 2,
			MinWidth: 300 * time.Microsecond, MaxWidth: 700 * time.Microsecond}}
	case slices.Equal(projects, []string{projDisk}):
		m.Run.Segments = 1
		m.Health.Sessions = []viewmodel.Session{{Project: projDisk, Host: w.cfg.Host2, End: "disk_limit"}}
	case slices.Equal(projects, spreadNames()):
		m.Run.FusedConns = 2
		m.Services, m.Edges = w.spreadServices()
	case len(projects) > 4:
		m.Services, m.Edges = w.microServices()
		m.Health.Sessions = []viewmodel.Session{{Project: projSingle, Host: w.cfg.Host1, End: "stop"}}
	}
	return m
}

// microServices are the refresh's services and edges: the three
// microservices servers, and the two-host server named after naming.
func (w *fakeWorld) microServices() ([]viewmodel.Service, []viewmodel.Edge) {
	ep := func(off int) string { return fmt.Sprintf("127.0.0.1:%d", w.cfg.port(off)) }
	orders, billing := ep(portOrders), "10.9.0.2:"+fmt.Sprint(w.cfg.port(portTwoHost))
	named := false
	if w.named {
		orders, billing, named = nameOrders, nameBilling, true
	}
	svcs := []viewmodel.Service{
		{Name: orders, Named: named, Endpoints: []string{ep(portOrders)}, Conns: 1, TLS: "tls"},
		{Name: ep(portBilling), Endpoints: []string{ep(portBilling)}, Conns: 1, TLS: "tls"},
		{Name: ep(portInventory), Endpoints: []string{ep(portInventory)}, Conns: 2, TLS: "plain"},
		{Name: billing, Named: named, Endpoints: []string{billing}, Conns: 1, TLS: "plain"},
	}
	var edges []viewmodel.Edge
	for _, s := range svcs {
		edges = append(edges, viewmodel.Edge{From: "app", To: s.Name, State: viewmodel.StateFused, Conns: s.Conns})
	}
	return svcs, edges
}

// spreadNames are the spread's projects as a build names them, sorted.
func spreadNames() []string {
	return []string{projSpreadBil, projSpreadGW, projSpreadInv, projSpreadOrd}
}

// spreadServices are the spread build's servers at their hosts'
// addresses (the fake's getent answers), every edge fused.
func (w *fakeWorld) spreadServices() ([]viewmodel.Service, []viewmodel.Edge) {
	ep := func(addr string, off int) string { return fmt.Sprintf("%s:%d", addr, w.cfg.port(off)) }
	svcs := []viewmodel.Service{
		{Name: ep("10.9.0.2", portSpreadOrders), Endpoints: []string{ep("10.9.0.2", portSpreadOrders)},
			Conns: 1, TLS: "tls"},
		{Name: ep("10.9.0.2", portSpreadBilling), Endpoints: []string{ep("10.9.0.2", portSpreadBilling)},
			Conns: 1, TLS: "tls"},
		{Name: ep("10.9.0.1", portSpreadInventory),
			Endpoints: []string{ep("10.9.0.1", portSpreadInventory)}, Conns: 2, TLS: "plain"},
	}
	var edges []viewmodel.Edge
	for _, s := range svcs {
		edges = append(edges, viewmodel.Edge{From: "app", To: s.Name, State: viewmodel.StateFused, Conns: s.Conns})
	}
	return svcs, edges
}

// fakeSpreadGraph fuses two connections, each between a process on hA
// and one on hB.
func fakeSpreadGraph() schema.Graph {
	g := schema.Graph{SchemaVersion: schema.Version}
	a, b := schema.ProcessNodeID("hA", 1, 1), schema.ProcessNodeID("hB", 2, 1)
	for _, c := range []string{"c1", "c2"} {
		conn := schema.SocketNodeID(c)
		g.Nodes = append(g.Nodes, schema.Node{Type: "socket", ID: conn})
		g.Edges = append(g.Edges, schema.Edge{Type: "connected", From: a, To: conn, State: schema.StateFused},
			schema.Edge{Type: "connected", From: b, To: conn, State: schema.StateFused})
	}
	g.Nodes = append(g.Nodes, schema.Node{Type: "process", ID: a}, schema.Node{Type: "process", ID: b})
	g.Sort()
	return g
}

// query is the NAT pair's connection seen from both ends: the client
// dialed the DNAT'ed port, the server accepted on the inner one. Any
// other slice is empty, which the single-host golden must reject.
func (w *fakeWorld) query(projects []string) string {
	if !slices.Equal(projects, []string{projNATClient, projNATServer}) {
		return ""
	}
	dial, inner := uint16(w.cfg.NATPort), uint16(w.cfg.port(portNATInner))
	connect := fakeEvent("hA", "connect", 1)
	connect.Args.RemotePort = &dial
	accept := fakeEvent("hB", "accept4", 1)
	accept.Args.LocalPort = &inner
	b := append(schema.AppendEvent(nil, &connect), '\n')
	return string(append(schema.AppendEvent(b, &accept), '\n'))
}

func fakeEvent(host, kind string, seq uint64) schema.Event {
	return schema.Event{SchemaVersion: schema.Version, Project: projNATClient, Seq: seq, BootNS: 1,
		WallNS: 1, HostID: host, PID: 7, PIDInc: 1, TID: 7, Comm: "java", Event: kind, FD: 5,
		ConnID: "c1", Attribution: "none"}
}
