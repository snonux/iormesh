package federate

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"math"
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
// different projects: project A's app calling project B's (docs/milestones.md
// M4, "Build is project-aware").
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
// compared across hosts: NAT rewrites them. Within one host they are:
// first, the halves several sessions of the host reported for one
// socket, under different conn ids, fold into one (foldSessions), so a
// second session tracing the same process does not make its
// connections ambiguous.
func Fuse(halves []Half, window time.Duration) Fusion {
	var f Fusion
	// A negative window admits nothing rather than wrapping into a huge
	// unsigned one that would admit everything.
	ns := uint64(max(window, 0))
	groups := map[ISNPair][]Half{}
	for _, h := range foldSessions(halves, ns) {
		if reason := unkeyed(h); reason != "" {
			f.Unresolved = append(f.Unresolved, Unresolved{h, reason})
			continue
		}
		groups[h.Key] = append(groups[h.Key], h)
	}
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
// A hostile segment can put tens of thousands of halves on one key, so
// the group is never walked pairwise: candidates only need counting
// (0, 1 or more), and naming only when there is exactly one.
func (f *Fusion) fuseGroup(g []Half, window uint64) {
	m := countCandidates(g, window)
	for i, h := range g {
		switch c := m[i]; {
		case c.n == 0:
			f.Unresolved = append(f.Unresolved, Unresolved{h, ReasonNoMatch})
		case c.n > 1 || m[c.only].n > 1:
			f.Unresolved = append(f.Unresolved, Unresolved{h, ReasonAmbiguous})
		case h.Role == RoleClient: // a mutual single match, added once
			f.Conns = append(f.Conns, newConn(h, g[c.only]))
		}
	}
}

// candidates is how many opposite-role halves of the group lie within
// the window of one half, and, when n == 1, that one's index in g.
type candidates struct{ n, only int }

// countCandidates counts every half's candidates in O(n log n) time and
// O(n) memory: each role's halves sorted by Established, a half's
// candidates are one contiguous run of the other role's, found by two
// binary searches. Unkeyed halves are already gone, so a role is client
// or server.
func countCandidates(g []Half, window uint64) []candidates {
	var clients, servers []int
	for i, h := range g {
		if h.Role == RoleClient {
			clients = append(clients, i)
		} else {
			servers = append(servers, i)
		}
	}
	byTime := func(a, b int) int { return cmp.Compare(g[a].Established, g[b].Established) }
	slices.SortFunc(clients, byTime)
	slices.SortFunc(servers, byTime)
	m := make([]candidates, len(g))
	countRun(g, clients, servers, window, m)
	countRun(g, servers, clients, window, m)
	return m
}

// countRun fills m for each half in own from opp, sorted by Established:
// the run of opp within [t-window, t+window], saturating at both ends of
// uint64: two halves are candidates when their Established times differ
// by at most window.
func countRun(g []Half, own, opp []int, window uint64, m []candidates) {
	for _, i := range own {
		t := g[i].Established
		from := t - min(t, window)
		to := t + min(window, math.MaxUint64-t)
		lo, _ := slices.BinarySearchFunc(opp, from, func(j int, v uint64) int {
			return cmp.Compare(g[j].Established, v)
		})
		// n is the run's length: searching opp[lo:], with equal times
		// counted as smaller, lands just past the last half at exactly to.
		n, _ := slices.BinarySearchFunc(opp[lo:], to, func(j int, v uint64) int {
			if g[j].Established <= v {
				return -1
			}
			return 1
		})
		m[i] = candidates{n: n}
		if n > 0 {
			m[i].only = opp[lo]
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
