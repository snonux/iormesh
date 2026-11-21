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
	// bootEnter and bootExit are the same instants on the host's boot
	// clock, which all of the host's sessions share (two sessions derive
	// wall ns from slightly different offsets): the counter proofs order
	// them against tcp_state records (clockproof.go).
	bootEnter, bootExit uint64
}

// sendRange is a streamRec's send as a stream offset range, where offset
// = seq − (iss + 1) is the same number on both hosts; boot and bootExit
// are its enter and exit on the boot clock.
type sendRange struct{ start, n, enter, boot, bootExit uint64 }

// timing is what one half's records say about when things happened on
// its host's clock: the instants around the SYN, every data record with
// a sequence range, and the tcp_sock byte counters of its tcp_state
// records.
type timing struct {
	connect  earliest // client: the earliest connect enter
	synSent  earliest // client: the SYN_SENT transition
	afterSYN earliest // server: the earliest accept exit or tcp_state
	// opened is the earliest enter, on the boot clock, of a connect
	// (client) or a successful accept (server): no data call of this host
	// moves the connection's bytes before it (dropTaints).
	opened earliest
	sends  []streamRec
	recvs  []streamRec
	// sent and received sum the bytes of every data record of the half,
	// folded ones and those without a tcp_seq included; maxCount is the
	// largest tcp_sock byte counter any tcp_state record carried. Any of
	// them at 4 GiB or more means offsets may repeat (streamProof).
	sent, received, maxCount uint64
	counts                   []counterRec // the counter proofs read them
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
		t.opened.see(ev.BootNS)
	case (ev.Event == schema.KindAccept || ev.Event == schema.KindAccept4) && ev.Ret >= 0:
		t.afterSYN.see(ev.WallNS + ev.LatencyNS)
		t.opened.see(ev.BootNS)
	case ev.Event == schema.KindTCPState:
		t.state(ev)
	case schema.IsStreamSend(ev.Event) || schema.IsStreamReceive(ev.Event):
		t.data(ev)
	}
}

// state reads a tcp_state record. On an accepting end every record of the
// child socket follows the client's SYN — the child exists only once the
// SYN arrived — so its earliest is a receive-side instant of the SYN.
//
// A final snapshot (isFinal) gives only its counters: it marks no
// transition, so it dates neither the SYN nor the handshake — a connect
// still pending at the end reads SYN_SENT long after its SYN left.
func (t *timing) state(ev *schema.Event) {
	if !isFinal(ev) {
		t.afterSYN.see(ev.WallNS)
		if ev.Args.SockState != nil && *ev.Args.SockState == "SYN_SENT" {
			t.synSent.see(ev.WallNS)
		}
	}
	if ev.Args.BytesAcked != nil && ev.Args.BytesReceived != nil {
		r := counterRec{boot: ev.BootNS, acked: *ev.Args.BytesAcked,
			received: *ev.Args.BytesReceived}
		t.maxCount = max(t.maxCount, r.acked, r.received)
		t.counts = append(t.counts, r)
	}
}

// data sums one data record's bytes and, when it carries args.tcp_seq,
// keeps it with its sequence range: the send's enter (the bytes cannot
// leave before the call started) and the receive's exit (they had
// arrived when it returned). Every record counts toward the sums — a
// folded record (args.count) stands for many calls and carries no
// tcp_seq, nor does a call another thread interleaved with — since the
// sums must not undercount the stream (streamProof).
func (t *timing) data(ev *schema.Event) {
	send := schema.IsStreamSend(ev.Event)
	if send {
		t.sent += ev.Bytes
	} else {
		t.received += ev.Bytes
	}
	if ev.Args.TCPSeq == nil {
		return
	}
	r := streamRec{seq: *ev.Args.TCPSeq, n: ev.Bytes,
		enter: ev.WallNS, exit: ev.WallNS + ev.LatencyNS,
		bootEnter: ev.BootNS, bootExit: ev.BootNS + ev.LatencyNS}
	if send {
		t.sends = append(t.sends, r)
	} else {
		t.recvs = append(t.recvs, r)
	}
}

// messages returns every causal edge one fused connection gives: the
// handshake (client to server only) and each matched data message in
// both directions — each only when it is proven not to pair bytes of two
// different laps of the 4 GiB sequence space (direction,
// clockproof.go). A connection both of whose ends one host traced (two
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
	client := end{host: conn.Client.Host, acc: cl, client: true}
	server := end{host: conn.Server.Host, acc: sv}
	out = append(out, c.direction(conn.ID, client, server, conn.Key.Client)...)
	out = append(out, c.direction(conn.ID, server, client, conn.Key.Server)...)
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
// sends. The byte sums then prove nothing (streamProof).
func long(t *timing) bool {
	return t.sent >= wrap4G || t.received >= wrap4G || t.maxCount >= wrap4G
}

// streamMessages pairs each receive with the send that queued the
// receive's last byte (S2: matched by stream offset, which NAT leaves
// alone). A receive whose byte no traced send holds — the send was
// lost, or predates the session — gives nothing; so does a pair keep
// rejects (nil keeps all): one not proven to share a lap (direction).
func streamMessages(from, to, conn string, sends, recvs []streamRec, iss uint32,
	keep func(sendRange, streamRec) bool) []message {
	idx, ok := sendIndex(sends, iss)
	if !ok {
		return nil
	}
	var out []message
	for _, r := range recvs {
		last := uint64(r.seq-iss-1) + r.n - 1
		i := sort.Search(len(idx), func(i int) bool { return idx[i].start > last }) - 1
		if i < 0 || idx[i].start+idx[i].n <= last || (keep != nil && !keep(idx[i], r)) {
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
		idx = append(idx, sendRange{start: uint64(s.seq - iss - 1), n: s.n, enter: s.enter,
			boot: s.bootEnter, bootExit: s.bootExit})
	}
	slices.SortFunc(idx, func(a, b sendRange) int {
		return cmp.Or(cmp.Compare(a.start, b.start), cmp.Compare(a.n, b.n),
			cmp.Compare(a.enter, b.enter), cmp.Compare(a.boot, b.boot))
	})
	idx = slices.Compact(idx)
	for i, s := range idx {
		if s.start+s.n > wrap4G || (i > 0 && idx[i-1].start+idx[i-1].n > s.start) {
			return nil, false
		}
	}
	return idx, true
}
