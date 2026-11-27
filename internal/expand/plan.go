package expand

import (
	"cmp"
	"fmt"
	"maps"
	"net/netip"
	"slices"

	"iormesh/internal/federate"
)

// Why a peer is not a candidate (Skip.Reason). Each one names a fact the
// traces or the registry state (see the package comment).
const (
	ReasonUnjoined        = "unjoined"         // merge said ambiguous, no_key, no_role, key_conflict, pending
	ReasonInbound         = "inbound"          // the peer connected in: an ephemeral port
	ReasonViaUnregistered = "via_unregistered" // revealed by a host no longer registered
	ReasonLocal           = "local"            // loopback, link-local, multicast, none
	ReasonEnrolled        = "enrolled"         // registered, or owned by a traced host
	ReasonNAT             = "nat"              // translates to a traced server
	ReasonIPv6            = "ipv6"             // the registry takes no IPv6 literal
	ReasonNotAllowed      = "not_allowed"      // no allowlist rule matches
	ReasonHopLimit        = "hop_limit"        // further than --hops from the start
	ReasonNodeCap         = "node_cap"         // the mesh is full
	ReasonTransport       = "transport"        // the controller's own SSH/rsync traffic
	ReasonAnotherPort     = "another_port"     // expand -apply enrolls one port per host
)

// Input is what a plan reads: the merged build, which registry host each
// trace came from, and the registry.
type Input struct {
	Merge federate.Result
	// HostDirs maps a trace's host_id to its store directory — the
	// registry host collect fetched it from (federate.Segments.Hosts,
	// inverted).
	HostDirs map[string]string
	Registry federate.Registry
	// Projects, when set, are the projects whose connections may propose
	// peers (iormeshctl expand -project). Every project's halves still
	// feed the visited set: a host traced under another project is in
	// the mesh all the same.
	Projects []string
}

// Via is one traced app that talked to a peer, and how often.
type Via struct {
	Host  string `json:"host"` // registry host (or host_id when unknown)
	App   string `json:"app"`  // app node id, else comm; "" when unknown
	Conns int    `json:"conns"`
}

// Peer is an unresolved far end as the clients reached it: the address
// and port, which traced apps reached it and over how many connections.
type Peer struct {
	Addr  netip.Addr `json:"addr"`
	Port  uint16     `json:"port"`
	Conns int        `json:"conns"`
	Via   []Via      `json:"via"`
	// Other counts connections to the same address and port set aside
	// for a per-connection reason (unjoined, inbound, via_unregistered)
	// while the peer itself is proposed: listed once, not twice.
	Other int `json:"other_conns,omitempty"`
}

// Candidate is a peer to enroll: a tracer on Addr bound to whatever
// listens on Port (the tracer's -listen-port, docs/milestones.md M2).
type Candidate struct {
	Peer
	// Hop is one more than the smallest hop among the registered hosts
	// that reached it.
	Hop int `json:"hop"`
	// Rule is the allowlist rule that allows it ("cidr 10.0.0.0/8").
	Rule string `json:"rule"`
	// Projects are the projects whose connections revealed it, sorted:
	// expand -apply enrolls it under the first (Enroll).
	Projects []string `json:"projects"`
}

// Skip is a peer left alone, the reason and the evidence for it.
type Skip struct {
	Peer
	Reason   string `json:"reason"`
	Evidence string `json:"evidence"`
}

// Proposal is one round's plan.
type Proposal struct {
	Deploy  []Candidate `json:"deploy"`
	Skipped []Skip      `json:"skipped"`
	Limits  Limits      `json:"limits"`
	// Nodes is the registry's host count before the round.
	Nodes int `json:"nodes"`
}

// verdict is one unresolved half's peer and what to do about it.
type verdict struct {
	addr             netip.Addr
	port             uint16
	reason, evidence string // reason "" means eligible; evidence is then the rule
	hop              int
}

// groupKey groups halves with the same peer and the same verdict.
type groupKey struct {
	addr             netip.Addr
	port             uint16
	reason, evidence string
}

// group accumulates one groupKey's halves.
type group struct {
	conns    int
	hop      int
	via      map[Via]int // Conns zero in the key
	projects map[string]bool
}

