package internal_test

import (
	"io"
	"testing"
	"time"

	"iormesh/internal"
	"iormesh/internal/corereloc"
	"iormesh/internal/schema"
)

// sendmsg, recvmsg, sendmmsg and recvmmsg as socket data calls (task oh4)
// from the real kernel, through the seq child (streamseq_root_test.go):
// on a TCP connection each carries its bytes - for the mmsg calls the sum
// of the messages' msg_len, read at the exit, while ret stays the message
// count - and the sequence number of its first byte, like the other data
// calls; between two UDP sockets the mmsg calls' byte sums are exact and
// no record carries tcp_seq. Root only.

// TestMsgCallsTCP: a sendmsg of two buffers and a sendmmsg of two
// messages, then a recvmsg and a recvmmsg of two messages, each pinned to
// the bytes it moved and to where the stream's bytes before it end.
func TestMsgCallsTCP(t *testing.T) {
	requireRoot(t)
	child := startSeqChild(t, msgScenarioTCP)
	log := collectRecords(openSeqTracer(t, child.pid(), false))
	child.start(t)
	if _, err := io.ReadFull(child.conn, make([]byte, sum(msgTCPSends))); err != nil {
		t.Fatalf("read the child's sends: %v", err)
	}
	for _, n := range msgTCPRecvs {
		if _, err := child.conn.Write(make([]byte, n)); err != nil {
			t.Fatalf("send to the child: %v", err)
		}
		time.Sleep(20 * time.Millisecond) // one chunk per child receive
	}
	r := child.result(t)
	evs := log.events(t, child.pid(), r.Fd)
	sndUna, rcvNxt := established(t, evs)
	checkRanges(t, "sends", evs, schema.IsStreamSend, sndUna, msgTCPSends)
	checkRanges(t, "receives", evs, schema.IsStreamReceive, rcvNxt, msgTCPRecvs)
	checkMsgKinds(t, evs, map[string]int64{
		schema.KindSendmsg: int64(msgTCPSends[0]), schema.KindSendmmsg: int64(len(msgTCPSendmmsg)),
		schema.KindRecvmsg: int64(msgTCPRecvmsg), schema.KindRecvmmsg: int64(len(msgTCPRecvmmsg)),
	})
}

// TestMsgCallsUDP: a sendmmsg of three datagrams and the recvmmsg that
// takes them carry the datagrams' byte sum and their count as ret; the
// sendmsg and recvmsg of one datagram its size. None carries tcp_seq,
// which is TCP's alone.
func TestMsgCallsUDP(t *testing.T) {
	requireRoot(t)
	child := startSeqChild(t, msgScenarioUDP)
	log := collectRecords(openSeqTracer(t, child.pid(), false))
	child.start(t)
	r := child.result(t)
	all := log.all(t)
	datagrams := int64(len(msgUDPDatagrams))
	for i, want := range []map[string]int64{
		{schema.KindSendmmsg: datagrams, schema.KindSendmsg: int64(msgUDPSingle)},
		{schema.KindRecvmmsg: datagrams, schema.KindRecvmsg: int64(msgUDPSingle)},
	} {
		evs := eventsOf(all, child.pid(), r.UDP[i])
		checkMsgKinds(t, evs, want)
		for _, e := range evs {
			if e.Args.TCPSeq != nil {
				t.Errorf("udp %s carries tcp_seq %#x", e.Event, *e.Args.TCPSeq)
			}
			wantBytes := uint64(msgUDPSingle)
			if schema.IsMmsg(e.Event) {
				wantBytes = uint64(sum(msgUDPDatagrams))
			}
			if want[e.Event] != 0 && e.Bytes != wantBytes {
				t.Errorf("udp %s moved %d bytes, want %d", e.Event, e.Bytes, wantBytes)
			}
		}
	}
}

// mmsgVerifierBudget is the most instructions TestMmsgVerifierHeadroom
// lets this kernel's verifier process for an mmsg handler: a tenth of the
// 1M complexity limit every kernel since 5.2 has, el9's 5.14 included.
const mmsgVerifierBudget = 100_000

// TestMmsgVerifierHeadroom loads the mmsg handlers - whose exit sums up to
// 1024 messages in a bounded loop (internal/c/mmsg.c) - on this kernel and
// logs how many instructions its verifier processed for each, requiring
// a tenth of the limit at most. The count is the running kernel's
// verifier's: run on el9 too (iom1, 5.14.0-741.22.1.el9_9, task rj4), it
// was 42,078 for the exit handler, as on 7.2 (docs/testing.md, "el9
// runbook and runs").
func TestMmsgVerifierHeadroom(t *testing.T) {
	requireRoot(t)
	progs := []string{"handle_sys_enter_sendmmsg", "handle_sys_exit_sendmmsg",
		"handle_sys_enter_recvmmsg", "handle_sys_exit_recvmmsg"}
	got, err := corereloc.VerifiedInsns(internal.EmbeddedBPFObject(), "iormesh.bpf.o", progs)
	if err != nil {
		t.Fatalf("load the mmsg handlers: %v", err)
	}
	for _, p := range progs {
		t.Logf("%s: %d instructions processed", p, got[p])
		if got[p] <= 0 || got[p] > mmsgVerifierBudget {
			t.Errorf("%s: verifier processed %d instructions, want 1..%d", p, got[p], mmsgVerifierBudget)
		}
	}
}

// checkMsgKinds requires exactly one event of each kind of wantRet among
// evs, with that ret (bytes for sendmsg and recvmsg, the message count
// for the mmsg calls) and a complete byte sum.
func checkMsgKinds(t *testing.T, evs []schema.Event, wantRet map[string]int64) {
	t.Helper()
	seen := map[string]int{}
	for _, e := range evs {
		ret, ok := wantRet[e.Event]
		if !ok {
			continue
		}
		seen[e.Event]++
		if e.Ret != ret {
			t.Errorf("%s returned %d, want %d", e.Event, e.Ret, ret)
		}
		if e.Args.BytesPartial != nil {
			t.Errorf("%s of %d bytes marked partial", e.Event, e.Bytes)
		}
	}
	for kind := range wantRet {
		if seen[kind] != 1 {
			t.Errorf("%d %s events, want 1", seen[kind], kind)
		}
	}
}
