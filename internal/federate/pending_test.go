package federate

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"iormesh/internal/schema"
)

// openSegment stands in for a collected Segment of a session that
// recorded no end yet: an NDJSON trace that names its session, the way
// Segment.Session does, so the merge treats it as a session that may
// still publish segments the collect has not pulled (pending.go).
type openSegment struct {
	NDJSONDir
	session string
}

func (s openSegment) Session() string { return s.session }

// pendingMesh is the M8 trial's tail (task cn4): orders and inventory,
// two projects on one host, each a running session. Their first
// connection fuses; the second one orders made after inventory's last
// collected segment closed — inventory's next segment, which holds the
// accept, is still on the host. The returned traces let a case add
// what it needs before they are written.
func pendingMesh() (orders, inventory *trace) {
	const srv = "127.0.0.1:18215"
	orders, inventory = newTrace("iom2", "orders"), newTrace("iom2", "inventory")
	k1, k2 := ISNPair{0x100, 0x200}, ISNPair{0x300, 0x400}
	orders.client(100, "o1", "127.0.0.1:47904", srv, k1, wall0)
	inventory.server(200, "i1", srv, "127.0.0.1:47904", k1, wall0)
	orders.client(100, "o2", "127.0.0.1:47910", srv, k2, wall0+30*sec)
	return orders, inventory
}

// mergeOne merges the traces and returns the reason the connection o2
// stayed unfused, failing when it fused or is missing.
func mergeOne(t *testing.T, srcs []Source) string {
	t.Helper()
	r, err := Merge(context.Background(), srcs, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(r.Conns, func(c Conn) bool { return c.Client.ConnID == "o1" }) {
		t.Fatalf("want o1 fused:\n%s", report(t, r))
	}
	for _, u := range r.Unresolved {
		if u.Half.ConnID == "o2" {
			return u.Reason
		}
	}
	t.Fatalf("o2 not unresolved:\n%s", report(t, r))
	return ""
}

// open writes tr as an open session's segment under dir.
func open(t *testing.T, tr *trace, dir string) Source {
	return openSegment{tr.write(t, filepath.Join(dir, tr.project)), tr.project + "/" + tr.host + "/s1"}
}

// TestMergePendingTail: a half made in the build's last minute whose far
// end may sit in a running session's uncollected segment is pending,
// not no_match — the trial's mixed edges (task cn4). Every case where
// no later segment can bring the far end stays no_match.
func TestMergePendingTail(t *testing.T) {
	cases := map[string]struct {
		prep func(orders, inventory *trace) []*trace // extra traces, if any
		open bool                                    // the sources are open segments
		want string
	}{
		"far session's collected trace ends before the half": {open: true, want: ReasonPending},
		"far session recorded its end": {open: true, want: ReasonNoMatch,
			prep: func(_, inv *trace) []*trace { inv.end(schema.EndTargetsExited, wall0+sec); return nil }},
		"complete traces (no segments)": {want: ReasonNoMatch},
		"far session traced well past the half": {open: true, want: ReasonNoMatch,
			prep: func(o, inv *trace) []*trace {
				k := ISNPair{0x500, 0x600}
				o.client(100, "o3", "127.0.0.1:47920", "127.0.0.1:18215", k, wall0+60*sec)
				inv.server(200, "i3", "127.0.0.1:18215", "127.0.0.1:47920", k, wall0+60*sec)
				return nil
			}},
		"half older than the build's last minute": {open: true, want: ReasonNoMatch,
			prep: func(_, _ *trace) []*trace {
				gw := newTrace("iom2", "gateway")
				gw.client(300, "g1", "127.0.0.1:47930", "203.0.113.9:443", ISNPair{1, 2}, wall0+5*60*sec)
				return []*trace{gw}
			}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			orders, inventory := pendingMesh()
			traces := []*trace{orders, inventory}
			if c.prep != nil {
				traces = append(traces, c.prep(orders, inventory)...)
			}
			dir := t.TempDir()
			var srcs []Source
			for _, tr := range traces {
				if c.open {
					srcs = append(srcs, open(t, tr, dir))
				} else {
					srcs = append(srcs, tr.write(t, filepath.Join(dir, tr.project)))
				}
			}
			if got := mergeOne(t, srcs); got != c.want {
				t.Fatalf("o2: %s, want %s", got, c.want)
			}
		})
	}
}

// TestMergePendingAlone: a running session built alone has no other
// session that could still bring the far end, so its one-sided
// connection is no_match however recent (M4's "client project alone
// stays unresolved").
func TestMergePendingAlone(t *testing.T) {
	orders := newTrace("iom2", "orders")
	orders.client(100, "o2", "127.0.0.1:47910", "127.0.0.1:18215", ISNPair{0x300, 0x400}, wall0)
	r, err := Merge(context.Background(), []Source{open(t, orders, t.TempDir())}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Unresolved) != 1 || r.Unresolved[0].Reason != ReasonNoMatch {
		t.Fatalf("want o2 no_match:\n%s", report(t, r))
	}
}
