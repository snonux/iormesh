//go:build mage

// M7's acceptance on a local kind cluster (task yb4, docs/testing.md "M7
// acceptance on kind"): K8sAcceptance stands up what the tests in
// integrationtests/kind need — a local registry, one kind cluster wired to
// it, the tracer and workload images pushed there (so a pod's imageID
// carries a registry digest), Traefik — runs them, and tears the cluster
// and the registry down again. Every docker and kind call goes through
// `sudo -n`; nobody joins the docker group. Starting and stopping the
// Docker daemon is the operator's: without a usable `sudo -n docker`,
// kind, kubectl or helm the target says SKIP and succeeds, so it is safe
// to run anywhere.

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"
)

// The acceptance run's fixed names. The kubeconfig sits at the repository
// root, owned by the operator, and is never committed (.gitignore).
const (
	kindCluster    = "iormesh-m7"
	kindContext    = "kind-" + kindCluster
	kindNode       = kindCluster + "-control-plane"
	kindKubeconfig = ".kind-kubeconfig"
	kindRegistry   = "iormesh-registry"
	kindRegistryAt = "localhost:5001"
	// registryImage is the distribution registry, pinned by digest.
	registryImage = "docker.io/library/registry:2@sha256:" +
		"a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373"
	traefikChart   = "traefik/traefik"
	traefikVersion = "42.4.1"
	// minFreeGiB is the disk guard: below it nothing is created, built or
	// pushed (images and the node take a few GiB).
	minFreeGiB = 20
)

// kindConfig points the node's containerd at /etc/containerd/certs.d,
// where kindRegistryHosts maps the local registry (kind's documented
// local-registry setup).
const kindConfig = `kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
containerdConfigPatches:
- |-
  [plugins."io.containerd.grpc.v1.cri".registry]
    config_path = "/etc/containerd/certs.d"
`

// K8sAcceptance runs PLAN.md M7's acceptance on kind: TestKindAcceptance
// under kube-proxy's iptables mode, then TestKindClusterIP again under
// nftables. IORMESH_KIND_KEEP=1 keeps the cluster and the registry;
// IORMESH_KIND_KUBECONFIG puts the kubeconfig elsewhere.
func K8sAcceptance() error {
	if why := kindMissing(); why != "" {
		fmt.Println("SKIP k8sAcceptance:", why)
		return nil
	}
	mg.Deps(Build, ReleaseTracer, AgentBuild, WorkloadBuild)
	if err := diskGuard(); err != nil {
		return err
	}
	if os.Getenv("IORMESH_KIND_KEEP") == "" {
		defer kindTeardown()
	}
	env, err := kindUp()
	if err != nil {
		return err
	}
	return kindTests(env)
}

// kindMissing names the first missing prerequisite, "" when all are there.
func kindMissing() string {
	for _, bin := range []string{"kind", "kubectl", "helm", "docker"} {
		if _, err := exec.LookPath(bin); err != nil {
			return bin + " is not on PATH"
		}
	}
	if err := exec.Command("sudo", "-n", "docker", "info").Run(); err != nil {
		return "`sudo -n docker info` failed (is the daemon running, may sudo run docker?)"
	}
	return ""
}

// kindUp brings up the registry, the cluster, the images and Traefik and
// returns the tests' environment.
func kindUp() (map[string]string, error) {
	if err := kindRegistryUp(); err != nil {
		return nil, err
	}
	if err := kindClusterUp(); err != nil {
		return nil, err
	}
	tracer, err := kindTracerImage()
	if err != nil {
		return nil, err
	}
	workload, err := kindWorkloadImage()
	if err != nil {
		return nil, err
	}
	if err := kindTraefik(); err != nil {
		return nil, err
	}
	kc, err := filepath.Abs(kubeconfigPath())
	if err != nil {
		return nil, err
	}
	ctl, err := filepath.Abs(ctlBinaryName)
	return map[string]string{"IORMESH_KIND_KUBECONFIG": kc, "IORMESH_KIND_CONTEXT": kindContext,
		"IORMESH_KIND_TRACER_IMAGE": tracer, "IORMESH_KIND_WORKLOAD_IMAGE": workload,
		"IORMESH_KIND_CTL": ctl, "IORMESH_KIND_NODE": kindNode, "CGO_ENABLED": "0"}, err
}

// kindTests runs the suite, then switches kube-proxy to nftables and runs
// the ClusterIP join again.
func kindTests(env map[string]string) error {
	run := func(test string) error {
		return sh.RunWithV(env, "go", "test", "./integrationtests/kind/", "-count=1", "-v",
			"-timeout=40m", "-run=^"+test+"$")
	}
	if err := kindProxyMode(env, "iptables"); err != nil {
		return err
	}
	if err := run("TestKindAcceptance"); err != nil {
		return err
	}
	if err := kindProxyMode(env, "nftables"); err != nil {
		return err
	}
	return run("TestKindClusterIP")
}

// kubectlKind runs kubectl in the acceptance context.
func kubectlKind(env map[string]string, args ...string) (string, error) {
	return sh.OutputWith(map[string]string{"KUBECONFIG": env["IORMESH_KIND_KUBECONFIG"]}, "kubectl",
		append([]string{"--context=" + kindContext}, args...)...)
}

