package integrationtests

// M4 (decision #26, task ve4) end to end on one host: the tracer's
// tcp_state records carry the four tcp_sock fields, and internal/federate
// rebuilds the same ISN pair from either end of a real connection. A
// loopback connection is fused by the host's own correlator, so the merge
// never sees it as two halves; the check splits the trace into two
// pretend hosts — every record of the connecting socket on one, of the
// accepted socket on the other — and requires the merge's own Halves and
// Fuse to pair them by key alone. The same split checks the clock bounds
// (task xe4): the data records' tcp_seq pair each receive with its send,
// and the two pretend hosts, one clock in truth, must come out bounded
// from both sides around offset 0.

import (
	"strings"
	"testing"

	"iormesh/internal/federate"
	"iormesh/internal/schema"
)

// endKey names one socket of a trace: its owner's fd instance.
type endKey struct {
	pid   uint32
	fd    int32
	fdGen uint32
}

// assertISNKeysMatch splits events into a client host and a server host
// and requires every TCP connection to come back fused by key: two keyed
// halves of opposite roles, nothing unresolved.
func assertISNKeysMatch(t *testing.T, events []schema.Event) {
	t.Helper()
	split, conns := splitByEnd(events)
	if conns == 0 {
		t.Fatal("no TCP connection with a connect and an accept in the trace")
	}
	halves := federate.Halves(split)
	for _, h := range halves {
		if !h.HasKey || h.KeyConflict {
			t.Errorf("half %s/%s (%s): has_key %v conflict %v; want one key from the tcp_sock fields",
				h.Host, h.ConnID, h.Role, h.HasKey, h.KeyConflict)
		}
	}
	f := federate.Fuse(halves, federate.DefaultWindow)
	for _, u := range f.Unresolved {
		t.Errorf("half %s/%s (%s) unresolved: %s", u.Half.Host, u.Half.ConnID, u.Half.Role, u.Reason)
	}
	if len(f.Conns) != conns {
		t.Fatalf("fused %d connections by ISN pair, want %d", len(f.Conns), conns)
	}
	for _, c := range f.Conns {
		if c.Client.ConnID != c.Server.ConnID {
			t.Errorf("key %s fused conn %s with conn %s: two connections, one key",
				c.Key, c.Client.ConnID, c.Server.ConnID)
		}
	}
}

// splitByEnd returns the TCP events of connections the trace saw both
// ends of, each on host "<host>/client" or "<host>/server" by the socket
// it concerns, and how many such connections there are. A socket is the
// fd instance a connect or an accept named; tcp_state records carry the
// same owner.
func splitByEnd(events []schema.Event) ([]schema.Event, int) {
	role := map[endKey]string{}
	sides := map[string]map[string]bool{}
	for _, e := range events {
		r := endRole(e.Event)
		if r == "" || e.ConnID == "" {
			continue
		}
		role[endKey{e.PID, e.FD, e.FDGen}] = r
		if sides[e.ConnID] == nil {
			sides[e.ConnID] = map[string]bool{}
		}
		sides[e.ConnID][r] = true
	}
	var out []schema.Event
	conns := 0
	for _, s := range sides {
		if s[federate.RoleClient] && s[federate.RoleServer] {
			conns++
		}
	}
	for _, e := range events {
		r, ok := role[endKey{e.PID, e.FD, e.FDGen}]
		if !ok || !sides[e.ConnID][federate.RoleClient] || !sides[e.ConnID][federate.RoleServer] {
			continue
		}
		e.HostID += "/" + r
		out = append(out, e)
	}
	return out, conns
}

// endRole is the role an event's kind proves for its socket, or "".
func endRole(kind string) string {
	switch kind {
	case schema.KindConnect:
		return federate.RoleClient
	case schema.KindAccept, schema.KindAccept4:
		return federate.RoleServer
	}
	return ""
}

// assertClockBounds splits events like assertISNKeysMatch and requires the
// merge's clock bounds between the two pretend hosts to be two-sided — a
// lower bound needs a server→client message, which only the data records'
// tcp_seq give (the handshake bounds one side) — and, the two being one
// clock, to hold offset 0 in the raw interval, no drift allowance needed.
func assertClockBounds(t *testing.T, events []schema.Event) {
	t.Helper()
	split, _ := splitByEnd(events)
	seqs := 0
	for _, e := range split {
		if e.Args.TCPSeq != nil {
			seqs++
		}
	}
	bounds := federate.ClockBounds(split, federate.Options{})
	if len(bounds) == 0 {
		t.Fatalf("no clock bound between the pretend hosts (%d records with tcp_seq)", seqs)
	}
	for _, b := range bounds {
		if !strings.HasSuffix(b.HostA, "/client") || !strings.HasSuffix(b.HostB, "/server") {
			t.Errorf("bound between %s and %s, want client and server", b.HostA, b.HostB)
		}
		w, ok := b.Width()
		if !b.HasLower || !b.HasUpper || !ok {
			t.Errorf("bound %s→%s one-sided or empty (lower %v %d, upper %v %d; %d messages, %d tcp_seq records)",
				b.HostA, b.HostB, b.HasLower, b.Lower, b.HasUpper, b.Upper, b.Messages, seqs)
			continue
		}
		if b.Lower > 0 || b.Upper < 0 {
			t.Errorf("one clock bounded to [%d, %d] ns, which misses 0", b.Lower, b.Upper)
		}
		t.Logf("clock bound [%d, %d] ns from %d messages, width %d ns with drift", b.Lower, b.Upper,
			b.Messages, w)
	}
}
