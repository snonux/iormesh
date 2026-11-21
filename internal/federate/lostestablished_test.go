package federate

import (
	"slices"
	"testing"

	"iormesh/internal/schema"
)

// A client half whose SYN_SENT -> ESTABLISHED record a skipped
// inet_sock_set_state run lost (task qf4: M4-9's gateway->orders hop on
// Rocky 9.9) still fuses when a later record — its CLOSE, after a FIN
// close or an RST abort — rebuilds the key; a record that holds no peer
// ISN, belongs to another connection, or rebuilds a foreign key never
// makes a match.

// lostEstClient records a connecting end whose ESTABLISHED record was
// lost: SYN_SENT and the connect at wall, then only the given later
// records (state and fields, at wall+offset).
func (t *trace) lostEstClient(pid uint32, conn string, wall uint64, later ...lateRec) {
	t.socket(pid, conn)
	a := tuple("10.0.0.1:5000", "10.0.0.9:80")
	syn := a
	syn.SockState = ptr("SYN_SENT")
	t.event(pid, schema.KindTCPState, 7, 0, wall, conn, syn)
	t.event(pid, schema.KindConnect, 7, -115, wall, conn, a)
	for _, r := range later {
		t.event(pid, schema.KindTCPState, 7, 0, wall+r.after, conn, withSeq(a, r.state, r.f))
	}
}

// lateRec is one state record after the lost ESTABLISHED.
type lateRec struct {
	state string
	after uint64
	f     seqFields
}

// Counters at the client's CLOSE after a 20-byte request and a 300-byte
// response, on top of the client's SYN (acked) and the server's: closed
// by FIN both ways (each FIN counted like a byte), or by an RST with the
// last 100 response bytes and no FIN received, and 4 request bytes still
// unacknowledged (snd_una short of write_seq).
func finClose(k ISNPair) seqFields {
	return seqFields{SndUna: k.Client + 1 + 20 + 1, RcvNxt: k.Server + 1 + 300 + 1,
		BytesAcked: 1 + 20 + 1, BytesReceived: 300 + 1}
}

func rstClose(k ISNPair) seqFields {
	return seqFields{SndUna: k.Client + 1 + 16, RcvNxt: k.Server + 1 + 200,
		BytesAcked: 1 + 16, BytesReceived: 200}
}

