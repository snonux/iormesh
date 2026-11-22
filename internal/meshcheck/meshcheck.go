// Package meshcheck holds the cross-service checks of the microservices
// scenario (PLAN.md M0 "Microservices end-to-end scenario") that every run
// of it with one service per host makes, wherever the hosts are: the
// three emulated hosts of the namespaces rig (integrationtests,
// TestMicroservicesAcrossHosts, root) and the four pods of the kind
// acceptance (integrationtests/kind, TestKindMicroservices, task uh4).
// Both runs merge their hosts' traces themselves and hand the result to
// Check, so the two can never drift apart: every declared hop fused at
// its server's address with one 4-tuple seen mirrored, nothing left
// unresolved, the run's golden (expect-spread.json, expect-pods.json) with
// its http joins, every request's two ends agreeing, and the golden's
// wrong variants caught.
//
// It is a plain package, not a _test file, because Go shares no test
// code across packages; it imports no BPF, so the kind tests, which run
// without root, can use it.
package meshcheck

import (
	"net/netip"
	"slices"
	"strings"

	"iormesh/internal/federate"
	"iormesh/internal/normalize"
	"iormesh/internal/schema"
)

// Scenario is the workload scenario, and the marker bracket, the checks
// are for.
const Scenario = "microservices"

// T is the part of *testing.T the checks report through.
type T interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// Hop is one hop between hosts the merge must fuse: the address and port
// the server bound, which its client dialed (no NAT between the hosts),
// and a label for the messages. Two hops may share a server end — in the
// pod run inventory is dialed by orders and by the gateway, each from a
// pod of its own.
type Hop struct {
	Server netip.AddrPort
	Label  string
}

// Mesh is one merged run of the scenario to check.
type Mesh struct {
	// Result is federate.Merge's over every host of the run.
	Result federate.Result
	// Merged is the hosts' events interleaved, conn ids renamed to the
	// merged graph's and the hosts' method tables merged (MergeMethods),
	// not yet normalized.
	Merged normalize.Trace
	// TmpPrefix is the services' --dir, the golden's $tmp.
	TmpPrefix string
	// Want is the run's golden, its addresses the run's.
	Want normalize.Expectation
	// Hops are the hops between hosts; a hop within one host is its
	// tracer's to fuse and is not listed.
	Hops []Hop
	// Project is the one project every fused end must belong to; ""
	// wants every hop to cross two projects (one per host).
	Project string
}

// Check makes the scenario's cross-service assertions on a merged run
// and returns the normalized trace: the hops fused (Fused), the golden
// held, every request paired across the wire (RequestsPaired) and timed
// (HTTPTimed), and each of Mutations' wrong goldens failing (Caught).
func Check(t T, m Mesh) normalize.Trace {
	t.Helper()
	Fused(t, m.Result, m.Hops, m.Project)
	got, err := normalize.Normalize(m.Merged, normalize.Options{Bracket: Scenario, TmpPrefix: m.TmpPrefix})
	if err != nil {
		t.Fatalf("normalize merged: %v", err)
	}
	for _, v := range normalize.Check(got, m.Want) {
		t.Errorf("golden %s: %v", m.Want.Scenario, v)
	}
	RequestsPaired(t, got.Events)
	HTTPTimed(t, m.Merged.Events, false)
	Caught(t, got, m.Want, Mutations())
	return got
}

