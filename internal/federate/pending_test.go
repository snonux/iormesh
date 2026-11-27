package federate

import (
	"context"
	"math"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"iormesh/internal/correlate"
	"iormesh/internal/schema"
	"iormesh/internal/segment"
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

// TestSettleSlackCoversHold: the pending slack exceeds how long a
// session's records may wait before they reach a segment — the
// correlator's fuse window plus the Java join's http timeout (task cn4,
// decision #43). A grown window must grow the slack too, or tail
// connections turn mixed again.
func TestSettleSlackCoversHold(t *testing.T) {
	hold := correlate.DefaultFuseWindowNS + correlate.DefaultJavaTimeoutNS
	if uint64(settleSlack) <= uint64(hold) {
		t.Fatalf("settleSlack %v does not exceed the correlator's hold %v (fuse window + http timeout)",
			settleSlack, time.Duration(hold))
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

// reasons merges srcs and returns each unresolved half's reason by its
// conn id.
func reasons(t *testing.T, srcs []Source) map[string]string {
	t.Helper()
	r, err := Merge(context.Background(), srcs, Options{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, u := range r.Unresolved {
		out[u.Half.ConnID] = u.Reason
	}
	return out
}

// openAll writes every trace as an open session's segment.
func openAll(t *testing.T, traces ...*trace) []Source {
	dir := t.TempDir()
	srcs := make([]Source, 0, len(traces))
	for _, tr := range traces {
		srcs = append(srcs, open(t, tr, dir))
	}
	return srcs
}

// The limits pending's three guards draw, for TestMergePendingBoundaries.
const (
	slackNS    = uint64(settleSlack)
	intervalNS = uint64(segment.DefaultInterval)
	// forgedAbove is the newest stamp the guard still takes for a tail.
	forgedAbove = math.MaxUint64 - slackNS - intervalNS
)

// TestMergePendingBoundaries pins pending's three limits at the
// nanosecond (task rp4): the far session's coverage test (its records
// end before the half plus settleSlack), the tail test (the half lies
// within one segment interval plus settleSlack of the build's newest
// record) and the forged-stamp guard (a stamp too near MaxUint64 to add
// the two to is never a tail). Each limit has a case just inside and one
// just outside, so dropping a slack or bending the guard fails a case.
// o2 is established at t2 (its ESTABLISHED record, pendingMesh);
// inventory's records otherwise end 2 µs after wall0, orders' 8 µs
// after t2.
func TestMergePendingBoundaries(t *testing.T) {
	t2 := wall0 + 30*sec + 1000
	// farEnds stretches inventory's coverage to at; newest makes at the
	// build's newest record (orders is o2's own session, so its coverage
	// is never judged); stamp adds client "of" established at at.
	farEnds := func(at uint64) func(o, inv *trace) string {
		return func(_, inv *trace) string { inv.drop(0, "", at); return "o2" }
	}
	newest := func(at uint64) func(o, inv *trace) string {
		return func(o, _ *trace) string { o.drop(0, "", at); return "o2" }
	}
	stamp := func(at uint64) func(o, inv *trace) string {
		return func(o, _ *trace) string {
			o.client(100, "of", "127.0.0.1:47950", "127.0.0.1:18215", ISNPair{0x700, 0x800}, at-1000)
			return "of"
		}
	}
	cases := map[string]struct {
		prep func(o, inv *trace) string // returns the conn id to judge
		want string
	}{
		"far records end exactly at the half":            {farEnds(t2), ReasonPending},
		"far records end 1 ns short of half plus slack":  {farEnds(t2 + slackNS - 1), ReasonPending},
		"far records end exactly at half plus slack":     {farEnds(t2 + slackNS), ReasonNoMatch},
		"half exactly interval plus slack before newest": {newest(t2 + slackNS + intervalNS), ReasonPending},
		"half 1 ns older than interval plus slack":       {newest(t2 + slackNS + intervalNS + 1), ReasonNoMatch},
		"stamp at the last instant that is no forgery":   {stamp(forgedAbove), ReasonPending},
		"stamp 1 ns past it is forged":                   {stamp(forgedAbove + 1), ReasonNoMatch},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			orders, inventory := pendingMesh()
			conn := c.prep(orders, inventory)
			if got := reasons(t, openAll(t, orders, inventory))[conn]; got != c.want {
				t.Fatalf("%s: %q, want %s", conn, got, c.want)
			}
		})
	}
}

// TestMergePendingOnlyNoMatch: settle relabels no_match halves only. A
// tail half unresolved for another reason — no role, no key, two
// partners — is a fact about the traces however recent, so it keeps its
// reason and stays counted, while o2 beside it turns pending (task rp4).
func TestMergePendingOnlyNoMatch(t *testing.T) {
	orders, inventory := pendingMesh()
	at := wall0 + 30*sec
	const srv = "127.0.0.1:18215"
	// o4: a connect nobody answered (SYN_SENT only), so no keyed record.
	o4 := tuple("127.0.0.1:47940", srv)
	o4syn := o4
	o4syn.SockState = ptr("SYN_SENT")
	orders.socket(100, "o4")
	orders.event(100, schema.KindTCPState, 7, 0, at, "o4", o4syn)
	orders.event(100, schema.KindConnect, 7, -115, at, "o4", o4)
	// o6: a server end whose accept the trace never saw.
	orders.socket(100, "o6")
	orders.event(100, schema.KindTCPState, 9, 0, at, "o6", withSeq(tuple(srv, "127.0.0.1:47960"),
		"CLOSE_WAIT", seqFields{SndUna: 0x901, RcvNxt: 0xa16, BytesReceived: 21}))
	// o5: a client whose key two servers on another host share.
	k5 := ISNPair{0x500, 0x600}
	orders.client(100, "o5", "127.0.0.1:47970", srv, k5, at)
	edge := newTrace("iom3", "edge")
	edge.server(300, "e1", srv, "127.0.0.1:47970", k5, at)
	edge.server(300, "e2", srv, "127.0.0.1:47971", k5, at)
	got := reasons(t, openAll(t, orders, inventory, edge))
	want := map[string]string{"o2": ReasonPending, "o4": ReasonNoKey, "o5": ReasonAmbiguous,
		"o6": ReasonNoRole, "e1": ReasonAmbiguous, "e2": ReasonAmbiguous}
	for conn, w := range want {
		if got[conn] != w {
			t.Errorf("%s: %q, want %s", conn, got[conn], w)
		}
	}
}

// TestMergePendingAliasOwnsSession: two sessions on one host trace one
// process (task ei4), so its socket folds into one half that keeps the
// smaller conn id and lists the other as an alias. The session that
// reported only the alias recorded the half too: it is no far-end
// candidate, and with no other running session the half is no_match,
// not pending (task rp4).
func TestMergePendingAliasOwnsSession(t *testing.T) {
	const local, srv = "127.0.0.1:47910", "127.0.0.1:18215"
	k := ISNPair{0x300, 0x400}
	a, b := newTrace("iom2", "orders"), newTrace("iom2", "orders-dup")
	a.client(100, "o2a", local, srv, k, wall0)
	b.client(100, "o2b", local, srv, k, wall0)
	got := reasons(t, openAll(t, a, b))
	if len(got) != 1 || got["o2a"] != ReasonNoMatch {
		t.Fatalf("want only o2a no_match (o2b its alias), got %v", got)
	}
}
