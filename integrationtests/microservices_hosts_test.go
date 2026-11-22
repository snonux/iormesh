package integrationtests

import (
	"net/netip"
	"path/filepath"
	"testing"

	"iormesh/internal/federate"
	"iormesh/internal/meshcheck"
	"iormesh/internal/normalize"
)

// The microservices topology spread over the mesh rig's three network
// namespaces as three emulated hosts (task 1f4; PLAN.md M4 "The
// microservices scenario spreads one service per host, each as its own
// project"):
//
//	hosta 10.231.0.1  gateway, inventory :80     project edge
//	hostb 10.231.0.2  orders :443 (TLS)          project orders
//	hostc 172.31.0.2  billing :443 (TLS)         project billing
//
// hostc is the rig's container namespace, which hostb reaches directly
// over its bridge veth. Three hops cross hosts — gateway→orders (TLS),
// orders→inventory (plain), orders→billing (TLS) — and gateway→inventory
// stays on hosta, so the merge must fuse plain and TLS hops across hosts
// and keep the local one its tracer fused. Every server binds its own
// address and its certificate names it; the callers pin the certificates
// in the harness's one --dir, which the namespaces share like a
// deployment would have copied them.
const (
	spreadPlainPort = "80"
	spreadTLSPort   = "443"
)

// spreadHostNames are the hosts' names and projects, in Host order.
var spreadHostNames = [][2]string{{"hosta", "edge"}, {"hostb", "orders"}, {"hostc", "billing"}}

// spreadService is one service of the spread: its role, the index of its
// host and its address options.
type spreadService struct {
	role string
	host int
	args []string
}

// spreadServices are in start order: leaves first, the gateway last.
var spreadServices = []spreadService{
	{"billing", 2, []string{"--bind", rigContainerAddr, "--port", spreadTLSPort}},
	{"inventory", 0, []string{"--bind", rigClientAddr, "--port", spreadPlainPort}},
	{"orders", 1, []string{"--bind", rigServerAddr, "--port", spreadTLSPort,
		"--inventory-host", rigClientAddr, "--inventory-port", spreadPlainPort,
		"--billing-host", rigContainerAddr, "--billing-port", spreadTLSPort}},
	{"gateway", 0, []string{"--orders-host", rigServerAddr, "--orders-port", spreadTLSPort,
		"--inventory-host", rigClientAddr, "--inventory-port", spreadPlainPort}},
}

// spreadRun is one judged run of the spread: each host's trace, in
// spreadHostNames order. Its tracedRun carries the tracers' loss summed.
type spreadRun struct {
	tracedRun
	hosts []tracedRun
}

// sessions are the hosts' tracer runs, each rebuilt from its segments.
func (s spreadRun) sessions() []Run {
	runs := make([]Run, len(s.hosts))
	for i, h := range s.hosts {
		runs[i] = h.Run
	}
	return runs
}

// spreadHosts are the rig's namespaces as the spread's hosts, each with a
// spool of its own for the Java agent.
func spreadHosts(h *Harness, rig meshRig) []Host {
	netns := []string{rig.client, rig.server, rig.container}
	hosts := make([]Host, len(spreadHostNames))
	for i, n := range spreadHostNames {
		hosts[i] = Host{Name: n[0], Project: n[1], Netns: netns[i],
			Spool: filepath.Join(h.Dir, "spool-"+n[0])}
	}
	return hosts
}

// traceSpread starts every service on its host, releases them in order,
// each server only once the one before printed its listening line, and
// traces each host with its own tracer until a run loses nothing.
func traceSpread(t *testing.T, h *Harness, rig meshRig) spreadRun {
	t.Helper()
	return judge(t, func() (spreadRun, error) {
		hosts := spreadHosts(h, rig)
		var order []*Workload
		for i, s := range spreadServices {
			w, err := h.StartWorkloadOn(hosts[s.host], "microservices",
				append([]string{"--role", s.role}, s.args...)...)
			if err != nil {
				return spreadRun{}, err
			}
			if i < len(spreadServices)-1 {
				w.ReleasedWhen = "listening "
			}
			hosts[s.host].Workloads = append(hosts[s.host].Workloads, w)
			order = append(order, w)
		}
		runs, err := h.TraceHosts(order, hosts...)
		if err != nil {
			return spreadRun{}, err
		}
		return newSpreadRun(h, hosts, runs), nil
	})
}

