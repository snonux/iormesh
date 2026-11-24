package kind

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"iormesh/internal/normalize"
)

// The ingress scenario's names (testdata/workloads.yaml).
const (
	fqdn      = "shop.iormesh.test"
	traefikNS = "traefik/traefik"
)

// TestKindAcceptance is docs/milestones.md M7's acceptance on kind: M0's
// tcp-nat client and server as pods traced by debug containers, joined across
// a ClusterIP and matching their goldens with pod identity; the hairpin
// (a pod calling its own Service); the ingress scenario through a real
// Traefik, its backend traced by a sidecar and its gateway by a debug
// container with the Java agent, rendered as one routed edge; route
// discovery as a list-only identity; and the tracer ending on SIGTERM
// when its pod is deleted.
func TestKindAcceptance(t *testing.T) {
	c := newCluster(t, "iormesh-m7")
	c.prepare(t, true)
	reg := c.writeRegistry(t, true)
	pods := c.runIngress(t, reg)
	for app, p := range c.deploy(t, reg, "server", "client", "hairpin") {
		pods[app] = p
	}
	for _, app := range []string{"server", "client", "hairpin", "gateway"} {
		c.assertTracerSpec(t, pods[app])
	}
	c.assertImageID(t, pods["inventory"], pods["inventory"].Status.Ctrs)
	clusterIP := c.serviceIP(t, "server")
	c.tcpPair(t, pods["server"].Metadata.Name, pods["server"].Status.PodIP, pods["client"].Metadata.Name, clusterIP)
	hp := pods["hairpin"].Metadata.Name
	c.tcpPair(t, hp, pods["hairpin"].Status.PodIP, hp, c.serviceIP(t, "hairpin"))
	time.Sleep(rotation)
	summary := c.collectAndBuild(t, reg, "-ingress-service", traefikNS)
	t.Run("clusterip", func(t *testing.T) { c.checkClusterIP(t, summary, pods, clusterIP) })
	t.Run("hairpin", func(t *testing.T) { c.checkHairpin(t, summary, pods["hairpin"]) })
	t.Run("identity", func(t *testing.T) {
		assertIdentity(t, loadStore(t, c.store(), c.ns), c.ns, pods)
	})
	t.Run("ingress", func(t *testing.T) { c.checkIngress(t, summary) })
	t.Run("rbac", func(t *testing.T) { c.checkDiscoverRBAC(t) })
	t.Run("sigterm", func(t *testing.T) { c.checkSigterm(t, "client") })
}

// TestKindClusterIP is the ClusterIP join alone, for a rerun under
// another kube-proxy mode (mage k8sAcceptance switches kind's kube-proxy
// to nftables and runs it again): tcp-nat across a ClusterIP fuses and
// matches its goldens.
func TestKindClusterIP(t *testing.T) {
	c := newCluster(t, "iormesh-m7-clusterip")
	c.prepare(t, false)
	reg := c.writeRegistry(t, false)
	pods := c.deploy(t, reg, "server", "client")
	clusterIP := c.serviceIP(t, "server")
	c.tcpPair(t, pods["server"].Metadata.Name, pods["server"].Status.PodIP, pods["client"].Metadata.Name, clusterIP)
	time.Sleep(rotation)
	summary := c.collectAndBuild(t, reg)
	t.Logf("kube-proxy mode: %s", c.proxyMode(t))
	c.checkClusterIP(t, summary, pods, clusterIP)
}

