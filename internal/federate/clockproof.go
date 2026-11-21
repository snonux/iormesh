package federate

import "iormesh/internal/schema"

// Which data messages the clock bounds may use (PLAN.md M4, "Clock
// reconciliation from the traffic itself"; task jf4).
//
// A data message pairs a receive with the send whose recorded range holds
// the receive's last byte. Each record's range is exact: the tracer gives
// args.tcp_seq only when the call's start and end reads differ by exactly
// its return (internal/c/tcpseq.c), so a lost or reordered record can
// never shift another record's range. What can go wrong is the lap:
// offsets are taken mod 2^32, so once a direction has carried 4 GiB, one
// offset names bytes of two different sends, and if the true sender of a
// receive's bytes was never recorded (lost, or folded without a tcp_seq),
// the receive pairs with a send of another lap — a message whose "send"
// may well come after its receive. A message is used only when one of two
// proofs rules that out:
//
//   - the stream proof (streamProof): the whole direction stayed below
//     4 GiB, by the byte sums of the half's records and the tcp_sock
//     counters, and no drop of the sending host could have hidden a send of
//     this connection (dropTaints). It covers every message of the
//     direction.
//   - the counter proof (sendProven, receiveProven): the tcp_sock counters
//     of tcp_state records taken after the message's send (on its host) and
//     after its receive (on its host) place both ends' true offsets below
//     2^32. Equal mod 2^32 and both below 2^32, they are the same offset.
//     The counters are the kernel's, so no drop touches this proof; it
//     covers one message at a time.
//
// Drops never enter the counter proof, and they enter the stream proof only
// on the sending host: lost records on the receiving host remove receives,
// which only removes messages. Every drop's records were lost at or before
// its own instant (dropRec), so a drop before the connection's socket
// existed on the sending host hid none of its sends.

// ackedProofMax caps a sender's bytes_acked for the counter proof. The
// send's start S (its write_seq when it entered) is at most write_seq at
// any later instant T, and write_seq − snd_una is the data still queued
// (unsent or unacknowledged), which the kernel charges to sk_wmem_queued:
// tcp_sendmsg waits once sk_wmem_queued reaches sk_sndbuf, an int below
// 2^31, and can overshoot by no more than the skb it is filling and one
// new one, each at most a size goal of half the peer's largest window
// (tcp_bound_to_half_wnd: 2^29). So the queue stays under 3 GiB, and
// bytes_acked at T (at least snd_una's offset) below 512 MiB keeps S below
// 3.5 GiB < 2^32.
const ackedProofMax = uint64(1) << 29

// counterRec is one tcp_state record's tcp_sock byte counters, at its
// instant on the host's boot clock.
type counterRec struct{ boot, acked, received uint64 }

// dropRec is one drop event: its instant on the host's boot clock and its
// reason ("" for ring-buffer or decoder loss). Every kind of drop is
// written after the records it stands for were lost, never before: an
// attach_gap when the session adopted the process whose earlier records
// it counts; ring, decoder and skipped-run loss at the first loss-counter
// poll that saw the counter grow, which a lost record's reservation, its
// failed decode or the skipped program run precedes. The one exception
// is unattributed: its instant is the first of the folded calls it stands
// for, and the others came later.
type dropRec struct {
	boot   uint64
	reason string
}

func newDropRec(ev *schema.Event) dropRec {
	r := dropRec{boot: ev.BootNS}
	if ev.Args.DropReason != nil {
		r.reason = *ev.Args.DropReason
	}
	return r
}

// end is one end of a fused connection as the bounds see it.
type end struct {
	host   string
	acc    *halfAcc
	client bool // the connecting end
}

// direction returns the data messages from one end to the other, iss
// being the sender's ISN: all of them when the stream proof holds, those
// the counter proof covers otherwise.
func (c *collector) direction(conn string, from, to end, iss uint32) []message {
	sends, recvs := from.acc.timing.sends, to.acc.timing.recvs
	if c.streamProof(from, to) {
		return streamMessages(from.host, to.host, conn, sends, recvs, iss, nil)
	}
	return streamMessages(from.host, to.host, conn, sends, recvs, iss,
		func(s sendRange, r streamRec) bool {
			return sendProven(&from.acc.timing, s.boot) && receiveProven(&to.acc.timing, r.bootExit)
		})
}

// streamProof reports whether every byte the sender queued on the
// connection is in its records' sums, and those stay below 4 GiB: then
// all offsets of the direction are below 2^32 and each names one send.
// The receiver's sums and counters are checked too (long), a cheap second
// witness of the same total. A drop of any session that reported the
// sending half may have hidden a send unless dropTaints says otherwise.
func (c *collector) streamProof(from, to end) bool {
	if long(&from.acc.timing) || long(&to.acc.timing) {
		return false
	}
	for project := range from.acc.projects {
		for _, d := range c.drops[hostProject{from.host, project}] {
			if dropTaints(d, from) {
				return false
			}
		}
	}
	return true
}

// dropTaints reports whether drop d, on the sending end's host, may have
// hidden one of the connection's sends. The data calls of that host can
// carry the connection's bytes only once its socket is open there — a
// client's connect, a server's accept (timing.opened) — and d's records
// were lost at or before its instant (dropRec), so a drop before that
// instant hid none of them: the attach_gap of a process adopted before it
// connected, a ring overflow before the connection. Three cases stay
// tainting whatever their instant:
//   - an end whose opening call the trace lacks: nothing dates its socket;
//   - an unattributed drop: its folded calls run on from its instant into
//     an unknown future, on an fd nobody could name;
//   - skipped runs on the connecting end. A skipped task_newtask run leaves
//     the new child untraced for good, and the child keeps every socket
//     the parent held at the fork: a client socket exists from its
//     socket() call, which the trace does not date, before connect. An
//     accepted socket gets its fd only from accept, so no child forked
//     before it holds the socket.
func dropTaints(d dropRec, sender end) bool {
	opened := sender.acc.timing.opened
	switch {
	case !opened.ok, d.reason == schema.DropUnattributed:
		return true
	case d.reason == schema.DropSkippedRuns && sender.client:
		return true
	}
	return d.boot >= opened.ns
}

// sendProven reports whether a tcp_state record of the sending half taken
// at or after the send's enter (boot ns) proves the send's start offset
// below 2^32 (ackedProofMax). write_seq only grows, so a record after the
// enter bounds the start.
func sendProven(t *timing, enter uint64) bool {
	for _, r := range t.counts {
		if r.boot >= enter && r.acked < ackedProofMax {
			return true
		}
	}
	return false
}

// receiveProven reports whether a tcp_state record of the receiving half
// taken at or after the receive's exit (boot ns) proves the receive's
// offsets below 2^32: by its exit the receive had copied bytes up to its
// end, all of which bytes_received (which only grows) already counted.
func receiveProven(t *timing, exit uint64) bool {
	for _, r := range t.counts {
		if r.boot >= exit && r.received < wrap4G {
			return true
		}
	}
	return false
}
