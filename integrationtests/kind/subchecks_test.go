package kind

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"iormesh/internal/normalize"
	"iormesh/internal/routemap"
	"iormesh/internal/schema"
)

// checkClusterIP: the client's connection to the server's ClusterIP
// fuses with the server's accept (the ISN pair survives kube-proxy's
// DNAT), the build names the Service, its Deployment and the server pod
// (matched by UID), and each pod's trace and the merged one match
// tcp-nat's goldens — the rig's addresses replaced by the client pod's,
// the ClusterIP and the server pod's (no SNAT between pods: the server
// sees the client's own address).
func (c *cluster) checkClusterIP(t *testing.T, summary string, pods map[string]podJSON, clusterIP string) {
	ns, srv, cli := c.ns, pods["server"], pods["client"]
	assertLines(t, summary, "-> "+ns+"/server -> "+ns+"/deployment/server -> "+ns+"/"+srv.Metadata.Name+
		" (fused, 1 connections)")
	hosts := loadStore(t, c.store(), ns)
	sh, ch := hosts["pod-"+srv.Metadata.UID], hosts["pod-"+cli.Metadata.UID]
	if sh == nil || ch == nil {
		t.Fatalf("no store directory for the server or the client: %v", hosts)
	}
	addrs := []string{"10.231.0.1", cli.Status.PodIP, "10.231.0.2", clusterIP, "172.31.0.2", srv.Status.PodIP}
	for role, h := range map[string]*hostTrace{"client": ch, "server": sh} {
		_, alone := merge(t, h)
		assertGolden(t, alone, "tcp-nat", "/tmp/w", golden(t, "tcp-nat", "expect-"+role+".json", addrs...))
	}
	res, merged := merge(t, ch, sh)
	if len(res.Conns) != 1 {
		t.Fatalf("merge fused %d connections, want 1", len(res.Conns))
	}
	want := golden(t, "tcp-nat", "expect.json", addrs...)
	got := assertGolden(t, merged, "tcp-nat", "/tmp/w", want)
	assertCaught(t, got, want, map[string]func(*normalize.Expectation){
		"the client dialing the pod, not the ClusterIP": func(x *normalize.Expectation) {
			for i, e := range x.Events {
				if e.Event == schema.KindConnect {
					a := srv.Status.PodIP
					x.Events[i].Args.RemoteAddr = &a
				}
			}
		},
	})
}

// checkHairpin: a pod calling its own Service — both ends in one pod —
// fuses, though the server end saw the node's masquerade address, and
// the build names the pod's Service and the pod itself.
func (c *cluster) checkHairpin(t *testing.T, summary string, p podJSON) {
	ns := c.ns
	assertLines(t, summary, "-> "+ns+"/hairpin -> "+ns+"/deployment/hairpin -> "+ns+"/"+p.Metadata.Name+
		" (fused, 1 connections)")
	h := loadStore(t, c.store(), ns)["pod-"+p.Metadata.UID]
	if h == nil {
		t.Fatal("no hairpin store directory")
	}
	res, _ := merge(t, h)
	if len(res.Conns) != 1 {
		t.Fatalf("hairpin: %d fused connections, want 1 (unresolved %v)", len(res.Conns), res.Unresolved)
	}
	peer := res.Conns[0].Server.Peer.Addr().Unmap().String()
	if peer == p.Status.PodIP {
		t.Errorf("hairpin: the server saw the pod's own address %s, not a masquerade source", peer)
	}
	t.Logf("hairpin: client %s -> %s, server saw %s", res.Conns[0].Client.Local, res.Conns[0].Client.Peer,
		res.Conns[0].Server.Peer)
}

