package expand

import (
	"net/netip"
	"path"

	"iormesh/internal/federate"
)

// Defaults for a registry whose expand: section leaves them out.
const (
	// DefaultMaxHops is one ring: the peers the operator's own hosts
	// talk to, and no further, unless --hops says otherwise.
	DefaultMaxHops = 1
	// DefaultMaxNodes caps the mesh, registered hosts included. Small on
	// purpose: every node is a root-run tracer on someone's server, so
	// growing past it is a decision the operator writes down.
	DefaultMaxNodes = 16
)

// Allowlist is the parsed allow: of the registry's expand: section.
// The zero value allows nothing.
type Allowlist struct {
	cidrs []netip.Prefix
	hosts []string
}

// Limits bound one round: how far from the originally enrolled hosts a
// candidate may be, and how many hosts the mesh may hold in all.
type Limits struct {
	MaxHops  int `json:"max_hops"`
	MaxNodes int `json:"max_nodes"`
}

// NewAllowlist parses the registry's allowlist; a nil section allows
// nothing. The registry validated it on load, so an error here means a
// Registry built in code skipped Validate.
func NewAllowlist(x *federate.Expand) (Allowlist, error) {
	var a Allowlist
	if x == nil {
		return a, nil
	}
	for _, c := range x.Allow.CIDRs {
		p, err := federate.ParseAllowCIDR(c)
		if err != nil {
			return Allowlist{}, err
		}
		a.cidrs = append(a.cidrs, p)
	}
	a.hosts = append(a.hosts, x.Allow.Hosts...)
	return a, nil
}

// NewLimits is hops (DefaultMaxHops when negative) and the section's
// max_nodes (DefaultMaxNodes when unset). Zero hops is kept: it plans
// nothing, which is a valid way to only list.
func NewLimits(x *federate.Expand, hops int) Limits {
	l := Limits{MaxHops: hops, MaxNodes: DefaultMaxNodes}
	if hops < 0 {
		l.MaxHops = DefaultMaxHops
	}
	if x != nil && x.MaxNodes > 0 {
		l.MaxNodes = x.MaxNodes
	}
	return l
}

// broadcast reports the limited broadcast address 255.255.255.255, or
// the broadcast (last) address of an allowlisted IPv4 network holding
// addr. Networks of /31 and /32 have none (RFC 3021).
func (a Allowlist) broadcast(addr netip.Addr) bool {
	addr = addr.Unmap()
	if addr == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
		return true
	}
	for _, p := range a.cidrs {
		if !p.Addr().Is4() || p.Bits() > 30 || !p.Contains(addr) {
			continue
		}
		b := p.Addr().As4()
		host := ^uint32(0) >> p.Bits()
		last := [4]byte{b[0] | byte(host>>24), b[1] | byte(host>>16), b[2] | byte(host>>8), b[3] | byte(host)}
		if addr == netip.AddrFrom4(last) {
			return true
		}
	}
	return false
}

// Match returns the first rule that allows addr — CIDRs before host
// globs, each in the file's order, so the rule shown is stable — or ""
// when none does. Globs see the address's canonical text ("10.0.3.7"),
// an IPv4-mapped IPv6 address unmapped first.
func (a Allowlist) Match(addr netip.Addr) string {
	addr = addr.Unmap()
	for _, p := range a.cidrs {
		if p.Contains(addr) {
			return "cidr " + p.String()
		}
	}
	text := addr.String()
	for _, g := range a.hosts {
		if ok, _ := path.Match(g, text); ok { // validated by the registry
			return "host " + g
		}
	}
	return ""
}
