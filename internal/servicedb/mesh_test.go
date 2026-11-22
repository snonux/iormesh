package servicedb

import (
	"context"
	"net/netip"
	"path/filepath"
	"testing"

	"iormesh/internal/federate"
	"iormesh/internal/schema"
)

// wall0 is the synthetic traces' first wall-clock instant.
const wall0 = uint64(1_700_000_000_000_000_000)

// memSource is one host session held in memory: Observe only reads its
// events, so no graph is needed.
type memSource struct {
	name   string
	events []schema.Event
}

var _ federate.Source = memSource{}

func (m memSource) Name() string { return m.name }

func (m memSource) Graph(context.Context) (schema.Graph, error) { return schema.Graph{}, nil }

func (m memSource) Events(_ context.Context, fn func(*schema.Event) error) error {
	for i := range m.events {
		if err := fn(&m.events[i]); err != nil {
			return err
		}
	}
	return nil
}

func ptr[T any](v T) *T { return &v }

// ev builds one event of host/pid; the fields Observe reads only.
func ev(host, project string, pid uint32, kind string, wall uint64, conn string) schema.Event {
	return schema.Event{HostID: host, Project: project, PID: pid, PIDInc: 1, Event: kind,
		WallNS: wall, ConnID: conn}
}

// withTuple sets the 4-tuple args from "addr:port" strings.
func withTuple(e schema.Event, local, remote string) schema.Event {
	l, r := netip.MustParseAddrPort(local), netip.MustParseAddrPort(remote)
	e.Args = schema.Args{LocalAddr: ptr(l.Addr().String()), LocalPort: ptr(l.Port()),
		RemoteAddr: ptr(r.Addr().String()), RemotePort: ptr(r.Port())}
	return e
}

func httpEv(host, project string, pid uint32, wall uint64, conn, scheme, reqHost, path string) schema.Event {
	e := ev(host, project, pid, schema.KindHTTP, wall, conn)
	e.LatencyNS = 500
	e.HTTP = &schema.HTTP{Method: "GET", Scheme: scheme, Host: reqHost, Path: path, Status: 200}
	return e
}

func dnsEv(host, project string, pid uint32, wall uint64, name string, addrs ...string) schema.Event {
	e := ev(host, project, pid, schema.KindDNS, wall, "")
	e.DNS = &schema.DNS{Host: name, Addrs: addrs}
	return e
}

// procID returns the process node id of host's pid.
func procID(host string, pid uint32) string { return schema.ProcessNodeID(host, pid, 1) }

// mesh is the test fixture, two hosts and two projects:
//
//   - hA (shop): orders (pid 100) calls inventory.svc:8443 on hB over
//     https, twice, fused; it calls 203.0.113.9:5432 (untraced: no_match);
//     curl (pid 102) calls orders on 127.0.0.1:9000, a same-host pair.
//   - hB (inv): inventory (pid 200) accepted the fused call, and one from
//     the untraced 10.9.9.9 (server half, no_match); one half had no role.
func mesh() ([]federate.Source, federate.Result) {
	a := memSource{name: "hA", events: []schema.Event{
		dnsEv("hA", "shop", 100, wall0, "inventory.svc", "10.0.0.2", "0:0:0:0:0:0:0:1"),
		withTuple(ev("hA", "shop", 100, schema.KindConnect, wall0+10, "a1"), "10.0.0.1:40000", "10.0.0.2:8443"),
		httpEv("hA", "shop", 100, wall0+20, "a1", "https", "inventory.svc", "/items"),
		httpEv("hA", "shop", 100, wall0+30, "a1", "https", "inventory.svc", "/items"),
		withTuple(ev("hA", "shop", 100, schema.KindConnect, wall0+40, "a2"), "10.0.0.1:40001", "203.0.113.9:5432"),
		withTuple(ev("hA", "shop", 102, schema.KindConnect, wall0+50, "lo"), "127.0.0.1:41000", "127.0.0.1:9000"),
		withTuple(ev("hA", "shop", 100, schema.KindAccept4, wall0+51, "lo"), "127.0.0.1:9000", "127.0.0.1:41000"),
		httpEv("hA", "shop", 102, wall0+60, "lo", "http", "127.0.0.1", "/orders"),
		httpEv("hA", "shop", 100, wall0+60, "lo", "http", "", "/orders"),
	}}
	b := memSource{name: "hB", events: []schema.Event{
		withTuple(ev("hB", "inv", 200, schema.KindAccept4, wall0+15, "b1"), "10.0.0.2:8443", "10.0.0.1:40000"),
		httpEv("hB", "inv", 200, wall0+25, "b1", "https", "", "/items"),
		httpEv("hB", "inv", 200, wall0+35, "b1", "https", "", "/items"),
		ev("hB", "inv", 200, schema.KindClose, wall0+90, "b1"),
		withTuple(ev("hB", "inv", 200, schema.KindAccept4, wall0+70, "b2"), "10.0.0.2:8443", "10.9.9.9:5555"),
		ev("hB", "inv", 200, schema.KindClose, wall0+80, "b3"),
	}}
	return []federate.Source{a, b}, meshResult()
}

