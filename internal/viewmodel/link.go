package viewmodel

import (
	"cmp"
	"slices"
	"sort"

	"iormesh/internal/federate"
)

const (
	// nestLookback bounds how many earlier server requests of a thread
	// are tried as a client request's container: requests on one thread
	// run one after another, so the latest that started before the
	// client's is nearly always it, and a few more cover nesting. A
	// container further back is missed — the request stays a root, which
	// is honest, never wrong.
	nestLookback = 8
	// maxClockGroup bounds the unpaired requests of one connection that
	// the clock join compares pairwise: a multiplexed connection with
	// thousands of them would cost millions of comparisons, and a
	// unique match among so many is unlikely anyway. Past it the hops
	// stay unpaired.
	maxClockGroup = 64
)

// links is the shape of the conversations: who answered whom, and which
// client request was made while serving which.
type links struct {
	// partner is the other end of a joined hop, -1 when none; hop says
	// how a client's hop was joined (Hop*).
	partner []int
	hop     []string
	// parent is the server request a client request nested in, -1 for
	// none; children are a server request's nested client requests, in
	// start order.
	parent   []int
	children [][]int
	// roots are the requests nothing leads to: client requests made
	// outside any traced server request, and server requests no client
	// end was joined to.
	roots []int
}

// link builds the links of reqs (sorted by ref, so indices are stable).
func link(reqs []request, tl federate.Timeline) links {
	n := len(reqs)
	l := links{partner: make([]int, n), hop: make([]string, n), parent: make([]int, n),
		children: make([][]int, n)}
	for i := range n {
		l.partner[i], l.parent[i] = -1, -1
	}
	pairBySeq(reqs, &l)
	pairByClock(reqs, tl, &l)
	nest(reqs, &l)
	for i, q := range reqs {
		switch {
		case q.client && l.hop[i] == "" && !q.hasStart:
			l.hop[i] = HopAsync
		case q.client && l.hop[i] == "":
			l.hop[i] = HopUnpaired
		}
		if (q.client && l.parent[i] < 0) || (!q.client && l.partner[i] < 0) {
			l.roots = append(l.roots, i)
		}
	}
	return l
}

// pairBySeq joins the two ends of each HTTP/1 request: the same merged
// conn id and req_seq, exactly one client end and one server end.
// Anything else — a duplicate, a missing end — joins nothing.
func pairBySeq(reqs []request, l *links) {
	type key struct {
		conn string
		seq  uint32
	}
	ends := map[key][2][]int{}
	for i, q := range reqs {
		if q.conn == "" || q.reqSeq == 0 {
			continue
		}
		k := key{q.conn, q.reqSeq}
		e := ends[k]
		e[btoi(q.client)] = append(e[btoi(q.client)], i)
		ends[k] = e
	}
	for _, e := range ends {
		if len(e[0]) == 1 && len(e[1]) == 1 {
			s, c := e[0][0], e[1][0]
			l.partner[s], l.partner[c], l.hop[c] = c, s, HopPaired
		}
	}
}

// pairByClock joins an unpaired client request to an unpaired server
// request on the same connection when the clock bounds prove the
// server's span inside the client's — started after the client's start,
// ended before its end — and each is the other's only such match. A
// request without its start proves no span and joins nothing.
func pairByClock(reqs []request, tl federate.Timeline, l *links) {
	groups := map[string][2][]int{}
	for i, q := range reqs {
		if q.conn == "" || !q.hasStart || l.partner[i] >= 0 {
			continue
		}
		g := groups[q.conn]
		g[btoi(q.client)] = append(g[btoi(q.client)], i)
		groups[q.conn] = g
	}
	for _, g := range groups {
		if len(g[0]) == 0 || len(g[1]) == 0 || len(g[0]) > maxClockGroup || len(g[1]) > maxClockGroup {
			continue
		}
		matches := map[int][]int{}
		count := map[int]int{}
		for _, c := range g[1] {
			for _, s := range g[0] {
				if inside(tl, reqs[s], reqs[c]) {
					matches[c] = append(matches[c], s)
					count[s]++
				}
			}
		}
		for c, ss := range matches {
			if len(ss) == 1 && count[ss[0]] == 1 {
				l.partner[c], l.partner[ss[0]], l.hop[c] = ss[0], c, HopClock
			}
		}
	}
}

// inside reports whether the bounds prove server's span inside client's.
func inside(tl federate.Timeline, server, client request) bool {
	return tl.Order(stampAt(client.th.host, client.start), stampAt(server.th.host, server.start)) == federate.Before &&
		tl.Order(stampAt(server.th.host, server.end), stampAt(client.th.host, client.end)) == federate.Before
}

// nest finds each client request's server request on the same thread
// whose span holds it. The two share one clock, so the containment is a
// fact; a request without its start, or on a virtual thread's carrier,
// proves none.
func nest(reqs []request, l *links) {
	servers := map[threadKey][]int{}
	for i, q := range reqs {
		if !q.client && q.hasStart && !q.carrier {
			servers[q.th] = append(servers[q.th], i)
		}
	}
	for _, ss := range servers {
		slices.SortFunc(ss, func(a, b int) int {
			return cmp.Or(cmp.Compare(reqs[a].start, reqs[b].start), cmp.Compare(a, b))
		})
	}
	for i, q := range reqs {
		if !q.client || !q.hasStart || q.carrier {
			continue
		}
		if p := container(reqs, servers[q.th], q); p >= 0 {
			l.parent[i] = p
			l.children[p] = append(l.children[p], i)
		}
	}
	for p := range l.children {
		slices.SortFunc(l.children[p], func(a, b int) int {
			return cmp.Or(cmp.Compare(reqs[a].start, reqs[b].start), cmp.Compare(a, b))
		})
	}
}

// container is the innermost of ss (sorted by start) whose span holds
// q's, -1 when none of the nestLookback latest starting no later than q
// does.
func container(reqs []request, ss []int, q request) int {
	n := sort.Search(len(ss), func(i int) bool { return reqs[ss[i]].start > q.start })
	for k := n - 1; k >= 0 && k >= n-nestLookback; k-- {
		if s := reqs[ss[k]]; s.end >= q.end {
			return ss[k]
		}
	}
	return -1
}

// walk calls fn for every request of root's conversation, depth first:
// a client request, its server end, that one's nested requests. Each
// client has one parent and each server one partner, so the structure
// is a forest and the walk ends; seen guards it all the same.
func (l links) walk(root int, fn func(int)) {
	seen := map[int]bool{}
	var visit func(int)
	visit = func(i int) {
		if i < 0 || seen[i] {
			return
		}
		seen[i] = true
		fn(i)
		visit(l.partner[i])
		for _, c := range l.children[i] {
			visit(c)
		}
	}
	visit(root)
}

// stampAt is an instant on a host's clock.
func stampAt(host string, wall uint64) federate.Stamp { return federate.Stamp{Host: host, Wall: wall} }

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
