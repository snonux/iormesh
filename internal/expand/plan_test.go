package expand

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"iormesh/internal/federate"
	"iormesh/internal/schema"
)

// Synthetic merges: hosts hostA, hostB, hostC have host ids id-A, id-B,
// id-C and own 10.0.1.1, 10.0.2.2, 10.0.3.3. The gateway on hostA was
// enrolled by hand (hop 0); hostB came in one round later (hop 1).

func half(hostID, role, local, peer string) federate.Half {
	h := federate.Half{Host: hostID, Project: "shop", ConnID: "c-" + local + "-" + peer, Role: role,
		Procs: []string{schema.ProcessNodeID(hostID, 100, 1)}, HasKey: true}
	if local != "" {
		h.Local = netip.MustParseAddrPort(local)
	}
	if peer != "" {
		h.Peer = netip.MustParseAddrPort(peer)
	}
	return h
}

// out is a client half on hostID dialing peer; no_match unless reason.
func out(hostID, local, peer string, reason ...string) federate.Unresolved {
	r := federate.ReasonNoMatch
	if len(reason) > 0 {
		r = reason[0]
	}
	return federate.Unresolved{Half: half(hostID, federate.RoleClient, local, peer), Reason: r}
}

func registry(hosts ...string) federate.Registry {
	var r federate.Registry
	for i, h := range hosts {
		r.Servers = append(r.Servers, federate.Server{Host: h, Hop: min(i, 1),
			Projects: []federate.Project{{Name: "shop", Target: federate.Target{Pattern: "*java*"}}}})
	}
	return r
}

// input is a merge of unresolved halves (and fused conns), traced by the
// three hosts; hostA and hostB are registered unless reg says otherwise.
func input(reg federate.Registry, conns []federate.Conn, unresolved ...federate.Unresolved) Input {
	g := schema.Graph{}
	for _, id := range []string{"id-A", "id-B", "id-C"} {
		pid := schema.ProcessNodeID(id, 100, 1)
		g.Nodes = append(g.Nodes, schema.Node{Type: schema.NodeProcess, ID: pid,
			Attrs: map[string]string{"comm": "java"}})
	}
	g.Edges = append(g.Edges, schema.Edge{Type: schema.EdgeIncarnation, From: "app:shop/gateway",
		To: schema.ProcessNodeID("id-A", 100, 1)})
	return Input{Merge: federate.Result{Graph: g, Fusion: federate.Fusion{Conns: conns, Unresolved: unresolved}},
		HostDirs: map[string]string{"id-A": "hostA", "id-B": "hostB", "id-C": "hostC"}, Registry: reg}
}

