package integrationtests

import (
	"net/netip"
	"path/filepath"
	"slices"
	"testing"

	"iormesh/internal/federate"
	"iormesh/internal/normalize"
	"iormesh/internal/schema"
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

// spreadServerEnds are the server ends of the hops between hosts: the
// address and port each server bound, which its client dialed (no NAT
// between the namespaces).
var spreadServerEnds = map[netip.AddrPort]string{
	netip.MustParseAddrPort(rigServerAddr + ":" + spreadTLSPort):    "gateway→orders (TLS)",
	netip.MustParseAddrPort(rigClientAddr + ":" + spreadPlainPort):  "orders→inventory",
	netip.MustParseAddrPort(rigContainerAddr + ":" + spreadTLSPort): "orders→billing (TLS)",
}

// assertSpreadFused: the merge fused exactly the three cross-host hops,
// each across two projects with one 4-tuple seen mirrored, and left no
// end unresolved — the local gateway→inventory hop was its tracer's.
func assertSpreadFused(t *testing.T, res federate.Result) {
	t.Helper()
	for _, u := range res.Unresolved {
		t.Errorf("unresolved %s end %s on %s: %s", u.Half.Role, u.Half.ConnID, u.Half.Host, u.Reason)
	}
	seen := map[netip.AddrPort]bool{}
	for _, c := range res.Conns {
		server := unmap(c.Server.Local)
		hop, ok := spreadServerEnds[server]
		switch {
		case !ok:
			t.Errorf("fused connection %s to %v is no cross-host hop", c.ID, server)
		case !c.CrossProject():
			t.Errorf("%s fused within project %s", hop, c.Client.Project)
		case unmap(c.Client.Peer) != server || unmap(c.Client.Local) != unmap(c.Server.Peer):
			t.Errorf("%s: the ends saw different 4-tuples: client %v->%v, server %v<-%v", hop,
				c.Client.Local, c.Client.Peer, c.Server.Local, c.Server.Peer)
		}
		seen[server] = true
	}
	if len(res.Conns) != len(spreadServerEnds) || len(seen) != len(spreadServerEnds) {
		t.Errorf("merge fused %d connections to %v, want one per cross-host hop %v",
			len(res.Conns), seen, spreadServerEnds)
	}
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
	assertSpreadFused(t, res)
	got, err := normalize.Normalize(merged, normalize.Options{Bracket: "microservices",
		TmpPrefix: h.Tmp})
	if err != nil {
		t.Fatalf("normalize merged: %v", err)
	}
	want := expectationFile(t, "microservices", "expect-spread.json")
	assertGolden(t, got, want)
	assertRequestsPaired(t, got.Events)
	assertHTTPTimed(t, merged.Events, false)
	t.Run("wrong expectations are caught", func(t *testing.T) {
		assertSpreadCaught(t, got, want)
	})
}

// mergeMethods gives the merged trace the hosts' Java method tables.
// Method ids are per trace — the controller scopes them per session — so
// each host's ids move into a range of their own (the host's index + 1 in
// the top 16 bits), and every event's stack moves with its host's. The
// stacks are copied: the hosts' own traces stay as read.
func mergeMethods(t *testing.T, merged *normalize.Trace, runs []tracedRun) {
	t.Helper()
	merged.Methods = map[uint64]schema.Method{}
	offset := map[string]uint64{}
	for i, r := range runs {
		off := uint64(i+1) << 48
		offset[hostID(t, r)] = off
		for id, m := range r.Trace.Methods {
			merged.Methods[id+off] = m
		}
	}
	for i, e := range merged.Events {
		if e.Java == nil || len(e.Java.Stack) == 0 {
			continue
		}
		j := *e.Java
		j.Stack = make([]uint64, len(e.Java.Stack))
		for k, id := range e.Java.Stack {
			j.Stack[k] = id + offset[e.HostID]
		}
		merged.Events[i].Java = &j
	}
}

// assertSpreadCaught: the spread golden is not vacuous — a service on
// the wrong host, a hop at the wrong address and a cross-host TLS hop
// declared plain each fail it.
func assertSpreadCaught(t *testing.T, got normalize.Trace, want normalize.Expectation) {
	t.Helper()
	assertCaught(t, got, want, map[string]func(*normalize.Expectation){
		"orders on the gateway's host": func(x *normalize.Expectation) {
			// assertCaught copies the events only.
			x.Edges = slices.Clone(x.Edges)
			for i, e := range x.Edges {
				if e.Type == "runs_on" && e.From == "$p3" {
					x.Edges[i].To = "host:$h2"
				}
			}
		},
		"orders dialed inventory on loopback": func(x *normalize.Expectation) {
			httpOf(x, clientHTTPIndex(x, "/inventory/reserve")).Host = "127.0.0.1"
		},
		"the cross-host TLS hop declared plain": func(x *normalize.Expectation) {
			httpOf(x, clientHTTPIndex(x, "/orders")).Scheme = "http"
		},
	})
}

// clientHTTPIndex is the index of the declared client-side http event
// for path: the one that names the host it dialed.
func clientHTTPIndex(x *normalize.Expectation, path string) int {
	for i, e := range x.Events {
		if e.HTTP != nil && e.HTTP.Path == path && e.HTTP.Host != "" {
			return i
		}
	}
	panic("no declared client http event for " + path)
}
