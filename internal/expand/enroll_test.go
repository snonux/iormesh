package expand

import (
	"errors"
	"net/netip"
	"slices"
	"testing"

	"iormesh/internal/federate"
)

// TestPlanTransport: the controller's own traffic never proposes — a
// peer on the SSH port, or any connection of a traced ssh/rsync process —
// while the same process's other peers are judged as usual.
func TestPlanTransport(t *testing.T) {
	ten := allowlist(t, nil, "10.0.0.0/8")
	lim := Limits{MaxHops: 1, MaxNodes: 16}
	sshPeer := input(registry("hostA", "hostB"), nil, out("id-A", "10.0.1.1:40001", "10.0.4.4:22"))
	if got := summary(Plan(sshPeer, ten, lim)); !slices.Equal(got, []string{"skip 10.0.4.4:22 transport"}) {
		t.Errorf("peer on port 22: %q", got)
	}
	for _, comm := range []string{"ssh", "rsync", "sshd"} {
		in := input(registry("hostA", "hostB"), nil, out("id-A", "10.0.1.1:40001", "10.0.4.4:873"))
		in.Merge.Graph.Edges = nil // no app: the label is the comm
		in.Merge.Graph.Nodes[0].Attrs = map[string]string{"comm": comm}
		got := summary(Plan(in, ten, lim))
		if !slices.Equal(got, []string{"skip 10.0.4.4:873 transport"}) {
			t.Errorf("revealed by %s: %q", comm, got)
		}
	}
}

// TestPlanRevealingProjects: a candidate names the projects that revealed it.
func TestPlanRevealingProjects(t *testing.T) {
	a := out("id-A", "10.0.1.1:40001", "10.0.4.4:5432")
	b := out("id-B", "10.0.2.2:40001", "10.0.4.4:5432")
	b.Half.Project = "billing"
	p := Plan(input(registry("hostA", "hostB"), nil, a, b), allowlist(t, nil, "10.0.0.0/8"),
		Limits{MaxHops: 2, MaxNodes: 16})
	if len(p.Deploy) != 1 || !slices.Equal(p.Deploy[0].Projects, []string{"billing", "shop"}) {
		t.Errorf("deploy %+v", p.Deploy)
	}
}

func candidate(addr string, port uint16, hop, conns int) Candidate {
	return Candidate{Peer: Peer{Addr: netip.MustParseAddr(addr), Port: port, Conns: conns}, Hop: hop,
		Rule: "cidr 10.0.0.0/8", Projects: []string{"shop", "zeta"}}
}

