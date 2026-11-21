package kind

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"iormesh/internal/k8s"
)

// config is the cluster the tests run against, from the environment
// `mage k8sAcceptance` sets (magefile_k8sacceptance.go). Unset, the tests
// skip: they need a kind cluster with Traefik, a registry holding the
// images, and kubectl on PATH.
type config struct {
	kubeconfig, context string
	tracer, workload    string // images, the tracer pinned by digest
	ctl                 string // the iormeshctl binary
	node                string // kind's node container, for crictl
	// crictl is the argv prefix that runs crictl on the node: on kind
	// `sudo -n docker exec <node> crictl`, on an existing cluster the
	// operator's (IORMESH_KIND_CRICTL, say `ssh root@node k3s crictl`,
	// task zg4). Empty skips the SIGTERM check.
	crictl []string
}

// Environment variables of the acceptance run.
const (
	envKubeconfig = "IORMESH_KIND_KUBECONFIG"
	envContext    = "IORMESH_KIND_CONTEXT"
	envTracer     = "IORMESH_KIND_TRACER_IMAGE"
	envWorkload   = "IORMESH_KIND_WORKLOAD_IMAGE"
	envCtl        = "IORMESH_KIND_CTL"
	envNode       = "IORMESH_KIND_NODE"
	envCrictl     = "IORMESH_KIND_CRICTL"
	envKeep       = "IORMESH_KIND_KEEP"
)

// rotation outlasts the segment writer's interval (segment.DefaultInterval,
// a minute), so every event of a run is in a published segment.
const rotation = 75 * time.Second

// loadConfig reads the environment or skips the test.
func loadConfig(t *testing.T) config {
	t.Helper()
	c := config{kubeconfig: os.Getenv(envKubeconfig), context: os.Getenv(envContext),
		tracer: os.Getenv(envTracer), workload: os.Getenv(envWorkload),
		ctl: os.Getenv(envCtl), node: os.Getenv(envNode)}
	if c.kubeconfig == "" || c.context == "" || c.tracer == "" || c.workload == "" {
		t.Skipf("no kind cluster: set %s, %s, %s and %s (mage k8sAcceptance does)",
			envKubeconfig, envContext, envTracer, envWorkload)
	}
	c.crictl = crictlPrefix(os.Getenv(envCrictl), c.node)
	if c.ctl == "" {
		c.ctl = "../../iormeshctl"
	}
	abs, err := filepath.Abs(c.ctl)
	if err != nil {
		t.Fatal(err)
	}
	c.ctl = abs
	return c
}

// crictlPrefix is the argv prefix running crictl on the node: the
// operator's, split on blanks, else docker exec into kind's node
// container (through sudo -n, as every docker call of the run), else
// none.
func crictlPrefix(operator, node string) []string {
	if f := strings.Fields(operator); len(f) > 0 {
		return f
	}
	if node != "" {
		return []string{"sudo", "-n", "docker", "exec", node, "crictl"}
	}
	return nil
}

// newCluster gives the test a fresh namespace (deleted again unless
// IORMESH_KIND_KEEP is set) and a private directory for the registry,
// the store and the builds.
func newCluster(t *testing.T, ns string) *cluster {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	c := &cluster{cfg: loadConfig(t), ns: ns, dir: dir, logs: &strings.Builder{}}
	_, _ = c.try("delete", "namespace", ns, "--ignore-not-found", "--wait=true", "--timeout=120s")
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("iormeshctl transcript:\n%s", c.logs.String())
		}
		if os.Getenv(envKeep) == "" {
			_, _ = c.try("delete", "namespace", ns, "--wait=false")
		}
	})
	return c
}

// prepare applies the manifests and, as an operator would, deploy/'s
// patches rendered for the test's tracer image: the prepare patch on the
// debug-traced Deployments, the sidecar patch (its target set to the
// workload's pattern) on inventory.
func (c *cluster) prepare(t *testing.T, full bool) {
	t.Helper()
	data := map[string]any{"NS": c.ns, "Workload": c.cfg.workload, "Full": full}
	if full {
		data["TraefikIP"] = strings.TrimSpace(c.kubectl(t, "--namespace=traefik", "get", "service",
			"traefik", "--output=jsonpath={.spec.clusterIP}"))
	}
	c.apply(t, "workloads.yaml", data)
	m := k8s.Manifests{Image: c.cfg.tracer, TraceLimit: k8s.DefaultTraceLimit,
		SidecarArgs: []string{"run", "-pattern", "*workload.jar*", "-duration", "1h"}}
	files, err := m.Files()
	if err != nil {
		t.Fatal(err)
	}
	debug := []string{"server", "client"}
	if full {
		debug = append(debug, "hairpin", "gateway")
	}
	for _, d := range debug {
		c.patch(t, d, files["prepare-patch.yaml"])
	}
	c.rollout(t, debug...)
	if full {
		c.patch(t, "inventory", files["sidecar-patch.yaml"])
		c.rollout(t, "inventory")
	}
}

// TestCrictlPrefix: the operator's prefix wins (an existing cluster's
// node, task zg4), kind's node container is the default, and with
// neither the SIGTERM check has none.
func TestCrictlPrefix(t *testing.T) {
	for _, tc := range []struct {
		operator, node, want string
	}{
		{"ssh root@iom1  k3s crictl", "n", "ssh root@iom1 k3s crictl"},
		{"", "iormesh-m7-control-plane", "sudo -n docker exec iormesh-m7-control-plane crictl"},
		{" ", "", ""},
	} {
		if got := strings.Join(crictlPrefix(tc.operator, tc.node), " "); got != tc.want {
			t.Errorf("crictlPrefix(%q, %q) = %q, want %q", tc.operator, tc.node, got, tc.want)
		}
	}
}