// proxyModeLine is the mode line of kube-proxy's config.conf as the
// configmap's YAML indents it (kind writes iptables; empty is iptables
// too, but kind never leaves it empty).
var proxyModeLine = regexp.MustCompile(`(?m)^    mode: [a-z]*$`)

// kindProxyMode sets kube-proxy's mode and restarts it; a cluster already
// in that mode (a kept cluster's second run) is left alone.
func kindProxyMode(env map[string]string, mode string) error {
	cm, err := kubectlKind(env, "--namespace=kube-system", "get", "configmap", "kube-proxy", "--output=yaml")
	if err != nil {
		return err
	}
	line := proxyModeLine.FindString(cm)
	if line == "" {
		return errors.New("kube-proxy's configmap names no mode")
	}
	if line == "    mode: "+mode {
		return nil
	}
	edited := strings.Replace(cm, line, "    mode: "+mode, 1)
	path := filepath.Join(os.TempDir(), "iormesh-kube-proxy.yaml")
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		return err
	}
	defer func() { _ = os.Remove(path) }()
	for _, args := range [][]string{{"apply", "--filename=" + path},
		{"--namespace=kube-system", "rollout", "restart", "daemonset/kube-proxy"},
		{"--namespace=kube-system", "rollout", "status", "daemonset/kube-proxy", "--timeout=120s"}} {
		if _, err := kubectlKind(env, args...); err != nil {
			return err
		}
	}
	return nil
}

// kindRegistryUp starts the local registry unless it runs already.
func kindRegistryUp() error {
	if out, _ := sh.Output("sudo", "-n", "docker", "inspect", "--format={{.State.Running}}", kindRegistry); out == "true" {
		return nil
	}
	return sh.RunV("sudo", "-n", "docker", "run", "--detach", "--restart=no",
		"--publish=127.0.0.1:5001:5000", "--name="+kindRegistry, registryImage)
}

// kindClusterUp creates the cluster unless it exists, hands the
// kubeconfig to the operator and wires the registry into the node.
func kindClusterUp() error {
	out, err := sh.Output("sudo", "-n", "kind", "get", "clusters")
	if err != nil {
		return err
	}
	if !strings.Contains("\n"+out+"\n", "\n"+kindCluster+"\n") {
		if err := createKindCluster(); err != nil {
			return err
		}
	}
	// Already connected is no error worth stopping for.
	_ = exec.Command("sudo", "-n", "docker", "network", "connect", "kind", kindRegistry).Run()
	dir := "/etc/containerd/certs.d/" + kindRegistryAt
	hosts := fmt.Sprintf("[host.\"http://%s:5000\"]\n", kindRegistry)
	return sh.RunV("sudo", "-n", "docker", "exec", kindNode, "sh", "-c",
		"mkdir -p "+dir+" && printf '%s' '"+hosts+"' > "+dir+"/hosts.toml")
}

// createKindCluster runs kind create with kindConfig and chowns the
// kubeconfig kind (run as root) wrote.
func createKindCluster() error {
	cfg := filepath.Join(os.TempDir(), "iormesh-kind.yaml")
	if err := os.WriteFile(cfg, []byte(kindConfig), 0o600); err != nil {
		return err
	}
	defer func() { _ = os.Remove(cfg) }()
	if err := sh.RunV("sudo", "-n", "kind", "create", "cluster", "--name="+kindCluster,
		"--kubeconfig="+kubeconfigPath(), "--config="+cfg); err != nil {
		return err
	}
	owner := strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
	return sh.RunV("sudo", "-n", "chown", owner, kubeconfigPath())
}

// kindTraefik installs Traefik (its CRDs under traefik.io included) with a
// ClusterIP Service: kind has no load balancer.
func kindTraefik() error {
	env := map[string]string{"KUBECONFIG": kubeconfigPath()}
	if err := sh.RunV("helm", "repo", "add", "--force-update", "traefik", "https://traefik.github.io/charts"); err != nil {
		return err
	}
	return sh.RunWithV(env, "helm", "upgrade", "--install", "traefik", traefikChart, "--version="+traefikVersion,
		"--kube-context="+kindContext, "--namespace=traefik", "--create-namespace",
		"--set=service.spec.type=ClusterIP", "--set=ingressClass.isDefaultClass=true", "--wait", "--timeout=5m")
}

// kindTeardown deletes the cluster and the registry container; failures
// are reported, not fatal, so a failed run still cleans up what it can.
func kindTeardown() {
	for _, argv := range [][]string{{"sudo", "-n", "kind", "delete", "cluster", "--name=" + kindCluster},
		{"sudo", "-n", "docker", "rm", "--force", kindRegistry}} {
		if err := sh.RunV(argv[0], argv[1:]...); err != nil {
			fmt.Printf("teardown: %s: %v\n", strings.Join(argv, " "), err)
		}
	}
}

// kubeconfigPath is where the cluster's kubeconfig goes:
// $IORMESH_KIND_KUBECONFIG (a worktree pointing at its main checkout's
// file), else kindKubeconfig at the repository root.
func kubeconfigPath() string {
	if p := os.Getenv("IORMESH_KIND_KUBECONFIG"); p != "" {
		return p
	}
	return kindKubeconfig
}
