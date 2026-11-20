package federate

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// k8sImage is a digest-pinned image (placeholder repository, #18).
const k8sImage = "registry.example/iormesh@sha256:" +
	"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// k8sYAML is a registry with both sections (placeholders only, #18).
const k8sYAML = `servers:
  - host: app1
    projects:
      - name: shop
        target: {pattern: "*orders*"}
k8s:
  image: ` + k8sImage + `
  clusters:
    - name: kind
      context: kind-iormesh
      namespace: shop
    - name: prod
      context: ops@prod
pods:
  - cluster: kind
    pod: checkout-0
    container: app
    projects:
      - target: {pattern: "*java*"}
  - cluster: prod
    namespace: web
    selector: app=web
    container: app
    mode: sidecar
    projects:
      - name: web
        target: {listen_port: 8080}
      - name: web-admin
        target: {listen_port: 9090}
`

// writeRegistry writes a private registry file holding text.
func writeRegistry(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "servers.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestK8sRegistry: both sections load, references name the entries, the
// namespace and the project default to the cluster's namespace (#21),
// and Save writes back what was read.
func TestK8sRegistry(t *testing.T) {
	path := writeRegistry(t, k8sYAML)
	r, err := LoadRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Contexts(); !reflect.DeepEqual(got, []string{"kind-iormesh", "ops@prod"}) {
		t.Errorf("Contexts = %q", got)
	}
	refs := []string{r.PodRef(r.Pods[0]), r.PodRef(r.Pods[1])}
	if !reflect.DeepEqual(refs, []string{"kind/shop/checkout-0", "prod/web/app=web"}) {
		t.Errorf("refs = %q", refs)
	}
	p, err := r.LookupPod("kind/shop/checkout-0")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.PodProjects(p); got[0].Name != "shop" || p.Projects[0].Name != "" {
		t.Errorf("project default: %+v (entry %+v)", got, p.Projects)
	}
	if r.PodMode(p) != "debug" || r.PodMode(r.Pods[1]) != "sidecar" {
		t.Errorf("modes %q %q", r.PodMode(p), r.PodMode(r.Pods[1]))
	}
	if _, err := r.LookupPod("kind/shop/other"); !errors.Is(err, ErrUnknownPod) {
		t.Errorf("unknown ref: %v", err)
	}
	if err := r.Save(path); err != nil {
		t.Fatal(err)
	}
	again, err := LoadRegistry(path)
	if err != nil || !reflect.DeepEqual(again, r) {
		t.Errorf("round trip: %v\n got %+v\nwant %+v", err, again, r)
	}
}

// TestRemovePod: one project goes, then the entry with its last; a
// defaulted project name is found by its default.
func TestRemovePod(t *testing.T) {
	r, err := LoadRegistry(writeRegistry(t, k8sYAML))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.RemovePod("prod/web/app=web", "web-admin"); err != nil || len(r.Pods) != 2 {
		t.Fatalf("remove project: %v, %d entries", err, len(r.Pods))
	}
	if err := r.RemovePod("prod/web/app=web", "nope"); !errors.Is(err, ErrUnknownProject) {
		t.Errorf("unknown project: %v", err)
	}
	if err := r.RemovePod("kind/shop/checkout-0", "shop"); err != nil || len(r.Pods) != 1 {
		t.Fatalf("remove last project: %v, %d entries", err, len(r.Pods))
	}
	if err := r.RemovePod("kind/shop/checkout-0", ""); !errors.Is(err, ErrUnknownPod) {
		t.Errorf("removed twice: %v", err)
	}
}

// TestK8sRegistryRefusals: every malformed k8s: or pods: value is refused
// at load, naming what is wrong.
func TestK8sRegistryRefusals(t *testing.T) {
	cases := map[string][2]string{
		"unpinned default image": {"image: " + k8sImage, "image: registry.example/iormesh:latest"},
		"bad mode":               {"    mode: sidecar", "    mode: daemonset"},
		"bad context":            {"context: ops@prod", "context: -kubeconfig=/x"},
		"bad cluster name":       {"- name: prod", "- name: Prod_1"},
		"unknown cluster":        {"  - cluster: prod", "  - cluster: staging"},
		"pod and selector":       {"    pod: checkout-0", "    pod: checkout-0\n    selector: app=x"},
		"neither":                {"    pod: checkout-0\n", ""},
		"negative selector only": {"selector: app=web", "selector: app!=web"},
		"empty selector":         {"selector: app=web", "selector: \"\""},
		"no namespace":           {"    namespace: web\n", ""},
		"tracer as app":          {"    pod: checkout-0\n    container: app", "    pod: checkout-0\n    container: iormesh"},
		"debug with 2 projects":  {"    mode: sidecar\n", ""},
		"pod-uid host":           {"host: app1", "host: pod-0123abcd-0000-4000-8000-000000000001"},
		"duplicate entry":        {"pods:\n", "pods:\n  - cluster: kind\n    pod: checkout-0\n    container: app\n    projects:\n      - target: {pattern: x}\n"},
		"unknown key":            {"    container: app\n    projects:\n      - target: {pattern: \"*java*\"}", "    container: app\n    replicas: 2\n    projects:\n      - target: {pattern: \"*java*\"}"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(k8sYAML, c[0]) {
				t.Fatalf("fixture lacks %q", c[0])
			}
			text := strings.Replace(k8sYAML, c[0], c[1], 1)
			if _, err := LoadRegistry(writeRegistry(t, text)); err == nil {
				t.Errorf("accepted:\n%s", text)
			}
		})
	}
}

// TestValidServer: a host named like a pod's store directory is refused
// as a registry host, though it is a valid store directory name.
func TestValidServer(t *testing.T) {
	dir := "pod-0123abcd-0000-4000-8000-000000000001"
	if !ValidHost(dir) || ValidServer(dir) {
		t.Errorf("%s: ValidHost %v, ValidServer %v", dir, ValidHost(dir), ValidServer(dir))
	}
	if !ValidServer("pod-1") {
		t.Error("pod-1 refused")
	}
	// The same directory on a case-insensitive filesystem (N7).
	if upper := strings.ToUpper(dir); ValidServer(upper) {
		t.Errorf("%s accepted as a server", upper)
	}
	var r Registry
	if err := r.Add(dir, shop()); !errors.Is(err, ErrInvalid) {
		t.Errorf("Add(%s): %v", dir, err)
	}
}
