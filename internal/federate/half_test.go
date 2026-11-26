package federate

import (
	"testing"

	"iormesh/internal/schema"
)

// TestHalvesFromEvents covers how the collector reads a host's records:
// role from connect/accept/SYN_SENT, the key only from records with the
// handshake complete, and every keyed record agreeing.
func TestHalvesFromEvents(t *testing.T) {
	const peer, local = "10.0.0.2:80", "10.0.0.1:5000"
	k := ISNPair{100, 200}
	clientEst := seqFields{SndUna: 101, RcvNxt: 201, BytesAcked: 1}
	cases := []struct {
		name  string
		build func(tr *trace)
		check func(t *testing.T, h Half)
	}{
		{"refused connect: CLOSE before ESTABLISHED gives no key", func(tr *trace) {
			tr.event(1, schema.KindTCPState, 3, 0, wall0, "x",
				withSeq(tuple(local, peer), "SYN_SENT", seqFields{SndUna: 100}))
			tr.event(1, schema.KindTCPState, 3, 0, wall0, "x",
				withSeq(tuple(local, peer), "CLOSE", seqFields{SndUna: 100}))
		}, func(t *testing.T, h Half) {
			if h.Role != RoleClient || h.HasKey {
				t.Fatalf("got %+v, want a keyless client", h)
			}
		}},
		{"child reset in the accept queue: keyed from its CLOSE snapshot", func(tr *trace) {
			tr.event(1, schema.KindTCPState, 3, 0, wall0, "x", withSeq(tuple(local, peer),
				"CLOSE", seqFields{SndUna: 201, RcvNxt: 101 + 5, BytesReceived: 5}))
			tr.event(1, schema.KindAccept4, 3, 3, wall0, "x", tuple(local, peer))
		}, func(t *testing.T, h Half) {
			if h.Role != RoleServer || !h.HasKey || h.Key != k {
				t.Fatalf("got %+v, want server keyed %v", h, k)
			}
		}},
		{"records disagree: key conflict", func(tr *trace) {
			tr.event(1, schema.KindConnect, 3, 0, wall0, "x", tuple(local, peer))
			tr.event(1, schema.KindTCPState, 3, 0, wall0, "x",
				withSeq(tuple(local, peer), "ESTABLISHED", clientEst))
			tr.event(1, schema.KindTCPState, 3, 0, wall0, "x",
				withSeq(tuple(local, peer), "FIN_WAIT1", seqFields{SndUna: 999, RcvNxt: 201}))
		}, func(t *testing.T, h Half) {
			if !h.KeyConflict {
				t.Fatalf("got %+v, want a key conflict", h)
			}
		}},
		{"torn accept snapshot: the state changes key the server, no conflict", func(tr *trace) {
			tr.event(1, schema.KindTCPState, 3, 0, wall0, "x", withSeq(tuple(local, peer),
				"ESTABLISHED", seqFields{SndUna: 201, RcvNxt: 101 + 9, BytesReceived: 5}))
			tr.event(1, schema.KindAccept4, 3, 3, wall0+1, "x", tuple(local, peer))
			tr.event(1, schema.KindTCPState, 3, 0, wall0+20, "x", withSeq(tuple(local, peer),
				"CLOSE_WAIT", seqFields{SndUna: 201, RcvNxt: 101 + 6, BytesReceived: 6}))
		}, func(t *testing.T, h Half) {
			if h.Role != RoleServer || !h.HasKey || h.Key != k || h.KeyConflict || h.Established != wall0 {
				t.Fatalf("got %+v, want server keyed %v by CLOSE_WAIT, established at the snapshot", h, k)
			}
		}},
		{"two state changes of a server disagree: key conflict", func(tr *trace) {
			tr.event(1, schema.KindTCPState, 3, 0, wall0, "x", withSeq(tuple(local, peer),
				"ESTABLISHED", seqFields{SndUna: 201, RcvNxt: 101}))
			tr.event(1, schema.KindAccept4, 3, 3, wall0+1, "x", tuple(local, peer))
			tr.event(1, schema.KindTCPState, 3, 0, wall0+20, "x", withSeq(tuple(local, peer),
				"CLOSE_WAIT", seqFields{SndUna: 201, RcvNxt: 101 + 6, BytesReceived: 6}))
			tr.event(1, schema.KindTCPState, 3, 0, wall0+30, "x", withSeq(tuple(local, peer),
				"LAST_ACK", seqFields{SndUna: 999, RcvNxt: 101 + 6, BytesReceived: 6}))
		}, func(t *testing.T, h Half) {
			if !h.KeyConflict {
				t.Fatalf("got %+v, want a key conflict", h)
			}
		}},
		{"no connect or accept record: no role", func(tr *trace) {
			tr.event(1, schema.KindTCPState, 3, 0, wall0, "x",
				withSeq(tuple(local, peer), "ESTABLISHED", clientEst))
		}, func(t *testing.T, h Half) {
			if h.Role != "" || h.HasKey {
				t.Fatalf("got %+v, want no role and no key", h)
			}
		}},
		{"Established is the earliest keyed record, not the first read", func(tr *trace) {
			tr.event(1, schema.KindConnect, 3, 0, wall0+50, "x", tuple(local, peer))
			tr.event(1, schema.KindTCPState, 3, 0, wall0+40, "x",
				withSeq(tuple(local, peer), "FIN_WAIT1", clientEst))
			tr.event(1, schema.KindTCPState, 3, 0, wall0+30, "x",
				withSeq(tuple(local, peer), "ESTABLISHED", clientEst))
		}, func(t *testing.T, h Half) {
			if h.Established != wall0+30 || h.KeyConflict {
				t.Fatalf("got %+v, want established at wall0+30", h)
			}
		}},
		// Sockets open before the session (decision #35): the initial
		// snapshot names the role and keys the half when nothing locked
		// does; Established is then only an upper bound (Before).
		{"initial client snapshot: client, keyed, open before", func(tr *trace) {
			tr.event(1, schema.KindTCPState, 3, 0, wall0, "x", initialSnap(
				withSeq(tuple(local, peer), "ESTABLISHED", clientEst), schema.SockSnapshotInitialClient))
			tr.event(1, schema.KindWrite, 3, 0, wall0+5, "x", schema.Args{})
		}, func(t *testing.T, h Half) {
			if h.Role != RoleClient || !h.HasKey || h.Key != k || !h.Before || h.Established != wall0 {
				t.Fatalf("got %+v, want a client keyed %v, open before, established by wall0", h, k)
			}
		}},
		{"initial server snapshot disagreeing with a state change: no conflict", func(tr *trace) {
			tr.event(1, schema.KindTCPState, 3, 0, wall0, "x", initialSnap(withSeq(tuple(local, peer),
				"ESTABLISHED", seqFields{SndUna: 999, RcvNxt: 101}), schema.SockSnapshotInitialServer))
			tr.event(1, schema.KindTCPState, 3, 0, wall0+20, "x", withSeq(tuple(local, peer),
				"CLOSE_WAIT", seqFields{SndUna: 201, RcvNxt: 101 + 6, BytesReceived: 6}))
		}, func(t *testing.T, h Half) {
			if h.Role != RoleServer || !h.HasKey || h.Key != k || h.KeyConflict || !h.Before ||
				h.Established != wall0 {
				t.Fatalf("got %+v, want a server keyed %v by CLOSE_WAIT, open before by wall0", h, k)
			}
		}},
		{"forked child shares the socket: both processes listed", func(tr *trace) {
			tr.event(1, schema.KindConnect, 3, 0, wall0, "x", tuple(local, peer))
			tr.event(1, schema.KindTCPState, 3, 0, wall0+5, "x",
				withSeq(tuple(local, peer), "ESTABLISHED", clientEst))
			tr.event(2, schema.KindWrite, 3, 10, wall0+9, "x", schema.Args{})
		}, func(t *testing.T, h Half) {
			if len(h.Procs) != 2 || h.Key != k || h.Established != wall0+5 {
				t.Fatalf("got %+v, want two procs, key %v, established at the keyed record", h, k)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := newTrace("h", "p")
			c.build(tr)
			hs := Halves(tr.events)
			if len(hs) != 1 {
				t.Fatalf("got %d halves, want 1: %+v", len(hs), hs)
			}
			c.check(t, hs[0])
		})
	}
}

// initialSnap marks a tcp_state record's args as an initial snapshot.
func initialSnap(a schema.Args, mark string) schema.Args {
	a.SockSnapshot = &mark
	return a
}

// TestHalvesSkipNonTCP: a conn_id without a tcp_state record — a unix
// stream, a pipe, a UDP flow — is no TCP half and the merge leaves it.
func TestHalvesSkipNonTCP(t *testing.T) {
	tr := newTrace("h", "p")
	tr.event(1, schema.KindConnect, 3, 0, wall0, "unix", schema.Args{Path: ptr("/run/x.sock")})
	tr.event(1, schema.KindWrite, 3, 1, wall0, "unix", schema.Args{})
	tr.event(1, schema.KindWrite, 4, 1, wall0, "", schema.Args{})
	if hs := Halves(tr.events); len(hs) != 0 {
		t.Fatalf("got halves %+v, want none", hs)
	}
}