// Plan decides which unresolved peers to enroll in one round, and says
// of every other peer why not. It never touches the network.
func Plan(in Input, allow Allowlist, lim Limits) Proposal {
	ev := gather(in)
	groups, ambiguous := ev.group(in, allow)
	eligible, skipped := split(groups, ambiguous)
	p := Proposal{Limits: lim, Nodes: len(in.Registry.Servers)}
	var capped []Skip
	p.Deploy, capped = admit(eligible, lim, p.Nodes)
	p.Skipped = append(skipped, capped...)
	slices.SortFunc(p.Deploy, func(a, b Candidate) int { return comparePeers(a.Peer, b.Peer) })
	slices.SortFunc(p.Skipped, func(a, b Skip) int {
		return cmp.Or(comparePeers(a.Peer, b.Peer), cmp.Compare(a.Reason, b.Reason),
			cmp.Compare(a.Evidence, b.Evidence))
	})
	return p
}

// group judges every unresolved half of the proposing projects and
// groups them by peer and verdict. It also returns the peers some
// connection to which the merge found ambiguous: more than one traced
// half shares that connection's key, so the far end is likely traced.
func (ev *evidence) group(in Input, allow Allowlist) (map[groupKey]*group, map[netip.AddrPort]bool) {
	groups := map[groupKey]*group{}
	ambiguous := map[netip.AddrPort]bool{}
	for _, u := range in.Merge.Unresolved {
		if len(in.Projects) > 0 && !slices.Contains(in.Projects, u.Half.Project) {
			continue // in the visited set (gather), but proposes nothing
		}
		v := ev.judge(allow, u)
		if u.Reason == federate.ReasonAmbiguous {
			ambiguous[netip.AddrPortFrom(v.addr, v.port)] = true
		}
		k := groupKey{v.addr, v.port, v.reason, v.evidence}
		g := groups[k]
		if g == nil {
			g = &group{hop: v.hop, via: map[Via]int{}, projects: map[string]bool{}}
			groups[k] = g
		}
		g.conns++
		g.hop = min(g.hop, v.hop)
		g.via[Via{Host: ev.host(u.Half.Host), App: ev.app(u.Half.Procs)}]++
		g.projects[u.Half.Project] = true
	}
	return groups, ambiguous
}

// split turns the groups into candidates and skips, so that a peer is
// never both. Only per-connection verdicts (unjoined, inbound,
// via_unregistered) can disagree with a candidate for the same peer —
// the rest depend on the address alone. An ambiguous connection outranks
// the candidate (its far end may be traced: the candidate becomes an
// unjoined skip); any other per-connection skip is folded into the
// candidate's Other count rather than listed beside it.
func split(groups map[groupKey]*group, ambiguous map[netip.AddrPort]bool) ([]Candidate, []Skip) {
	keys := slices.SortedFunc(maps.Keys(groups), compareKeys)
	cands := map[netip.AddrPort]*Candidate{}
	var skipped []Skip
	for _, k := range keys {
		g := groups[k]
		peer := Peer{Addr: k.addr, Port: k.port, Conns: g.conns, Via: vias(g.via)}
		switch ap := netip.AddrPortFrom(k.addr, k.port); {
		case k.reason == "" && ambiguous[ap]:
			skipped = append(skipped, Skip{Peer: peer, Reason: ReasonUnjoined,
				Evidence: "another connection to it was ambiguous (the far end may be traced)"})
		case k.reason == "":
			cands[ap] = &Candidate{Peer: peer, Hop: g.hop, Rule: k.evidence,
				Projects: slices.Sorted(maps.Keys(g.projects))}
		}
	}
	for _, k := range keys {
		g := groups[k]
		if k.reason == "" {
			continue
		}
		if c := cands[netip.AddrPortFrom(k.addr, k.port)]; c != nil && perConn(k.reason) {
			c.Other += g.conns
			continue
		}
		skipped = append(skipped, Skip{Peer: Peer{Addr: k.addr, Port: k.port, Conns: g.conns,
			Via: vias(g.via)}, Reason: k.reason, Evidence: k.evidence})
	}
	eligible := make([]Candidate, 0, len(cands))
	for _, c := range cands {
		eligible = append(eligible, *c)
	}
	return eligible, skipped
}

// perConn reports a reason that is about one connection, not about the
// peer's address.
func perConn(reason string) bool {
	return reason == ReasonUnjoined || reason == ReasonInbound || reason == ReasonViaUnregistered ||
		reason == ReasonTransport
}

