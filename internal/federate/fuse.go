package federate

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"time"
)

// DefaultWindow bounds how far apart (wall clock) two halves' Established
// times may lie and still be candidates for each other. It only guards
// against the ISN clock wrapping (~275 s at 64 ns a tick), which could
// hand a later connection the same pair: the pair itself already tells
// reused 4-tuples apart (S2). Two minutes is far wider than any NTP
// offset and well inside one wrap. The window assumes NTP-disciplined
// hosts: two wall clocks skewed by more than it put the two ends of a
// connection outside each other's window, and both stay no_match.
const DefaultWindow = 2 * time.Minute

// Reasons a half stays unresolved. Every one is a fact about the traces,
// never a judgment call: the merge fuses only a unique, mutual match.
const (
	// ReasonNoRole: no record said whether the half connected or accepted,
	// so the key's SYN correction is unknown.
	ReasonNoRole = "no_role"
	// ReasonNoKey: no record at ESTABLISHED or later carried the tcp_sock
	// fields (a pre-M4 trace, or a connect that was never answered).
	ReasonNoKey = "no_key"
	// ReasonKeyConflict: the half's records rebuilt different keys.
	ReasonKeyConflict = "key_conflict"
	// ReasonNoMatch: no other trace has the key in the window — the far
	// end is untraced, or a sequence-randomizing firewall or SYN proxy
	// sits between the two (S2 "Limits"). No 4-tuple fallback: across
	// NAT and port reuse that would be a guess.
	ReasonNoMatch = "no_match"
	// ReasonAmbiguous: more than one partner has the key in the window.
	ReasonAmbiguous = "ambiguous"
)

// Conn is one connection fused across traces: a client half and the
// server half that, alone among all halves, has the same ISN pair.
type Conn struct {
	ID             string // the merged conn_id both ends now share
	Key            ISNPair
	Client, Server Half
}

// CrossProject reports whether the two ends were traced by sessions of
// different projects: project A's app calling project B's (PLAN.md M4,
// "Build is project-aware").
func (c Conn) CrossProject() bool { return c.Client.Project != c.Server.Project }

// Unresolved is a half the merge could not fuse, and why.
type Unresolved struct {
	Half   Half
	Reason string
}

// Fusion is what Fuse found: fused connections sorted by ID, unresolved
// halves sorted by (host, conn_id).
type Fusion struct {
	Conns      []Conn
	Unresolved []Unresolved
}

// Fuse pairs client and server halves by ISN pair. A client and a server
// fuse only when each is the other's single candidate: equal keys and
// Established times at most window apart. Anything else — no key, no
// partner, or two — leaves every half involved unresolved, so a fused
// edge is always a fact and never a coin toss. The 4-tuples are not
// compared: NAT rewrites them.
func Fuse(halves []Half, window time.Duration) Fusion {
	var f Fusion
	groups := map[ISNPair][]Half{}
	for _, h := range halves {
		if reason := unkeyed(h); reason != "" {
			f.Unresolved = append(f.Unresolved, Unresolved{h, reason})
			continue
		}
		groups[h.Key] = append(groups[h.Key], h)
	}
	// A negative window admits nothing rather than wrapping into a huge
	// unsigned one that would admit everything.
	ns := uint64(max(window, 0))
	for _, g := range groups {
		f.fuseGroup(g, ns)
	}
	slices.SortFunc(f.Conns, func(a, b Conn) int { return cmp.Compare(a.ID, b.ID) })
	slices.SortFunc(f.Unresolved, func(a, b Unresolved) int {
		return cmp.Or(cmp.Compare(a.Half.Host, b.Half.Host),
			cmp.Compare(a.Half.ConnID, b.Half.ConnID))
	})
	return f
}

// unkeyed returns why h cannot take part in the join, or "".
func unkeyed(h Half) string {
	switch {
	case h.Role == "":
		return ReasonNoRole
	case h.KeyConflict:
		return ReasonKeyConflict
	case !h.HasKey:
		return ReasonNoKey
	}
	return ""
}

// fuseGroup fuses within one key's halves. Usually a group is one client
// and one server; more appear only after an ISN-clock wrap (kept apart
// by the window) or when something is wrong (reported as ambiguous).
func (f *Fusion) fuseGroup(g []Half, window uint64) {
	candidates := make([][]int, len(g))
	for i := range g {
		for j := range g {
			if g[i].Role != g[j].Role && within(g[i].Established, g[j].Established, window) {
				candidates[i] = append(candidates[i], j)
			}
		}
	}
	for i, h := range g {
		c := candidates[i]
		switch {
		case len(c) == 0:
			f.Unresolved = append(f.Unresolved, Unresolved{h, ReasonNoMatch})
		case len(c) > 1 || len(candidates[c[0]]) > 1:
			f.Unresolved = append(f.Unresolved, Unresolved{h, ReasonAmbiguous})
		case h.Role == RoleClient: // a mutual single match, added once
			f.Conns = append(f.Conns, newConn(h, g[c[0]]))
		}
	}
}

// newConn names a fused connection. The id hashes the key and both
// ends' hosts and host-local ids, so it is the same on every rebuild of
// the same traces, and two connections can share it only if they share
// all of that.
func newConn(client, server Half) Conn {
	return Conn{Key: client.Key, Client: client, Server: server,
		ID: hashID("tcp-isn|" + client.Key.String() + "|" + client.Host + "|" +
			client.ConnID + "|" + server.Host + "|" + server.ConnID)}
}

// hashID is the first 16 hex digits of the SHA-256 of s: the length the
// tracer's own conn ids have, so merged and host-local ids look alike.
func hashID(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:16]
}

func within(a, b, window uint64) bool {
	if a > b {
		a, b = b, a
	}
	return b-a <= window
}
