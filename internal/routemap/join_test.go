package routemap

import (
	"encoding/json"
	"flag"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"iormesh/internal/federate"
	"iormesh/internal/schema"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden from the current output")

// Pod host ids of the synthetic cluster: an in-pod tracer's host id is
// "pod-<uid>" (docs/milestones.md M7, task 7f4).
const (
	clientPod = "pod-5c1e0d2a-2222-4d4e-8b7a-0a1b2c3d4e5f"
	backendA  = "pod-0b6e7a52-1111-4c3e-9a63-1d2f3e4a5b6c"
	clientIP  = "10.244.3.9"
)

// clusterMap is the test cluster's map: goodMap's ingress route and
// backend, plus a database with two endpoints, a cache with one, a
// legacy Service whose endpoint list is stale (the backend pod answers
// its ClusterIP now), and the Service CIDR.
const clusterMap = goodRoutes + `  - service: prod/db
    cluster_ips: [10.96.0.30]
    ports: [{port: 5432}]
    workload: prod/statefulset/db
    endpoints: [{addr: 10.244.4.1, pod: prod/db-0}, {addr: 10.244.5.1, pod: prod/db-1}]
  - service: prod/cache
    cluster_ips: [10.96.0.40]
    ports: [{port: 6379}]
    workload: prod/deployment/cache
    endpoints: [{addr: 10.244.6.1, pod: prod/cache-5f-x}]
  - service: prod/legacy
    cluster_ips: [10.96.0.50]
    ports: [{port: 80, target_port: 8080}]
    endpoints: [{addr: 10.244.9.9, pod: prod/legacy-old}]
`

// trace builds one pod's events as the tracer records them, enough for
// the merge's halves: tcp_state records with the four tcp_sock fields,
// connect or accept4 for the role, and http events with the host.
type trace struct {
	host   string
	seq    uint64
	events []schema.Event
}

func ptr[T any](v T) *T { return &v }

func (tr *trace) add(kind, conn string, at int, args schema.Args) *schema.Event {
	tr.seq++
	tr.events = append(tr.events, schema.Event{SchemaVersion: schema.Version, Project: "prod", Seq: tr.seq,
		HostID: tr.host, WallNS: uint64(1_763_600_000+at) * uint64(time.Second), PID: 7, PIDInc: 1, TID: 7,
		Comm: "java", Event: kind, FD: 5, ConnID: conn, Args: args, Attribution: schema.AttributionNone})
	return &tr.events[len(tr.events)-1]
}

func tuple(local, peer string) schema.Args {
	l, p := netip.MustParseAddrPort(local), netip.MustParseAddrPort(peer)
	return schema.Args{LocalAddr: ptr(l.Addr().String()), LocalPort: ptr(l.Port()),
		RemoteAddr: ptr(p.Addr().String()), RemotePort: ptr(p.Port())}
}

// dial is a client connection from the client pod to peer: connect, then
// ESTABLISHED with the client's ISN iss and the server's irs.
func (tr *trace) dial(conn string, at int, local, peer string, iss, irs uint32) {
	tr.add(schema.KindConnect, conn, at, tuple(local, peer))
	a := tuple(local, peer)
	a.SockState, a.SndUna, a.RcvNxt, a.BytesAcked, a.BytesReceived =
		ptr("ESTABLISHED"), ptr(iss+1), ptr(irs+1), ptr(uint64(1)), ptr(uint64(0))
	tr.add(schema.KindTCPState, conn, at, a)
}

// answer is the server pod's end of the same connection: the accepted
// child's snapshot, then ESTABLISHED, keyed by the same pair.
func (tr *trace) answer(conn string, at int, local, peer string, iss, irs uint32) {
	for _, state := range []string{"ESTABLISHED", "ESTABLISHED"} {
		a := tuple(local, peer)
		a.SockState, a.SndUna, a.RcvNxt, a.BytesAcked, a.BytesReceived =
			ptr(state), ptr(irs+1), ptr(iss+1), ptr(uint64(0)), ptr(uint64(0))
		tr.add(schema.KindTCPState, conn, at, a)
	}
	tr.add(schema.KindAccept4, conn, at, tuple(local, peer))
}

func (tr *trace) http(conn string, at int, host string) {
	ev := tr.add(schema.KindHTTP, conn, at, schema.Args{})
	ev.FD = schema.NoFD
	ev.HTTP = &schema.HTTP{Method: "GET", Scheme: "http", Host: host, Path: "/", Status: 200, Version: "HTTP/1.1"}
}

// cluster is the synthetic kind cluster's traces: the client pod and
// one traced backend pod. kube-proxy DNATs every ClusterIP connection;
// the server sees the client's pod address (no SNAT inside the cluster)
// and its own pod address, the client the ClusterIP.
func cluster() (client, backend *trace, hostnames map[string]string) {
	client, backend = &trace{host: clientPod}, &trace{host: backendA}
	cli := func(p int) string { return clientIP + ":" + strconv.Itoa(p) }
	// c1: ClusterIP prod/backend → backend pod A, both traced: fused.
	client.dial("c1", 1, cli(40001), "10.96.0.20:80", 0x1000, 0x9000)
	backend.answer("s1", 1, "10.244.1.5:8080", cli(40001), 0x1000, 0x9000)
	client.http("c1", 2, "backend.prod.svc.cluster.local")
	// c2: ClusterIP prod/db, untraced, two endpoints: no pod named.
	client.dial("c2", 3, cli(40002), "10.96.0.30:5432", 0x2000, 0xa000)
	// c3: ClusterIP prod/cache, untraced, one endpoint: that pod.
	client.dial("c3", 4, cli(40003), "10.96.0.40:6379", 0x3000, 0xb000)
	// c4: inside the Service CIDR, no Service holds it.
	client.dial("c4", 5, cli(40004), "10.96.0.99:80", 0x4000, 0xc000)
	// c5: the FQDN on Traefik behind a load balancer: routed.
	client.dial("c5", 6, cli(40005), "203.0.113.10:443", 0x5000, 0xd000)
	// c6: ClusterIP prod/legacy now answered by backend pod A, which its
	// endpoint list does not name: fused, and the map flagged stale.
	client.dial("c6", 7, cli(40006), "10.96.0.50:80", 0x6000, 0xe000)
	backend.answer("s6", 7, "10.244.1.5:8080", cli(40006), 0x6000, 0xe000)
	// c7: an untraced address no map names: stays a bare unresolved edge.
	client.dial("c7", 8, cli(40007), "198.51.100.7:80", 0x7000, 0xf000)
	// c8: the client asks prod/db's ClusterIP for shop.example.org, which
	// routes to prod/backend: the route is contradicted, not changed.
	client.dial("c8", 9, cli(40008), "10.96.0.30:5432", 0x8000, 0xf100)
	client.http("c8", 9, "shop.example.org")
	return client, backend, map[string]string{"c5": "shop.example.org", "c8": "shop.example.org"}
}

// flows turns the merge's fusion into the join's flows, the way the
// build does from its observations: every traced client end, fused or
// not.
func flows(f federate.Fusion, events []schema.Event, hostnames map[string]string) []Flow {
	hosts := map[string][]string{}
	for _, ev := range events {
		if ev.HTTP != nil && ev.HTTP.Host != "" {
			hosts[ev.ConnID] = append(hosts[ev.ConnID], ev.HTTP.Host)
		}
	}
	var out []Flow
	for _, c := range f.Conns {
		out = append(out, Flow{From: "checkout", Endpoint: c.Client.Peer, Hostname: hostnames[c.Client.ConnID],
			HTTPHosts: hosts[c.Client.ConnID], Fused: true, ServerHost: c.Server.Host, ServerAddr: c.Server.Local,
			First: c.Client.Established, Last: c.Client.Established})
	}
	for _, u := range f.Unresolved {
		if u.Half.Role == federate.RoleClient {
			out = append(out, Flow{From: "checkout", Endpoint: u.Half.Peer, Hostname: hostnames[u.Half.ConnID],
				HTTPHosts: hosts[u.Half.ConnID], First: u.Half.Established, Last: u.Half.Established})
		}
	}
	return out
}

// TestClusterIPGoldenMerge merges the two pods' traces with the M4
// merge, unchanged — the ClusterIP connections fuse by the ISN pair
// across kube-proxy's DNAT — and joins the result with the map: the
// fused edge names the Service and the pod, the untraced Services stop
// at their workload (at a pod only for the one with a sole endpoint),
// the FQDN is routed through Traefik, and the unknown Service, the
// stale endpoint list and the contradicted route are flagged. The
// result matches testdata/clusterip.golden.
func TestClusterIPGoldenMerge(t *testing.T) {
	client, backend, hostnames := cluster()
	events := append(append([]schema.Event{}, client.events...), backend.events...)
	fusion := federate.Fuse(federate.Halves(events), federate.DefaultWindow)
	if len(fusion.Conns) != 2 {
		t.Fatalf("%d fused, want c1 and c6 across the DNAT: %+v", len(fusion.Conns), fusion)
	}
	m, err := Parse("cluster", []byte(clusterMap+"service_cidrs: [10.96.0.0/12]\n"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(Compile(m, nil, nil).Join(flows(fusion, events, hostnames), nil), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "clusterip.golden", string(got)+"\n")
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s differs (-update to accept):\n%s", path, got)
	}
}
