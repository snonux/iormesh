package kind

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"iormesh/internal/k8s"
	"iormesh/internal/k8sname"
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
	// task zg4). Empty skips the SIGTERM check and the restart test's
	// kill, and a skip fails the mage targets (task si4).
	crictl []string
	// seType and seLevel are the tracer's SELinux type and level:
	// k8s.DefaultSELinuxType and DefaultSELinuxLevel (spc_t at s0) unless
	// IORMESH_KIND_SELINUX_TYPE or _LEVEL names others (task 7h4, the
	// opt-in iormesh_container_t). The registry, the sidecar patch and
	// the spec check take them; a level other than s0 is also pinned on
	// every workload pod (podLevel), as an owner pinning MCS pairs would.
	seType, seLevel string
}

// podLevel is the MCS level the workload pods pin: none at the default
// s0, which every pod's pair dominates, else the tracer's own pair, so the
// tracer and the app containers share it (decision #13, task 8h4).
func (c config) podLevel() string {
	if c.seLevel == k8s.DefaultSELinuxLevel {
		return ""
	}
	return c.seLevel
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
	envSEType     = "IORMESH_KIND_SELINUX_TYPE"
	envSELevel    = "IORMESH_KIND_SELINUX_LEVEL"
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
	c.seType, c.seLevel = selinuxOptions(os.Getenv(envSEType), os.Getenv(envSELevel))
	if !k8sname.SELinuxType(c.seType) || !k8sname.SELinuxLevel(c.seLevel) {
		t.Fatalf("%s %q or %s %q: want a type [a-z0-9_]+ and a level s0 or s0:cN,cM",
			envSEType, c.seType, envSELevel, c.seLevel)
	}
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

// selinuxOptions is the tracer's SELinux type and level: the operator's,
// each defaulting to the shipped one (spc_t, s0).
func selinuxOptions(seType, seLevel string) (string, string) {
	if seType == "" {
		seType = k8s.DefaultSELinuxType
	}
	if seLevel == "" {
		seLevel = k8s.DefaultSELinuxLevel
	}
	return seType, seLevel
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
	data := map[string]any{"NS": c.ns, "Workload": c.cfg.workload, "Full": full, "PodLevel": c.cfg.podLevel()}
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
		c.patch(t, "inventory", sidecarSELinux(files["sidecar-patch.yaml"], c.cfg.seType, c.cfg.seLevel))
		c.rollout(t, "inventory")
	}
}

// sidecarSELinux sets the static sidecar patch's tracer type and level
// (spc_t at s0) to seType and seLevel, as its header tells an owner to.
func sidecarSELinux(patch []byte, seType, seLevel string) []byte {
	s := strings.Replace(string(patch), "level: "+k8s.DefaultSELinuxLevel+"\n", "level: "+seLevel+"\n", 1)
	return []byte(strings.Replace(s, "type: "+k8s.DefaultSELinuxType+"\n", "type: "+seType+"\n", 1))
}

// TestSELinuxOptions: the shipped spc_t at s0 by default, the operator's
// type and level otherwise (task 7h4), the pod level pinned only off s0,
// and the sidecar patch's tracer options rewritten.
func TestSELinuxOptions(t *testing.T) {
	if ty, lv := selinuxOptions("", ""); ty != "spc_t" || lv != "s0" {
		t.Errorf("default = %s %s, want spc_t s0", ty, lv)
	}
	ty, lv := selinuxOptions("iormesh_container_t", "s0:c100,c200")
	if ty != "iormesh_container_t" || lv != "s0:c100,c200" {
		t.Errorf("operator's = %s %s", ty, lv)
	}
	if (config{seLevel: "s0"}).podLevel() != "" || (config{seLevel: lv}).podLevel() != lv {
		t.Error("podLevel: want none at s0, the tracer's pair otherwise")
	}
	files, err := k8s.Defaults().Files()
	if err != nil {
		t.Fatal(err)
	}
	got := string(sidecarSELinux(files["sidecar-patch.yaml"], ty, lv))
	if !strings.Contains(got, "level: s0:c100,c200\n") || !strings.Contains(got, "type: iormesh_container_t\n") ||
		strings.Contains(got, "type: spc_t\n") || strings.Contains(got, "level: s0\n") {
		t.Errorf("sidecar patch not rewritten:\n%s", got)
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
