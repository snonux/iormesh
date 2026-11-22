package kindrun

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// An existing cluster (task zg4): M7's acceptance against a Kubernetes
// node that is not a local kind cluster — k3s on an el9 VM with SELinux
// enforcing, say — named by the operator's kubeconfig and context. The
// run then creates no cluster and so deletes none: it keeps its local
// registry, image builds and Traefik, and its teardown takes out only
// what it put into the cluster besides the local objects the kind path
// removes. The kubeconfig is the operator's and stays.
//
// The cluster is the operator's, and a release named traefik in
// namespace traefik is the chart's documented install of a cluster's
// own ingress controller (task li4: the run used to rewrite its Service
// to ClusterIP and then delete it with its namespace). So before its
// helm step the run claims the namespaces (ClaimExisting): it refuses
// when any is already there and not its own, and records in the state
// file the kubeconfig, the context and what it is about to create. The
// teardown removes only what that record names, in that context; with
// no record it leaves the cluster alone.
//
// The cluster must be able to pull RegistryAt's images under that very
// name (localhost:5001/…): the images are pinned by the registry's
// digest, and that reference is what the tests deploy. On a remote node
// that is the operator's wiring, for example an `ssh -R 5001:…` tunnel
// to the laptop's registry and a containerd mirror for localhost:5001
// (docs/testing.md "M7 on an existing cluster").

// ErrForeignRecord is ClaimExisting's refusal when the state file holds
// another kubeconfig's or context's record. The refused run then tears
// down with LeaveCluster: that record and its objects wait for an
// explicit `mage k8sTeardown`, not the teardown of a run that never
// touched them.
var ErrForeignRecord = errors.New("existing cluster: the state file records another cluster's objects")

// ExistingNamespaces are the namespaces the run creates in an existing
// cluster: Traefik's, and the three of the tests mage k8sAcceptance runs
// in integrationtests/kind (TestKindAcceptance's, TestKindClusterIP's and
// TestKindMicroservices', task uh4).
var ExistingNamespaces = []string{"traefik", "iormesh-m7", "iormesh-m7-clusterip", "iormesh-m7-micro"}

// Owned is the record of what the run created in an existing cluster:
// the kubeconfig and context it reached the cluster by, whether it
// installed the Traefik release (in namespace traefik), and the
// namespaces. The teardown reads the cluster through these, not through
// the context the kubeconfig names at teardown time, which may have
// been switched to another cluster since.
type Owned struct {
	Kubeconfig string   `json:"kubeconfig"`
	Context    string   `json:"context"`
	Traefik    bool     `json:"traefik_release"`
	Namespaces []string `json:"namespaces"`
}

// Existing is the run's configuration against the existing cluster of
// kubeconfig and context; state is the base-image state file. Cluster
// and Node are empty: there is no kind cluster to delete and no kind
// node container to exec into.
func Existing(kubeconfig, context, state string) Config {
	c := Default(kubeconfig, state)
	c.Existing, c.Cluster, c.Node, c.Context = true, "", "", context
	c.Namespaces = ExistingNamespaces
	return c
}

// ClaimExisting runs before anything is put into the existing cluster.
// It refuses when the state file records objects of another kubeconfig
// or context (tear those down first: the record holds one cluster), when
// the namespaces cannot be listed (nothing is then known absent), or
// when any of c.Namespaces is there and not named by this cluster's
// record — the release lives in namespace traefik, so its absence also
// means no traefik release to take over. Otherwise it records what the
// run will create; a rerun over a kept run's objects passes.
func ClaimExisting(r Runner, c Config) error {
	s, _, err := c.Load()
	if err != nil {
		return err
	}
	if o := s.Owned; o != nil && (o.Context != c.Context || o.Kubeconfig != c.Kubeconfig) {
		return fmt.Errorf("%w: %s records the run's objects in context %s of %s: "+
			"run `mage k8sTeardown` against it first", ErrForeignRecord, c.StateFile, o.Context, o.Kubeconfig)
	}
	present, err := presentNamespaces(r, c)
	if err != nil {
		return fmt.Errorf("existing cluster: cannot list namespaces in context %s: %w", c.Context, err)
	}
	var foreign []string
	for _, ns := range present {
		if s.Owned == nil || !slices.Contains(s.Owned.Namespaces, ns) {
			foreign = append(foreign, ns)
		}
	}
	if len(foreign) > 0 {
		return fmt.Errorf("existing cluster: namespace %s already in context %s and not this run's: "+
			"refusing to take it over", strings.Join(foreign, ", "), c.Context)
	}
	s.Owned = &Owned{Kubeconfig: c.Kubeconfig, Context: c.Context, Traefik: true,
		Namespaces: slices.Clone(c.Namespaces)}
	return c.write(s)
}

