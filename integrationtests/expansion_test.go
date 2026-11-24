package integrationtests

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"iormesh/internal/expand"
	"iormesh/internal/federate"
	"iormesh/internal/meshcheck"
	"iormesh/internal/normalize"
)

// Mesh expansion's acceptance on the rig (task tb4; docs/milestones.md M6
// "Acceptance"): the microservices spread of task 1f4 with only the
// gateway's host enrolled by hand, grown round by round by internal/
// expand's Plan and Enroll — the code `iormeshctl expand -apply` runs.
// What the rig cannot do is the SSH deploy itself: an enrolled host's
// tracer is started here as that host's `iormesh run -listen-port PORT`,
// the target spec Enroll writes into the registry entry (deploy's argv
// over a fake transport is internal/ctl's TestExpandApply). Each round
// re-runs the whole scenario, as traffic keeps flowing on real hosts, and
// is merged together with every earlier round, as the controller's store
// accumulates them.

// expansionAllow is the rig's two networks: every service host may be
// enrolled.
var expansionAllow = []string{"10.231.0.0/24", "172.31.0.0/24"}

// rigHostIndex maps a registry host to its emulated host (spreadHosts'
// order): the gateway's host by the name the operator added it under, the
// others by the address expansion enrolled them at.
var rigHostIndex = map[string]int{"hosta": 0, rigServerAddr: 1, rigContainerAddr: 2}

// expansionRig is the state the rounds share: the registry, every round's
// host runs (the store) and which registry host each host_id is.
type expansionRig struct {
	h     *Harness
	rig   meshRig
	reg   federate.Registry
	store []tracedRun
	dirs  map[string]string
	round int
}

// traceRound runs the scenario once with every registry host traced and
// adds the runs to the store; it returns this round's runs.
func (x *expansionRig) traceRound(t *testing.T) spreadRun {
	t.Helper()
	x.round++
	traced := map[int]federate.Server{}
	for _, s := range x.reg.Servers {
		i, ok := rigHostIndex[s.Host]
		if !ok {
			t.Fatalf("round %d: registry host %s is no host of the rig", x.round, s.Host)
		}
		traced[i] = s
	}
	run := judge(t, func() (spreadRun, error) { return x.attempt(traced) })
	for i, r := range run.hosts {
		x.dirs[hostID(t, r)] = x.reg.Servers[i].Host
	}
	x.store = append(x.store, run.hosts...)
	return run
}