// newSpreadRun pairs each host's run with its workloads' PIDs.
func newSpreadRun(h *Harness, hosts []Host, runs []Run) spreadRun {
	s := spreadRun{}
	var loss KernelLoss
	for i, r := range runs {
		var pids []int
		for _, w := range hosts[i].Workloads {
			pids = append(pids, w.PID)
		}
		s.hosts = append(s.hosts, tracedRun{Run: r, pids: pids})
		loss = sumLoss(loss, r.Loss)
	}
	s.tracedRun = tracedRun{Run: Run{Dir: h.Dir, Loss: loss}}
	return s
}

// spreadHops are the hops between hosts, at the address and port each
// server bound, which its client dialed (no NAT between the namespaces);
// the gateway→inventory hop stays on hosta, its tracer's to fuse.
var spreadHops = []meshcheck.Hop{
	{Server: netip.MustParseAddrPort(rigServerAddr + ":" + spreadTLSPort), Label: "gateway→orders (TLS)"},
	{Server: netip.MustParseAddrPort(rigClientAddr + ":" + spreadPlainPort), Label: "orders→inventory"},
	{Server: netip.MustParseAddrPort(rigContainerAddr + ":" + spreadTLSPort), Label: "orders→billing (TLS)"},
}

// assertSpreadFused: the merge fused exactly the three cross-host hops
// and left nothing unresolved (meshcheck.Fused, which the kind pod run
// shares). With one project, every hop is within it; without, each
// crosses two projects.
func assertSpreadFused(t *testing.T, res federate.Result, project string) {
	t.Helper()
	meshcheck.Fused(t, res, spreadHops, project)
}

// TestMicroservicesAcrossHosts: the four services on three emulated
// hosts, each host its own project and tracer, under the Java agent
// (PLAN.md M4 acceptance, the microservices spread; the multi-host run of
// "Testing strategy"). Each host's trace holds only its own processes;
// merged, the three cross-host hops fuse and the whole satisfies
// expect-spread.json — expect.json's service graph with every service on
// its host, every hop at its address and scheme.
func TestMicroservicesAcrossHosts(t *testing.T) {
	enableParallelIfRequested(t)
	h := newHarness(t)
	h.JavaAgent = absPath(t, agentJarDefault)
	rig := newMeshRig(t)
	s := traceSpread(t, h, rig)
	ids := map[string]string{}
	for i, r := range s.hosts {
		assertOnlyTracedPIDs(t, r.Trace.Events, r.pids)
		assertJavaConsistent(t, r.Trace)
		if prev, dup := ids[hostID(t, r)]; dup {
			t.Fatalf("%s and %s stamped one host id: the hosts are not emulated", prev,
				spreadHostNames[i][0])
		}
		ids[hostID(t, r)] = spreadHostNames[i][0]
	}
	requireSeqFieldsIn(t, s.hosts...)
	res, merged := mergeHosts(t, s.hosts...)
	mergeMethods(t, &merged, s.hosts)
	// The cross-service assertions the kind pod run makes too (task uh4):
	// the hops fused, the golden, the requests paired and timed, and the
	// golden's wrong variants caught.
	meshcheck.Check(t, meshcheck.Mesh{Result: res, Merged: merged, TmpPrefix: h.Tmp,
		Want: expectationFile(t, meshcheck.Scenario, "expect-spread.json"), Hops: spreadHops})
}

// mergeMethods gives the merged trace the hosts' Java method tables, each
// host's ids in a range of its own (meshcheck.MergeMethods, which the
// kind pod run shares).
func mergeMethods(t *testing.T, merged *normalize.Trace, runs []tracedRun) {
	t.Helper()
	tables := make([]meshcheck.HostMethods, len(runs))
	for i, r := range runs {
		tables[i] = meshcheck.HostMethods{Host: hostID(t, r), Methods: r.Trace.Methods}
	}
	meshcheck.MergeMethods(merged, tables)
}
