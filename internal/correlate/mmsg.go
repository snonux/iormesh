package correlate

import (
	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// M4 (PLAN.md "Clock reconciliation from the traffic itself", task oh4):
// sendmmsg and recvmmsg return how many messages they moved, not bytes.
// The kernel side (internal/c/mmsg.c) sums the messages' msg_len at the
// exit and sends an mmsg_event as the exit record of every such call that
// moved messages: the ret (the count), the byte sum, a flag when the sum
// is incomplete, and - on a TCP socket whose stream moved by exactly the
// sum - the sequence number of the first byte and the socket, as a
// tcp_seq_event carries them (streamseq.go).

// mmsgRecord is exitRet for an mmsg_event: the exit pairs with the
// thread's pending enter as any exit does; the event's bytes are the
// record's sum, so the stream byte accounting (the merge's sums,
// internal/federate) counts every message. An incomplete sum is marked
// args.bytes_partial and carries no tcp_seq; a complete one gets tcp_seq
// under streamSeq's checks.
func (c *Correlator) mmsgRecord(r *types.MmsgEvent) {
	c.advance(r.Time)
	var ev schema.Event
	sock, ok := c.pair(&ev, r.Tid, r.TraceId.Name(), r.Ret, r.Time)
	if !ok {
		return
	}
	if schema.IsMmsg(ev.Event) {
		ev.Bytes, ev.Args.BytesPartial = r.TotalBytes, nil
		switch {
		case r.Flags&types.MMSG_BYTES_PARTIAL != 0:
			markPartial(&ev)
		case r.Sk != 0:
			c.streamSeq(&ev, sock, r.Sk, r.Seq)
		}
	}
	c.enqueue(ev, sock)
}

// markPartial marks ev's bytes as a lower bound (schema
// validateBytesPartial).
func markPartial(ev *schema.Event) {
	partial := true
	ev.Args.BytesPartial = &partial
}
