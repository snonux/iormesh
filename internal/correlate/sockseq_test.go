package correlate

import (
	"testing"

	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// TestCarriesSeq pins which tcp_state records pass their tcp_sock fields
// on (M4, decision #26): ESTABLISHED and every later state, and a CLOSE
// only when it ends an established connection — never the opening
// transitions, whose fields hold no peer ISN yet — nor a torn snapshot.
func TestCarriesSeq(t *testing.T) {
	const snap = types.TCP_STATE_SNAPSHOT
	cases := []struct {
		name     string
		old, new int32
		want     bool
	}{
		{"connect opens: CLOSE -> SYN_SENT", tcpClose, tcpSynSent, false},
		{"handshake done: SYN_SENT -> ESTABLISHED", tcpSynSent, tcpEstablished, true},
		{"active close: ESTABLISHED -> FIN_WAIT1", tcpEstablished, 4, true},
		{"passive close: ESTABLISHED -> CLOSE_WAIT", tcpEstablished, 8, true},
		{"LAST_ACK -> CLOSE ends an established connection", 9, tcpClose, true},
		{"FIN_WAIT2 -> CLOSE (the TIME_WAIT path)", 5, tcpClose, true},
		{"refused connect: SYN_SENT -> CLOSE", tcpSynSent, tcpClose, false},
		{"accept snapshot at ESTABLISHED", snap, tcpEstablished, true},
		{"accept snapshot at CLOSE_WAIT", snap, 8, true},
		{"accept snapshot of a child reset in the queue", snap, tcpClose, true},
		{"accept snapshot still at SYN_RECV", snap, tcpSynRecv, false},
		{"listener: LISTEN -> CLOSE", tcpListen, tcpClose, false},
		{"handshake aborted: SYN_RECV -> CLOSE", tcpSynRecv, tcpClose, false},
		{"NEW_SYN_RECV", snap, tcpNewSynRecv, false},
	}
	for _, tc := range cases {
		r := &types.TcpStateEvent{Oldstate: tc.old, Newstate: tc.new}
		if got := carriesSeq(r); got != tc.want {
			t.Errorf("%s: carriesSeq = %v, want %v", tc.name, got, tc.want)
		}
	}
	torn := &types.TcpStateEvent{Oldstate: snap, Newstate: tcpEstablished,
		SeqStatus: types.TCP_SEQ_TORN}
	if carriesSeq(torn) {
		t.Error("a torn accept snapshot carries its tcp_sock fields")
	}
}

// TestTCPStateSeqArgs: the correlator copies the four fields into the
// event's args at ESTABLISHED and leaves all four out at SYN_SENT, and
// both events pass schema validation (all four or none).
func TestTCPStateSeqArgs(t *testing.T) {
	c, sink := newTest(t)
	now := uint64(100)
	client := feeder{t, c, 100, 101, &now}
	client.handle(&types.FdEvent{EventType: types.ENTER_FD_EVENT, TraceId: types.SYS_ENTER_CONNECT,
		Time: client.tick(), Pid: 100, Tid: 101, Fd: 6})
	client.handle(tcp(client.tick(), 0xA, 100, 6, tcpClose, synSent, "127.0.0.1:0", srv))
	est := tcp(client.tick(), 0xA, 100, 6, synSent, tcpEstablished, cli, srv)
	est.SndUna, est.RcvNxt, est.BytesAcked, est.BytesReceived = 0x1001, 0xF0000001, 1, 0
	client.handle(est)
	client.exit(types.SYS_EXIT_CONNECT, 0)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	states := byKind(sink.events, schema.KindTCPState)
	if len(states) != 2 {
		t.Fatalf("%d tcp_state events, want 2", len(states))
	}
	if a := states[0].Args; a.SndUna != nil || a.RcvNxt != nil || a.BytesAcked != nil || a.BytesReceived != nil {
		t.Errorf("SYN_SENT event carries tcp_sock fields: %+v", a)
	}
	a := states[1].Args
	if a.SndUna == nil || *a.SndUna != 0x1001 || *a.RcvNxt != 0xF0000001 ||
		*a.BytesAcked != 1 || *a.BytesReceived != 0 {
		t.Fatalf("ESTABLISHED event args %+v, want the record's four fields", a)
	}
	for _, e := range states {
		if err := e.Validate(); err != nil {
			t.Errorf("%s event invalid: %v", *e.Args.SockState, err)
		}
	}
}

// TestFinalSnapshotEvent: the tracer's final snapshot of an open socket
// (task sf4) becomes a tcp_state event marked sock_snapshot "final" with
// the socket's conn, fd instance and the snapshot's counters, and changes
// nothing about the socket; one of a socket the trace never saw, one whose
// tuple belongs to another life at the address and one at SYN_SENT (no
// counters to give) are dropped.
func TestFinalSnapshotEvent(t *testing.T) {
	c, sink := newTest(t)
	now := uint64(100)
	client := feeder{t, c, 100, 101, &now}
	client.handle(&types.FdEvent{EventType: types.ENTER_FD_EVENT, TraceId: types.SYS_ENTER_CONNECT,
		Time: client.tick(), Pid: 100, Tid: 101, Fd: 6})
	client.handle(tcp(client.tick(), 0xA, 100, 6, tcpClose, synSent, "127.0.0.1:0", srv))
	est := tcp(client.tick(), 0xA, 100, 6, synSent, tcpEstablished, cli, srv)
	est.SndUna, est.RcvNxt, est.BytesAcked, est.BytesReceived = 0x1001, 0xF0000001, 1, 0
	client.handle(est)
	client.exit(types.SYS_EXIT_CONNECT, 0)
	final := tcp(client.tick(), 0xA, 100, 6, types.TCP_STATE_FINAL, tcpEstablished, cli, srv)
	final.SndUna, final.RcvNxt, final.BytesAcked, final.BytesReceived = 0x1101, 0xF0000041, 0x101, 0x40
	client.handle(final)
	unknown := tcp(client.tick(), 0xB, 100, 7, types.TCP_STATE_FINAL, tcpEstablished, cli, srv)
	other := tcp(client.tick(), 0xA, 100, 6, types.TCP_STATE_FINAL, tcpEstablished, "127.0.0.1:1", srv)
	pending := tcp(client.tick(), 0xA, 100, 6, types.TCP_STATE_FINAL, synSent, cli, srv)
	for _, r := range []*types.TcpStateEvent{unknown, other, pending} {
		r.BytesAcked = 1
		client.handle(r)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	states := byKind(sink.events, schema.KindTCPState)
	if len(states) != 3 {
		t.Fatalf("%d tcp_state events, want SYN_SENT, ESTABLISHED and one final", len(states))
	}
	e, est2 := states[2], states[1]
	a := e.Args
	if a.SockSnapshot == nil || *a.SockSnapshot != schema.SockSnapshotFinal || *a.SockState != "ESTABLISHED" {
		t.Fatalf("final event args %+v", a)
	}
	if *a.BytesAcked != 0x101 || *a.BytesReceived != 0x40 || *a.SndUna != 0x1101 {
		t.Errorf("final event counters %+v, want the snapshot's", a)
	}
	if e.ConnID == "" || e.ConnID != est2.ConnID || e.FD != 6 || e.FDGen != est2.FDGen {
		t.Errorf("final event conn %q fd %d/%d, want the socket's %q fd 6/%d",
			e.ConnID, e.FD, e.FDGen, est2.ConnID, est2.FDGen)
	}
	if est2.Args.SockSnapshot != nil {
		t.Error("a state change is marked as a snapshot")
	}
	for _, ev := range states {
		if err := ev.Validate(); err != nil {
			t.Errorf("%s event invalid: %v", *ev.Args.SockState, err)
		}
	}
}