// runIngress runs the ingress scenario: the backend in the inventory pod
// (its sidecar tracer already running), then the gateway behind the
// workload's startup gate, so the debug tracer deployed meanwhile finds
// its JVM at start and live-loads the agent, then releases the gateway,
// which asks for the FQDN through Traefik. It returns the two pods.
func (c *cluster) runIngress(t *testing.T, reg string) map[string]podJSON {
	t.Helper()
	inv := c.pod(t, "inventory")
	c.waitLog(t, inv.Metadata.Name, "iormesh: attached", time.Minute)
	backend := c.start(t, "backend", inv.Metadata.Name, nil, "java", "-jar", workloadJar, "ingress",
		"--role", "backend", "--bind", inv.Status.PodIP, "--port", "8000")
	backend.waitFor(t, "listening 8000", time.Minute)
	gw := c.pod(t, "gateway").Metadata.Name
	gateway := c.start(t, "gateway", gw, []string{"IORMESH_WORKLOAD_STARTUP_FILE=/tmp/go"}, "java", "-jar",
		workloadJar, "ingress", "--role", "gateway", "--fqdn", fqdn)
	gateway.waitFor(t, "\n", time.Minute) // the PID line: gated, not yet bracketed
	pods := c.deploy(t, reg, "gateway")
	c.waitLog(t, gw, "joined", time.Minute)
	c.kubectl(t, "exec", gw, "--container=app", "--", "touch", "/tmp/go")
	gateway.wait(t, "ingress")
	backend.wait(t, "ingress")
	pods["inventory"] = c.pod(t, "inventory")
	return pods
}

// serviceIP is a Service's ClusterIP.
func (c *cluster) serviceIP(t *testing.T, name string) string {
	t.Helper()
	return strings.TrimSpace(c.kubectl(t, "get", "service", name, "--output=jsonpath={.spec.clusterIP}"))
}

// proxyMode is kube-proxy's configured mode, as kind's configmap says;
// a cluster without one (k3s embeds kube-proxy, task zg4) is "unknown",
// only logged.
func (c *cluster) proxyMode(t *testing.T) string {
	t.Helper()
	cm, err := c.try("--namespace=kube-system", "get", "configmap", "kube-proxy",
		"--output=jsonpath={.data.config\\.conf}")
	if err != nil {
		return "unknown (no kube-proxy configmap)"
	}
	for _, l := range strings.Split(cm, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "mode:"); ok {
			return strings.TrimSpace(v)
		}
	}
	return "unknown"
}

func (c *cluster) store() string  { return filepath.Join(c.dir, "store") }
func (c *cluster) db() string     { return filepath.Join(c.dir, "services.db") }
func (c *cluster) builds() string { return filepath.Join(c.dir, "builds") }

// collectAndBuild collects every pod entry, discovers the routes of the
// test's namespace and Traefik's (discoverArgs added), and builds; it
// returns the build's summary.
func (c *cluster) collectAndBuild(t *testing.T, reg string, discoverArgs ...string) string {
	t.Helper()
	c.ctl(t, nil, "collect", "-registry", reg, "-store", c.store(), "-db", c.db())
	args := []string{"routes", "discover", "-registry", reg, "-cluster", "kind", "-namespace", c.ns, "-db", c.db()}
	if len(discoverArgs) > 0 {
		args = append(append(args, "-namespace", "traefik"), discoverArgs...)
	}
	c.ctl(t, nil, args...)
	out := c.ctl(t, nil, "build", "-registry", reg, "-project", c.ns, "-store", c.store(), "-db", c.db(),
		"-builds", c.builds())
	t.Logf("build:\n%s", out)
	return out
}

// assertLines fails for each want missing from the summary.
func assertLines(t *testing.T, summary string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(summary, w) {
			t.Errorf("build summary lacks %q", w)
		}
	}
}

// readModel reads the build's model.json routes and route flags.
func (c *cluster) readModel(t *testing.T) (routes []map[string]any, flags []any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(c.builds(), c.ns, "model.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Routes []map[string]any `json:"routes"`
		Health struct {
			RouteFlags []any `json:"route_flags"`
		} `json:"health"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m.Routes, m.Health.RouteFlags
}

// normalizedDump writes a normalized trace for a golden's author when
// IORMESH_KIND_DUMP names a directory.
func normalizedDump(t *testing.T, name string, got normalize.Trace) {
	dir := os.Getenv("IORMESH_KIND_DUMP")
	if dir == "" {
		return
	}
	data, err := json.MarshalIndent(got, "", " ")
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, name+".json"), data, 0o600)
	}
	if err != nil {
		t.Logf("dump %s: %v", name, err)
	}
}
