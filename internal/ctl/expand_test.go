package ctl

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
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

// TestExpandWritesNothing: expand reads the host pins but never records
// them — a listing must not change what a later build trusts.
func TestExpandWritesNothing(t *testing.T) {
	f := newBuildFixture(t)
	f.saveRegistry(t, nil)
	f.mustRun(t, "expand")
	pins := filepath.Join(filepath.Dir(f.db), hostPinsFileName)
	if _, err := os.Stat(pins); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expand wrote %s: %v", pins, err)
	}
}

// TestExpandBadSegment: a damaged segment is named on stderr and makes
// expand exit 1 after the plan is printed, since its host's addresses are
// missing from the visited set.
func TestExpandBadSegment(t *testing.T) {
	f := newBuildFixture(t)
	f.saveRegistry(t, &federate.Expand{Allow: federate.Allow{CIDRs: []string{"203.0.113.0/24"}}})
	bad := f.app2Files[len(f.app2Files)-1]
	data, err := os.ReadFile(bad)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, data[:len(data)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	code := f.run("expand")
	if code != exitError || !strings.Contains(f.stderr.String(), "bad segment billing/app2/") ||
		!strings.Contains(f.stdout.String(), "203.0.113.9:5432") {
		t.Errorf("exit %d\nstdout %s\nstderr %s", code, f.stdout, f.stderr)
	}
}

// TestExpandProjectNarrowsProposals: -project billing proposes nothing
// (only shop's app1 dialed the database), while the merge, and so the
// visited set, still spans every project.
func TestExpandProjectNarrowsProposals(t *testing.T) {
	f := newBuildFixture(t)
	f.saveRegistry(t, &federate.Expand{Allow: federate.Allow{CIDRs: []string{"203.0.113.0/24"}}})
	if out := f.mustRun(t, "expand", "-project", "billing"); !strings.Contains(out, "0 to deploy, 0 skipped") {
		t.Errorf("-project billing:\n%s", out)
	}
	if out := f.mustRun(t, "expand", "-project", "shop"); !strings.Contains(out, "1 to deploy") {
		t.Errorf("-project shop:\n%s", out)
	}
	if code := f.run("expand", "-project", "-bad"); code != exitUsage {
		t.Errorf("-project -bad: exit %d, want %d", code, exitUsage)
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
