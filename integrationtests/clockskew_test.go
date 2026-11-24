package integrationtests

import (
	"context"
	"testing"
	"time"

	"iormesh/internal/federate"
	"iormesh/internal/schema"
)

// The injected clock skew of docs/milestones.md's M4 acceptance (task ye4):
// the rig's emulated hosts share one kernel clock, so each host's tracer is
// told to stamp wall_ns that far off (Host.ClockSkew, the tracer's
// test-only IORMESH_TEST_CLOCK_SKEW). The merge's fusion window and clock
// bounds work on wall_ns; boot_ns stays the machine's one boot clock, so
// it is the ground truth the merge's claims are checked against.
const (
	// skewClient and skewServer put the hosts' clocks 1.5 s apart, both
	// off, in opposite directions — inside the fusion window
	// (federate.DefaultWindow, 2 min).
	skewClient = -250 * time.Millisecond
	skewServer = 1250 * time.Millisecond
	// skewBeyond puts the server's clock past the fusion window.
	skewBeyond = 3 * time.Minute
	// skewSlack is how far the offset the two tracers actually applied
	// may differ from the injected one: each tracer measures wall − boot
	// once at start (correlate.BootToWallOffset), two clock reads apart.
	skewSlack = time.Millisecond
)

// skewPair is tcp-two-host on rig with the given skews: one exchange
// each way, so two-sided bounds need the tracer's tcp_seq (task xe4).
func skewPair(rig meshRig, client, server time.Duration) meshPair {
	return meshPair{scenario: "tcp-two-host", serverNetns: rig.server, bind: rigServerAddr,
		dial: rigServerAddr, dialPort: rigPort, clientSkew: client, serverSkew: server}
}

// TestClockSkewRecovered: two tracers skewed 1.5 s apart still fuse
// their connection, the merge's clock bound for the pair contains the
// offset the tracers applied — two-sided, and excluding zero, so the
// merge knows the clocks differ — and Timeline.Order never contradicts
// the true (boot clock) order of any two cross-host instants, though the
// raw wall stamps do.
func TestClockSkewRecovered(t *testing.T) {
	enableParallelIfRequested(t)
	h := newHarness(t)
	rig := newMeshRig(t)
	m := traceMesh(t, h, rig, skewPair(rig, skewClient, skewServer))
	requireSeqFields(t, m)
	applied := appliedOffset(t, m, skewServer-skewClient)
	res, _ := mergeMesh(t, m)
	c := assertOneFusedConn(t, res)
	assertBoundsContain(t, res.Clocks, c.Client.Host, c.Server.Host, applied)
	assertOrderSound(t, federate.NewTimeline(res.Clocks), m)
}

// TestClockSkewBeyondWindow: a server clock 3 min off is beyond the
// fusion window, so the merge fuses nothing — both ends stay no_match —
// and, with no fused connection, proves no clock bound at all.
func TestClockSkewBeyondWindow(t *testing.T) {
	enableParallelIfRequested(t)
	h := newHarness(t)
	rig := newMeshRig(t)
	m := traceMesh(t, h, rig, skewPair(rig, 0, skewBeyond))
	requireSeqFields(t, m)
	appliedOffset(t, m, skewBeyond)
	res, err := federate.Merge(context.Background(), []federate.Source{
		federate.NDJSONDir{Dir: m.client.Dir}, federate.NDJSONDir{Dir: m.server.Dir}},
		federate.Options{})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if len(res.Conns) != 0 {
		t.Errorf("merge fused %d connections across a %v skew, want none", len(res.Conns), skewBeyond)
	}
	if len(res.Unresolved) != 2 {
		t.Errorf("unresolved %v, want both ends", res.Unresolved)
	}
	for _, u := range res.Unresolved {
		if u.Reason != federate.ReasonNoMatch {
			t.Errorf("%s end on %s: %s, want %s", u.Half.Role, u.Half.Host, u.Reason, federate.ReasonNoMatch)
		}
	}
	if len(res.Clocks) != 0 {
		t.Errorf("clock bounds %+v without a fused connection, want none", res.Clocks)
	}
}

// appliedOffset returns clock(server) − clock(client) as the two tracers
// actually stamped it, ns: the difference of their wall − boot offsets
// (wallOffset). It fails unless that is the injected want within
// skewSlack — the knob reached both tracers.
func appliedOffset(t *testing.T, m meshRun, want time.Duration) int64 {
	t.Helper()
	got := wallOffset(t, m.server) - wallOffset(t, m.client)
	if d := time.Duration(got) - want; d > skewSlack || d < -skewSlack {
		t.Fatalf("tracers stamped clocks %v apart, want %v (±%v): the skew was not applied",
			time.Duration(got), want, skewSlack)
	}
	return got
}

