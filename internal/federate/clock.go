package federate

import (
	"cmp"
	"slices"
	"sort"

	"iormesh/internal/schema"
)

// wrap4G is 2^32: TCP sequence numbers, and so stream offsets, repeat
// after this many bytes.
const wrap4G = uint64(1) << 32

// message is one causal edge between two hosts' clocks: something that
// left host from at send (from's wall clock, ns) and was observed on host
// to at recv (to's wall clock). Whatever the true times were, the send
// came first, so o_to − o_from ≤ recv − send for the two clocks' offsets
// (spike S2, experiment 3).
type message struct {
	from, to   string
	send, recv uint64
	conn       string // the merged conn id: the bound's witness
}

// earliest is the minimum of the instants seen, if any.
type earliest struct {
	ns uint64
	ok bool
}

// streamRec is one data record of a half that carried args.tcp_seq: the
// call moved bytes [seq, seq+n) mod 2^32 between enter and exit.
type streamRec struct {
	seq         uint32
	n           uint64
	enter, exit uint64 // wall ns
}

// sendRange is a streamRec's send as a stream offset range, where offset
// = seq − (iss + 1) is the same number on both hosts.
type sendRange struct{ start, n, enter uint64 }

// timing is what one half's records say about when things happened on
// its host's clock: the instants around the SYN, and every data record
// with a sequence range.
type timing struct {
	connect  earliest // client: the earliest connect enter
	synSent  earliest // client: the SYN_SENT transition
	afterSYN earliest // server: the earliest accept exit or tcp_state
	sends    []streamRec
	recvs    []streamRec
	// sent and received sum the data records' bytes; maxCount is the
	// largest tcp_sock byte counter any tcp_state record carried. Any of
	// them at 4 GiB or more means offsets may repeat (streamMessages).
	sent, received, maxCount uint64
}

func (e *earliest) see(ns uint64) {
	if !e.ok || ns < e.ns {
		e.ns, e.ok = ns, true
	}
}

// add folds one event's instants in. Only a successful accept counts: a
// failed one names no connection that saw a SYN.
func (t *timing) add(ev *schema.Event) {
	switch {
	case ev.Event == schema.KindConnect:
		t.connect.see(ev.WallNS)
	case (ev.Event == schema.KindAccept || ev.Event == schema.KindAccept4) && ev.Ret >= 0:
		t.afterSYN.see(ev.WallNS + ev.LatencyNS)
	case ev.Event == schema.KindTCPState:
		t.state(ev)
	case ev.Args.TCPSeq != nil:
		t.data(ev)
	}
}

// state reads a tcp_state record. On an accepting end every record of the
// child socket follows the client's SYN — the child exists only once the
// SYN arrived — so its earliest is a receive-side instant of the SYN.
func (t *timing) state(ev *schema.Event) {
	t.afterSYN.see(ev.WallNS)
	if ev.Args.SockState != nil && *ev.Args.SockState == "SYN_SENT" {
		t.synSent.see(ev.WallNS)
	}
	if ev.Args.BytesAcked != nil && ev.Args.BytesReceived != nil {
		t.maxCount = max(t.maxCount, *ev.Args.BytesAcked, *ev.Args.BytesReceived)
	}
}

// data keeps one data record with its sequence range: the send's enter
// (the bytes cannot leave before the call started) and the receive's
// exit (they had arrived when it returned).
func (t *timing) data(ev *schema.Event) {
	r := streamRec{seq: *ev.Args.TCPSeq, n: ev.Bytes,
		enter: ev.WallNS, exit: ev.WallNS + ev.LatencyNS}
	switch {
	case schema.IsStreamSend(ev.Event):
		t.sends = append(t.sends, r)
		t.sent += r.n
	case schema.IsStreamReceive(ev.Event):
		t.recvs = append(t.recvs, r)
		t.received += r.n
	}
}

