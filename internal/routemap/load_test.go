package routemap

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goodMap is the M0 ingress scenario's map: the FQDN on Traefik, the
// backend Service with its ClusterIP and two endpoints, the Service CIDR.
const goodMap = goodRoutes + "service_cidrs: [10.96.0.0/12]\n"

// goodRoutes is goodMap without its Service CIDR, for maps that append
// Services.
const goodRoutes = `routes:
  - fqdn: Shop.Example.ORG.
    ingress: traefik
    service: prod/backend
    workload: prod/deployment/backend
services:
  - service: prod/backend
    cluster_ips: [10.96.0.20]
    ports: [{port: 80, target_port: 8080}]
    endpoints:
      - {addr: 10.244.1.5, pod: prod/backend-7d9f-a, uid: 0b6e7a52-1111-4c3e-9a63-1d2f3e4a5b6c}
      - {addr: 10.244.2.6, pod: prod/backend-7d9f-b}
`

// writeMap writes content as a private routes.yaml in a private dir.
func writeMap(t *testing.T, content string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "iormesh")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLoadGood: the map loads, its FQDN normalized (lowercase, no
// trailing dot), and a missing file is no map.
func TestLoadGood(t *testing.T) {
	m, err := Load(writeMap(t, goodMap))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Routes) != 1 || m.Routes[0].FQDN != "shop.example.org" || len(m.Services[0].Endpoints) != 2 {
		t.Fatalf("loaded %+v", m)
	}
	if m, err := Load(filepath.Join(t.TempDir(), FileName)); m != nil || err != nil {
		t.Fatalf("missing file: %v, %v", m, err)
	}
	if got := DefaultPath("/home/u/.config/iormesh/servers.yaml"); got != "/home/u/.config/iormesh/routes.yaml" {
		t.Errorf("DefaultPath = %s", got)
	}
}

// TestLoadHostileFile: a map others could write — the file, or the
// directory a file could be renamed over in — is refused, as is one
// past the size bound.
func TestLoadHostileFile(t *testing.T) {
	path := writeMap(t, goodMap)
	if err := os.Chmod(path, 0o622); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); !errors.Is(err, ErrUnsafeFile) {
		t.Errorf("group-writable file: %v", err)
	}
	path = writeMap(t, goodMap)
	if err := os.Chmod(filepath.Dir(path), 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); !errors.Is(err, ErrUnsafeFile) {
		t.Errorf("world-writable dir: %v", err)
	}
	if _, err := Load(writeMap(t, "routes: []\n#"+strings.Repeat("x", maxFileSize))); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversized file: %v", err)
	}
}

// TestParseHostile: each forged or mistaken entry is refused with the
// whole file — never a partial map the build would trust.
func TestParseHostile(t *testing.T) {
	route := func(fqdn, ingress, service, workload string) string {
		return "routes:\n  - {fqdn: " + fqdn + ", ingress: " + ingress + ", service: " + service +
			", workload: " + workload + "}\n"
	}
	svc := func(body string) string { return "services:\n  - " + body + "\n" }
	for name, doc := range map[string]string{
		"unknown key":        "routes: []\nroutez: []\n",
		"second document":    "routes: []\n---\nroutes: []\n",
		"escape in fqdn":     route(`"shop\e[31m.example.org"`, "traefik", "prod/backend", "prod/deployment/b"),
		"single-label fqdn":  route("backend", "traefik", "prod/backend", ""),
		"space in ingress":   route("a.example.org", `"traefik; rm"`, "prod/backend", ""),
		"service without ns": route("a.example.org", "traefik", "backend", ""),
		"daemonset workload": route("a.example.org", "traefik", "prod/backend", "prod/daemonset/b"),
		"workload other ns":  route("a.example.org", "traefik", "prod/backend", "dev/deployment/b"),
		"duplicate fqdn":     route("a.example.org", "t", "prod/b", "") + "  - {fqdn: A.example.org., ingress: t, service: prod/c}\n",
		"bad cluster ip":     svc("{service: prod/b, cluster_ips: [10.96.0.300], ports: [{port: 80}]}"),
		"unspecified ip":     svc("{service: prod/b, cluster_ips: ['0.0.0.0'], ports: [{port: 80}]}"),
		"no ports":           svc("{service: prod/b, cluster_ips: [10.96.0.1], ports: []}"),
		"port out of range":  svc("{service: prod/b, cluster_ips: [10.96.0.1], ports: [{port: 70000}]}"),
		"endpoint pod other ns": svc("{service: prod/b, cluster_ips: [10.96.0.1], ports: [{port: 80}], " +
			"endpoints: [{addr: 10.244.0.1, pod: kube-system/x}]}"),
		"endpoint bad uid": svc("{service: prod/b, cluster_ips: [10.96.0.1], ports: [{port: 80}], " +
			"endpoints: [{addr: 10.244.0.1, uid: ../../etc}]}"),
		"two services one ip": "services:\n" +
			"  - {service: prod/a, cluster_ips: [10.96.0.1], ports: [{port: 80}]}\n" +
			"  - {service: prod/b, cluster_ips: [10.96.0.1], ports: [{port: 80}]}\n",
		"non-canonical cidr": "service_cidrs: [10.96.0.1/12]\n",
	} {
		if _, err := Parse(name, []byte(doc)); err == nil {
			t.Errorf("%s: accepted\n%s", name, doc)
		}
	}
}

// TestParseCaps: past the per-file caps a map is refused, not truncated.
func TestParseCaps(t *testing.T) {
	var b strings.Builder
	b.WriteString("services:\n  - service: prod/b\n    cluster_ips: [10.96.0.1]\n    ports: [{port: 80}]\n    endpoints:\n")
	for range maxEndpoints + 1 {
		b.WriteString("      - {addr: 10.244.0.1}\n")
	}
	if _, err := Parse("caps", []byte(b.String())); !errors.Is(err, ErrInvalid) {
		t.Errorf("too many endpoints: %v", err)
	}
}
