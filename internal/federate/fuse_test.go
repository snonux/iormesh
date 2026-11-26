package federate

import (
	"cmp"
	"fmt"
	"math"
	"math/rand/v2"
	"net/netip"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	sec   = uint64(time.Second)
	wall0 = uint64(1_763_000_000) * sec
)

// half builds a keyed half; tests then break one property at a time.
func half(host, conn, role string, key ISNPair, at uint64) Half {
	return Half{Host: host, Project: "shop", ConnID: conn, Role: role,
		Key: key, HasKey: true, Established: at,
		Procs: []string{"proc:" + host + "/1@1"}}
}

// before marks h as open before its session (decision #35).
func before(h Half) Half {
	h.Before = true
	return h
}

// outcome is a Fusion flattened for comparison: "client<>server" per
// fused connection, "conn=reason" per unresolved half.
func outcome(f Fusion) []string {
	var out []string
	for _, c := range f.Conns {
		out = append(out, c.Client.ConnID+"<>"+c.Server.ConnID)
	}
	for _, u := range f.Unresolved {
		out = append(out, u.Half.ConnID+"="+u.Reason)
	}
	slices.Sort(out)
	return out
}

// TestFuse covers the merge's rules (docs/implementation.md M4 "The
// merge"), negatives included: fusion needs a unique, mutual key match
// inside the window, and everything else stays unresolved.
func TestFuse(t *testing.T) {
	k1, k2 := ISNPair{1, 2}, ISNPair{3, 4}
	cases := []struct {
		name   string
		halves []Half
		want   []string
	}{
		{"pair", []Half{half("A", "c1", RoleClient, k1, wall0),
			half("B", "s1", RoleServer, k1, wall0+sec)}, []string{"c1<>s1"}},
		{"untraced far end", []Half{half("A", "c1", RoleClient, k1, wall0)},
			[]string{"c1=no_match"}},
		{"keys differ (SYN proxy, sequence randomizing)", []Half{
			half("A", "c1", RoleClient, k1, wall0), half("B", "s1", RoleServer, k2, wall0)},
			[]string{"c1=no_match", "s1=no_match"}},
		{"same role never fuses", []Half{half("A", "c1", RoleClient, k1, wall0),
			half("B", "c2", RoleClient, k1, wall0)}, []string{"c1=no_match", "c2=no_match"}},
		{"two servers with the key: ambiguous, never a coin toss", []Half{
			half("A", "c1", RoleClient, k1, wall0), half("B", "s1", RoleServer, k1, wall0),
			half("C", "s2", RoleServer, k1, wall0)},
			[]string{"c1=ambiguous", "s1=ambiguous", "s2=ambiguous"}},
		{"two clients with the key: ambiguous", []Half{
			half("A", "c1", RoleClient, k1, wall0), half("C", "c2", RoleClient, k1, wall0),
			half("B", "s1", RoleServer, k1, wall0)},
			[]string{"c1=ambiguous", "c2=ambiguous", "s1=ambiguous"}},
		{"ISN wrap: the window keeps a later reuse apart", []Half{
			half("A", "c1", RoleClient, k1, wall0), half("B", "s1", RoleServer, k1, wall0),
			half("A", "c2", RoleClient, k1, wall0+300*sec),
			half("B", "s2", RoleServer, k1, wall0+300*sec)},
			[]string{"c1<>s1", "c2<>s2"}},
		{"outside the window", []Half{half("A", "c1", RoleClient, k1, wall0),
			half("B", "s1", RoleServer, k1, wall0+uint64(DefaultWindow)+1)},
			[]string{"c1=no_match", "s1=no_match"}},
		// Halves open before their sessions (Half.Before, decision #35):
		// Established is an upper bound only.
		{"both open before sessions started minutes apart", []Half{
			before(half("A", "c1", RoleClient, k1, wall0)),
			before(half("B", "s1", RoleServer, k1, wall0+600*sec))}, []string{"c1<>s1"}},
		{"open before the client's session, accepted in the server's", []Half{
			before(half("A", "c1", RoleClient, k1, wall0+600*sec)),
			half("B", "s1", RoleServer, k1, wall0)}, []string{"c1<>s1"}},
		{"open before, but the partner established after it", []Half{
			before(half("A", "c1", RoleClient, k1, wall0)),
			half("B", "s1", RoleServer, k1, wall0+uint64(DefaultWindow)+1)},
			[]string{"c1=no_match", "s1=no_match"}},
		{"two open-before servers with the key: ambiguous", []Half{
			before(half("A", "c1", RoleClient, k1, wall0)),
			before(half("B", "s1", RoleServer, k1, wall0)), half("C", "s2", RoleServer, k1, wall0)},
			[]string{"c1=ambiguous", "s1=ambiguous", "s2=ambiguous"}},
		{"no key, no role, key conflict", []Half{
			{Host: "A", ConnID: "c1", Role: RoleClient},
			{Host: "A", ConnID: "c2", HasKey: true, Key: k1},
			{Host: "A", ConnID: "c3", Role: RoleServer, HasKey: true, Key: k1, KeyConflict: true},
			half("B", "s1", RoleServer, k1, wall0)},
			[]string{"c1=no_key", "c2=no_role", "c3=key_conflict", "s1=no_match"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := outcome(Fuse(c.halves, DefaultWindow))
			if !slices.Equal(got, c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

// TestFuseFoldsSessions: two sessions tracing one process on one host
// give its socket two conn ids — each id hashes the session's own
// bpf_ktime stamp of the establish (task ei4) — but the same role, key,
// 4-tuple and nearly the same Established. They are one half, folded
// before the join, so the cross-host connection still fuses. Negatives:
// another host, another tuple or a time outside the window is another
// socket, and the halves stay apart.
func TestFuseFoldsSessions(t *testing.T) {
	k := ISNPair{1, 2}
	srv := netip.MustParseAddrPort("10.0.2.2:8080")
	peer := netip.MustParseAddrPort("10.0.1.1:45678")
	server := func(host, conn string, at uint64, local netip.AddrPort) Half {
		h := half(host, conn, RoleServer, k, at)
		h.Local, h.Peer = local, peer
		return h
	}
	client := half("A", "c1", RoleClient, k, wall0)
	other := netip.MustParseAddrPort("10.0.2.3:8080")
	cases := []struct {
		name   string
		halves []Half
		want   []string
	}{
		{"two sessions, one socket", []Half{client,
			server("B", "s-ops", wall0+5, srv), server("B", "s-billing", wall0, srv)},
			[]string{"s-billing+s-ops<>c1"}},
		{"another host", []Half{client,
			server("B", "s1", wall0, srv), server("C", "s2", wall0, srv)},
			[]string{"c1=ambiguous", "s1=ambiguous", "s2=ambiguous"}},
		{"another tuple", []Half{client,
			server("B", "s1", wall0, srv), server("B", "s2", wall0, other)},
			[]string{"c1=ambiguous", "s1=ambiguous", "s2=ambiguous"}},
		{"outside the window", []Half{client, server("B", "s1", wall0, srv),
			server("B", "s2", wall0+uint64(DefaultWindow)+1, srv)},
			[]string{"s1<>c1", "s2=no_match"}},
		// A session started minutes later finds the socket already open
		// (decision #35): its half is the same socket, not a rival.
		{"a later session's open-before half", []Half{client, server("B", "s1", wall0, srv),
			before(server("B", "s2", wall0+600*sec, srv))},
			[]string{"s1+s2<>c1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			f := Fuse(c.halves, DefaultWindow)
			for _, conn := range f.Conns {
				ids := append([]string{conn.Server.ConnID}, conn.Server.Aliases...)
				slices.Sort(ids)
				got = append(got, strings.Join(ids, "+")+"<>"+conn.Client.ConnID)
			}
			for _, u := range f.Unresolved {
				got = append(got, u.Half.ConnID+"="+u.Reason)
			}
			slices.Sort(got)
			if !slices.Equal(got, c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

// TestFusePortReuse: S2's reuse phases — one client 4-tuple six times,
// through NAT, so the server sees another tuple — give six distinct keys
// and six edges, each with its own partner.
func TestFusePortReuse(t *testing.T) {
	cl := netip.MustParseAddrPort("10.0.1.1:45678")
	vip := netip.MustParseAddrPort("10.0.1.254:8080")
	srv := netip.MustParseAddrPort("10.0.2.2:8080")
	nat := netip.MustParseAddrPort("10.0.2.254:40001")
	var halves []Half
	for i := range 6 {
		// TIME_WAIT takeover: the next ISN is the old snd_nxt + 65537.
		k := ISNPair{Client: 1000 + uint32(i)*65537, Server: 7000 + uint32(i)*31}
		at := wall0 + uint64(i)*sec/10
		c := half("A", fmt.Sprintf("c%d", i), RoleClient, k, at)
		c.Local, c.Peer = cl, vip
		s := half("B", fmt.Sprintf("s%d", i), RoleServer, k, at)
		s.Local, s.Peer = srv, nat
		halves = append(halves, s, c)
	}
	f := Fuse(halves, DefaultWindow)
	if len(f.Conns) != 6 || len(f.Unresolved) != 0 {
		t.Fatalf("got %v, want six fused", outcome(f))
	}
	for _, c := range f.Conns {
		if c.Client.ConnID[1:] != c.Server.ConnID[1:] {
			t.Errorf("fused %s with %s", c.Client.ConnID, c.Server.ConnID)
		}
	}
}

// TestFuseDeterministic: input order never changes the result, and the
// merged id is stable and shared by nothing else.
func TestFuseDeterministic(t *testing.T) {
	hs := []Half{half("A", "c1", RoleClient, ISNPair{1, 2}, wall0),
		half("B", "s1", RoleServer, ISNPair{1, 2}, wall0),
		half("A", "c2", RoleClient, ISNPair{5, 6}, wall0),
		half("B", "s2", RoleServer, ISNPair{5, 6}, wall0),
		half("C", "x", RoleServer, ISNPair{9, 9}, wall0)}
	a := Fuse(hs, DefaultWindow)
	slices.Reverse(hs)
	b := Fuse(hs, DefaultWindow)
	if fmt.Sprint(a) != fmt.Sprint(b) {
		t.Fatalf("order changed the result:\n%v\n%v", a, b)
	}
	if a.Conns[0].ID == a.Conns[1].ID || len(a.Conns[0].ID) != 16 {
		t.Fatalf("conn ids %q %q", a.Conns[0].ID, a.Conns[1].ID)
	}
}

// TestCrossProject: the join ignores projects; the fused conn says when
// its ends came from two.
func TestCrossProject(t *testing.T) {
	c := half("A", "c1", RoleClient, ISNPair{1, 2}, wall0)
	s := half("B", "s1", RoleServer, ISNPair{1, 2}, wall0)
	s.Project = "billing"
	f := Fuse([]Half{c, s}, DefaultWindow)
	if len(f.Conns) != 1 || !f.Conns[0].CrossProject() {
		t.Fatalf("got %v, want one cross-project conn", outcome(f))
	}
}

// sameKeyHalves builds n halves on one ISN pair, alternating client and
// server, all established within a millisecond: the hostile segment of
// task ci4, every half a candidate of every opposite one.
func sameKeyHalves(n int) []Half {
	hs := make([]Half, n)
	role := [2]string{RoleClient, RoleServer}
	for i := range hs {
		hs[i] = Half{Host: "H", ConnID: fmt.Sprintf("x%06d", i), Role: role[i%2],
			Key: ISNPair{7, 7}, HasKey: true, Established: wall0 + uint64(i)}
	}
	return hs
}

// fuseAllocBytes is how many bytes one Fuse over hs allocates.
func fuseAllocBytes(hs []Half) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	Fuse(hs, DefaultWindow)
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// TestFuseSameKeyScales: a segment of tens of thousands of halves on one
// key (one compromised server can publish it) must fail itself, not the
// controller. Allocation grows near-linearly in n — measured on small n
// first, so a quadratic Fuse fails here after a few dozen MiB instead of
// allocating gigabytes — and 50,000 such halves fuse in well under a
// second, all ambiguous.
func TestFuseSameKeyScales(t *testing.T) {
	small, big := fuseAllocBytes(sameKeyHalves(250)), fuseAllocBytes(sameKeyHalves(2000))
	if big > 16*small {
		t.Fatalf("8x the halves allocated %.1fx the bytes (%d -> %d): not linear",
			float64(big)/float64(small), small, big)
	}
	// The allocation ratio above proves the scaling; the wall-clock
	// bound only catches a gross regression. It is generous (the old
	// quadratic code took minutes here, the new one well under a
	// second) so a -race run on a loaded machine cannot fail it.
	hs := sameKeyHalves(50_000)
	start := time.Now()
	f := Fuse(hs, DefaultWindow)
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("50,000 same-key halves took %v", d)
	}
	if len(f.Conns) != 0 || len(f.Unresolved) != len(hs) {
		t.Fatalf("got %d conns, %d unresolved", len(f.Conns), len(f.Unresolved))
	}
	for _, u := range f.Unresolved {
		if u.Reason != ReasonAmbiguous {
			t.Fatalf("%s: %s, want ambiguous", u.Half.ConnID, u.Reason)
		}
	}
}

// refFuse is Fuse as it was before task ci4: every half's full candidate
// list, quadratic in a group. It is the reference the property test
// holds the linear grouping to.
func refFuse(halves []Half, window time.Duration) Fusion {
	var f Fusion
	groups := map[ISNPair][]Half{}
	for _, h := range halves {
		if reason := unkeyed(h); reason != "" {
			f.Unresolved = append(f.Unresolved, Unresolved{h, reason})
			continue
		}
		groups[h.Key] = append(groups[h.Key], h)
	}
	ns := uint64(max(window, 0))
	for _, g := range groups {
		refFuseGroup(&f, g, ns)
	}
	slices.SortFunc(f.Conns, func(a, b Conn) int { return cmp.Compare(a.ID, b.ID) })
	slices.SortFunc(f.Unresolved, func(a, b Unresolved) int {
		return cmp.Or(cmp.Compare(a.Half.Host, b.Half.Host),
			cmp.Compare(a.Half.ConnID, b.Half.ConnID))
	})
	return f
}

// refFuseGroup is the old fuseGroup: a full double loop of candidates.
func refFuseGroup(f *Fusion, g []Half, window uint64) {
	candidates := make([][]int, len(g))
	for i := range g {
		for j := range g {
			if g[i].Role != g[j].Role && refCandidates(g[i], g[j], window) {
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
		case h.Role == RoleClient:
			f.Conns = append(f.Conns, newConn(h, g[c[0]]))
		}
	}
}

// refCandidates is the reference's window test of two halves: within for
// two exact ones; a half open before its session (Before) has only an
// upper bound, so an exact partner must be established no later than
// that bound plus window, and two such halves always qualify.
func refCandidates(a, b Half, window uint64) bool {
	switch {
	case a.Before && b.Before:
		return true
	case a.Before:
		return b.Established <= a.Established || b.Established-a.Established <= window
	case b.Before:
		return a.Established <= b.Established || a.Established-b.Established <= window
	}
	return within(a.Established, b.Established, window)
}

// within is the reference's window test: a and b at most window apart.
func within(a, b, window uint64) bool {
	if a > b {
		a, b = b, a
	}
	return b-a <= window
}

// randomHalves draws up to 12 halves over two keys, both roles, a few
// unkeyed, with times clustered on the window's edges (ties, exactly w
// apart, one past it, near zero and near the top of uint64) so every
// boundary gets hit. w >= 1.
func randomHalves(r *rand.Rand, w uint64) []Half {
	offsets := []uint64{0, 1, w - 1, w, w + 1, 2 * w, 2*w + 1, 3 * w}
	bases := []uint64{0, w / 2, wall0, math.MaxUint64 - 3*w}
	hs := make([]Half, r.IntN(13))
	for i := range hs {
		role := []string{RoleClient, RoleServer, RoleClient, RoleServer, ""}[r.IntN(5)]
		hs[i] = Half{Host: "H" + strconv.Itoa(r.IntN(3)), ConnID: fmt.Sprintf("c%02d", i),
			Role: role, Key: ISNPair{1, uint32(r.IntN(2))}, HasKey: r.IntN(8) != 0,
			Established: bases[r.IntN(len(bases))] + offsets[r.IntN(len(offsets))],
			Before: r.IntN(4) == 0}
	}
	return hs
}

// TestFuseMatchesReference: on many small random inputs the linear
// grouping gives exactly the reference's fusion — same conns, same ids,
// same unresolved halves and reasons — for zero, negative, tiny and the
// default window.
func TestFuseMatchesReference(t *testing.T) {
	r := rand.New(rand.NewPCG(4, 2))
	windows := []time.Duration{0, -1, 1, 10, DefaultWindow}
	for range 5000 {
		w := windows[r.IntN(len(windows))]
		hs := randomHalves(r, uint64(max(w, 1)))
		got, want := fmt.Sprint(Fuse(hs, w)), fmt.Sprint(refFuse(hs, w))
		if got != want {
			t.Fatalf("window %v, halves %+v:\ngot  %s\nwant %s", w, hs, got, want)
		}
	}
}
