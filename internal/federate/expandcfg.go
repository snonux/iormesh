package federate

import (
	"fmt"
	"net/netip"
	"path"
	"strings"
	"unicode"
)

// The registry's expand: section (PLAN.md M6, docs/implementation.md
// "Server registry"): which unresolved peers mesh expansion may enroll,
// and how large the mesh may grow. It lives in servers.yaml because it
// widens the deploy boundary the registry draws (principle #3): whoever
// may write it already chooses where the operator's key deploys, so the
// file's privacy rules (0600, owner-only) guard both.

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
// client looked up is a guess about which machine answered.
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

// validAllowGlob refuses an empty, oversized, unprintable or malformed
// host glob. A bare "*" is allowed on purpose: allowing every address is
// a legitimate, if broad, operator choice, and the file is private.
func validAllowGlob(g string) error {
	if g == "" || len(g) > maxAllowGlob {
		return fmt.Errorf("%w: expand: host glob must be 1 to %d bytes", ErrInvalid, maxAllowGlob)
	}
	if strings.IndexFunc(g, func(r rune) bool { return !unicode.IsPrint(r) || unicode.IsSpace(r) }) >= 0 {
		return fmt.Errorf("%w: expand: host glob %q: space or control character", ErrInvalid, g)
	}
	if _, err := path.Match(g, ""); err != nil {
		return fmt.Errorf("%w: expand: host glob %q: %v", ErrInvalid, g, err)
	}
	return nil
}

// ParseAllowCIDR parses one allowlist CIDR. It must be canonical (no host
// bits set): "10.0.0.7/8" is more likely a typo for a /32 than a wish to
// allow all of 10/8, and an allowlist is no place to guess which.
func ParseAllowCIDR(s string) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("%w: expand: cidr %q: %v", ErrInvalid, s, err)
	}
	if p != p.Masked() {
		return netip.Prefix{}, fmt.Errorf("%w: expand: cidr %q has host bits set (did you mean %s?)",
			ErrInvalid, s, p.Masked())
	}
	return p, nil
}
