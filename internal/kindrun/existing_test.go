package kindrun

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestTeardownExisting: against an existing cluster (task zg4) the
// teardown deletes no cluster — it uninstalls the run's Traefik and
// deletes the run's namespaces, in the context the state file records
// (task li4: not the one the kubeconfig names now) — removes the local
// registry and images as the kind path does, and keeps the operator's
// kubeconfig while removing the state file.
func TestTeardownExisting(t *testing.T) {
	dir := t.TempDir()
	c := Existing(filepath.Join(dir, "k3s.yaml"), "default", filepath.Join(dir, "state.json"))
	owned := &Owned{Kubeconfig: c.Kubeconfig, Context: "default", Traefik: true,
		Namespaces: ExistingNamespaces}
	if err := c.Save(State{BaseImages: map[string][]string{}, Owned: owned}); err != nil {
		t.Fatal(err)
	}
	c.Context = "switched-since"
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
			"traefik iormesh-m7 iormesh-m7-clusterip iormesh-m7-micro",
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

// existingTest is an existing-cluster configuration in a temporary
// directory.
func existingTest(t *testing.T) Config {
	dir := t.TempDir()
	return Existing(filepath.Join(dir, "k3s.yaml"), "default", filepath.Join(dir, "state.json"))
}

// inCluster are the calls that would change the cluster: a helm
// install or uninstall, a namespace deletion.
func inCluster(calls [][]string) []string {
	var out []string
	for _, a := range calls {
		line := strings.Join(a, " ")
		if strings.HasPrefix(line, "helm ") || strings.Contains(line, "delete namespace") {
			out = append(out, line)
		}
	}
	return out
}

// TestClaimExistingRefusesTaken (task li4): when namespace traefik — the
// chart's documented home of a cluster's own ingress controller — or
// either test namespace is already there, the run refuses before its
// helm step and records nothing, so its teardown cannot take them.
func TestClaimExistingRefusesTaken(t *testing.T) {
	for _, ns := range ExistingNamespaces {
		c := existingTest(t)
		f := &fake{replies: map[string]string{"get namespace": "namespace/" + ns + "\n"}}
		err := ClaimExisting(f, c)
		if err == nil || !strings.Contains(err.Error(), ns) {
			t.Errorf("%s present: claim %v", ns, err)
		}
		if s, _, _ := c.Load(); s.Owned != nil {
			t.Errorf("%s present: recorded %+v", ns, s.Owned)
		}
		if got := inCluster(f.calls); got != nil {
			t.Errorf("%s present: changed the cluster: %q", ns, got)
		}
	}
}

// TestClaimExistingUnreachable (task li4): a namespace listing that
// fails proves nothing absent, so the run refuses too.
func TestClaimExistingUnreachable(t *testing.T) {
	c := existingTest(t)
	if err := ClaimExisting(&fake{replies: map[string]string{"get namespace": "ERR"}}, c); err == nil {
		t.Error("claimed without a namespace listing")
	}
}

// TestClaimExistingRecords (task li4): with none of the namespaces
// there, the claim records the kubeconfig, the context, the release and
// the namespaces, in the state file beside the base images; a rerun over
// the kept objects of that record goes on, and a record of another
// context is refused until it is torn down.
func TestClaimExistingRecords(t *testing.T) {
	c := existingTest(t)
	if err := c.Save(State{BaseImages: map[string][]string{"registry": {"a"}}}); err != nil {
		t.Fatal(err)
	}
	f := &fake{}
	if err := ClaimExisting(f, c); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) == 0 || !slices.Contains(f.calls[0], "--context=default") {
		t.Errorf("listing not in the run's context: %q", f.calls)
	}
	s, _, err := c.Load()
	want := Owned{Kubeconfig: c.Kubeconfig, Context: "default", Traefik: true, Namespaces: ExistingNamespaces}
	if err != nil || s.Owned == nil || !reflect.DeepEqual(*s.Owned, want) || len(s.BaseImages["registry"]) != 1 {
		t.Fatalf("state %+v, %v", s, err)
	}
	kept := &fake{replies: map[string]string{"get namespace": "namespace/traefik\nnamespace/iormesh-m7\n"}}
	if err := ClaimExisting(kept, c); err != nil {
		t.Errorf("rerun over its own record: %v", err)
	}
	other := c
	other.Context = "elsewhere"
	if err := ClaimExisting(&fake{}, other); err == nil || !strings.Contains(err.Error(), "default") {
		t.Errorf("claim over a record of another context: %v", err)
	}
}