// wallOffset is wall_ns − boot_ns of a run's events. Every wall_ns a
// tracer stamps derives from one offset, the skew included, so all
// events must agree; one that does not would make boot_ns no ground
// truth for its wall_ns.
func wallOffset(t *testing.T, run tracedRun) int64 {
	t.Helper()
	if len(run.Trace.Events) == 0 {
		t.Fatalf("%s: no events", run.Dir)
	}
	off := eventOffset(run.Trace.Events[0])
	for _, e := range run.Trace.Events {
		if eventOffset(e) != off {
			t.Fatalf("%s: %s event #%d has wall − boot %d, others %d", run.Dir, e.Event, e.Seq,
				eventOffset(e), off)
		}
	}
	return off
}

func eventOffset(e schema.Event) int64 { return int64(e.WallNS) - int64(e.BootNS) }

// assertBoundsContain: the pair has bounds, each two-sided and not empty,
// and each's Interval contains the applied offset (clock(server) −
// clock(client), flipped when the server's host id sorts first, since a
// bound's Offset is clock(HostB) − clock(HostA)) and excludes zero.
func assertBoundsContain(t *testing.T, bounds []federate.ClockBound, client, server string, applied int64) {
	t.Helper()
	want := applied
	if server < client {
		want = -applied
	}
	n := 0
	for _, b := range bounds {
		if b.HostA != min(client, server) || b.HostB != max(client, server) {
			continue
		}
		n++
		lo, hi := b.Interval()
		width, ok := b.Width()
		t.Logf("bound [%d, %d] ns (width %d, drift %d, %d messages), applied offset %d",
			lo, hi, width, b.Drift, b.Messages, want)
		switch {
		case !ok:
			t.Errorf("bound %+v is one-sided or empty, want two-sided (tcp_seq, task xe4)", b)
		case want < lo || want > hi:
			t.Errorf("bound [%d, %d] misses the applied offset %d", lo, hi, want)
		case lo <= 0 && hi >= 0:
			t.Errorf("bound [%d, %d] contains 0: the skew was not recovered", lo, hi)
		}
	}
	if n == 0 {
		t.Fatalf("no clock bound for the host pair in %+v", bounds)
	}
}

// instant is one event's start or end on its host's wall clock, with the
// same instant on the machine's boot clock: the truth.
type instant struct {
	stamp federate.Stamp
	boot  uint64
}

// instants are the start and end of every event of run.
func instants(run tracedRun) []instant {
	var out []instant
	for _, e := range run.Trace.Events {
		out = append(out,
			instant{federate.Stamp{Host: e.HostID, Wall: e.WallNS}, e.BootNS},
			instant{federate.Stamp{Host: e.HostID, Wall: e.WallNS + e.LatencyNS}, e.BootNS + e.LatencyNS})
	}
	return out
}

// assertOrderSound relates every client instant to every server instant:
// an order the Timeline claims must be the true one, it must claim some
// (or the check is vacuous), and the raw wall stamps must get some wrong
// (or the skew tested nothing).
func assertOrderSound(t *testing.T, tl federate.Timeline, m meshRun) {
	t.Helper()
	var ordered, rawWrong, wrong int
	for _, x := range instants(m.client) {
		for _, y := range instants(m.server) {
			if (x.stamp.Wall < y.stamp.Wall) != (x.boot < y.boot) && x.boot != y.boot {
				rawWrong++
			}
			r := tl.Order(x.stamp, y.stamp)
			if r == federate.Unordered {
				continue
			}
			ordered++
			if truth := (x.boot < y.boot); (r == federate.Before) != truth || x.boot == y.boot {
				wrong++
				if wrong <= 5 {
					t.Errorf("Order(%v, %v) = %v, but boot clock says %d vs %d", x.stamp, y.stamp, r,
						x.boot, y.boot)
				}
			}
		}
	}
	t.Logf("cross-host instant pairs: %d ordered, %d wrong; raw wall order wrong for %d",
		ordered, wrong, rawWrong)
	if wrong > 0 {
		t.Errorf("%d cross-host orders contradict the true order", wrong)
	}
	if ordered == 0 || rawWrong == 0 {
		t.Errorf("vacuous: %d pairs ordered, %d misordered by raw wall stamps", ordered, rawWrong)
	}
}
