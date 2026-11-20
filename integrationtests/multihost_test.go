package integrationtests

import (
	"cmp"
	"context"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"iormesh/internal/federate"
	"iormesh/internal/normalize"
	"iormesh/internal/schema"
)

// The mesh rig's network (task kb4), the addresses its goldens declare:
//
//	client host  10.231.0.1 --veth-- 10.231.0.2  server host
//	                                 172.31.0.1 --veth-- 172.31.0.2  container
//
// The server host is also a docker host: it publishes the container's
// port 80 as its own port 880 the way `docker run -p 880:80` does — a
// DNAT rule in its nat table's prerouting chain — so a client that dials
// 10.231.0.2:880 reaches 172.31.0.2:80, and the two ends see different
// 4-tuples. The client's source address survives (docker DNATs inbound
// traffic, it does not SNAT it).
const (
	rigClientAddr    = "10.231.0.1"
	rigServerAddr    = "10.231.0.2"
	rigBridgeAddr    = "172.31.0.1"
	rigContainerAddr = "172.31.0.2"
	rigPort          = "80"
	rigPublishedPort = "880"
)

// rigSeq numbers the rigs of one test binary, so tests running in
// parallel never share a namespace.
var rigSeq atomic.Int32

// meshRig names the three network namespaces of one rig.
type meshRig struct{ client, server, container string }

// meshPair is one run of a cross-host pair scenario: where the server
// lives and binds, and what the client dials.
type meshPair struct {
	scenario, serverNetns, bind, dial, dialPort string
}

// meshRun is one judged multi-host run: each host's trace. Its tracedRun
// carries both tracers' loss summed, so judge retries when either lost
// records, and the harness directory, so a failure keeps both traces.
type meshRun struct {
	tracedRun
	client, server tracedRun
}

// newMeshRig builds the rig, skipping on a machine without the tools; the
// namespaces are deleted when the test ends. Every interface is created
// inside its namespace, so the machine's own network is never touched.
func newMeshRig(t *testing.T) meshRig {
	t.Helper()
	for _, tool := range []string{"ip", "nft", "nsenter", "unshare"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("the mesh rig needs %s: %v", tool, err)
		}
	}
	sweepStaleRigs(t)
	n := fmt.Sprintf("iorm%d-%d", os.Getpid(), rigSeq.Add(1))
	r := meshRig{client: n + "-a", server: n + "-b", container: n + "-c"}
	t.Cleanup(func() {
		for _, ns := range []string{r.client, r.server, r.container} {
			_ = exec.Command("ip", "netns", "del", ns).Run()
		}
	})
	for _, ns := range []string{r.client, r.server, r.container} {
		rigCmd(t, "ip", "netns", "add", ns)
		rigCmd(t, "ip", "-n", ns, "link", "set", "lo", "up")
	}
	r.link(t, r.client, "eth0", rigClientAddr, r.server, "eth0", rigServerAddr)
	r.link(t, r.server, "veth0", rigBridgeAddr, r.container, "eth0", rigContainerAddr)
	rigCmd(t, "ip", "-n", r.container, "route", "add", "default", "via", rigBridgeAddr)
	rigCmd(t, "ip", "netns", "exec", r.server, "sysctl", "-qw", "net.ipv4.ip_forward=1")
	r.publish(t)
	return r
}

// staleRig matches a rig namespace's name and captures the PID of the
// test binary that made it.
var staleRig = regexp.MustCompile(`^iorm([0-9]+)-[0-9]+-[abc]$`)

// sweepStaleRigs deletes the rig namespaces of test binaries no longer
// alive. t.Cleanup never runs when the binary is killed (Ctrl-C, SIGKILL,
// a -timeout panic), and a namespace pinned in /run/netns outlives its
// processes; deleting it removes its veths and nft table with it. A
// namespace whose PID is alive again (reused) waits for a later sweep.
func sweepStaleRigs(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(netnsDir)
	if err != nil {
		return // no /run/netns yet: nothing pinned, nothing stale
	}
	for _, e := range entries {
		m := staleRig.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		pid, err := strconv.Atoi(m[1])
		if err != nil || syscall.Kill(pid, 0) != syscall.ESRCH {
			continue
		}
		if out, err := exec.Command("ip", "netns", "del", e.Name()).CombinedOutput(); err != nil {
			t.Logf("stale rig namespace %s: %v: %s", e.Name(), err, out)
		}
	}
}