// messages returns every causal edge one fused connection gives: the
// handshake (client to server only) and each matched data message in
// both directions — data messages only when neither stream may have
// passed 4 GiB and neither host's trace dropped records (a drop makes the
// byte sums that rule out a lap undercount). A connection both of whose ends one host traced (two
// sessions over loopback) gives edges too; the bounds skip them, since a
// host's clock needs no offset to itself.
func (c *collector) messages(conn Conn) []message {
	cl := c.acc[LocalConn{conn.Client.Host, conn.Client.ConnID}]
	sv := c.acc[LocalConn{conn.Server.Host, conn.Server.ConnID}]
	if cl == nil || sv == nil {
		return nil
	}
	var out []message
	if m, ok := handshake(conn, &cl.timing, &sv.timing); ok {
		out = append(out, m)
	}
	if long(&cl.timing) || long(&sv.timing) ||
		c.lossy[conn.Client.Host] || c.lossy[conn.Server.Host] {
		return out // offsets may repeat: matching bytes would be a guess
	}
	out = append(out, streamMessages(conn.Client.Host, conn.Server.Host, conn.ID,
		cl.timing.sends, sv.timing.recvs, conn.Key.Client)...)
	out = append(out, streamMessages(conn.Server.Host, conn.Client.Host, conn.ID,
		sv.timing.sends, cl.timing.recvs, conn.Key.Server)...)
	return out
}

// handshake is the SYN as a message: the client's connect entry and its
// SYN_SENT transition both come before the SYN leaves, and the server's
// accept exit and the child socket's every state record after it
// arrives. The later client instant and the earlier server instant give
// the tightest of these always-true pairs. It bounds the offset in one
// direction only: no server record precedes the SYN-ACK.
func handshake(conn Conn, cl, sv *timing) (message, bool) {
	if !sv.afterSYN.ok || (!cl.connect.ok && !cl.synSent.ok) {
		return message{}, false
	}
	send := max(cl.connect.ns, cl.synSent.ns) // a missing one is 0
	return message{from: conn.Client.Host, to: conn.Server.Host,
		send: send, recv: sv.afterSYN.ns, conn: conn.ID}, true
}

// long reports a stream that may have passed 4 GiB in either direction:
// its offsets repeat, so one offset could name bytes of two different
// sends. Such a connection gives no data messages rather than a guess.
func long(t *timing) bool {
	return t.sent >= wrap4G || t.received >= wrap4G || t.maxCount >= wrap4G
}

// streamMessages pairs each receive with the send that queued the
// receive's last byte (S2: matched by stream offset, which NAT leaves
// alone). A receive whose byte no traced send holds — the send was
// lost, or predates the session — gives nothing.
func streamMessages(from, to, conn string, sends, recvs []streamRec, iss uint32) []message {
	idx, ok := sendIndex(sends, iss)
	if !ok {
		return nil
	}
	var out []message
	for _, r := range recvs {
		last := uint64(r.seq-iss-1) + r.n - 1
		i := sort.Search(len(idx), func(i int) bool { return idx[i].start > last }) - 1
		if i < 0 || idx[i].start+idx[i].n <= last {
			continue
		}
		out = append(out, message{from: from, to: to, send: idx[i].enter,
			recv: r.exit, conn: conn})
	}
	return out
}

// sendIndex returns the sends as offset ranges sorted by offset, an
// exact duplicate (two sessions on one host reporting one call) once.
// Ranges that overlap otherwise, or one that crosses 4 GiB, make the
// offset name no single send: ok is false and the direction gives no
// messages, never a guessed pair.
func sendIndex(sends []streamRec, iss uint32) ([]sendRange, bool) {
	idx := make([]sendRange, 0, len(sends))
	for _, s := range sends {
		idx = append(idx, sendRange{start: uint64(s.seq - iss - 1), n: s.n, enter: s.enter})
	}
	slices.SortFunc(idx, func(a, b sendRange) int {
		return cmp.Or(cmp.Compare(a.start, b.start), cmp.Compare(a.n, b.n),
			cmp.Compare(a.enter, b.enter))
	})
	idx = slices.Compact(idx)
	for i, s := range idx {
		if s.start+s.n > wrap4G || (i > 0 && idx[i-1].start+idx[i-1].n > s.start) {
			return nil, false
		}
	}
	return idx, true
}