// Fused: the merge fused exactly the hops, as many connections at each
// server end as hops declared there, each with one 4-tuple seen
// mirrored, and left no end unresolved — a hop within one host was its
// tracer's. With a project, every hop is within it (mesh expansion
// enrolls a peer under the project that revealed it, task tb4; the kind
// run traces every pod under one); without, each crosses two projects
// (one per host, as the rig's hosts were added by hand).
func Fused(t T, res federate.Result, hops []Hop, project string) {
	t.Helper()
	for _, u := range res.Unresolved {
		t.Errorf("unresolved %s end %s on %s: %s", u.Half.Role, u.Half.ConnID, u.Half.Host, u.Reason)
	}
	want := map[netip.AddrPort][]string{}
	for _, h := range hops {
		want[h.Server] = append(want[h.Server], h.Label)
	}
	seen := map[netip.AddrPort]int{}
	for _, c := range res.Conns {
		server := Unmap(c.Server.Local)
		labels, ok := want[server]
		hop := strings.Join(labels, " or ")
		switch {
		case !ok:
			t.Errorf("fused connection %s to %v is no declared hop", c.ID, server)
		case project == "" && !c.CrossProject():
			t.Errorf("%s fused within project %s", hop, c.Client.Project)
		case project != "" && (c.Client.Project != project || c.Server.Project != project):
			t.Errorf("%s fused across projects %s and %s, want %s", hop, c.Client.Project,
				c.Server.Project, project)
		case Unmap(c.Client.Peer) != server || Unmap(c.Client.Local) != Unmap(c.Server.Peer):
			t.Errorf("%s: the ends saw different 4-tuples: client %v->%v, server %v<-%v", hop,
				c.Client.Local, c.Client.Peer, c.Server.Local, c.Server.Peer)
		}
		seen[server]++
	}
	for end, labels := range want {
		if seen[end] != len(labels) {
			t.Errorf("%d fused connections at %v, want %d (%s)", seen[end], end, len(labels),
				strings.Join(labels, ", "))
		}
	}
	if len(res.Conns) != len(hops) {
		t.Errorf("merge fused %d connections, want one per hop (%d)", len(res.Conns), len(hops))
	}
}

// Unmap is ap with an IPv4-mapped address unmapped: the JVM's dual-stack
// sockets may report either form, the goldens declare IPv4.
func Unmap(ap netip.AddrPort) netip.AddrPort {
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
}

// HostMethods is one host's Java method table (methods.ndjson, or a
// collected session's methods table file) by method id.
type HostMethods struct {
	Host    string
	Methods map[uint64]schema.Method
}

// MergeMethods gives the merged trace the hosts' Java method tables.
// Method ids are per trace — the controller scopes them per session — so
// each host's ids move into a range of their own (the host's index in
// tables + 1 in the top 16 bits), and every event's stack moves with its
// host's. The stacks are copied: the hosts' own traces stay as read.
func MergeMethods(merged *normalize.Trace, tables []HostMethods) {
	merged.Methods = map[uint64]schema.Method{}
	offset := map[string]uint64{}
	for i, h := range tables {
		off := uint64(i+1) << 48
		offset[h.Host] = off
		for id, m := range h.Methods {
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

// Caught checks got against each mutated copy of want and fails for
// every mutation the golden check does not report: a check that passes
// a wrong golden would pass anything. The copies own their events and
// edges, so a mutation may change either in place.
func Caught(t T, got normalize.Trace, want normalize.Expectation,
	mutations map[string]func(*normalize.Expectation)) {
	t.Helper()
	for name, mutate := range mutations {
		wrong := want
		wrong.Events = slices.Clone(want.Events)
		wrong.Edges = slices.Clone(want.Edges)
		mutate(&wrong)
		if len(normalize.Check(got, wrong)) == 0 {
			t.Errorf("%s: the golden check passed an expectation that does not hold", name)
		}
	}
}

// Mutations are wrong variants of a one-service-per-host golden that
// must fail it: orders on another service's host, orders dialing
// inventory on loopback, and the gateway's TLS hop to orders declared
// plain.
func Mutations() map[string]func(*normalize.Expectation) {
	return map[string]func(*normalize.Expectation){
		"orders on another service's host": func(x *normalize.Expectation) {
			for i, e := range x.Edges {
				if e.Type == "runs_on" && e.From == "$p3" {
					x.Edges[i].To = "host:$h2"
				}
			}
		},
		"orders dialed inventory on loopback": func(x *normalize.Expectation) {
			HTTPOf(x, ClientHTTPIndex(x, "/inventory/reserve")).Host = "127.0.0.1"
		},
		"the cross-host TLS hop declared plain": func(x *normalize.Expectation) {
			HTTPOf(x, ClientHTTPIndex(x, "/orders")).Scheme = "http"
		},
	}
}
