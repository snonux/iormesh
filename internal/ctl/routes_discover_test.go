package ctl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"iormesh/internal/routemap"
)

// discoverKubectl answers `get <resource>` from $FAKE_DIR/<resource>.json
// and, for a resource without a file, fails as kubectl does for one the
// server does not serve. It logs each call's argv on one line.
const discoverKubectl = `#!/bin/sh
echo "$*" >> "$FAKE_DIR/log"
res="$4"
if [ ! -f "$FAKE_DIR/$res.json" ]; then
  echo "error: the server doesn't have a resource type \"$res\"" >&2; exit 1
fi
cat "$FAKE_DIR/$res.json"
`

// discoveredObjects is the prod namespace of the ClusterIP mesh as the
// API server lists it: the backend Deployment's pod behind its Service,
// the shop FQDN on an Ingress. No Traefik CRDs are served.
var discoveredObjects = map[string]string{
	"replicasets.apps": `{"items":[{"metadata":{"name":"backend-7d9f","namespace":"prod",
		"ownerReferences":[{"kind":"Deployment","name":"backend","controller":true}]}}]}`,
	"pods": `{"items":[{"metadata":{"name":"backend-7d9f-a","namespace":"prod",
		"uid":"0b6e7a52-1111-4c3e-9a63-1d2f3e4a5b6c",
		"ownerReferences":[{"kind":"ReplicaSet","name":"backend-7d9f","controller":true}]}}]}`,
	"services": `{"items":[{"metadata":{"name":"backend","namespace":"prod"},
		"spec":{"type":"ClusterIP","clusterIP":"10.96.0.20","ports":[{"port":80,"targetPort":8080}]}}]}`,
	"endpointslices.discovery.k8s.io": `{"items":[{"metadata":{"name":"backend-x","namespace":"prod",
		"labels":{"kubernetes.io/service-name":"backend"}},"addressType":"IPv4",
		"endpoints":[{"addresses":["10.244.1.5"],"conditions":{"ready":true},
		"targetRef":{"kind":"Pod","uid":"0b6e7a52-1111-4c3e-9a63-1d2f3e4a5b6c"}}]}]}`,
	"ingresses.networking.k8s.io": `{"items":[{"metadata":{"name":"web","namespace":"prod"},
		"spec":{"rules":[{"host":"shop.example.org","http":{"paths":[{"backend":{"service":{"name":"backend"}}}]}}]}}]}`,
}

const discoverRegistry = `servers: []
k8s:
  clusters:
    - name: kind
      context: kind-test
      namespace: prod
`

// discoverFixture is the ClusterIP mesh's build fixture with the fake
// kubectl on PATH and a registry naming one cluster; no static map.
func discoverFixture(t *testing.T) (*buildFixture, string) {
	t.Helper()
	f := newClusterFixture(t, "", 0o600)
	reg, _ := f.e.registry()
	if err := os.Remove(filepath.Join(filepath.Dir(reg), "routes.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reg, []byte(discoverRegistry), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(discoverKubectl), 0o700); err != nil {
		t.Fatal(err)
	}
	for res, body := range discoveredObjects {
		if err := os.WriteFile(filepath.Join(dir, res+".json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DIR", dir)
	return f, dir
}

// TestRoutesDiscover: discover reads every route resource in the
// cluster's namespace with read-only gets (the missing Traefik CRDs are
// no objects), writes the snapshot beside the database, and the next
// build — without running kubectl — routes the FQDN and names the
// Service and pod from it alone.
func TestRoutesDiscover(t *testing.T) {
	f, dir := discoverFixture(t)
	out := f.mustRun(t, "routes", "discover", "-ingress-service", "kube-system/traefik")
	if !strings.Contains(out, "discovered 1 routes, 1 services in kind-test (prod)") {
		t.Errorf("discover printed\n%s", out)
	}
	log, _ := os.ReadFile(filepath.Join(dir, "log"))
	for _, l := range strings.Split(strings.TrimSpace(string(log)), "\n") {
		if !strings.HasPrefix(l, "--context=kind-test --namespace=prod get ") || !strings.HasSuffix(l, " --output=json") {
			t.Errorf("kubectl call %q is not a read-only get in the registered scope", l)
		}
	}
	snap, err := routemap.LoadSnapshot(routemap.SnapshotPath(f.db))
	if err != nil || snap == nil || snap.Map.Routes[0].IngressService != "kube-system/traefik" {
		t.Fatalf("snapshot %+v, %v", snap, err)
	}
	if err := os.Remove(filepath.Join(dir, "log")); err != nil {
		t.Fatal(err)
	}
	got := f.mustRun(t, "build")
	for _, want := range []string{
		"routed     java -> prod/backend -> prod/deployment/backend -> prod/backend-7d9f-a (fused, 1 connections)",
		"routed     java -> shop.example.org -> ingress/prod/web -> prod/backend -> prod/deployment/backend (route",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("build lacks %q:\n%s", want, got)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "log")); err == nil {
		t.Error("build ran kubectl")
	}
}

// TestRoutesDiscoverPrecedence: a static route for the same FQDN wins
// over the discovered one, and the shadowing is flagged.
func TestRoutesDiscoverPrecedence(t *testing.T) {
	f, _ := discoverFixture(t)
	f.mustRun(t, "routes", "discover")
	reg, _ := f.e.registry()
	static := "routes:\n  - {fqdn: shop.example.org, ingress: traefik, service: prod/backend}\n"
	if err := os.WriteFile(filepath.Join(filepath.Dir(reg), "routes.yaml"), []byte(static), 0o600); err != nil {
		t.Fatal(err)
	}
	got := f.mustRun(t, "build")
	for _, want := range []string{"-> shop.example.org -> traefik -> prod/backend",
		"route flag shadowed route shop.example.org: the static map's entry wins over the discovered one"} {
		if !strings.Contains(got, want) {
			t.Errorf("build lacks %q:\n%s", want, got)
		}
	}
}

// TestRoutesSnapshotContext: a snapshot of a context the registry no
// longer lists fails the build rather than naming another cluster's
// addresses.
func TestRoutesSnapshotContext(t *testing.T) {
	f, _ := discoverFixture(t)
	f.mustRun(t, "routes", "discover")
	reg, _ := f.e.registry()
	if err := os.WriteFile(reg, []byte(strings.Replace(discoverRegistry, "kind-test", "kind-other", 1)),
		0o600); err != nil {
		t.Fatal(err)
	}
	if code := f.run("build"); code != exitError || !strings.Contains(f.stderr.String(), "no registered cluster") {
		t.Errorf("stale context: exit %d: %s", code, f.stderr)
	}
}

// TestRoutesDiscoverRefusals: no subcommand, an unregistered cluster, a
// bad namespace and a failing kubectl all fail without a snapshot.
func TestRoutesDiscoverRefusals(t *testing.T) {
	f, dir := discoverFixture(t)
	for _, args := range [][]string{{"routes"}, {"routes", "discover", "-cluster", "other"},
		{"routes", "discover", "-namespace", "Bad_NS"}, {"routes", "discover", "-ingress-service", "traefik"}} {
		if code := f.run(args...); code == exitOK {
			t.Errorf("%v: exit 0", args)
		}
	}
	if err := os.Remove(filepath.Join(dir, "services.json")); err != nil {
		t.Fatal(err)
	}
	if code := f.run("routes", "discover"); code != exitError {
		t.Errorf("failing kubectl: exit %d", code)
	}
	if _, err := os.Stat(routemap.SnapshotPath(f.db)); err == nil {
		t.Error("a snapshot was written")
	}
}
