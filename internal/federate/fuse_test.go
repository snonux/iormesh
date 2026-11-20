package federate

import (
	"fmt"
	"net/netip"
	"slices"
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