// link joins two namespaces with a veth pair, ifA in nsA and ifB in nsB,
// one /24 address per end. Interface names are per namespace, so every
// rig can use the same ones.
func (r meshRig) link(t *testing.T, nsA, ifA, addrA, nsB, ifB, addrB string) {
	t.Helper()
	rigCmd(t, "ip", "link", "add", ifA, "netns", nsA, "type", "veth",
		"peer", "name", ifB, "netns", nsB)
	for _, end := range [][3]string{{nsA, ifA, addrA}, {nsB, ifB, addrB}} {
		rigCmd(t, "ip", "-n", end[0], "addr", "add", end[2]+"/24", "dev", end[1])
		rigCmd(t, "ip", "-n", end[0], "link", "set", end[1], "up")
	}
}

// publish installs the server host's port mapping: docker's -p 880:80 as
// a DNAT rule.
func (r meshRig) publish(t *testing.T) {
	t.Helper()
	ruleset := fmt.Sprintf(`table ip kb4 {
  chain pre {
    type nat hook prerouting priority dstnat;
    ip daddr %s tcp dport %s dnat to %s:%s
  }
}
`, rigServerAddr, rigPublishedPort, rigContainerAddr, rigPort)
	cmd := exec.Command("ip", "netns", "exec", r.server, "nft", "-f", "-")
	cmd.Stdin = strings.NewReader(ruleset)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("nft: %v: %s", err, out)
	}
}

// rigCmd runs one rig command, failing the test with its output.
func rigCmd(t *testing.T, name string, args ...string) {
	t.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v: %s", name, strings.Join(args, " "), err, out)
	}
}

// traceMesh runs pair's server and client on their hosts, each host
// traced by its own tracer as its own project, until a run loses nothing.
func traceMesh(t *testing.T, h *Harness, rig meshRig, pair meshPair) meshRun {
	t.Helper()
	return judge(t, func() (meshRun, error) {
		server, err := h.StartWorkloadIn(pair.serverNetns, pair.scenario, "--role", "server",
			"--bind", pair.bind, "--port", rigPort)
		if err != nil {
			return meshRun{}, err
		}
		server.ReleasedWhen = "listening "
		client, err := h.StartWorkloadIn(rig.client, pair.scenario, "--role", "client",
			"--host", pair.dial, "--port", pair.dialPort)
		if err != nil {
			return meshRun{}, err
		}
		runs, err := h.TraceHosts([]*Workload{server, client},
			Host{Name: "hosta", Project: "shop", Netns: rig.client, Workloads: []*Workload{client}},
			Host{Name: "hostb", Project: "billing", Netns: rig.server,
				Workloads: []*Workload{server}})
		if err != nil {
			return meshRun{}, err
		}
		m := meshRun{client: tracedRun{Run: runs[0], pids: []int{client.PID}},
			server: tracedRun{Run: runs[1], pids: []int{server.PID}}}
		m.tracedRun = tracedRun{Run: Run{Dir: h.Dir, Loss: sumLoss(runs[0].Loss, runs[1].Loss)}}
		return m, nil
	})
}

// sumLoss adds two tracers' loss figures: a run is loss-free only when
// both of its tracers are.
func sumLoss(a, b KernelLoss) KernelLoss {
	return KernelLoss{Lost: a.Lost + b.Lost, Kernel: a.Kernel + b.Kernel,
		Skipped: a.Skipped + b.Skipped, Unpaired: a.Unpaired + b.Unpaired, NoPath: a.NoPath + b.NoPath}
}

// assertHostGoldens checks each host's own trace against its golden
// (expect-client.json, expect-server.json): what one host's tracer
// records, its end of the connection unresolved — no host alone sees the
// far end — and only its own process. A wrong 4-tuple in either golden
// is caught: the client's must be what it dialed (the published address
// behind NAT), the server's what it accepted on.
func assertHostGoldens(t *testing.T, h *Harness, m meshRun, pair meshPair) {
	t.Helper()
	for role, run := range map[string]tracedRun{"client": m.client, "server": m.server} {
		assertOnlyTracedPIDs(t, run.Trace.Events, run.pids)
		got := normalized(t, h, run, pair.scenario)
		want := expectationFile(t, pair.scenario, "expect-"+role+".json")
		assertGolden(t, got, want)
		assertCaught(t, got, want, map[string]func(*normalize.Expectation){
			"the " + role + " saw the other end's address": func(x *normalize.Expectation) {
				swapRigAddr(x, role)
			},
		})
	}
	if a, b := hostID(t, m.client), hostID(t, m.server); a == b {
		t.Fatalf("both tracers stamped host id %s: the hosts are not emulated", a)
	}
}

