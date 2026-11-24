package correlate

import (
	"testing"

	"golang.org/x/sys/unix"

	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// peekCall is one receive of TestPeekMarked: its enter and exit trace
// ids, the flags word of its enter record, ret, whether its exit is an
// mmsg_event (with bytes) or a tcp_seq_event, and whether the event must
// be marked args.peek.
type peekCall struct {
	name        string
	enter, exit types.TraceId
	flags       uint32
	ret         int64
	mmsg        bool
	bytes       uint64
	wantPeek    bool
}

// TestPeekMarked (task ok4): recvfrom, recvmsg and recvmmsg whose enter
// carries MSG_PEEK are marked args.peek, keep the bytes they copied and
// carry no tcp_seq even when their exit record names the socket; their
// StreamBytes is 0. The same calls without MSG_PEEK (other flags set),
// and a read whose record's flags word holds a stray MSG_PEEK bit, are
// not marked and keep tcp_seq.
func TestPeekMarked(t *testing.T) {
	const waitAll = unix.MSG_WAITALL
	calls := []peekCall{
		{name: "recvfrom peek", enter: types.SYS_ENTER_RECVFROM, exit: types.SYS_EXIT_RECVFROM,
			flags: unix.MSG_PEEK, ret: 12, wantPeek: true},
		{name: "recvmsg peek", enter: types.SYS_ENTER_RECVMSG, exit: types.SYS_EXIT_RECVMSG,
			flags: unix.MSG_PEEK | waitAll, ret: 12, wantPeek: true},
		{name: "recvmmsg peek", enter: types.SYS_ENTER_RECVMMSG, exit: types.SYS_EXIT_RECVMMSG,
			flags: unix.MSG_PEEK, ret: 1, mmsg: true, bytes: 12, wantPeek: true},
		{name: "recvfrom", enter: types.SYS_ENTER_RECVFROM, exit: types.SYS_EXIT_RECVFROM,
			flags: waitAll, ret: 4},
		{name: "recvmmsg", enter: types.SYS_ENTER_RECVMMSG, exit: types.SYS_EXIT_RECVMMSG,
			ret: 1, mmsg: true, bytes: 4},
		{name: "read with a stray flags word", enter: types.SYS_ENTER_READ, exit: types.SYS_EXIT_READ,
			flags: unix.MSG_PEEK, ret: 4},
	}
	c, sink := newTest(t)
	now := uint64(100)
	f := feeder{t, c, 100, 101, &now}
	f.handle(&types.FdEvent{EventType: types.ENTER_FD_EVENT, TraceId: types.SYS_ENTER_CONNECT,
		Time: f.tick(), Pid: 100, Tid: 101, Fd: 6})
	f.handle(tcp(f.tick(), 0xA, 100, 6, synSent, tcpEstablished, cli, srv))
	f.exit(types.SYS_EXIT_CONNECT, 0)
	for _, call := range calls {
		f.handle(&types.FdEvent{EventType: types.ENTER_FD_SIZE_EVENT, TraceId: call.enter,
			Time: f.tick(), Pid: 100, Tid: 101, Fd: 6, Flags: call.flags})
		if call.mmsg {
			f.handle(&types.MmsgEvent{EventType: types.MMSG_EVENT, TraceId: call.exit, Time: f.tick(),
				Ret: call.ret, Pid: 100, Tid: 101, TotalBytes: call.bytes, Sk: 0xA, Seq: 0x30})
			continue
		}
		f.handle(&types.TcpSeqEvent{EventType: types.TCP_SEQ_EVENT, TraceId: call.exit, Time: f.tick(),
			Ret: call.ret, Pid: 100, Tid: 101, Sk: 0xA, Seq: 0x30})
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	checkPeekEvents(t, sink.events, calls)
}

// checkPeekEvents matches the receives' events, in order, to calls.
func checkPeekEvents(t *testing.T, events []schema.Event, calls []peekCall) {
	t.Helper()
	var data []schema.Event
	for _, e := range events {
		if err := e.Validate(); err != nil {
			t.Errorf("%s event invalid: %v", e.Event, err)
		}
		if e.FD == 6 && schema.IsStreamReceive(e.Event) {
			data = append(data, e)
		}
	}
	if len(data) != len(calls) {
		t.Fatalf("%d receive events, want %d", len(data), len(calls))
	}
	for i, call := range calls {
		e := data[i]
		wantBytes := uint64(call.ret)
		if call.mmsg {
			wantBytes = call.bytes
		}
		if e.Bytes != wantBytes {
			t.Errorf("%s: bytes %d, want %d", call.name, e.Bytes, wantBytes)
		}
		if got := e.Args.Peek != nil && *e.Args.Peek; got != call.wantPeek {
			t.Errorf("%s: peek %v, want %v", call.name, got, call.wantPeek)
		}
		wantStream := wantBytes
		if call.wantPeek {
			wantStream = 0
		}
		if got := e.StreamBytes(); got != wantStream {
			t.Errorf("%s: stream bytes %d, want %d", call.name, got, wantStream)
		}
		if got := e.Args.TCPSeq != nil; got == call.wantPeek {
			t.Errorf("%s: tcp_seq present %v, want %v", call.name, got, !call.wantPeek)
		}
	}
}
