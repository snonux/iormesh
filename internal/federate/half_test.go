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
			col := newCollector()
			for i := range tr.events {
				col.add(&tr.events[i])
			}
			hs := col.halves()
			if len(hs) != 1 {
				t.Fatalf("got %d halves, want 1: %+v", len(hs), hs)
			}
			c.check(t, hs[0])
		})
	}
}

// TestHalvesSkipNonTCP: a conn_id without a tcp_state record — a unix
// stream, a pipe, a UDP flow — is no TCP half and the merge leaves it.
func TestHalvesSkipNonTCP(t *testing.T) {
	tr := newTrace("h", "p")
	tr.event(1, schema.KindConnect, 3, 0, wall0, "unix", schema.Args{Path: ptr("/run/x.sock")})
	tr.event(1, schema.KindWrite, 3, 1, wall0, "unix", schema.Args{})
	tr.event(1, schema.KindWrite, 4, 1, wall0, "", schema.Args{})
	col := newCollector()
	for i := range tr.events {
		col.add(&tr.events[i])
	}
	if hs := col.halves(); len(hs) != 0 {
		t.Fatalf("got halves %+v, want none", hs)
	}
}