// checkIngress: the gateway's call through Traefik is one routed edge —
// gateway → FQDN → the Ingress → the inventory Service → its Deployment
// → its one pod, traced by the sidecar (task ff4) — in the summary,
// model.json and graph.svg, with no route flag, though no tracer sits on
// Traefik; the discovered snapshot also holds the IngressRoute's host
// (traefik.io group).
func (c *cluster) checkIngress(t *testing.T, summary string) {
	ns := c.ns
	invPod := ns + "/" + c.pod(t, "inventory").Metadata.Name
	assertLines(t, summary, "-> "+fqdn+" -> ingress/"+ns+"/shop -> "+ns+"/inventory -> "+ns+
		"/deployment/inventory -> "+invPod+" (route, 1 connections)")
	if strings.Contains(summary, "route flag") {
		t.Errorf("the build flagged a route")
	}
	routes, flags := c.readModel(t)
	found := false
	for _, r := range routes {
		found = found || r["fqdn"] == fqdn && r["ingress"] == "ingress/"+ns+"/shop" &&
			r["service"] == ns+"/inventory" && r["workload"] == ns+"/deployment/inventory" && r["basis"] == "route" &&
			r["pod"] == invPod
	}
	if !found || len(flags) != 0 {
		t.Errorf("model.json routes %v, flags %v", routes, flags)
	}
	svg, err := os.ReadFile(filepath.Join(c.builds(), ns, "graph.svg"))
	if err != nil || !strings.Contains(string(svg), fqdn) || !strings.Contains(string(svg), ns+"/inventory") {
		t.Errorf("graph.svg lacks the routed edge (%v)", err)
	}
	c.checkDeclaredRoute(t)
	c.checkIngressGolden(t)
}