// presentNamespaces are those of c.Namespaces the cluster has: kubectl
// with --ignore-not-found prints only the ones that exist and fails
// only when it cannot ask.
func presentNamespaces(r Runner, c Config) ([]string, error) {
	argv := append([]string{"kubectl", "--kubeconfig=" + c.Kubeconfig, "--context=" + c.Context,
		"get", "namespace", "--ignore-not-found", "--output=name"}, c.Namespaces...)
	out, err := r.Output(argv...)
	if err != nil {
		return nil, err
	}
	var present []string
	for _, name := range strings.Fields(out) {
		present = append(present, strings.TrimPrefix(name, "namespace/"))
	}
	return present, nil
}

// clusterTeardown is the in-cluster part of the teardown: kind's cluster
// deletion, or, in an existing cluster, what the record o names — the
// Traefik release and the namespaces — in o's kubeconfig and context;
// nothing without a record or with one naming no kubeconfig or context
// (an empty --context= or --kubeconfig= would fall back to whatever the
// operator's kubeconfig points at; Teardown screens such a record out
// first and says so, this is the backstop). Traefik's CRDs, which helm
// never removes, stay: the cluster is the operator's.
func clusterTeardown(c Config, o *Owned) [][]string {
	if !c.Existing {
		return [][]string{kind("delete", "cluster", "--name="+c.Cluster)}
	}
	if !usable(o) {
		return nil
	}
	var plan [][]string
	if o.Traefik {
		plan = append(plan, []string{"helm", "uninstall", "traefik", "--namespace=traefik", "--wait",
			"--kubeconfig=" + o.Kubeconfig, "--kube-context=" + o.Context})
	}
	if del := namespaceDelete(o); del != nil {
		plan = append(plan, del)
	}
	return plan
}

// namespaceDelete is the deletion of o's namespaces, nil without any.
// Repeat-safe (--ignore-not-found); its success satisfies the whole
// record, the release's storage being in namespace traefik.
func namespaceDelete(o *Owned) []string {
	if !usable(o) || len(o.Namespaces) == 0 {
		return nil
	}
	return append([]string{"kubectl", "--kubeconfig=" + o.Kubeconfig, "--context=" + o.Context,
		"delete", "namespace", "--ignore-not-found", "--wait=true", "--timeout=180s"}, o.Namespaces...)
}

// usable is a record that names both the kubeconfig and the context the
// run reached the cluster by.
func usable(o *Owned) bool { return o != nil && o.Kubeconfig != "" && o.Context != "" }

// clusterRecord is the record Teardown may act on in the cluster, nil
// when it must leave the cluster alone, with the notice to print, whether
// the state file must stay, and the error to report. A refused run
// (LeaveCluster) keeps another run's record; a record naming no
// kubeconfig or context (hand-edited) is kept and fails the teardown,
// for the operator to fix; no record is the normal case of a run that
// never claimed. The kind path has no record.
func clusterRecord(c Config, o *Owned) (use *Owned, notice string, keep bool, err error) {
	const none = "nothing removed from the cluster"
	switch {
	case !c.Existing:
		return nil, "", false, nil
	case o == nil:
		return nil, "no record of what the run created in the existing cluster: " + none, false, nil
	case c.LeaveCluster:
		return nil, fmt.Sprintf("%s records another run's objects (context %s): %s; "+
			"state file kept for `mage k8sTeardown`", c.StateFile, o.Context, none), true, nil
	case !usable(o):
		err := fmt.Errorf("%s: record names no kubeconfig or no context: %s; state file kept",
			c.StateFile, none)
		return nil, err.Error(), true, err
	}
	return o, "", false, nil
}

// ownedFiles are the files the teardown removes: the state file, and the
// kubeconfig only when the run wrote it (kind's, not an existing
// cluster's).
func ownedFiles(c Config) []string {
	if c.Existing {
		return []string{c.StateFile}
	}
	return []string{c.Kubeconfig, c.StateFile}
}
