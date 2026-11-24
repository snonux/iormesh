package expand

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"

	"iormesh/internal/federate"
)

// ErrNotPlanned is a candidate Enroll refuses: one Plan could not have
// proposed against the same allowlist and limits. It means the proposal
// was built or edited outside Plan, and nothing is enrolled.
var ErrNotPlanned = errors.New("expand: candidate outside the plan's allowlist or limits")

// Enrollment is one host expand -apply adds to the registry: the new
// entry and the candidate it came from.
type Enrollment struct {
	Server    federate.Server
	Candidate Candidate
}

// Enroll adds p's candidates to reg (docs/milestones.md M6 "Apply"), in
// memory: the caller saves the registry before any host is contacted, so the
// registry stays the one list deploy reaches (principle #3).
//
// Each candidate address becomes one registry host, written as the
// address the clients reached (the visited set then knows it by that
// literal next round), with one project: the first project whose
// connections revealed it, tracing whatever listens on the peer's port
// (target spec: listening port, M2) — the server side of the edge, never
// a client's ephemeral port. It carries no pass-through flags: the
// confined defaults apply until the operator says otherwise. The entry
// records the candidate's hop, so the next round's limit counts from it.
//
// A registry project has one listen port, and a host one entry, so of
// several ports of one address the busiest (then the lowest) is enrolled
// and the others are returned as another_port skips: the next round sees
// the address as enrolled, so the operator adds those ports by hand.
//
// Every candidate is checked again against allow and p.Limits (hops,
// and max_nodes over reg's hosts), routability and broadcast before
// anything changes: Enroll must never widen what Plan allowed, whoever
// built the proposal. A violation is ErrNotPlanned and reg is untouched.
func Enroll(reg *federate.Registry, p Proposal, allow Allowlist) ([]Enrollment, []Skip, error) {
	for _, c := range p.Deploy {
		if err := checkPlanned(c, p.Limits, allow); err != nil {
			return nil, nil, err
		}
	}
	picked, skipped := onePortPerHost(p.Deploy)
	if n := len(reg.Servers) + len(picked); n > p.Limits.MaxNodes {
		return nil, nil, fmt.Errorf("%w: %d hosts would pass max_nodes %d", ErrNotPlanned, n,
			p.Limits.MaxNodes)
	}
	next := *reg
	next.Servers = slices.Clone(reg.Servers)
	out := make([]Enrollment, 0, len(picked))
	for _, c := range picked {
		host := c.Addr.String()
		proj := federate.Project{Name: c.Projects[0], Target: federate.Target{ListenPort: c.Port}}
		if err := next.AddHop(host, c.Hop, proj); err != nil {
			return nil, nil, err
		}
		srv, _ := next.Lookup(host)
		out = append(out, Enrollment{Server: srv, Candidate: c})
	}
	*reg = next
	return out, skipped, nil
}

// checkPlanned is Plan's own verdict on c, re-derived: a routable IPv4
// unicast address and a port, allowed, within the hop limit, revealed by
// a project. Broadcast is refused too — the limited one and the
// broadcast address of the allow rule's network — which Plan does not
// see as a fact of its own: no host answers there, and a deploy to it
// would reach whichever does.
func checkPlanned(c Candidate, lim Limits, allow Allowlist) error {
	switch {
	case !c.Addr.Is4() || c.Port == 0 || !routable(c.Addr):
		return fmt.Errorf("%w: %s is no routable IPv4 address and port", ErrNotPlanned, addrPort(c.Peer))
	case allow.broadcast(c.Addr):
		return fmt.Errorf("%w: %s is a broadcast address", ErrNotPlanned, c.Addr)
	case allow.Match(c.Addr) == "":
		return fmt.Errorf("%w: %s matches no allow rule", ErrNotPlanned, c.Addr)
	case c.Hop < 1 || c.Hop > lim.MaxHops:
		return fmt.Errorf("%w: %s at hop %d, limit %d", ErrNotPlanned, c.Addr, c.Hop, lim.MaxHops)
	case len(c.Projects) == 0:
		return fmt.Errorf("%w: %s revealed by no project", ErrNotPlanned, c.Addr)
	}
	return nil
}

// onePortPerHost keeps, per address, the candidate with the most
// connections (then the lowest port) and turns the rest into skips. The
// result is sorted by address.
func onePortPerHost(cands []Candidate) ([]Candidate, []Skip) {
	best := map[netip.Addr]Candidate{}
	for _, c := range cands {
		b, ok := best[c.Addr]
		if !ok || c.Conns > b.Conns || (c.Conns == b.Conns && c.Port < b.Port) {
			best[c.Addr] = c
		}
	}
	var picked []Candidate
	var skipped []Skip
	for _, c := range cands {
		b := best[c.Addr]
		if c.Port == b.Port {
			picked = append(picked, c)
			continue
		}
		skipped = append(skipped, Skip{Peer: c.Peer, Reason: ReasonAnotherPort,
			Evidence: fmt.Sprintf("the host is enrolled on port %d this round; "+
				"add this port by hand (iormeshctl add -listen-port %d)", b.Port, c.Port)})
	}
	slices.SortFunc(picked, func(a, b Candidate) int { return comparePeers(a.Peer, b.Peer) })
	slices.SortFunc(skipped, func(a, b Skip) int { return comparePeers(a.Peer, b.Peer) })
	return picked, skipped
}
