package federate

import (
	"cmp"
	"net/netip"
	"slices"
)

// socketKey is what one socket's halves share across the sessions of
// its host: two projects whose sessions both trace a process (pattern
// plus listen port overlapping) each report its sockets.
type socketKey struct {
	host, role  string
	key         ISNPair
	local, peer netip.AddrPort
}

// foldSessions folds the halves two or more sessions of one host
// reported for one socket into one half (task ei4). Each session's
// correlator hashes the socket's conn id from the 4-tuple and the
// establish time its own BPF program stamped (bpf_ktime_get_boot_ns,
// internal/c/sock.c), and two programs on one tracepoint read the clock
// nanoseconds apart: the ids differ. Unfolded, the two same-role halves
// with one key would make the far end's half ambiguous.
//
// Halves fold when they share host, role, ISN pair and a known
// 4-tuple, and their Established times lie within window of the
// cluster's earliest: on one host that is one socket (one host is
// taken as one network namespace: two namespaces holding the same
// tuple and the same ISN pair at once would fold, a contrived case the
// halves cannot tell apart since they carry no netns). The window keeps
// a reuse of tuple and pair after an ISN-clock wrap apart, as Fuse
// does. Halves without a key or a known tuple never fold. The folded
// half keeps the smallest conn id (deterministic) and the earliest
// Established, takes the union of the processes, and lists the other
// ids as Aliases, which the merge renames with it; its records are the
// kept id's (clock.go reads one session's records, which hold every
// event of the socket that session saw).
func foldSessions(halves []Half, window uint64) []Half {
	groups := map[socketKey][]Half{}
	var out []Half
	for _, h := range halves {
		if unkeyed(h) != "" || !h.Local.IsValid() || !h.Peer.IsValid() {
			out = append(out, h)
			continue
		}
		k := socketKey{h.Host, h.Role, h.Key, h.Local, h.Peer}
		groups[k] = append(groups[k], h)
	}
	for _, g := range groups {
		slices.SortFunc(g, func(a, b Half) int {
			return cmp.Or(cmp.Compare(a.Established, b.Established),
				cmp.Compare(a.ConnID, b.ConnID))
		})
		for len(g) > 0 {
			n := 1
			for n < len(g) && g[n].Established-g[0].Established <= window {
				n++
			}
			out = append(out, foldOne(g[:n]))
			g = g[n:]
		}
	}
	return out
}

// foldOne merges one socket's halves, sorted by Established, into one.
func foldOne(g []Half) Half {
	if len(g) == 1 {
		return g[0]
	}
	keep := slices.MinFunc(g, func(a, b Half) int { return cmp.Compare(a.ConnID, b.ConnID) })
	keep.Established = g[0].Established
	var procs, aliases []string
	for _, h := range g {
		procs = append(procs, h.Procs...)
		aliases = append(aliases, h.Aliases...)
		if h.ConnID != keep.ConnID {
			aliases = append(aliases, h.ConnID)
		}
	}
	slices.Sort(procs)
	slices.Sort(aliases)
	keep.Procs, keep.Aliases = slices.Compact(procs), slices.Compact(aliases)
	return keep
}