// judge decides one unresolved half: the facts first, then the
// allowlist. The hop limit and node cap need every candidate, so admit
// applies them afterwards.
func (ev *evidence) judge(allow Allowlist, u federate.Unresolved) verdict {
	v := verdict{addr: u.Half.Peer.Addr().Unmap(), port: u.Half.Peer.Port()}
	via := ev.host(u.Half.Host)
	hop, registered := ev.hops[via]
	if v.reason, v.evidence = ev.fact(u, via, registered, v.addr); v.reason != "" {
		return v
	}
	if allow.broadcast(v.addr) {
		// No host answers at a broadcast address; Enroll refuses it too.
		v.reason, v.evidence = ReasonLocal, "a broadcast address"
		return v
	}
	rule := allow.Match(v.addr)
	if rule == "" {
		v.reason, v.evidence = ReasonNotAllowed, "no allow rule matches"
		return v
	}
	v.evidence, v.hop = rule, hop+1
	return v
}

// fact is the first fact that rules the half's peer out, or "": the order
// puts what the merge said before what the registry and traces say.
func (ev *evidence) fact(u federate.Unresolved, via string, registered bool,
	addr netip.Addr) (reason, evidence string) {
	switch {
	case u.Reason != federate.ReasonNoMatch:
		return ReasonUnjoined, "merge: " + u.Reason + " (the far end may be traced)"
	case u.Half.Role != federate.RoleClient:
		return ReasonInbound, "the peer connected in; its port is ephemeral"
	case u.Half.Peer.Port() == sshPort:
		return ReasonTransport, "port 22 is SSH, the controller's transport: never bound by a tracer"
	case ev.transportProc(u.Half.Procs) != "":
		return ReasonTransport, "revealed by " + ev.transportProc(u.Half.Procs) +
			", the controller's own SSH/rsync traffic"
	case !registered:
		return ReasonViaUnregistered, via + " is not in the registry"
	case !routable(addr):
		return ReasonLocal, "loopback, link-local, multicast or no address"
	}
	if h, ok := ev.literal[addr]; ok {
		return ReasonEnrolled, "registered as " + h
	}
	if h, ok := ev.owned[addr]; ok {
		return ReasonEnrolled, "local address of traced host " + h
	}
	if h, ok := ev.nat[addr]; ok {
		return ReasonNAT, "a fused connection to it reached traced host " + h + " at another address"
	}
	if addr.Is6() {
		return ReasonIPv6, "the registry takes no IPv6 literal; add an ssh_config alias by hand"
	}
	return "", ""
}

// admit applies the hop limit and the node cap. Candidates are taken
// nearest first, then by how many connections reached them, so a full
// mesh keeps the busiest of the closest peers; another port of a host
// already admitted costs no node.
func admit(cands []Candidate, lim Limits, nodes int) (deploy []Candidate, skipped []Skip) {
	slices.SortFunc(cands, func(a, b Candidate) int {
		return cmp.Or(cmp.Compare(a.Hop, b.Hop), -cmp.Compare(a.Conns, b.Conns), comparePeers(a.Peer, b.Peer))
	})
	added := map[netip.Addr]bool{}
	for _, c := range cands {
		switch {
		case c.Hop > lim.MaxHops:
			skipped = append(skipped, Skip{Peer: c.Peer, Reason: ReasonHopLimit,
				Evidence: fmt.Sprintf("hop %d, limit %d", c.Hop, lim.MaxHops)})
		case added[c.Addr]:
			deploy = append(deploy, c)
		case nodes+len(added) >= lim.MaxNodes:
			skipped = append(skipped, Skip{Peer: c.Peer, Reason: ReasonNodeCap,
				Evidence: fmt.Sprintf("mesh at %d of %d nodes", nodes+len(added), lim.MaxNodes)})
		default:
			added[c.Addr] = true
			deploy = append(deploy, c)
		}
	}
	return deploy, skipped
}

// vias turns a group's via counts into a sorted list.
func vias(m map[Via]int) []Via {
	out := make([]Via, 0, len(m))
	for v, n := range m {
		v.Conns = n
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b Via) int {
		return cmp.Or(cmp.Compare(a.Host, b.Host), cmp.Compare(a.App, b.App))
	})
	return out
}

func compareKeys(a, b groupKey) int {
	return cmp.Or(a.addr.Compare(b.addr), cmp.Compare(a.port, b.port),
		cmp.Compare(a.reason, b.reason), cmp.Compare(a.evidence, b.evidence))
}

func comparePeers(a, b Peer) int {
	return cmp.Or(a.Addr.Compare(b.Addr), cmp.Compare(a.Port, b.Port))
}
