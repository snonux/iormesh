package ctl

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"iormesh/internal/schema"
	"iormesh/internal/viewmodel"
)

// podClient is an in-pod tracer's session of pod name in namespace ns
// (project shop), whose pid 300 dials peer once, unresolved: its events
// carry the pod object (task sh4), and it is stored under pod-<uid>/.
func podClient(t *testing.T, f *buildFixture, uid, name, ns, peer string) {
	t.Helper()
	h := &hostTrace{host: "pod-" + uid, project: "shop", fqdn: name + "." + ns + "." + uid[:8],
		pod: &schema.Pod{Namespace: ns, Name: name, ContainerID: strings.Repeat("ab", 32)}}
	h.header()
	h.add(300, schema.KindConnect, 5, -115, 100, "p1", tupleArgs("10.1.0.5:41000", peer))
	h.tcp(300, 5, 200, "p1", "10.1.0.5:41000", peer, "ESTABLISHED", 0x11, 0x21, 1, 0)
	h.end(1000, schema.EndStop)
	writeStoreSegments(t, f.store, h.host, h, 100)
}

// endpointsOf is the services view's endpoints, from show -json.
func endpointsOf(t *testing.T, out string) []string {
	t.Helper()
	var svcs []viewmodel.Service
	if err := json.Unmarshal([]byte(out), &svcs); err != nil {
		t.Fatalf("services -json: %v\n%s", err, out)
	}
	var eps []string
	for _, s := range svcs {
		eps = append(eps, s.Endpoints...)
	}
	slices.Sort(eps)
	return eps
}

// TestBuildNamespace: build -namespace NS builds only the sessions of
// pods in NS — no machine host, no pod of another namespace — and its
// services view only their connections, though an unfiltered build put
// every connection in the database first; it writes <projects>@NS,
// which show -namespace reads, and the unfiltered build stays as it was.
func TestBuildNamespace(t *testing.T) {
	f := newBuildFixture(t)
	podClient(t, f, "3b1c9a52-7e0d-4c1f-9a8e-2f6d5b4c3a21", "web-7d9f", "prod", "10.0.9.9:80")
	podClient(t, f, "9d8c7b6a-5e4f-4a3b-8c2d-1e0f9a8b7c6d", "web-1a2b", "dev", "10.0.8.8:80")
	f.mustRun(t, "build")
	all := endpointsOf(t, f.mustRun(t, "show", "services", "-json"))
	if !slices.Contains(all, "10.0.8.8:80") || !slices.Contains(all, "10.0.9.9:80") {
		t.Fatalf("unfiltered services %v: want both pods' peers", all)
	}
	f.mustRun(t, "build", "-namespace", "prod")
	if !dirExists(filepath.Join(f.builds, "billing+shop@prod")) {
		t.Fatalf("no build directory billing+shop@prod")
	}
	if got := endpointsOf(t, f.mustRun(t, "show", "services", "-json", "-namespace", "prod")); !slices.Equal(got,
		[]string{"10.0.9.9:80"}) {
		t.Errorf("prod services %v, want the prod pod's peer alone", got)
	}
	var health viewmodel.Health
	if err := json.Unmarshal([]byte(f.mustRun(t, "show", "health", "-json", "-namespace", "prod")),
		&health); err != nil {
		t.Fatal(err)
	}
	if len(health.Sessions) != 1 || health.Sessions[0].FQDN != "web-7d9f.prod.3b1c9a52" {
		t.Errorf("prod sessions %+v, want the prod pod's alone", health.Sessions)
	}
	if again := endpointsOf(t, f.mustRun(t, "show", "services", "-json")); !slices.Equal(again, all) {
		t.Errorf("the unfiltered build changed: %v, was %v", again, all)
	}
}

// TestBuildNamespaceRefusals: a namespace no API server would issue is a
// usage error before anything is read; one without pods in the store
// builds nothing and says so; show of a namespace never built says so.
func TestBuildNamespaceRefusals(t *testing.T) {
	f := newBuildFixture(t)
	for _, ns := range []string{"Prod", "a.b", "-x", strings.Repeat("a", 64)} {
		if code := f.run("build", "-namespace", ns); code != exitUsage {
			t.Errorf("build -namespace %q: exit %d, want %d", ns, code, exitUsage)
		}
		if code := f.run("show", "-namespace", ns); code != exitUsage {
			t.Errorf("show -namespace %q: exit %d, want %d", ns, code, exitUsage)
		}
	}
	if code := f.run("build", "-namespace", "prod"); code == exitOK ||
		!strings.Contains(f.stderr.String(), "namespace prod") {
		t.Errorf("build of an empty namespace: exit %d, %s", code, f.stderr)
	}
	if code := f.run("show", "-namespace", "prod"); code == exitOK ||
		!strings.Contains(f.stderr.String(), "billing+shop@prod") {
		t.Errorf("show of an unbuilt namespace: exit %d, %s", code, f.stderr)
	}
}