// checkDeclaredRoute: the route the scenario declares beside its golden
// (workload/scenarios/ingress/routes.yaml, loaded under the static map's
// rules) is the one discovery found from the Ingress, and the snapshot
// also holds the IngressRoute's host (traefik.io group).
func (c *cluster) checkDeclaredRoute(t *testing.T) {
	declared, err := routemap.Load(filepath.Join(scenariosDir, "ingress", "routes.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	snap, err := routemap.LoadSnapshot(filepath.Join(c.dir, routemap.SnapshotName))
	if err != nil || snap == nil {
		t.Fatalf("snapshot: %v", err)
	}
	found := map[string]routemap.Route{}
	for _, r := range snap.Map.Routes {
		found[r.FQDN] = r
	}
	want := declared.Routes[0]
	got := found[want.FQDN]
	if got.Ingress != want.Ingress || got.Service != want.Service || got.IngressService != want.IngressService {
		t.Errorf("discovered %+v, declared %+v", got, want)
	}
	if found["stock.iormesh.test"].Ingress != "ingressroute/"+c.ns+"/stock" {
		t.Errorf("the snapshot lacks the IngressRoute: %+v", snap.Map.Routes)
	}
}

// checkIngressGolden: the gateway's and the backend's traces, merged,
// match the ingress scenario's golden — both ends unresolved, since
// Traefik splits the call into two connections no tracer joins, the
// gateway's request carrying the FQDN as its host (the agent's http
// event). The golden's TEST-NET addresses stand for the gateway pod,
// Traefik's ClusterIP, the backend pod and Traefik's pod.
func (c *cluster) checkIngressGolden(t *testing.T) {
	hosts := loadStore(t, c.store(), c.ns)
	gw, inv := c.pod(t, "gateway"), c.pod(t, "inventory")
	gh, ih := hosts["pod-"+gw.Metadata.UID], hosts["pod-"+inv.Metadata.UID]
	if gh == nil || ih == nil {
		t.Fatal("no store directory for the gateway or the backend")
	}
	traefikPod := strings.TrimSpace(c.kubectl(t, "--namespace=traefik", "get", "pods",
		"--output=jsonpath={.items[0].status.podIP}"))
	traefikIP := strings.TrimSpace(c.kubectl(t, "--namespace=traefik", "get", "service", "traefik",
		"--output=jsonpath={.spec.clusterIP}"))
	addrs := []string{"192.0.2.10", gw.Status.PodIP, "192.0.2.80", traefikIP,
		"192.0.2.20", inv.Status.PodIP, "192.0.2.30", traefikPod}
	_, merged := merge(t, gh, ih)
	got, err := normalize.Normalize(merged, normalize.Options{Bracket: "ingress"})
	if err != nil {
		t.Fatalf("normalize ingress: %v", err)
	}
	normalizedDump(t, "ingress", got)
	want := golden(t, "ingress", "expect.json", addrs...)
	for _, v := range normalize.Check(got, want) {
		t.Errorf("golden ingress: %v", v)
	}
	assertCaught(t, got, want, map[string]func(*normalize.Expectation){
		"the request's host the ingress's address": func(x *normalize.Expectation) {
			for i, e := range x.Events {
				if e.HTTP != nil {
					h := *e.HTTP
					h.Host = traefikIP
					x.Events[i].HTTP = &h
				}
			}
		},
		"the gateway's call fused through the ingress": func(x *normalize.Expectation) {
			for i, e := range x.Edges {
				if e.To == "conn:$c1" {
					x.Edges[i].State = "fused"
				}
			}
		},
	})
}

// checkDiscoverRBAC runs `routes discover` as the namespace's list-only
// ServiceAccount (testdata: list on six resources, nothing else) through a
// kubeconfig of its own: it must find the same two routes.
func (c *cluster) checkDiscoverRBAC(t *testing.T) {
	token := strings.TrimSpace(c.kubectl(t, "create", "token", "iormesh-routes", "--duration=10m"))
	kc := filepath.Join(c.dir, "kubeconfig-ro")
	raw := c.kubectl(t, "config", "view", "--raw", "--minify")
	if err := os.WriteFile(kc, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	ro := &cluster{cfg: c.cfg, ns: c.ns, dir: c.dir, logs: c.logs}
	ro.cfg.kubeconfig = kc
	ro.kubectl(t, "config", "set-credentials", "iormesh-routes", "--token="+token)
	ro.kubectl(t, "config", "set-context", "kind-ro", "--cluster="+c.clusterName(t), "--user=iormesh-routes")
	if out, _ := ro.try("auth", "can-i", "list", "secrets", "--context=kind-ro"); strings.TrimSpace(out) != "no" {
		t.Fatalf("the list-only account can list secrets: %q", out)
	}
	reg := filepath.Join(c.dir, "servers-ro.yaml")
	text := "k8s:\n  clusters:\n    - name: ro\n      context: kind-ro\n      namespace: " + c.ns + "\n"
	if err := os.WriteFile(reg, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(c.dir, "ro", "services.db")
	if err := os.MkdirAll(filepath.Dir(db), 0o700); err != nil {
		t.Fatal(err)
	}
	out := ro.ctl(t, nil, "routes", "discover", "-registry", reg, "-cluster", "ro", "-db", db)
	if !strings.Contains(out, "discovered 2 routes, 3 services in kind-ro ("+c.ns+")") {
		t.Errorf("list-only discover: %s", out)
	}
}

// clusterName is the kubeconfig cluster of the test's context.
func (c *cluster) clusterName(t *testing.T) string {
	return strings.TrimSpace(c.kubectl(t, "config", "view", "--minify",
		"--output=jsonpath={.contexts[0].context.cluster}"))
}

// checkSigterm deletes app's pod and reads, from containerd on the kind
// node, how its debug tracer ended: on the kubelet's SIGTERM, exit 0,
// within the pod's grace period (5 s), not killed after it.
func (c *cluster) checkSigterm(t *testing.T, app string) {
	if c.cfg.node == "" {
		t.Skipf("%s unset: no node to read the container's exit from", envNode)
	}
	p := c.pod(t, app)
	var id string
	for _, s := range p.Status.Ephemeral {
		if s.Name == "iormesh" {
			id = strings.TrimPrefix(s.ContainerID, "containerd://")
		}
	}
	start := time.Now()
	c.kubectl(t, "delete", "pod", p.Metadata.Name, "--wait=false")
	for time.Since(start) < 30*time.Second {
		st := c.crictlState(t, id)
		if st.State == "CONTAINER_EXITED" {
			if st.ExitCode != 0 || st.Finished.Sub(start) > 5*time.Second {
				t.Errorf("tracer ended %+v, %s after the delete", st, st.Finished.Sub(start))
			}
			t.Logf("tracer exited %d, %s after the delete", st.ExitCode, st.Finished.Sub(start).Round(time.Millisecond))
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Errorf("tracer container %s did not exit within 30 s", id)
}

// crictlStatus is the part of `crictl inspect` the SIGTERM check reads.
type crictlStatus struct {
	State    string    `json:"state"`
	ExitCode int       `json:"exitCode"`
	Finished time.Time `json:"finishedAt"`
}

// crictlState inspects a container on the kind node (docker exec through
// sudo -n, as every docker call of the acceptance run).
func (c *cluster) crictlState(t *testing.T, id string) crictlStatus {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sudo", "-n", "docker", "exec", c.cfg.node,
		"crictl", "inspect", "-o", "json", id).Output()
	var v struct {
		Status crictlStatus `json:"status"`
	}
	if err == nil {
		err = json.Unmarshal(out, &v)
	}
	if err != nil {
		t.Logf("crictl inspect %s: %v", id, err)
	}
	return v.Status
}