// meshResult is what federate.Merge would make of mesh's traces.
func meshResult() federate.Result {
	half := func(host, project, conn, role string, pid uint32, local, peer string) federate.Half {
		h := federate.Half{Host: host, Project: project, ConnID: conn, Role: role,
			Procs: []string{procID(host, pid)}, Established: wall0}
		if local != "" {
			h.Local = netip.MustParseAddrPort(local)
		}
		if peer != "" {
			h.Peer = netip.MustParseAddrPort(peer)
		}
		return h
	}
	r := federate.Result{Graph: meshGraph()}
	r.Conns = []federate.Conn{{ID: "m1",
		Client: half("hA", "shop", "a1", federate.RoleClient, 100, "10.0.0.1:40000", "10.0.0.2:8443"),
		Server: half("hB", "inv", "b1", federate.RoleServer, 200, "10.0.0.2:8443", "10.0.0.1:40000")}}
	r.Unresolved = []federate.Unresolved{
		{Half: half("hA", "shop", "a2", federate.RoleClient, 100, "10.0.0.1:40001", "203.0.113.9:5432"),
			Reason: federate.ReasonNoMatch},
		{Half: half("hB", "inv", "b2", federate.RoleServer, 200, "10.0.0.2:8443", "10.9.9.9:5555"),
			Reason: federate.ReasonNoMatch},
		{Half: half("hB", "inv", "b3", "", 200, "10.0.0.2:8443", "10.0.0.7:6000"),
			Reason: federate.ReasonNoRole},
	}
	return r
}

// meshGraph names mesh's processes and apps; curl has no app.
func meshGraph() schema.Graph {
	g := schema.Graph{SchemaVersion: schema.Version}
	for _, p := range []struct {
		host string
		pid  uint32
		comm string
		app  string
	}{{"hA", 100, "java", schema.AppNodeID("shop", "orders")}, {"hA", 102, "curl", ""},
		{"hB", 200, "java", schema.AppNodeID("inv", "inventory")}} {
		id := procID(p.host, p.pid)
		g.Nodes = append(g.Nodes, schema.Node{Type: schema.NodeProcess, ID: id,
			Attrs: map[string]string{"comm": p.comm}})
		if p.app != "" {
			g.Nodes = append(g.Nodes, schema.Node{Type: schema.NodeApp, ID: p.app})
			g.Edges = append(g.Edges, schema.Edge{Type: schema.EdgeIncarnation, From: p.app, To: id})
		}
	}
	return g
}

// observeMesh runs Observe over the fixture.
func observeMesh(t *testing.T) Snapshot {
	t.Helper()
	sources, r := mesh()
	s, err := Observe(context.Background(), sources, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// openTemp opens a fresh database in a temp directory.
func openTemp(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "iormesh", FileName)
	d, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d, path
}