// TestTeardownExistingWithoutRecord (task li4): with no record of what
// the run created — a run that failed or was refused before its claim,
// or a bare `mage k8sTeardown` — the teardown uninstalls no release and
// deletes no namespace; the local registry and images still go.
func TestTeardownExistingWithoutRecord(t *testing.T) {
	for name, save := range map[string]bool{"no state file": false, "base images only": true} {
		c := existingTest(t)
		if save {
			if err := c.Save(State{BaseImages: map[string][]string{}}); err != nil {
				t.Fatal(err)
			}
		}
		f := &fake{}
		var log strings.Builder
		if err := Teardown(f, c, &log); err != nil {
			t.Fatal(err)
		}
		if got := inCluster(f.calls); got != nil {
			t.Errorf("%s: changed the cluster: %q", name, got)
		}
		if !strings.Contains(log.String(), "nothing removed from the cluster") {
			t.Errorf("%s: no notice: %q", name, log.String())
		}
	}
}

// TestTeardownExistingKeepsRecord (task li4): when the namespaces could
// not be deleted the record stays, so a later `mage k8sTeardown` still
// knows what is the run's; a failed helm uninstall alone does not keep
// it (the release lives in namespace traefik and goes with it).
func TestTeardownExistingKeepsRecord(t *testing.T) {
	for step, kept := range map[string]bool{"delete namespace": true, "helm uninstall": false} {
		c := existingTest(t)
		owned := &Owned{Kubeconfig: c.Kubeconfig, Context: "default", Traefik: true, Namespaces: ExistingNamespaces}
		if err := c.Save(State{BaseImages: map[string][]string{}, Owned: owned}); err != nil {
			t.Fatal(err)
		}
		err := Teardown(&fake{fail: map[string]bool{step: true}}, c, &strings.Builder{})
		s, _, _ := c.Load()
		if kept && (err == nil || s.Owned == nil) {
			t.Errorf("%s failed: record %+v, error %v", step, s.Owned, err)
		}
		if !kept && (err != nil || s.Owned != nil) {
			t.Errorf("%s failed: record %+v, error %v", step, s.Owned, err)
		}
	}
}

// TestRefusedRunLeavesForeignRecord (task li4): a run refused because the
// state file records another context's objects tears down with
// LeaveCluster — no helm uninstall, no namespace deletion there, and the
// state file with that record stays for an explicit `mage k8sTeardown`.
func TestRefusedRunLeavesForeignRecord(t *testing.T) {
	c := existingTest(t)
	owned := &Owned{Kubeconfig: c.Kubeconfig, Context: "elsewhere", Traefik: true, Namespaces: ExistingNamespaces}
	if err := c.Save(State{BaseImages: map[string][]string{}, Owned: owned}); err != nil {
		t.Fatal(err)
	}
	if err := ClaimExisting(&fake{}, c); !errors.Is(err, ErrForeignRecord) {
		t.Fatalf("claim over another context's record: %v", err)
	}
	c.LeaveCluster = true
	f := &fake{}
	var log strings.Builder
	if err := Teardown(f, c, &log); err != nil {
		t.Fatal(err)
	}
	if got := inCluster(f.calls); got != nil {
		t.Errorf("refused run changed the other context: %q", got)
	}
	if s, _, err := c.Load(); err != nil || s.Owned == nil || s.Owned.Context != "elsewhere" {
		t.Errorf("foreign record lost: %+v, %v", s.Owned, err)
	}
	if !strings.Contains(log.String(), "nothing removed from the cluster") {
		t.Errorf("no notice: %q", log.String())
	}
}

// TestTeardownRecordWithoutContext (task li4): a record naming no
// context or no kubeconfig (a hand-edited state file) would let
// `--context=` or `--kubeconfig=` fall back to the kubeconfig's current
// cluster, so the teardown removes nothing in the cluster, says so,
// keeps the state file and fails.
func TestTeardownRecordWithoutContext(t *testing.T) {
	for name, o := range map[string]Owned{"no context": {Kubeconfig: "/k"}, "no kubeconfig": {Context: "default"}} {
		c := existingTest(t)
		o.Traefik, o.Namespaces = true, ExistingNamespaces
		if err := c.Save(State{BaseImages: map[string][]string{}, Owned: &o}); err != nil {
			t.Fatal(err)
		}
		f := &fake{}
		var log strings.Builder
		err := Teardown(f, c, &log)
		if got := inCluster(f.calls); got != nil {
			t.Errorf("%s: changed the cluster: %q", name, got)
		}
		if _, serr := os.Stat(c.StateFile); err == nil || serr != nil {
			t.Errorf("%s: error %v, state file %v", name, err, serr)
		}
		if !strings.Contains(log.String(), "nothing removed from the cluster") {
			t.Errorf("%s: no notice: %q", name, log.String())
		}
	}
}