func allowlist(t *testing.T, hosts []string, cidrs ...string) Allowlist {
	t.Helper()
	a, err := NewAllowlist(&federate.Expand{Allow: federate.Allow{Hosts: hosts, CIDRs: cidrs}})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// summary flattens a proposal to "deploy addr:port hop N" and "skip
// addr:port reason" lines, sorted as Plan sorts them.
func summary(p Proposal) []string {
	var s []string
	for _, c := range p.Deploy {
		s = append(s, fmt.Sprintf("deploy %s hop %d", addrPort(c.Peer), c.Hop))
	}
	for _, k := range p.Skipped {
		s = append(s, fmt.Sprintf("skip %s %s", addrPort(k.Peer), k.Reason))
	}
	return s
}

func TestPlan(t *testing.T) {
	ten := []string{"10.0.0.0/8"}
	lim := Limits{MaxHops: 1, MaxNodes: 16}
	regAB := registry("hostA", "hostB")
	// A fused connection hostA → VIP 10.9.9.9:80, which DNAT sent to
	// hostB's 10.0.2.2:8080: the VIP is proven NAT.
	nat := []federate.Conn{{ID: "m1", Client: half("id-A", federate.RoleClient, "10.0.1.1:40000", "10.9.9.9:80"),
		Server: half("id-B", federate.RoleServer, "10.0.2.2:8080", "10.0.1.1:40000")}}
	cases := []struct {
		name  string
		in    Input
		hosts []string
		cidrs []string
		lim   Limits
		want  []string
	}{
		{"first ring", input(regAB, nil, out("id-A", "10.0.1.1:40001", "10.0.4.4:5432")), nil, ten, lim,
			[]string{"deploy 10.0.4.4:5432 hop 1"}},
		{"outside the allowlist is listed, not deployed",
			input(regAB, nil, out("id-A", "10.0.1.1:40001", "203.0.113.9:5432")), nil, ten, lim,
			[]string{"skip 203.0.113.9:5432 not_allowed"}},
		{"no expand section allows nothing", input(regAB, nil, out("id-A", "10.0.1.1:40001", "10.0.4.4:5432")),
			nil, nil, lim, []string{"skip 10.0.4.4:5432 not_allowed"}},
		{"host glob on the address", input(regAB, nil, out("id-A", "10.0.1.1:40001", "10.0.4.4:5432")),
			[]string{"10.0.4.*"}, nil, lim, []string{"deploy 10.0.4.4:5432 hop 1"}},
		{"hop limit 1 stops at the first ring", input(regAB, nil, out("id-B", "10.0.2.2:40001", "10.0.4.4:5432")),
			nil, ten, lim, []string{"skip 10.0.4.4:5432 hop_limit"}},
		{"hop limit 2 takes the second ring", input(regAB, nil, out("id-B", "10.0.2.2:40001", "10.0.4.4:5432")),
			nil, ten, Limits{MaxHops: 2, MaxNodes: 16}, []string{"deploy 10.0.4.4:5432 hop 2"}},
		{"the nearest revealer sets the hop", input(regAB, nil, out("id-B", "10.0.2.2:40001", "10.0.4.4:5432"),
			out("id-A", "10.0.1.1:40001", "10.0.4.4:5432")), nil, ten, lim, []string{"deploy 10.0.4.4:5432 hop 1"}},
		{"hops zero lists only", input(regAB, nil, out("id-A", "10.0.1.1:40001", "10.0.4.4:5432")), nil, ten,
			Limits{MaxHops: 0, MaxNodes: 16}, []string{"skip 10.0.4.4:5432 hop_limit"}},
		{"cycle: a registered address is enrolled",
			input(registry("ops@10.0.1.1", "hostB"), nil, out("id-B", "10.0.2.2:40001", "10.0.1.1:9090")),
			[]string{"*"}, nil, lim, []string{"skip 10.0.1.1:9090 enrolled"}},
		{"cycle: a traced host's own address is enrolled",
			input(regAB, nil, out("id-B", "10.0.2.2:40001", "10.0.1.1:9090"),
				out("id-A", "10.0.1.1:40002", "203.0.113.9:443")),
			[]string{"*"}, nil, lim, []string{"deploy 203.0.113.9:443 hop 1", "skip 10.0.1.1:9090 enrolled"}},
		{"NAT address of a traced server", input(regAB, nat, out("id-A", "10.0.1.1:40001", "10.9.9.9:80")),
			nil, ten, lim, []string{"skip 10.9.9.9:80 nat"}},
		{"ambiguous is never a target", input(regAB, nil, out("id-A", "10.0.1.1:40001", "10.0.4.4:5432",
			federate.ReasonAmbiguous)), nil, ten, lim, []string{"skip 10.0.4.4:5432 unjoined"}},
		{"no_key is never a target", input(regAB, nil, out("id-A", "10.0.1.1:40001", "10.0.4.4:5432",
			federate.ReasonNoKey)), nil, ten, lim, []string{"skip 10.0.4.4:5432 unjoined"}},
		{"no_role and key_conflict are never targets", input(regAB, nil,
			out("id-A", "10.0.1.1:40001", "10.0.4.4:5432", federate.ReasonNoRole),
			out("id-A", "10.0.1.1:40002", "10.0.4.4:5433", federate.ReasonKeyConflict)), nil, ten, lim,
			[]string{"skip 10.0.4.4:5432 unjoined", "skip 10.0.4.4:5433 unjoined"}},
		{"an inbound client is no listener", input(regAB, nil, federate.Unresolved{Reason: federate.ReasonNoMatch,
			Half: half("id-A", federate.RoleServer, "10.0.1.1:8080", "10.0.4.4:51234")}), nil, ten, lim,
			[]string{"skip 10.0.4.4:51234 inbound"}},
		{"loopback, link-local and multicast are local", input(regAB, nil,
			out("id-A", "127.0.0.1:40001", "127.0.0.1:5432"), out("id-A", "10.0.1.1:40002", "169.254.169.254:80"),
			out("id-A", "[fe80::1]:40003", "[fe80::2]:80"), out("id-A", "10.0.1.1:40004", "224.0.0.1:5353")),
			[]string{"*"}, []string{"::/0"}, lim, []string{"skip 127.0.0.1:5432 local",
				"skip 169.254.169.254:80 local", "skip 224.0.0.1:5353 local", "skip [fe80::2]:80 local"}},
		{"IPv6 waits for an alias, v4-mapped is v4", input(regAB, nil,
			out("id-A", "[2001:db8::1]:40001", "[2001:db8::5]:443"),
			out("id-A", "[::ffff:10.0.1.1]:40002", "[::ffff:10.0.4.4]:5432")),
			nil, []string{"10.0.0.0/8", "2001:db8::/32"}, lim,
			[]string{"deploy 10.0.4.4:5432 hop 1", "skip [2001:db8::5]:443 ipv6"}},
		{"an unregistered host reveals nothing", input(regAB, nil, out("id-C", "10.0.3.3:40001", "10.0.4.4:5432")),
			nil, ten, lim, []string{"skip 10.0.4.4:5432 via_unregistered"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := summary(Plan(c.in, allowlist(t, c.hosts, c.cidrs...), c.lim))
			if !slices.Equal(got, c.want) {
				t.Errorf("Plan:\n got %q\nwant %q", got, c.want)
			}
		})
	}
}

// TestPlanNodeCap: with two hosts registered and a cap of three, one new
// host fits — the busiest — and a second port on it costs no node.
func TestPlanNodeCap(t *testing.T) {
	in := input(registry("hostA", "hostB"), nil,
		out("id-A", "10.0.1.1:40001", "10.0.4.4:5432"),
		out("id-A", "10.0.1.1:40002", "10.0.5.5:80"),
		out("id-A", "10.0.1.1:40003", "10.0.5.5:80"),
		out("id-A", "10.0.1.1:40004", "10.0.5.5:443"))
	got := summary(Plan(in, allowlist(t, nil, "10.0.0.0/8"), Limits{MaxHops: 1, MaxNodes: 3}))
	want := []string{"deploy 10.0.5.5:80 hop 1", "deploy 10.0.5.5:443 hop 1", "skip 10.0.4.4:5432 node_cap"}
	if !slices.Equal(got, want) {
		t.Errorf("Plan:\n got %q\nwant %q", got, want)
	}
}

// TestPlanEvidence: a candidate names its allow rule and the apps that
// reached it with their counts; a skip names its evidence.
func TestPlanEvidence(t *testing.T) {
	in := input(registry("hostA", "hostB"), nil,
		out("id-A", "10.0.1.1:40001", "10.0.4.4:5432"), out("id-A", "10.0.1.1:40002", "10.0.4.4:5432"),
		out("id-B", "10.0.2.2:40001", "10.0.4.4:5432"), out("id-B", "10.0.2.2:40002", "10.0.1.1:9090"))
	p := Plan(in, allowlist(t, nil, "10.0.0.0/8"), Limits{MaxHops: 1, MaxNodes: 16})
	if len(p.Deploy) != 1 || len(p.Skipped) != 1 {
		t.Fatalf("Plan = %+v", p)
	}
	c := p.Deploy[0]
	wantVia := []Via{{Host: "hostA", App: "app:shop/gateway", Conns: 2}, {Host: "hostB", App: "java", Conns: 1}}
	if c.Conns != 3 || c.Hop != 1 || c.Rule != "cidr 10.0.0.0/8" || !slices.Equal(c.Via, wantVia) {
		t.Errorf("candidate = %+v", c)
	}
	if s := p.Skipped[0]; s.Reason != ReasonEnrolled || s.Evidence != "local address of traced host hostA" {
		t.Errorf("skip = %+v", s)
	}
	if p.Nodes != 2 {
		t.Errorf("Nodes = %d, want 2", p.Nodes)
	}
}

// TestPlanDeterministic: the order the merge lists halves in does not
// change a byte of the plan.
func TestPlanDeterministic(t *testing.T) {
	us := []federate.Unresolved{
		out("id-A", "10.0.1.1:40001", "10.0.4.4:5432"), out("id-B", "10.0.2.2:40001", "10.0.4.4:5432"),
		out("id-A", "10.0.1.1:40002", "203.0.113.9:443"), out("id-B", "10.0.2.2:40002", "10.0.1.1:9090"),
		out("id-A", "10.0.1.1:40003", "10.0.6.6:80", federate.ReasonAmbiguous),
		out("id-A", "10.0.1.1:40004", "10.0.5.5:80"), out("id-A", "10.0.1.1:40005", "10.0.7.7:80"),
	}
	allow := allowlist(t, []string{"203.0.113.*"}, "10.0.0.0/8")
	lim := Limits{MaxHops: 1, MaxNodes: 4}
	first := ""
	for i := range len(us) {
		rot := append(slices.Clone(us[i:]), us[:i]...)
		data, err := json.Marshal(Plan(input(registry("hostA", "hostB"), nil, rot...), allow, lim))
		if err != nil {
			t.Fatal(err)
		}
		if first == "" {
			first = string(data)
		} else if string(data) != first {
			t.Fatalf("rotation %d planned differently:\n%s\n%s", i, data, first)
		}
	}
	if !strings.Contains(first, `"reason":"node_cap"`) {
		t.Errorf("want a node_cap tie broken deterministically: %s", first)
	}
}
