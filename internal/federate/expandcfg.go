package federate

import (
	"fmt"
	"net/netip"
	"path"
	"strings"
)

// The registry's expand: section (docs/milestones.md M6,
// docs/implementation.md "Server registry"): which unresolved peers mesh
// expansion may enroll, and how large the mesh may grow. It lives in
// servers.yaml because it widens the deploy boundary the registry draws
// (principle #3): whoever may write it already chooses where the operator's
// key deploys, so the file's privacy rules (0600, owner-only) guard both.

// Bounds on the section, so a hand-edited file cannot make every peer
// pay for thousands of rules.
const (
	maxAllowRules = 256
	maxAllowGlob  = 255
)

// Expand is the registry's expand: section. A registry without one allows
// nothing: expansion lists every peer and enrolls none.
type Expand struct {
	Allow Allow `yaml:"allow"`
	// MaxNodes caps the mesh, registered hosts included; zero means the
	// default (internal/expand DefaultMaxNodes).
	MaxNodes int `yaml:"max_nodes,omitempty"`
}

// Allow is the expansion allowlist. A peer is allowed when its address
// is inside one of CIDRs or its address, as text, matches one of Hosts
// (path.Match globs such as "10.20.3.*"). Names are never matched: a
// trace knows a peer by the address it reached, and a DNS name the
// client looked up is a guess about which machine answered. CIDRs are
// the better tool: a glob matches text, not a network — "10.0.0.1*"
// allows .1, .10–.19 and .100–.199 alike.
//
// The allowlist is the only guard against fronts the traces cannot
// unmask: a VIP, load balancer or SYN proxy whose backend is untraced
// is proposed by its own address, as is a container bridge address
// (Docker's 172.17.0.0/16 exists on every host and names a different
// container on each). Keep such ranges out of it.
type Allow struct {
	Hosts []string `yaml:"hosts,omitempty"`
	CIDRs []string `yaml:"cidrs,omitempty"`
}

// validate checks the section a hand-edited registry may carry.
func (x *Expand) validate() error {
	if x == nil {
		return nil
	}
	if x.MaxNodes < 0 {
		return fmt.Errorf("%w: expand: max_nodes %d is negative", ErrInvalid, x.MaxNodes)
	}
	if n := len(x.Allow.Hosts) + len(x.Allow.CIDRs); n > maxAllowRules {
		return fmt.Errorf("%w: expand: %d allow rules, at most %d", ErrInvalid, n, maxAllowRules)
	}
	for _, g := range x.Allow.Hosts {
		if err := validAllowGlob(g); err != nil {
			return err
		}
	}
	for _, c := range x.Allow.CIDRs {
		if _, err := ParseAllowCIDR(c); err != nil {
			return err
		}
	}
	return nil
}

// validAllowGlob refuses an empty, oversized or malformed host glob, and
// one holding a character no address text has (notAddrGlob). A bare "*" is allowed on purpose: allowing every address is
// a legitimate, if broad, operator choice, and the file is private.
func validAllowGlob(g string) error {
	if g == "" || len(g) > maxAllowGlob {
		return fmt.Errorf("%w: expand: host glob must be 1 to %d bytes", ErrInvalid, maxAllowGlob)
	}
	if i := strings.IndexFunc(g, notAddrGlob); i >= 0 {
		return fmt.Errorf("%w: expand: host glob %q: %q can never match an address "+
			"(globs see the address text: digits, lowercase hex, '.', ':'; prefer cidrs)",
			ErrInvalid, g, g[i:i+1])
	}
	if _, err := path.Match(g, ""); err != nil {
		return fmt.Errorf("%w: expand: host glob %q: %v", ErrInvalid, g, err)
	}
	return nil
}

// notAddrGlob reports a rune no address text holds and no glob syntax
// needs. Globs match the canonical address text — IPv4 dotted decimal or
// lowercase IPv6 — so a name ("db-*.example.org"), an uppercase hex
// digit or a space would only make a rule that silently never matches.
func notAddrGlob(r rune) bool {
	switch {
	case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
		return false
	}
	return !strings.ContainsRune(".:*?[]!^-", r)
}

// ParseAllowCIDR parses one allowlist CIDR. It must be canonical (no host
// bits set): "10.0.0.7/8" is more likely a typo for a /32 than a wish to
// allow all of 10/8, and an allowlist is no place to guess which. An
// IPv4-mapped IPv6 prefix (::ffff:10.0.0.0/104) is refused too: peers
// are matched as unmapped IPv4, so it would never match one.
func ParseAllowCIDR(s string) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("%w: expand: cidr %q: %v", ErrInvalid, s, err)
	}
	if p.Addr().Is4In6() {
		return netip.Prefix{}, fmt.Errorf("%w: expand: cidr %q is IPv4-mapped; write it as IPv4",
			ErrInvalid, s)
	}
	if p != p.Masked() {
		return netip.Prefix{}, fmt.Errorf("%w: expand: cidr %q has host bits set (did you mean %s?)",
			ErrInvalid, s, p.Masked())
	}
	return p, nil
}
