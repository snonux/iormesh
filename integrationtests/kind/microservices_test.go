package kind

import (
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"iormesh/internal/meshcheck"
	"iormesh/internal/schema"
)

// The microservices scenario as pods (task uh4; PLAN.md M0 "Runs on plain
// hosts (M4/M6) and as pods (M7)"): one pod per service, each its own
// host, traced by a debug container with the Java agent, the services
// dialing each other's pod addresses at the rig spread's ports:
//
//	billing   :443 (TLS)  pinned by orders
//	inventory :80         called by orders and by the gateway
//	orders    :443 (TLS)  pinned by the gateway
//	gateway               drives the checkout
//
// Every hop crosses pods, so the merge fuses all four (the rig's spread
// leaves gateway→inventory to hosta's tracer).
var microServices = []string{"billing", "inventory", "orders", "gateway"}

// Ports the servers bind on their pod addresses, as in the rig's spread.
const (
	microPlainPort = "80"
	microTLSPort   = "443"
)

// microDir is every service's --dir in its pod: the golden's $tmp, where
// a TLS server publishes its certificate and its callers pin it.
const microDir = "/tmp/w"

// startGate is the workload's startup gate file in the app container
// (IORMESH_WORKLOAD_STARTUP_FILE): the JVM prints its PID and waits for
// it, so the tracer deployed meanwhile live-loads the agent before the
// bracket.
const startGate = "/tmp/go"

// microCerts are the certificates the deployment ships before a caller
// boots, as the rig's shared --dir and the two-host acceptance's copy do:
// caller → the TLS callee whose published certificate it pins.
var microCerts = map[string]string{"orders": "billing", "gateway": "orders"}

// TestKindMicroservices runs the microservices scenario as four pods,
// traced, collected and built through iormeshctl's k8s path, and makes
// the cross-service assertions the rig's spread makes (meshcheck.Check):
// every hop fused at its server pod's address, nothing unresolved, the
// merged trace holding expect-pods.json — expect-spread.json with the
// gateway on a host of its own — at the pods' addresses, every request
// paired across the wire; then that every event names its pod and the
// process's app container (task sh4), and the pod identity rules.
func TestKindMicroservices(t *testing.T) {
	c := newCluster(t, "iormesh-m7-micro")
	c.prepareMicro(t)
	entries := make([]podEntry, len(microServices))
	for i, s := range microServices {
		entries[i] = podEntry{app: s, flags: agentFlags}
	}
	reg := c.writeEntries(t, entries)
	pods := c.runMicroservices(t, reg)
	time.Sleep(rotation)
	summary := c.collectAndBuild(t, reg)
	assertLines(t, summary, ": 4 hosts, 4 sessions,", "; tcp 4 fused, 0 unresolved")
	hosts := loadStore(t, c.store(), c.ns)
	t.Run("mesh", func(t *testing.T) { checkMicroMesh(t, hosts, pods, c.ns) })
	t.Run("pod fields", func(t *testing.T) { assertPodFields(t, hosts, c.ns, pods) })
	t.Run("identity", func(t *testing.T) { assertIdentity(t, hosts, c.ns, pods) })
}

// prepareMicro applies the four Deployments and, as an operator would,
// the prepare patch on each (all debug-traced), then waits for them.
func (c *cluster) prepareMicro(t *testing.T) {
	t.Helper()
	c.apply(t, "microservices.yaml", microData(c.ns, c.cfg.workload, c.cfg.podLevel()))
	files := c.manifests(t)
	for _, s := range microServices {
		c.patch(t, s, files["prepare-patch.yaml"])
	}
	c.rollout(t, microServices...)
}

// microData is testdata/microservices.yaml's template data.
func microData(ns, workload, podLevel string) map[string]any {
	return map[string]any{"NS": ns, "Workload": workload, "PodLevel": podLevel, "Apps": microServices}
}

// microArgs are each service's options after --role and --dir: a server
// binds its pod's address, a caller dials each callee's, which its
// certificate names (Microservices.java).
func microArgs(ip map[string]string) map[string][]string {
	return map[string][]string{
		"billing":   {"--bind", ip["billing"], "--port", microTLSPort},
		"inventory": {"--bind", ip["inventory"], "--port", microPlainPort},
		"orders": {"--bind", ip["orders"], "--port", microTLSPort,
			"--inventory-host", ip["inventory"], "--inventory-port", microPlainPort,
			"--billing-host", ip["billing"], "--billing-port", microTLSPort},
		"gateway": {"--orders-host", ip["orders"], "--orders-port", microTLSPort,
			"--inventory-host", ip["inventory"], "--inventory-port", microPlainPort},
	}
}

