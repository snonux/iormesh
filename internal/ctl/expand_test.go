package ctl

import (
	"encoding/json"
	"strings"
	"testing"

	"iormesh/internal/expand"
	"iormesh/internal/federate"
)

// saveRegistry writes the fixture's registry: app1 and app2 enrolled,
// with the expand: section x.
func (f *buildFixture) saveRegistry(t *testing.T, x *federate.Expand) {
	t.Helper()
	path, err := f.e.registry()
	if err != nil {
		t.Fatal(err)
	}
	var r federate.Registry
	for _, h := range []string{"app1", "app2"} {
		if err := r.Add(h, federate.Project{Name: "shop", Target: federate.Target{Pattern: "*java*"}}); err != nil {
			t.Fatal(err)
		}
	}
	r.Expand = x
	if err := r.Save(path); err != nil {
		t.Fatal(err)
	}
}

// TestExpandListing: the shop mesh's untraced database is listed as a
// candidate once the allowlist covers it, and as not_allowed before; the
// fused app1 → app2 connection proposes nothing. Expand touches no
// server: the fixture has no transport.
func TestExpandListing(t *testing.T) {
	f := newBuildFixture(t)
	f.saveRegistry(t, nil)
	got := f.mustRun(t, "expand")
	if !strings.Contains(got, "203.0.113.9:5432") || !strings.Contains(got, "not_allowed") ||
		!strings.Contains(got, "0 to deploy") {
		t.Errorf("without an allowlist:\n%s", got)
	}
	f.saveRegistry(t, &federate.Expand{Allow: federate.Allow{CIDRs: []string{"203.0.113.0/24"}}})
	out := f.mustRun(t, "expand", "-json")
	var p expand.Proposal
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	if len(p.Deploy) != 1 || len(p.Skipped) != 0 {
		t.Fatalf("plan = %s", out)
	}
	c := p.Deploy[0]
	if c.Addr.String() != "203.0.113.9" || c.Port != 5432 || c.Hop != 1 || c.Rule != "cidr 203.0.113.0/24" ||
		len(c.Via) != 1 || c.Via[0].Host != "app1" {
		t.Errorf("candidate = %+v", c)
	}
	if again := f.mustRun(t, "expand", "-json"); again != out {
		t.Errorf("a second plan differs:\n%s\n%s", again, out)
	}
	if code := f.run("expand", "-hops", "-1"); code != exitUsage {
		t.Errorf("-hops -1: exit %d, want %d", code, exitUsage)
	}
}

// TestExpandRefusesBadRegistry: an invalid expand: section stops expand
// before the store is read.
func TestExpandRefusesBadRegistry(t *testing.T) {
	f := newBuildFixture(t)
	path, err := f.e.registry()
	if err != nil {
		t.Fatal(err)
	}
	f.saveRegistry(t, nil)
	if err := writeFileAtomic(path, []byte("servers: []\nexpand: {allow: {cidrs: [10.0.0.7/8]}}\n")); err != nil {
		t.Fatal(err)
	}
	if code := f.run("expand"); code != exitError || !strings.Contains(f.stderr.String(), "host bits") {
		t.Errorf("exit %d, stderr %s", code, f.stderr)
	}
}