// TestEnroll: one registry host per address, written as the address,
// under the first revealing project, bound to the busiest port, its hop
// recorded; another port of the same host is returned, not enrolled.
func TestEnroll(t *testing.T) {
	reg := registry("hostA")
	p := Proposal{Limits: Limits{MaxHops: 2, MaxNodes: 16}, Deploy: []Candidate{
		candidate("10.0.4.4", 5432, 1, 3), candidate("10.0.4.4", 8080, 1, 5), candidate("10.0.5.5", 443, 2, 1)}}
	got, skipped, err := Enroll(&reg, p, allowlist(t, nil, "10.0.0.0/8"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || len(reg.Servers) != 3 {
		t.Fatalf("enrolled %+v, registry %+v", got, reg.Servers)
	}
	want := []federate.Server{
		{Host: "10.0.4.4", Hop: 1, Projects: []federate.Project{{Name: "shop", Target: federate.Target{ListenPort: 8080}}}},
		{Host: "10.0.5.5", Hop: 2, Projects: []federate.Project{{Name: "shop", Target: federate.Target{ListenPort: 443}}}},
	}
	for i, w := range want {
		if s := reg.Servers[i+1]; s.Host != w.Host || s.Hop != w.Hop || s.Projects[0].Name != "shop" ||
			s.Projects[0].Target != w.Projects[0].Target || got[i].Server.Host != w.Host {
			t.Errorf("entry %d = %+v, want %+v", i, s, w)
		}
	}
	if len(skipped) != 1 || skipped[0].Port != 5432 || skipped[0].Reason != ReasonAnotherPort {
		t.Errorf("skipped %+v", skipped)
	}
	if err := reg.Validate(); err != nil {
		t.Error(err)
	}
}

// TestEnrollRefusesUnplanned: a candidate Plan could not have proposed —
// outside the allowlist, past the hop limit, IPv6, already registered —
// enrolls nothing and leaves the registry as it was.
func TestEnrollRefusesUnplanned(t *testing.T) {
	ten := allowlist(t, nil, "10.0.0.0/8")
	lim := Limits{MaxHops: 1, MaxNodes: 16}
	v6 := candidate("10.0.4.4", 80, 1, 1)
	v6.Addr = netip.MustParseAddr("fd00::4")
	cases := map[string]struct {
		c    Candidate
		want error
	}{
		"outside the allowlist": {candidate("192.0.2.1", 80, 1, 1), ErrNotPlanned},
		"past the hop limit":    {candidate("10.0.4.4", 80, 2, 1), ErrNotPlanned},
		"ipv6":                  {v6, ErrNotPlanned},
		"no project":            {Candidate{Peer: candidate("10.0.4.4", 80, 1, 1).Peer, Hop: 1}, ErrNotPlanned},
		"registered":            {candidate("10.0.1.1", 80, 1, 1), federate.ErrDuplicate},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			reg := registry("hostA", "10.0.1.1")
			p := Proposal{Limits: lim, Deploy: []Candidate{candidate("10.0.9.9", 80, 1, 1), tc.c}}
			got, _, err := Enroll(&reg, p, ten)
			if !errors.Is(err, tc.want) || got != nil {
				t.Errorf("err %v, enrolled %+v", err, got)
			}
			if len(reg.Servers) != 2 {
				t.Errorf("registry changed: %+v", reg.Servers)
			}
		})
	}
}

// TestRoundsTerminate: hostA (hop 0) reaches B; B, enrolled at hop 1 and
// traced, reaches A back and C. With -hops 1 the second round stops at
// the first ring; with -hops 2 it enrolls C at hop 2; a third round over
// C's traces (which dial A and B: a cycle) enrolls nothing.
func TestRoundsTerminate(t *testing.T) {
	ten := allowlist(t, nil, "10.0.0.0/8")
	reg := registry("hostA")
	reg.Servers[0].Hop = 0
	round := func(hops int, dirs map[string]string, halves ...federate.Unresolved) []Enrollment {
		t.Helper()
		in := input(reg, nil, halves...)
		in.HostDirs = dirs
		got, _, err := Enroll(&reg, Plan(in, ten, Limits{MaxHops: hops, MaxNodes: 16}), ten)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	dirs := map[string]string{"id-A": "hostA", "id-B": "10.0.2.2", "id-C": "10.0.3.3"}
	r1 := []federate.Unresolved{out("id-A", "10.0.1.1:40001", "10.0.2.2:443")}
	if got := round(1, dirs, r1...); len(got) != 1 || got[0].Server.Host != "10.0.2.2" || got[0].Server.Hop != 1 {
		t.Fatalf("round 1: %+v", got)
	}
	r2 := append(r1, out("id-B", "10.0.2.2:40002", "10.0.1.1:80"), out("id-B", "10.0.2.2:40003", "10.0.3.3:443"))
	if got := round(1, dirs, r2...); len(got) != 0 {
		t.Fatalf("round 2 at -hops 1 enrolled %+v", got)
	}
	if got := round(2, dirs, r2...); len(got) != 1 || got[0].Server.Host != "10.0.3.3" || got[0].Server.Hop != 2 {
		t.Fatalf("round 2 at -hops 2: %+v", got)
	}
	r3 := append(r2, out("id-C", "10.0.3.3:40004", "10.0.1.1:80"), out("id-C", "10.0.3.3:40005", "10.0.2.2:443"))
	if got := round(5, dirs, r3...); len(got) != 0 {
		t.Fatalf("round 3 (a cycle) enrolled %+v", got)
	}
	if len(reg.Servers) != 3 {
		t.Errorf("registry %+v", reg.Servers)
	}
}
