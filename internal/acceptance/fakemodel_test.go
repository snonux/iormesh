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
