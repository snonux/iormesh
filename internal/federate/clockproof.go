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
// checks rules that out:
//
//   - the counter proof (sendProven, receiveProven): the tcp_sock counters
//     of tcp_state records taken after the message's send (on its host) and
//     after its receive (on its host) place both ends' true offsets below
//     2^32. Equal mod 2^32 and both below 2^32, they are the same offset.
//     The counters are the kernel's, so no drop and no untraced call
//     touches this proof; it covers one message at a time. Every end has a
//     counter record after all of its calls: a socket closed during the
//     session its CLOSE, one still open when the session ended the
//     tracer's final snapshot (args.sock_snapshot "final", task sf4). So
//     every message of a direction whose sender acknowledged less than
//     ackedProofMax and whose receiver received less than 4 GiB is proven,
//     whatever the trace missed — unless the kernel skipped the very run
//     that would have written the CLOSE, which a skipped_runs drop then
//     records. It relies on the records being this connection's: a lost
//     CLOSE can leave the tracer's socket-to-connection binding stale, so
//     a later connection reusing the socket reports under the same conn_id
//     with counters reset low — but its records rebuild another ISN pair,
//     the half is a KeyConflict (half.go, keyRecords) and fuses with
//     nothing. A final snapshot never keys a half (isFinal), and the
//     tracer drops one whose socket file is not the registered one.
//   - the stream check (streamProof), for the directions the counter proof
//     leaves (longer ones, or an end whose last records were lost): the
//     whole direction stayed below 4 GiB by the byte sums of the half's
//     records and the tcp_sock counters, no recorded offset lies past the
//     sender's sum, no counter record of the sender counts more
//     acknowledged bytes than its sum (ackedCovered), and no drop of the
//     sending host could have hidden a send of this connection
//     (dropTaints). It covers every message of the direction. It is not a
//     proof: it trusts the sums to hold every byte queued, and bytes can
//     escape them with no drop recorded — untraced sendmsg, sendmmsg,
//     splice and io_uring; a fork between the untraced socket() and
//     connect(), an exec that keeps the socket (the correlator drops the
//     fd's binding), or an fd passed by SCM_RIGHTS or pidfd_getfd, any of
//     which leaves a holder whose calls name no connection. It also reads
//     only the drops of the sessions that reported the half. What limits
//     the damage: an offset past the sender's sum (sumsCover) or a
//     bytes_acked above it (ackedCovered) proves untraced bytes and fails
//     the check — with the final snapshot every end has a bytes_acked
//     taken after all of its calls, so what can still pass unseen is
//     untraced bytes no more than the data still unacknowledged at that
//     reading (none on a connection idle at the end); the receiver's sums
//     are a second witness (long); and any counter record at 4 GiB or more
//     fails it.

// Drops never enter the counter proof, and they enter the stream check only
// on the sending host: lost records on the receiving host remove receives,
// which only removes messages. Every drop's records were lost at or before
// its own instant (dropRec), so a drop before the connection's socket
// existed on the sending host hid none of its sends.

// ackedProofMax caps a sender's bytes_acked for the counter proof. The
// send's start S (write_seq read in its enter hook, a little after the
// enter's timestamp) is at most write_seq at any instant T after the
// call's exit, and write_seq − snd_una is the data still queued
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
// being the sender's ISN: all of them when the stream check holds, those
// the counter proof covers otherwise.
func (c *collector) direction(conn string, from, to end, iss uint32) []message {
	sends, recvs := from.acc.timing.sends, to.acc.timing.recvs
	if c.streamProof(from, to, iss) {
		return streamMessages(from.host, to.host, conn, sends, recvs, iss, nil)
	}
	return streamMessages(from.host, to.host, conn, sends, recvs, iss,
		func(s sendRange, r streamRec) bool {
			return sendProven(&from.acc.timing, s.bootExit) &&
				receiveProven(&to.acc.timing, r.bootExit)
		})
}

// streamProof reports whether the sender's records' sums stay below
// 4 GiB and nothing shows bytes they miss: then, as far as the trace can
// tell, all offsets of the direction are below 2^32 and each names one
// send (the residue the header names remains). The receiver's sums and
// counters are checked too (long), a cheap second witness of the same
// total. A drop of any session that reported the sending half may have
// hidden a send unless dropTaints says otherwise.
func (c *collector) streamProof(from, to end, iss uint32) bool {
	if long(&from.acc.timing) || long(&to.acc.timing) ||
		!sumsCover(&from.acc.timing, &to.acc.timing, iss) || !ackedCovered(&from.acc.timing) {
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

// sumsCover reports whether every recorded offset of the direction ends
// within the sender's byte sum. The stream starts at offset 0, so a
// recorded send or receive ending past the sum proves bytes the sums never
// saw — an untraced call or holder — and the sums prove nothing. It is a
// partial guard: untraced bytes after the last recorded offset leave no
// such trace.
func sumsCover(from, to *timing, iss uint32) bool {
	for _, recs := range [][]streamRec{from.sends, to.recvs} {
		for _, r := range recs {
			if uint64(r.seq-iss-1)+r.n > from.sent {
				return false
			}
		}
	}
	return true
}

// ackedSlack is how many of the sequence numbers bytes_acked counts no
// data call queued: the connecting end's SYN (key.go) and either end's
// FIN.
const ackedSlack = 2

// ackedCovered reports whether the sender's byte sum holds every data byte
// its tcp_state records say the peer acknowledged. bytes_acked is the
// kernel's own count of the stream and only grows, so a record — the
// final snapshot (task sf4) or a CLOSE above all, taken after every call —
// counting more than the sum plus ackedSlack proves bytes the sum never
// saw, however they were sent: an untraced sendmsg, splice or io_uring
// call, or a holder the trace cannot tie to the connection.
func ackedCovered(from *timing) bool {
	for _, r := range from.counts {
		if r.acked > from.sent+ackedSlack {
			return false
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
// at or after the send's exit (boot ns) proves the send's start offset
// below 2^32 (ackedProofMax). write_seq only grows, so a record after the
// exit bounds the start; one merely after the enter's timestamp need not,
// since the start is read in the enter hook a little later.
func sendProven(t *timing, exit uint64) bool {
	for _, r := range t.counts {
		if r.boot >= exit && r.acked < ackedProofMax {
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
