package expand

import (
	"bytes"
	"net/netip"
	"strings"
	"testing"

	"iormesh/internal/federate"
)

func TestAllowlistMatch(t *testing.T) {
	a := allowlist(t, []string{"10.20.3.*", "192.168.1.?"}, "10.0.0.0/16", "fd00::/8")
	cases := []struct{ addr, want string }{
		{"10.0.9.9", "cidr 10.0.0.0/16"},
		{"::ffff:10.0.9.9", "cidr 10.0.0.0/16"}, // v4-mapped is the v4 address
		{"10.20.3.7", "host 10.20.3.*"},
		{"192.168.1.5", "host 192.168.1.?"},
		{"192.168.1.50", ""},
		{"fd12::1", "cidr fd00::/8"},
		{"10.1.0.1", ""},
	}
	for _, c := range cases {
		if got := a.Match(netip.MustParseAddr(c.addr)); got != c.want {
			t.Errorf("Match(%s) = %q, want %q", c.addr, got, c.want)
		}
	}
	none, err := NewAllowlist(nil)
	if err != nil || none.Match(netip.MustParseAddr("10.0.9.9")) != "" {
		t.Errorf("a nil section must allow nothing: %v", err)
	}
	if _, err := NewAllowlist(&federate.Expand{Allow: federate.Allow{CIDRs: []string{"10.0.0.1/8"}}}); err == nil {
		t.Error("a non-canonical CIDR must be refused")
	}
}

func TestNewLimits(t *testing.T) {
	cases := []struct {
		x    *federate.Expand
		hops int
		want Limits
	}{
		{nil, -1, Limits{DefaultMaxHops, DefaultMaxNodes}},
		{nil, 0, Limits{0, DefaultMaxNodes}},
		{&federate.Expand{MaxNodes: 5}, 3, Limits{3, 5}},
		{&federate.Expand{}, 2, Limits{2, DefaultMaxNodes}},
	}
	for _, c := range cases {
		if got := NewLimits(c.x, c.hops); got != c.want {
			t.Errorf("NewLimits(%+v, %d) = %+v, want %+v", c.x, c.hops, got, c.want)
		}
	}
}

// TestWriteText: the listing names candidates, skips, evidence and the
// apps; a comm with an escape sequence prints quoted, never raw.
func TestWriteText(t *testing.T) {
	in := input(registry("hostA", "hostB"), nil,
		out("id-A", "10.0.1.1:40001", "10.0.4.4:5432"), out("id-B", "10.0.2.2:40001", "203.0.113.9:443"))
	in.Merge.Graph.Nodes[1].Attrs["comm"] = "evil\x1b[2J"
	p := Plan(in, allowlist(t, nil, "10.0.0.0/8"), Limits{MaxHops: 1, MaxNodes: 16})
	var b bytes.Buffer
	if err := WriteText(&b, p); err != nil {
		t.Fatal(err)
	}
	want := `expand: 1 to deploy, 1 skipped (hop limit 1, 2 of 16 nodes registered)

deploy
  10.0.4.4:5432         hop 1  1 conn  cidr 10.0.0.0/8
      via hostA app:shop/gateway (1 conn)

skipped
  203.0.113.9:443       not_allowed      1 conn  no allow rule matches
      via hostB "evil\x1b[2J" (1 conn)
`
	if b.String() != want {
		t.Errorf("WriteText:\n%s\nwant:\n%s", b.String(), want)
	}
	if strings.Contains(b.String(), "\x1b") {
		t.Error("an escape sequence reached the output")
	}
}
