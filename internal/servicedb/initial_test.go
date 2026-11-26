package servicedb

import (
	"net/netip"
	"testing"

	"iormesh/internal/schema"
)

// TestInitialSnapshotLocalPair (decision #35, task cm4): a same-host
// connection both of whose ends were open before the session has no
// connect and no accept, only the two initial snapshots; their roles
// stand in, so it is observed as a fused local pair, client to the
// server's port. Without them (a plain state change) there is no pair.
func TestInitialSnapshotLocalPair(t *testing.T) {
	state := func(pid uint32, local, remote, mark string) schema.Event {
		l, r := netip.MustParseAddrPort(local), netip.MustParseAddrPort(remote)
		la, ra := l.Addr().String(), r.Addr().String()
		lp, rp := l.Port(), r.Port()
		ev := schema.Event{HostID: "h", Project: "shop", PID: pid, PIDInc: 1, ConnID: "c1",
			Event: schema.KindTCPState, WallNS: wall0 + uint64(pid),
			Args: schema.Args{LocalAddr: &la, LocalPort: &lp, RemoteAddr: &ra, RemotePort: &rp}}
		if mark != "" {
			ev.Args.SockSnapshot = &mark
		}
		return ev
	}
	const cli, srv = "127.0.0.1:40000", "127.0.0.1:8080"
	for _, c := range []struct {
		name  string
		marks [2]string
		pairs int
	}{
		{"initial snapshots", [2]string{schema.SockSnapshotInitialClient, schema.SockSnapshotInitialServer}, 1},
		{"state changes only", [2]string{"", ""}, 0},
	} {
		name := c.name
		tr := newTraffic()
		tr.source = "h"
		for _, ev := range []schema.Event{state(1, cli, srv, c.marks[0]), state(2, srv, cli, c.marks[1])} {
			if err := tr.add(&ev); err != nil {
				t.Fatal(err)
			}
		}
		got := builder{t: tr}.localPairs()
		if len(got) != c.pairs {
			t.Fatalf("%s: %d local pairs, want %d: %+v", name, len(got), c.pairs, got)
		}
		if c.pairs == 1 && (got[0].Endpoint != netip.MustParseAddrPort(srv) || got[0].State != schema.StateFused) {
			t.Errorf("%s: pair %+v, want fused, endpoint %s", name, got[0], srv)
		}
	}
}