// runMicroservices runs the services leaves first, each started behind
// the startup gate, its tracer deployed and joined by the agent, the
// certificate it pins shipped in, then released — a server only left once
// it listens — and waits for every service's bracket to close. It returns
// the pods as they are after the deploys (their debug containers listed).
func (c *cluster) runMicroservices(t *testing.T, reg string) map[string]podJSON {
	t.Helper()
	ip, name := map[string]string{}, map[string]string{}
	for _, s := range microServices {
		p := c.pod(t, s)
		ip[s], name[s] = p.Status.PodIP, p.Metadata.Name
		c.kubectl(t, "exec", name[s], "--container=app", "--", "sh", "-c",
			"rm -rf "+microDir+" "+startGate+" && mkdir -p "+microDir)
	}
	args := microArgs(ip)
	roles := map[string]*role{}
	for _, s := range microServices {
		if callee, ok := microCerts[s]; ok {
			c.copyFile(t, name[callee], name[s], microDir+"/"+callee+".crt")
		}
		roles[s] = c.startGated(t, s, name[s], reg, args[s])
	}
	for i := len(microServices) - 1; i >= 0; i-- {
		roles[microServices[i]].wait(t, meshcheck.Scenario)
	}
	pods := map[string]podJSON{}
	for _, s := range microServices {
		pods[s] = c.pod(t, s)
	}
	return pods
}

// startGated starts app's service in its pod behind the startup gate,
// deploys the pod's debug tracer (deploy returns once it is attached) and
// waits for the agent to join the JVM, then opens the gate; a server is
// waited for until it listens, its certificate published.
func (c *cluster) startGated(t *testing.T, app, pod, reg string, args []string) *role {
	t.Helper()
	argv := append([]string{"java", "-jar", workloadJar, meshcheck.Scenario, "--role", app, "--dir", microDir}, args...)
	r := c.start(t, app, pod, []string{"IORMESH_WORKLOAD_STARTUP_FILE=" + startGate}, argv...)
	r.waitFor(t, "\n", time.Minute) // the PID line: gated, not yet bracketed
	c.deploy(t, reg, app)
	c.waitLog(t, pod, "joined", time.Minute)
	c.kubectl(t, "exec", pod, "--container=app", "--", "touch", startGate)
	if app != "gateway" {
		r.waitFor(t, "listening ", time.Minute)
	}
	return r
}

// copyFile copies path from one pod's app container to the same path in
// another's, through kubectl exec (cat out, cat in): the shell processes
// match no tracer's pattern, so the copy stays out of the traces.
func (c *cluster) copyFile(t *testing.T, fromPod, toPod, path string) {
	t.Helper()
	data := c.kubectl(t, "exec", fromPod, "--container=app", "--", "cat", path)
	if data == "" {
		t.Fatalf("pod %s: %s is empty", fromPod, path)
	}
	if _, err := c.tryIn([]byte(data), "exec", "-i", toPod, "--container=app", "--", "sh", "-c",
		"cat > "+path); err != nil {
		t.Fatalf("copy %s to pod %s: %v", path, toPod, err)
	}
}

// microHops are the four hops between pods at their server pods'
// addresses; inventory's end is dialed twice, by orders and the gateway.
func microHops(ip map[string]string) []meshcheck.Hop {
	end := func(s, port string) netip.AddrPort { return netip.MustParseAddrPort(ip[s] + ":" + port) }
	return []meshcheck.Hop{
		{Server: end("orders", microTLSPort), Label: "gateway→orders (TLS)"},
		{Server: end("inventory", microPlainPort), Label: "orders→inventory"},
		{Server: end("billing", microTLSPort), Label: "orders→billing (TLS)"},
		{Server: end("inventory", microPlainPort), Label: "gateway→inventory"},
	}
}

// microAddrs replace the rig's addresses in expect-pods.json (old → new
// pairs) with the pods': hosta's (inventory there), hostb's (orders) and
// the container namespace's (billing). The gateway's own address is in
// no declaration: only servers' ends and the hosts callers dial are.
func microAddrs(ip map[string]string) []string {
	return []string{"10.231.0.1", ip["inventory"], "10.231.0.2", ip["orders"], "172.31.0.2", ip["billing"]}
}

// checkMicroMesh merges the four pods' traces and makes the rig spread's
// cross-service assertions on them (meshcheck.Check) with the pods'
// addresses, every hop within the test's project (one registry project
// traces every pod).
func checkMicroMesh(t *testing.T, hosts map[string]*hostTrace, pods map[string]podJSON, ns string) {
	ip := map[string]string{}
	var traces []*hostTrace
	for _, s := range microServices {
		h := hosts["pod-"+pods[s].Metadata.UID]
		if h == nil {
			t.Fatalf("no store directory for %s's pod", s)
		}
		traces = append(traces, h)
		ip[s] = pods[s].Status.PodIP
	}
	res, merged := merge(t, traces...)
	want := golden(t, meshcheck.Scenario, "expect-pods.json", microAddrs(ip)...)
	got := meshcheck.Check(t, meshcheck.Mesh{Result: res, Merged: merged, TmpPrefix: microDir,
		Want: want, Hops: microHops(ip), Project: ns})
	normalizedDump(t, meshcheck.Scenario, got)
	t.Logf("mesh: %d fused connections, %d unresolved", len(res.Conns), len(res.Unresolved))
}

