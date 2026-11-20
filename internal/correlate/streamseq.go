package correlate

import (
	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// M4 (PLAN.md "Clock reconciliation from the traffic itself", task xe4):
// args.tcp_seq, the TCP sequence number of the first byte a single send or
// receive moved. The kernel side (internal/c/tcpseq.c) sends a
// tcp_seq_event as the exit record of a write, writev, sendto,
// sendfile64, read, readv or recvfrom on a TCP socket - a ret_event plus
// the sequence number and the socket - only when the socket's write_seq
// (copied_seq) moved by exactly the call's return between its enter and
// its exit, so the range is the call's own. A folded read or write never
// gets one: its calls send no exit record.

// tcpSeqRecord is exitRet for a tcp_seq_event: the exit pairs with the
// thread's pending enter as any exit does, carrying the sequence number
// and the socket into the event (an exit without its enter is counted,
// not guessed at).
func (c *Correlator) tcpSeqRecord(r *types.TcpSeqEvent) {
	c.advance(r.Time)
	c.exitSeq(r.Tid, r.TraceId.Name(), r.Ret, r.Time, r.Sk, r.Seq)
}

// streamSeq sets args.tcp_seq when everything the schema asks of it holds
// (schema validateStreamSeq): a stream data kind that moved bytes, on a
// socket this trace bound - and the record's struct sock is that very
// socket. The last check is what makes a record the kernel parked on one
// socket unable to name bytes of another (a tid's stale park, an fd the
// call's own thread closed and reused mid-call): the kernel's arithmetic
// is exact for the socket it read, so only the socket must be the call's.
// Residue: an untraced dup2 of another socket onto the fd mid-call while
// exactly ret bytes moved on the parked one (tcpseq.c).
// The conn id comes later, when the queue releases the event (emit drops
// tcp_seq from an event that ends up without one).
func (c *Correlator) streamSeq(ev *schema.Event, sock *sockState, sk uint64, seq uint32) {
	if ev.Bytes == 0 || sock == nil || c.socks.bySk[sk] != sock {
		return
	}
	if !schema.IsStreamSend(ev.Event) && !schema.IsStreamReceive(ev.Event) {
		return
	}
	ev.Args.TCPSeq = &seq
}
