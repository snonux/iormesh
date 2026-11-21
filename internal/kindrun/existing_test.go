package kindrun

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestTeardownExisting: against an existing cluster (task zg4) the
// teardown deletes no cluster — it uninstalls the run's Traefik and
// deletes the run's namespaces, in the operator's context — removes the
// local registry and images as the kind path does, and keeps the
// operator's kubeconfig while removing the state file.
func TestTeardownExisting(t *testing.T) {
	dir := t.TempDir()
	c := Existing(filepath.Join(dir, "k3s.yaml"), "default", filepath.Join(dir, "state.json"))
	if err := c.Save(State{BaseImages: map[string][]string{}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.Kubeconfig, []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fake{}
	var log strings.Builder
	if err := Teardown(f, c, &log); err != nil {
		t.Fatal(err)
	}
	var runs []string
	for _, a := range f.calls {
		if slices.Contains(a, "kind") {
			t.Errorf("kind call against an existing cluster: %q", a)
		}
		if line := strings.Join(a, " "); !strings.Contains(line, "image ls") {
			runs = append(runs, line)
		}
	}
	kc := "--kubeconfig=" + c.Kubeconfig
	want := []string{
		"helm uninstall traefik --namespace=traefik --wait " + kc + " --kube-context=default",
		"kubectl " + kc + " --context=default delete namespace --ignore-not-found --wait=true --timeout=180s " +
			"traefik iormesh-m7 iormesh-m7-clusterip",
		"sudo -n docker rm --force --volumes iormesh-registry",
		"sudo -n docker volume rm --force iormesh-registry-data",
		"sudo -n docker image rm --force localhost:5001/iormesh/tracer:m7",
		"sudo -n docker image rm --force localhost:5001/iormesh/workload:m7",
		"sudo -n docker image prune --all --force --filter=label=org.iormesh.acceptance=m7",
	}
	if !slices.Equal(runs, want) {
		t.Errorf("teardown ran\n%s\nwant\n%s", strings.Join(runs, "\n"), strings.Join(want, "\n"))
	}
	if _, err := os.Stat(c.Kubeconfig); err != nil {
		t.Errorf("the operator's kubeconfig went: %v", err)
	}
	if _, err := os.Stat(c.StateFile); !errors.Is(err, os.ErrNotExist) {
		t.Error("state file left behind")
	}
}

// TestExistingConfig: an existing cluster's configuration names no kind
// cluster or node, keeps the default registry and images (the cluster
// pulls localhost:5001's digests), and takes the operator's context;
// the default stays a kind run.
func TestExistingConfig(t *testing.T) {
	c := Existing("/k", "ctx", "/s")
	d := Default("/k", "/s")
	if !c.Existing || c.Cluster != "" || c.Node != "" || c.Context != "ctx" || d.Existing {
		t.Errorf("existing config %+v", c)
	}
	if !slices.Equal(c.Images, d.Images) || c.Registry != d.Registry {
		t.Errorf("existing config changed the registry or images: %+v", c)
	}
}