// assertPodFields: every event a pod's tracer wrote names the pod (PLAN.md
// M7 "Pod fields on events", task sh4) — namespace and name on every
// event, and on a process's events the runtime id of the app container
// its JVM runs in, as the pod's status lists it; none on host-level
// events (pid 0).
func assertPodFields(t *testing.T, hosts map[string]*hostTrace, ns string, pods map[string]podJSON) {
	for app, p := range pods {
		h := hosts["pod-"+p.Metadata.UID]
		if h == nil {
			t.Errorf("pod %s: no store directory", app)
			continue
		}
		ctr := appContainerID(p)
		if bad, procs := podFieldMismatches(t, app, h.events, ns, p.Metadata.Name, ctr); bad > 0 || procs == 0 {
			t.Errorf("pod %s: %d of %d events with a wrong pod object, %d process events", app, bad,
				len(h.events), procs)
		}
	}
}

// podFieldMismatches counts the events whose pod object is not the
// pod's, reporting the first, and the process events seen.
func podFieldMismatches(t *testing.T, app string, events []schema.Event, ns, name, ctr string) (bad, procs int) {
	for _, e := range events {
		want := schema.Pod{Namespace: ns, Name: name}
		if e.PID != 0 {
			want.ContainerID = ctr
			procs++
		}
		if e.Pod == nil || *e.Pod != want {
			if bad == 0 {
				t.Errorf("pod %s: event seq %d (%s, pid %d) pod %s, want %+v", app, e.Seq, e.Event, e.PID,
					podString(e.Pod), want)
			}
			bad++
		}
	}
	return bad, procs
}

// podString prints a pod object, or none.
func podString(p *schema.Pod) string {
	if p == nil {
		return "none"
	}
	return fmt.Sprintf("%+v", *p)
}

// appContainerID is the runtime id of the pod's app container, its
// status' containerID without the runtime's scheme.
func appContainerID(p podJSON) string {
	for _, s := range p.Status.Ctrs {
		if s.Name == "app" {
			_, id, _ := strings.Cut(s.ContainerID, "://")
			return id
		}
	}
	return ""
}

// TestMicroTemplate: the template renders one Deployment per service, the
// pod level pinned only when set.
func TestMicroTemplate(t *testing.T) {
	for _, level := range []string{"", "s0:c1,c2"} {
		text, err := render("microservices.yaml", microData("ns1", "reg/w@sha256:1", level))
		if err != nil {
			t.Fatal(err)
		}
		y := string(text)
		if n := strings.Count(y, "kind: Deployment"); n != len(microServices) {
			t.Errorf("level %q: %d Deployments, want %d", level, n, len(microServices))
		}
		for _, s := range microServices {
			if !strings.Contains(y, "metadata: {name: "+s+", namespace: ns1}") ||
				!strings.Contains(y, "selector: {matchLabels: {app: "+s+"}}") {
				t.Errorf("no Deployment %s:\n%s", s, y)
			}
		}
		if pinned := strings.Contains(y, "seLinuxOptions"); pinned != (level != "") {
			t.Errorf("level %q: seLinuxOptions present %v", level, pinned)
		}
	}
}

// TestMicroAddresses: every service gets the options the rig's spread
// gives it at the pods' addresses, the hops name inventory's end twice,
// and the golden's rig addresses map onto the right pods.
func TestMicroAddresses(t *testing.T) {
	ip := map[string]string{"billing": "10.244.0.10", "inventory": "10.244.0.11", "orders": "10.244.0.12"}
	args := microArgs(ip)
	if got := strings.Join(args["orders"], " "); got != "--bind 10.244.0.12 --port 443 --inventory-host 10.244.0.11 "+
		"--inventory-port 80 --billing-host 10.244.0.10 --billing-port 443" {
		t.Errorf("orders' options: %s", got)
	}
	if got := strings.Join(args["gateway"], " "); got != "--orders-host 10.244.0.12 --orders-port 443 "+
		"--inventory-host 10.244.0.11 --inventory-port 80" {
		t.Errorf("gateway's options: %s", got)
	}
	inv := 0
	for _, h := range microHops(ip) {
		if h.Server.String() == "10.244.0.11:80" {
			inv++
		}
	}
	if inv != 2 {
		t.Errorf("inventory's end in %d hops, want 2", inv)
	}
	x := golden(t, meshcheck.Scenario, "expect-pods.json", microAddrs(ip)...)
	if h := x.Events[meshcheck.ClientHTTPIndex(&x, "/billing/charge")].HTTP.Host; h != "10.244.0.10" {
		t.Errorf("orders dials billing at %s, want its pod's address", h)
	}
	if h := x.Events[meshcheck.ClientHTTPIndex(&x, "/inventory/stock/widget")].HTTP.Host; h != "10.244.0.11" {
		t.Errorf("the gateway dials inventory at %s, want its pod's address", h)
	}
}
