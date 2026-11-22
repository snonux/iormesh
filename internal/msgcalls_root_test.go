package internal_test

import (
	"io"
	"testing"
	"time"

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