// swapRigAddr rewrites the declared address of role's own socket end on
// its connect or accept: the client's remote address becomes the
// container's (as if no DNAT hop had been there, or for tcp-two-host an
// address nobody used), the server's local address becomes the client's.
func swapRigAddr(x *normalize.Expectation, role string) {
	for i, e := range x.Events {
		switch {
		case role == "client" && e.Event == schema.KindConnect:
			x.Events[i].Args.RemoteAddr = ptrTo(rigContainerAddr)
		case role == "server" && e.Event == schema.KindAccept:
			x.Events[i].Args.LocalAddr = ptrTo(rigClientAddr)
		}
	}
}

// ptrTo returns a pointer to a copy of v.
func ptrTo[T any](v T) *T { return &v }

// hostID is the host id of a run's events (all share it).
func hostID(t *testing.T, run tracedRun) string {
	t.Helper()
	if len(run.Trace.Events) == 0 {
		t.Fatalf("%s: no events", run.Dir)
	}
	return run.Trace.Events[0].HostID
}

// requireSeqFields fails the merge's half of a test when no tcp_state
// record carries the four tcp_sock fields (task ve4): without them
// neither end has a key and the merge rightly fuses nothing, so the
// golden failures that followed would hide the cause.
func requireSeqFields(t *testing.T, m meshRun) {
	t.Helper()
	for _, e := range slices.Concat(m.client.Trace.Events, m.server.Trace.Events) {
		if e.Event == schema.KindTCPState && e.Args.SndUna != nil {
			return
		}
	}
	t.Fatal("no tcp_state record carries snd_una: the tracer's tcp_sock capture " +
		"(task ve4) is missing, so the merge cannot key either end")
}

// mergeMesh merges the two host traces as `iormeshctl build` does and
// returns the result with the merged trace: both hosts' events in
// wall-clock order (the emulated hosts share one clock), their conn ids
// renamed to the merged graph's, and the merged graph.
func mergeMesh(t *testing.T, m meshRun) (federate.Result, normalize.Trace) {
	t.Helper()
	res, err := federate.Merge(context.Background(), []federate.Source{
		federate.NDJSONDir{Dir: m.client.Dir}, federate.NDJSONDir{Dir: m.server.Dir}},
		federate.Options{})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	events := slices.Concat(m.client.Trace.Events, m.server.Trace.Events)
	for i, e := range events {
		if to, ok := res.Renames[federate.LocalConn{Host: e.HostID, ConnID: e.ConnID}]; ok {
			events[i].ConnID = to
		}
	}
	slices.SortStableFunc(events, func(a, b schema.Event) int { return cmp.Compare(a.WallNS, b.WallNS) })
	return res, normalize.Trace{Events: events, Graph: res.Graph}
}

// assertOneFusedConn: the merge fused exactly one connection, across the
// two projects, and left no TCP end unresolved. It returns that
// connection.
func assertOneFusedConn(t *testing.T, res federate.Result) federate.Conn {
	t.Helper()
	for _, u := range res.Unresolved {
		t.Errorf("unresolved %s end %s on %s: %s", u.Half.Role, u.Half.ConnID, u.Half.Host, u.Reason)
	}
	if len(res.Conns) != 1 {
		t.Fatalf("merge fused %d connections, want 1", len(res.Conns))
	}
	c := res.Conns[0]
	if !c.CrossProject() {
		t.Errorf("fused connection %s is not cross-project (%s, %s)", c.ID, c.Client.Project,
			c.Server.Project)
	}
	return c
}

// assertMergedGolden checks the merged trace against the scenario's
// expect.json — both processes on two hosts, one fused connection — and
// that the check catches what it must.
func assertMergedGolden(t *testing.T, h *Harness, merged normalize.Trace, scenario string) {
	t.Helper()
	got, err := normalize.Normalize(merged, normalize.Options{Bracket: scenario, TmpPrefix: h.Tmp})
	if err != nil {
		t.Fatalf("normalize merged: %v", err)
	}
	want := expectation(t, scenario)
	assertGolden(t, got, want)
	assertCaught(t, got, want, map[string]func(*normalize.Expectation){
		"both ends on one host": func(x *normalize.Expectation) {
			x.Edges = replaceEdgeTo(x.Edges, "host:$h2", "host:$h1")
		},
		"the ends on two connections": func(x *normalize.Expectation) {
			x.Edges = replaceEdgeTo(x.Edges, "conn:$c1", "conn:$c2")
		},
	})
}