// attempt starts every service on its host and traces the hosts in
// traced, in registry order: hosta by its workloads' PIDs, an enrolled
// host by its entry's listening port. The other hosts' services run
// untraced, as on hosts no tracer was deployed to yet.
func (x *expansionRig) attempt(traced map[int]federate.Server) (spreadRun, error) {
	hosts := spreadHosts(x.h, x.rig)
	for i := range hosts {
		// A fresh spool per round: an untraced JVM's rings are never
		// drained, and must not wait for a later round's tracer.
		hosts[i].Spool += fmt.Sprintf("-r%d", x.round)
	}
	var order []*Workload
	for i, s := range spreadServices {
		w, err := x.h.StartWorkloadOn(hosts[s.host], "microservices",
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
	var on []Host
	for _, s := range x.reg.Servers {
		host := hosts[rigHostIndex[s.Host]]
		host.Project, host.ListenPort = s.Projects[0].Name, s.Projects[0].Target.ListenPort
		on = append(on, host)
	}
	runs, err := x.h.TraceHosts(order, on...)
	if err != nil {
		return spreadRun{}, err
	}
	return newSpreadRun(x.h, on, runs), nil
}

// plan merges the whole store and plans a round at hops and allow.
func (x *expansionRig) plan(t *testing.T, hops int, allow ...string) (expand.Proposal, expand.Allowlist) {
	t.Helper()
	res, _ := mergeHosts(t, x.store...)
	a, err := expand.NewAllowlist(&federate.Expand{Allow: federate.Allow{CIDRs: allow}})
	if err != nil {
		t.Fatal(err)
	}
	in := expand.Input{Merge: res, HostDirs: x.dirs, Registry: x.reg}
	return expand.Plan(in, a, expand.NewLimits(nil, hops)), a
}

// enroll applies p to the registry and returns the hosts it enrolled.
func (x *expansionRig) enroll(t *testing.T, p expand.Proposal, a expand.Allowlist) []string {
	t.Helper()
	got, skipped, err := expand.Enroll(&x.reg, p, a)
	if err != nil || len(skipped) != 0 {
		t.Fatalf("enroll: %v, skipped %+v", err, skipped)
	}
	var hosts []string
	for _, n := range got {
		hosts = append(hosts, fmt.Sprintf("%s hop %d %s :%d", n.Server.Host, n.Server.Hop,
			n.Server.Projects[0].Name, n.Server.Projects[0].Target.ListenPort))
	}
	return hosts
}

// planLines is a proposal as "deploy|skip addr:port hop|reason" lines.
// Inbound skips are left out: they are a traced server's view of an
// untraced client (inventory's of orders in round 1), whose port is
// ephemeral and differs every run; Plan never proposes them.
func planLines(p expand.Proposal) []string {
	var s []string
	for _, c := range p.Deploy {
		s = append(s, fmt.Sprintf("deploy %v:%d hop %d", c.Addr, c.Port, c.Hop))
	}
	for _, k := range p.Skipped {
		if k.Reason == expand.ReasonInbound {
			continue
		}
		s = append(s, fmt.Sprintf("skip %v:%d %s", k.Addr, k.Port, k.Reason))
	}
	return s
}

func wantPlan(t *testing.T, what string, p expand.Proposal, want ...string) {
	t.Helper()
	if got := planLines(p); !slices.Equal(got, want) {
		t.Errorf("%s:\n got %q\nwant %q", what, got, want)
	}
}

// TestMeshExpansion: only the gateway's host is enrolled; round 1 finds
// orders' host (hop 1), round 2 — with orders' host traced by its
// listening port — reaches back to the gateway's host (the cycle
// orders→inventory: both ends traced, it fuses and proposes nothing) and
// on to billing's, which -hops 1 stops at and an allowlist without its
// network lists but never enrolls; at -hops 2 it is enrolled, and round 3
// finds nothing new — every peer the store holds is enrolled: expansion
// terminates. The last round's merge is the mesh `add`ing all three
// hosts by hand gives (TestMicroservicesAcrossHosts), within the one
// project expansion grew, and the gateway's first, one-sided connection
// to orders stays unresolved in the store, never dropped and never fused
// with a later one.
func TestMeshExpansion(t *testing.T) {
	enableParallelIfRequested(t)
	h := newHarness(t)
	h.JavaAgent = absPath(t, agentJarDefault)
	x := &expansionRig{h: h, rig: newMeshRig(t), dirs: map[string]string{}, reg: federate.Registry{
		Servers: []federate.Server{{Host: "hosta", Projects: []federate.Project{{Name: "edge",
			Target: federate.Target{Pattern: "*microservices*"}}}}}}}

	x.traceRound(t)
	p, a := x.plan(t, 2, expansionAllow...)
	wantPlan(t, "round 1", p, "deploy 10.231.0.2:443 hop 1")
	if got := x.enroll(t, p, a); !slices.Equal(got, []string{"10.231.0.2 hop 1 edge :443"}) {
		t.Fatalf("round 1 enrolled %q", got)
	}
	gatewayFirst := x.store[0]

	x.traceRound(t)
	assertOneSidedKept(t, x, gatewayFirst)
	p, _ = x.plan(t, 1, expansionAllow...)
	wantPlan(t, "round 2 at -hops 1", p, "skip 10.231.0.2:443 enrolled", "skip 172.31.0.2:443 hop_limit")
	p, a = x.plan(t, 2, "10.231.0.0/24")
	wantPlan(t, "round 2 without billing's network", p, "skip 10.231.0.2:443 enrolled",
		"skip 172.31.0.2:443 not_allowed")
	if got := x.enroll(t, p, a); len(got) != 0 {
		t.Fatalf("enrolled outside the allowlist: %q", got)
	}
	p, a = x.plan(t, 2, expansionAllow...)
	if got := x.enroll(t, p, a); !slices.Equal(got, []string{"172.31.0.2 hop 2 edge :443"}) {
		t.Fatalf("round 2 enrolled %q (plan %q)", got, planLines(p))
	}

	last := x.traceRound(t)
	assertOneSidedKept(t, x, gatewayFirst)
	// Every earlier round's one-sided peer is in the visited set now.
	p, _ = x.plan(t, 5, expansionAllow...)
	wantPlan(t, "round 3", p, "skip 10.231.0.2:443 enrolled", "skip 172.31.0.2:443 enrolled")
	assertMeshAsAdded(t, h, last)
}

// assertOneSidedKept: the gateway's connection to orders in round 1,
// traced on its side only, is still in the store's merge as an
// unresolved no_match half — kept, not dropped, and fused with nothing.
func assertOneSidedKept(t *testing.T, x *expansionRig, first tracedRun) {
	t.Helper()
	res, _ := mergeHosts(t, x.store...)
	orders := netip.MustParseAddrPort(rigServerAddr + ":" + spreadTLSPort)
	var conns []string
	for _, e := range first.Trace.Events {
		if e.ConnID != "" && e.Event == "connect" && e.Args.RemoteAddr != nil &&
			strings.TrimPrefix(*e.Args.RemoteAddr, "::ffff:") == rigServerAddr {
			conns = append(conns, e.ConnID)
		}
	}
	if len(conns) == 0 {
		t.Fatal("round 1: the gateway's trace holds no connect to orders")
	}
	for _, c := range res.Conns {
		if c.Client.Host == hostID(t, first) && slices.Contains(conns, c.Client.ConnID) {
			t.Errorf("round 1's one-sided connection %s was fused with %s", c.Client.ConnID, c.Server.ConnID)
		}
	}
	kept := 0
	for _, u := range res.Unresolved {
		if unmap(u.Half.Peer) == orders && slices.Contains(conns, u.Half.ConnID) {
			kept++
			if u.Reason != federate.ReasonNoMatch {
				t.Errorf("round 1's connection to orders is %s, want %s", u.Reason, federate.ReasonNoMatch)
			}
		}
	}
	if kept != len(conns) {
		t.Errorf("%d of round 1's %d connections to orders are unresolved in the store", kept, len(conns))
	}
}

// assertMeshAsAdded: the last round, all three hosts traced — two of them
// by expansion, bound by listening port — merges into the mesh that
// adding every host by hand gives: the same three cross-host hops fused
// end to end and nothing unresolved (assertSpreadFused, as
// TestMicroservicesAcrossHosts checks its merge), and the spread golden's
// mesh — every service on its host, every accept, connect and request at
// its address and scheme, every connection fused — holds. The golden's
// file and JVM-startup events are left out: a tracer deployed by
// expansion attaches to a server already listening, after them.
func assertMeshAsAdded(t *testing.T, h *Harness, last spreadRun) {
	t.Helper()
	res, merged := mergeHosts(t, last.hosts...)
	assertSpreadFused(t, res, "edge")
	got, err := normalize.Normalize(merged, normalize.Options{TmpPrefix: h.Tmp})
	if err != nil {
		t.Fatalf("normalize merged: %v", err)
	}
	want := meshOf(expectationFile(t, "microservices", "expect-spread.json"))
	assertGolden(t, got, want)
	t.Run("a wrong mesh is caught", func(t *testing.T) {
		assertCaught(t, got, want, map[string]func(*normalize.Expectation){
			"the TLS hop to orders declared plain": func(x *normalize.Expectation) {
				httpOf(x, meshcheck.ClientHTTPIndex(x, "/orders")).Scheme = "http"
			},
			"orders dialed inventory on loopback": func(x *normalize.Expectation) {
				httpOf(x, meshcheck.ClientHTTPIndex(x, "/inventory/reserve")).Host = "127.0.0.1"
			},
			"billing on the gateway's host": func(x *normalize.Expectation) {
				x.Edges = slices.Clone(x.Edges) // assertCaught copies the events only
				for i, e := range x.Edges {
					if e.Type == "runs_on" && e.From == "$p1" {
						x.Edges[i].To = "host:$h2"
					}
				}
			},
		})
	})
}

// meshOf is the network part of a golden: its accept, connect and http
// events without code attribution (the methods are the Java agent's
// business, M3, not the mesh's), and its runs_on and connected edges.
func meshOf(x normalize.Expectation) normalize.Expectation {
	m := normalize.Expectation{Scenario: x.Scenario + " (mesh)", SchemaVersion: x.SchemaVersion}
	for _, e := range x.Events {
		if e.Event == "accept" || e.Event == "connect" || e.Event == "http" {
			e.Java, e.Attribution = nil, ""
			m.Events = append(m.Events, e)
		}
	}
	for _, e := range x.Edges {
		if e.Type == "runs_on" || (e.Type == "connected" && strings.HasPrefix(e.To, "conn:")) {
			m.Edges = append(m.Edges, e)
		}
	}
	return m
}