// TestLostEstablishedFuses: the client's CLOSE keys the half after a FIN
// close and after an RST abort, and the half fuses with the server even
// when the CLOSE comes minutes after the handshake, past the fusion
// window: the half counts as established at its SYN_SENT record.
func TestLostEstablishedFuses(t *testing.T) {
	k := ISNPair{0xfffffff0, 0x1234} // the client's ISN wraps within the request
	late := 5 * 60 * sec
	cases := []struct {
		name string
		recs []lateRec
	}{
		{"FIN close, only CLOSE survives", []lateRec{{"CLOSE", late, finClose(k)}}},
		{"RST abort, snd_una short of write_seq", []lateRec{{"CLOSE", late, rstClose(k)}}},
		{"FIN close, FIN_WAIT1 and CLOSE agree", []lateRec{
			{"FIN_WAIT1", late - sec, seqFields{SndUna: k.Client + 21, RcvNxt: k.Server + 301,
				BytesAcked: 21, BytesReceived: 300}},
			{"CLOSE", late, finClose(k)}}},
		{"RST abort, then the socket's next SYN_SENT", []lateRec{
			{"CLOSE", late, rstClose(k)}, {"SYN_SENT", late + sec, seqFields{}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, b := newTrace("gw", "shop"), newTrace("orders", "shop")
			a.lostEstClient(1, "c1", wall0, c.recs...)
			b.server(2, "s1", "10.0.1.5:8080", "10.0.1.1:41000", k, wall0)
			hs := Halves(slices.Concat(a.events, b.events))
			if len(hs) != 2 || !hs[0].HasKey || hs[0].Key != k || hs[0].KeyConflict {
				t.Fatalf("halves %+v, want the client keyed %v", hs, k)
			}
			if hs[0].Established != wall0 {
				t.Errorf("client established at wall0%+d ns, want its SYN_SENT at wall0",
					int64(hs[0].Established-wall0))
			}
			got := outcome(Fuse(hs, DefaultWindow))
			if !slices.Equal(got, []string{"c1<>s1"}) {
				t.Fatalf("fusion %v, want c1<>s1", got)
			}
		})
	}
}

// TestLostEstablishedNoFalseMatch: what must not fuse. An unanswered
// connect's CLOSE (bytes_acked 0, as an older trace carrying its fields
// would have it) gives no key. A CLOSE rebuilding another pair — a
// foreign or corrupt record — stays no_match beside the real server. A
// stale binding — the socket's next connection reported under the same
// conn_id after its CLOSE was lost — rebuilds a second key, and the
// KeyConflict stops the half from fusing even though the server has the
// first one.
func TestLostEstablishedNoFalseMatch(t *testing.T) {
	k, other := ISNPair{100, 200}, ISNPair{5000, 6000}
	late := 30 * sec
	cases := []struct {
		name string
		recs []lateRec
		want []string
	}{
		{"unanswered connect's CLOSE", []lateRec{
			{"CLOSE", sec, seqFields{SndUna: k.Client, RcvNxt: 0}}},
			[]string{"c1=no_key", "s1=no_match"}},
		{"CLOSE of a foreign pair", []lateRec{{"CLOSE", late, rstClose(other)}},
			[]string{"c1=no_match", "s1=no_match"}},
		{"stale binding: the next connection's records", []lateRec{
			{"CLOSE", late, finClose(k)},
			{"ESTABLISHED", late + sec, seqFields{SndUna: other.Client + 1,
				RcvNxt: other.Server + 1, BytesAcked: 1}}},
			[]string{"c1=key_conflict", "s1=no_match"}},
		{"stale binding: the next connection's CLOSE alone", []lateRec{
			{"CLOSE", late, finClose(k)}, {"CLOSE", late + sec, rstClose(other)}},
			[]string{"c1=key_conflict", "s1=no_match"}},
		// Two sessions may report one socket's records in either order;
		// read in wall-clock order, a CLOSE with no SYN acked after the
		// keyed ESTABLISHED is no record of this connection.
		{"zero-acked CLOSE read before the earlier ESTABLISHED", []lateRec{
			{"CLOSE", 2 * sec, seqFields{SndUna: k.Client, RcvNxt: 0}},
			{"ESTABLISHED", sec, seqFields{SndUna: k.Client + 1, RcvNxt: k.Server + 1,
				BytesAcked: 1}}},
			[]string{"c1=key_conflict", "s1=no_match"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, b := newTrace("gw", "shop"), newTrace("orders", "shop")
			a.lostEstClient(1, "c1", wall0, c.recs...)
			b.server(2, "s1", "10.0.1.5:8080", "10.0.1.1:41000", k, wall0)
			got := outcome(Fuse(Halves(slices.Concat(a.events, b.events)), DefaultWindow))
			if !slices.Equal(got, c.want) {
				t.Fatalf("fusion %v, want %v", got, c.want)
			}
		})
	}
}

// TestClientEstablishedKeepsEstablished: with its ESTABLISHED record in
// the trace a client half is established there, not at SYN_SENT.
func TestClientEstablishedKeepsEstablished(t *testing.T) {
	tr := newTrace("gw", "shop")
	tr.client(1, "c1", "10.0.0.1:5000", "10.0.0.9:80", ISNPair{100, 200}, wall0)
	hs := Halves(tr.events)
	if len(hs) != 1 || hs[0].Established != wall0+1000 {
		t.Fatalf("halves %+v, want established at the ESTABLISHED record", hs)
	}
}
