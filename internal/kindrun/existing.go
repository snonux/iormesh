package kindrun

// An existing cluster (task zg4): M7's acceptance against a Kubernetes
// node that is not a local kind cluster — k3s on an el9 VM with SELinux
// enforcing, say — named by the operator's kubeconfig and context. The
// run then creates no cluster and so deletes none: it keeps its local
// registry, image builds and Traefik, and its teardown takes out only
// what it put into the cluster (the Traefik release and the run's
// namespaces) besides the local objects the kind path removes. The
// kubeconfig is the operator's and stays.
//
// The cluster must be able to pull RegistryAt's images under that very
// name (localhost:5001/…): the images are pinned by the registry's
// digest, and that reference is what the tests deploy. On a remote node
// that is the operator's wiring, for example an `ssh -R 5001:…` tunnel
// to the laptop's registry and a containerd mirror for localhost:5001
// (docs/testing.md "M7 on an existing cluster").

// ExistingNamespaces are the namespaces the run creates in an existing
// cluster: Traefik's, and the two of integrationtests/kind.
var ExistingNamespaces = []string{"traefik", "iormesh-m7", "iormesh-m7-clusterip"}

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

// clusterTeardown is the in-cluster part of the teardown: kind's cluster
// deletion, or, in an existing cluster, the Traefik release and the
// run's namespaces (Traefik's CRDs, which helm never removes, stay: the
// cluster is the operator's).
func clusterTeardown(c Config) [][]string {
	if !c.Existing {
		return [][]string{kind("delete", "cluster", "--name="+c.Cluster)}
	}
	auth := []string{"--kubeconfig=" + c.Kubeconfig, "--kube-context=" + c.Context}
	helm := append([]string{"helm", "uninstall", "traefik", "--namespace=traefik", "--wait"}, auth...)
	kubectl := []string{"kubectl", "--kubeconfig=" + c.Kubeconfig, "--context=" + c.Context,
		"delete", "namespace", "--ignore-not-found", "--wait=true", "--timeout=180s"}
	return [][]string{helm, append(kubectl, c.Namespaces...)}
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
