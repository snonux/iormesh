package ctl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"iormesh/internal/schema"
)

// clusterRoutes is the route map of the ClusterIP mesh: the backend
// Service (its endpoint is hostB's pod address), a database with two
// endpoints, and the FQDN routed through Traefik.
const clusterRoutes = `routes:
  - fqdn: shop.example.org
    ingress: traefik
    service: prod/backend
    workload: prod/deployment/backend
services:
  - service: prod/backend
    cluster_ips: [10.96.0.20]
    ports: [{port: 80, target_port: 8080}]
    workload: prod/deployment/backend
    endpoints: [{addr: 10.244.1.5, pod: prod/backend-7d9f-a}]
  - service: prod/db
    cluster_ips: [10.96.0.30]
    ports: [{port: 5432}]
    workload: prod/statefulset/db
    endpoints: [{addr: 10.244.4.1, pod: prod/db-0}, {addr: 10.244.5.1, pod: prod/db-1}]
`

// clusterMesh is the build tests' ClusterIP mesh: hostA's checkout dials
// the backend Service's ClusterIP and kube-proxy DNATs it to hostB's pod
// (the merge fuses the two ends by the ISN pair, as on any NAT); it also
// dials the untraced database's ClusterIP and the shop FQDN on an
// ingress load balancer, which stay unresolved for the merge.
func clusterMesh() (client, server *hostTrace) {
	const cli, vip, pod = "10.244.3.9:40001", "10.96.0.20:80", "10.244.1.5:8080"
	client, server = &hostTrace{host: "hostA", project: "prod"}, &hostTrace{host: "hostB", project: "prod"}
	client.header()
	client.tcp(100, 7, 100, "c1", cli, vip, "SYN_SENT", 0, 0, 0, 0)
	client.add(100, schema.KindConnect, 7, -115, 100, "c1", tupleArgs(cli, vip))
	client.tcp(100, 7, 1000, "c1", cli, vip, "ESTABLISHED", 0x1001, 0x9001, 1, 0)
	client.add(100, schema.KindConnect, 8, -115, 2000, "c2", tupleArgs("10.244.3.9:40002", "10.96.0.30:5432"))
	client.tcp(100, 8, 2100, "c2", "10.244.3.9:40002", "10.96.0.30:5432", "ESTABLISHED", 0x2001, 0xa001, 1, 0)
	client.add(100, schema.KindConnect, 9, -115, 3000, "c3", tupleArgs("10.244.3.9:40003", "203.0.113.10:80"))
	client.tcp(100, 9, 3100, "c3", "10.244.3.9:40003", "203.0.113.10:80", "ESTABLISHED", 0x3001, 0xb001, 1, 0)
	client.http(100, 3200, "c3", "shop.example.org", "/cart")
	client.end(9000, schema.EndStop)
	server.header()
	server.tcp(200, 9, 1500, "s1", pod, cli, "ESTABLISHED", 0x9001, 0x1001, 0, 0)
	server.add(200, schema.KindAccept4, 9, 9, 1500, "s1", tupleArgs(pod, cli))
	server.end(9000, schema.EndStop)
	return client, server
}

// newClusterFixture is a build fixture over the ClusterIP mesh, with the
// route map written beside the fixture's registry.
func newClusterFixture(t *testing.T, routes string, mode os.FileMode) *buildFixture {
	t.Helper()
	f := newBuildFixture(t)
	if err := os.RemoveAll(f.store); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(f.store, 0o700); err != nil {
		t.Fatal(err)
	}
	client, server := clusterMesh()
	writeStoreSegments(t, f.store, "hostA", client, 100)
	writeStoreSegments(t, f.store, "hostB", server, 100)
	reg, err := f.e.registry()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(reg), "routes.yaml"), []byte(routes), mode); err != nil {
		t.Fatal(err)
	}
	return f
}

// TestBuildRoutes builds the ClusterIP mesh with the route map: the
// fused connection names the backend Service and its pod, the untraced
// database stops at its StatefulSet (two endpoints, no pod picked), and
// the FQDN is one routed edge — checkout → FQDN → Traefik → Service →
// Deployment — with no tracer on the ingress. The summary's route lines
// match testdata/routes_summary.golden and the model carries the edges.
func TestBuildRoutes(t *testing.T) {
	f := newClusterFixture(t, clusterRoutes, 0o600)
	out := f.mustRun(t, "build")
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "routed") || strings.HasPrefix(l, "route flag") || strings.HasPrefix(l, "unresolved") {
			lines = append(lines, l)
		}
	}
	golden(t, "routes_summary.golden", strings.Join(lines, "\n")+"\n")
	data, err := os.ReadFile(filepath.Join(f.builds, "prod", modelFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"basis": "route"`) || !strings.Contains(string(data), `"ingress": "traefik"`) {
		t.Errorf("model lacks the routed edge:\n%s", data)
	}
	// graph.svg draws them (task bf4): the ingress route one row, java →
	// FQDN → traefik → Service → Deployment, dashed as not traced.
	svg, err := os.ReadFile(filepath.Join(f.builds, "prod", graphSVGName))
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "routes_graph_svg.golden", string(svg))
}

// TestBuildRoutesStale: an endpoint list that no longer names the pod
// that answered flags the Service stale; the fused edge stays, named by
// the server end's host.
func TestBuildRoutesStale(t *testing.T) {
	stale := strings.Replace(clusterRoutes, "{addr: 10.244.1.5, pod: prod/backend-7d9f-a}",
		"{addr: 10.244.8.8, pod: prod/backend-old}", 1)
	out := newClusterFixture(t, stale, 0o600)
	got := out.mustRun(t, "build")
	for _, want := range []string{
		"route flag stale_service prod/backend: reached hostB, not one of its endpoints (1 connections)",
		"routed     java -> prod/backend -> prod/deployment/backend -> hostB (fused, 1 connections)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q:\n%s", want, got)
		}
	}
}

// TestBuildRoutesHostile: a route map others could write, or one that
// fails its checks, fails the build before any segment is read, and
// -routes points the build at another file — one that must exist.
func TestBuildRoutesHostile(t *testing.T) {
	f := newClusterFixture(t, clusterRoutes, 0o666)
	reg, _ := f.e.registry()
	path := filepath.Join(filepath.Dir(reg), "routes.yaml")
	if err := os.Chmod(path, 0o666); err != nil { // past the umask
		t.Fatal(err)
	}
	if code := f.run("build"); code != exitError || !strings.Contains(f.stderr.String(), "not private") {
		t.Errorf("writable map: exit %d: %s", code, f.stderr)
	}
	if err := os.WriteFile(path, []byte("routes:\n  - {fqdn: \"x\\e[2J.example.org\", ingress: t, service: prod/b}\n"),
		0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := f.run("build"); code != exitError || !strings.Contains(f.stderr.String(), "invalid entry") {
		t.Errorf("forged fqdn: exit %d: %s", code, f.stderr)
	}
	other := filepath.Join(t.TempDir(), "routes.yaml")
	if err := os.WriteFile(other, []byte(clusterRoutes), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := f.mustRun(t, "build", "-routes", other); !strings.Contains(out, "routed") {
		t.Errorf("-routes ignored:\n%s", out)
	}
	if code := f.run("build", "-routes", filepath.Join(t.TempDir(), "typo.yaml")); code != exitError ||
		!strings.Contains(f.stderr.String(), "no such file") {
		t.Errorf("missing -routes file: exit %d: %s", code, f.stderr)
	}
}