// replaceEdgeTo is edges, copied, with every edge into from (the last
// such edge, for a whole-node rename) pointing to to instead.
func replaceEdgeTo(edges []normalize.ExpectedEdge, from, to string) []normalize.ExpectedEdge {
	out := slices.Clone(edges)
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].To == from {
			out[i].To = to
			break
		}
	}
	return out
}

// assertNoMatchAlone: the client's trace merged alone fuses nothing —
// its end stays unresolved as no_match, never a guess.
func assertNoMatchAlone(t *testing.T, m meshRun) {
	t.Helper()
	res, err := federate.Merge(context.Background(),
		[]federate.Source{federate.NDJSONDir{Dir: m.client.Dir}}, federate.Options{})
	if err != nil {
		t.Fatalf("merge client alone: %v", err)
	}
	if len(res.Conns) != 0 || len(res.Unresolved) != 1 ||
		res.Unresolved[0].Reason != federate.ReasonNoMatch {
		t.Errorf("client merged alone: conns %v, unresolved %v; want one no_match end",
			res.Conns, res.Unresolved)
	}
}

// unmap is ap with an IPv4-mapped address unmapped: the JVM's dual-stack
// sockets may report either form, the rig's goldens declare IPv4.
func unmap(ap netip.AddrPort) netip.AddrPort {
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
}

// TestTwoHostsTwoProjects: M0's client and server on two hosts, traced
// as two projects (PLAN.md M4 acceptance) — each host's trace satisfies
// its own golden, and merging both fuses the connection into one
// cross-project edge whose ends saw one 4-tuple, mirrored.
func TestTwoHostsTwoProjects(t *testing.T) {
	enableParallelIfRequested(t)
	h := newHarness(t)
	rig := newMeshRig(t)
	pair := meshPair{scenario: "tcp-two-host", serverNetns: rig.server,
		bind: rigServerAddr, dial: rigServerAddr, dialPort: rigPort}
	m := traceMesh(t, h, rig, pair)
	assertHostGoldens(t, h, m, pair)
	t.Run("merged", func(t *testing.T) {
		requireSeqFields(t, m)
		res, merged := mergeMesh(t, m)
		c := assertOneFusedConn(t, res)
		if unmap(c.Client.Peer) != unmap(c.Server.Local) ||
			unmap(c.Client.Local) != unmap(c.Server.Peer) {
			t.Errorf("no NAT, yet the ends saw different 4-tuples: client %v->%v, server %v<-%v",
				c.Client.Local, c.Client.Peer, c.Server.Local, c.Server.Peer)
		}
		assertMergedGolden(t, h, merged, "tcp-two-host")
		assertNoMatchAlone(t, m)
	})
}

// TestNATPair: the NAT'ed pair (PLAN.md M4 acceptance) — the server in a
// container behind docker-style port publishing, so the client dials
// 10.231.0.2:880 and the server accepts on 172.31.0.2:80. Each host's
// trace shows its own 4-tuple; the merge fuses the two into one edge by
// the ISN pair, which the DNAT hop leaves untouched.
func TestNATPair(t *testing.T) {
	enableParallelIfRequested(t)
	h := newHarness(t)
	rig := newMeshRig(t)
	pair := meshPair{scenario: "tcp-nat", serverNetns: rig.container,
		bind: rigContainerAddr, dial: rigServerAddr, dialPort: rigPublishedPort}
	m := traceMesh(t, h, rig, pair)
	assertHostGoldens(t, h, m, pair)
	t.Run("merged", func(t *testing.T) {
		requireSeqFields(t, m)
		res, merged := mergeMesh(t, m)
		c := assertOneFusedConn(t, res)
		dialed := netip.MustParseAddrPort(rigServerAddr + ":" + rigPublishedPort)
		served := netip.MustParseAddrPort(rigContainerAddr + ":" + rigPort)
		if unmap(c.Client.Peer) != dialed || unmap(c.Server.Local) != served {
			t.Errorf("fused ends saw %v (client's peer) and %v (server's local), want %v and %v",
				c.Client.Peer, c.Server.Local, dialed, served)
		}
		assertMergedGolden(t, h, merged, "tcp-nat")
		assertNoMatchAlone(t, m)
	})
}
