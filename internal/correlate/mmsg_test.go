package correlate

import (
	"testing"

	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// mmsgCall is one msghdr data call of TestMmsgRecords: its enter and exit
// trace ids, fd and ret; for an mmsg exit the record's byte sum, flags,
// socket and sequence number (rec false: a plain ret_event); what the
// event must carry.
type mmsgCall struct {
	name        string
	enter, exit types.TraceId
	fd          int32
	ret         int64
	rec         bool
	bytes       uint64
	flags       uint32
	sk          uint64
	seq         uint32
	wantBytes   uint64
	wantPartial bool
	wantSeq     bool
}

// TestMmsgRecords: sendmsg and recvmsg take their bytes from ret like the
// other data calls, the mmsg calls from their mmsg_event's sum while ret
// stays the message count (task oh4). A partial sum is marked
// args.bytes_partial and carries no tcp_seq; a complete one on the
// connection's socket carries it; an mmsg exit that came as a plain
// ret_event with messages has no sum at all, so it is partial too. Every
// event passes schema validation.
func TestMmsgRecords(t *testing.T) {
	calls := []mmsgCall{
		{name: "sendmsg", enter: types.SYS_ENTER_SENDMSG, exit: types.SYS_EXIT_SENDMSG, fd: 6, ret: 7,
			wantBytes: 7},
		{name: "recvmsg", enter: types.SYS_ENTER_RECVMSG, exit: types.SYS_EXIT_RECVMSG, fd: 6, ret: 8,
			wantBytes: 8},
		{name: "sendmmsg with tcp_seq", enter: types.SYS_ENTER_SENDMMSG, exit: types.SYS_EXIT_SENDMMSG,
			fd: 6, ret: 2, rec: true, bytes: 11, sk: 0xA, seq: 0x2000, wantBytes: 11, wantSeq: true},
		{name: "recvmmsg of another socket's record", enter: types.SYS_ENTER_RECVMMSG,
			exit: types.SYS_EXIT_RECVMMSG, fd: 6, ret: 2, rec: true, bytes: 9, sk: 0xB, seq: 3, wantBytes: 9},
		{name: "partial recvmmsg", enter: types.SYS_ENTER_RECVMMSG, exit: types.SYS_EXIT_RECVMMSG, fd: 6,
			ret: 3, rec: true, bytes: 4, flags: types.MMSG_BYTES_PARTIAL, wantBytes: 4, wantPartial: true},
		{name: "sendmmsg as a plain ret_event", enter: types.SYS_ENTER_SENDMMSG,
			exit: types.SYS_EXIT_SENDMMSG, fd: 6, ret: 2, wantPartial: true},
		{name: "failed sendmmsg", enter: types.SYS_ENTER_SENDMMSG, exit: types.SYS_EXIT_SENDMMSG, fd: 6,
			ret: -11},
	}
	c, sink := newTest(t)
	now := uint64(100)
	f := feeder{t, c, 100, 101, &now}
	f.handle(&types.FdEvent{EventType: types.ENTER_FD_EVENT, TraceId: types.SYS_ENTER_CONNECT,
		Time: f.tick(), Pid: 100, Tid: 101, Fd: 6})
	f.handle(tcp(f.tick(), 0xA, 100, 6, synSent, tcpEstablished, cli, srv))
	f.exit(types.SYS_EXIT_CONNECT, 0)
	for _, call := range calls {
		f.handle(&types.FdEvent{EventType: types.ENTER_FD_EVENT, TraceId: call.enter,
			Time: f.tick(), Pid: 100, Tid: 101, Fd: call.fd})
		if !call.rec {
			f.exit(call.exit, call.ret)
			continue
		}
		f.handle(&types.MmsgEvent{EventType: types.MMSG_EVENT, TraceId: call.exit, Time: f.tick(),
			Ret: call.ret, Pid: 100, Tid: 101, TotalBytes: call.bytes, Sk: call.sk, Seq: call.seq,
			Flags: call.flags})
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	checkMmsgEvents(t, sink.events, calls)
}

// checkMmsgEvents matches the msghdr calls' events, in order, to calls.
func checkMmsgEvents(t *testing.T, events []schema.Event, calls []mmsgCall) {
	t.Helper()
	var data []schema.Event
	for _, e := range events {
		if err := e.Validate(); err != nil {
			t.Errorf("%s event invalid: %v", e.Event, err)
		}
		if e.FD == 6 && (schema.IsStreamSend(e.Event) || schema.IsStreamReceive(e.Event)) {
			data = append(data, e)
		}
	}
	if len(data) != len(calls) {
		t.Fatalf("%d data events, want %d", len(data), len(calls))
	}
	for i, call := range calls {
		e := data[i]
		if e.Ret != call.ret || e.Bytes != call.wantBytes {
			t.Errorf("%s: ret %d bytes %d, want ret %d bytes %d", call.name, e.Ret, e.Bytes,
				call.ret, call.wantBytes)
		}
		if got := e.Args.BytesPartial != nil; got != call.wantPartial {
			t.Errorf("%s: bytes_partial %v, want %v", call.name, got, call.wantPartial)
		}
		switch got := e.Args.TCPSeq; {
		case call.wantSeq && (got == nil || *got != call.seq):
			t.Errorf("%s: tcp_seq %v, want %#x", call.name, got, call.seq)
		case !call.wantSeq && got != nil:
			t.Errorf("%s: tcp_seq %#x, want none", call.name, *got)
		}
	}
}
