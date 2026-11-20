package correlate

import (
	"testing"

	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// seqCall is one data call of TestStreamSeq: its kind, fd and return, and
// the socket and sequence number of its exit record, a tcp_seq_event (sk
// 0: a plain ret_event).
type seqCall struct {
	name    string
	enter   types.TraceId
	exit    types.TraceId
	fd      int32
	ret     int64
	sk      uint64
	seq     uint32
	wantSeq bool
}

// TestStreamSeq: the correlator passes a tcp_seq record on as args.tcp_seq
// only for a data call that moved bytes on the TCP socket the record names
// (M4, task xe4); a record naming another socket, one on a call that moved
// nothing, one on a file and a call without a record give none, and every
// event passes schema validation (validateStreamSeq).
func TestStreamSeq(t *testing.T) {
	calls := []seqCall{
		{"write on its socket", types.SYS_ENTER_WRITE, types.SYS_EXIT_WRITE, 6, 5, 0xA, 0xFFFFFFFE, true},
		{"read on its socket", types.SYS_ENTER_READ, types.SYS_EXIT_READ, 6, 7, 0xA, 0x1001, true},
		{"record of another socket", types.SYS_ENTER_WRITE, types.SYS_EXIT_WRITE, 6, 5, 0xB, 9, false},
		{"read of no bytes (EOF)", types.SYS_ENTER_READ, types.SYS_EXIT_READ, 6, 0, 0xA, 9, false},
		{"write without a record", types.SYS_ENTER_WRITE, types.SYS_EXIT_WRITE, 6, 5, 0, 0, false},
		{"write on a file", types.SYS_ENTER_WRITE, types.SYS_EXIT_WRITE, 9, 5, 0xA, 9, false},
	}
	c, sink := newTest(t)
	now := uint64(100)
	f := feeder{t, c, 100, 101, &now}
	f.handle(&types.FdEvent{EventType: types.ENTER_FD_EVENT, TraceId: types.SYS_ENTER_CONNECT,
		Time: f.tick(), Pid: 100, Tid: 101, Fd: 6})
	f.handle(tcp(f.tick(), 0xA, 100, 6, synSent, tcpEstablished, cli, srv))
	f.exit(types.SYS_EXIT_CONNECT, 0)
	f.open("/tmp/x", 0, 9)
	for _, call := range calls {
		f.handle(&types.FdEvent{EventType: types.ENTER_FD_EVENT, TraceId: call.enter,
			Time: f.tick(), Pid: 100, Tid: 101, Fd: call.fd})
		if call.sk == 0 {
			f.exit(call.exit, call.ret)
			continue
		}
		f.handle(&types.TcpSeqEvent{EventType: types.TCP_SEQ_EVENT, TraceId: call.exit,
			Time: f.tick(), Ret: call.ret, Sk: call.sk, Pid: 100, Tid: 101, Seq: call.seq})
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	checkStreamSeq(t, sink.events, calls)
}

// checkStreamSeq matches the data events, in order, to the calls.
func checkStreamSeq(t *testing.T, events []schema.Event, calls []seqCall) {
	t.Helper()
	var data []schema.Event
	for _, e := range events {
		if err := e.Validate(); err != nil {
			t.Errorf("%s event invalid: %v", e.Event, err)
		}
		if e.Event == schema.KindRead || e.Event == schema.KindWrite {
			data = append(data, e)
		}
	}
	if len(data) != len(calls) {
		t.Fatalf("%d data events, want %d", len(data), len(calls))
	}
	for i, call := range calls {
		got := data[i].Args.TCPSeq
		switch {
		case call.wantSeq && (got == nil || *got != call.seq):
			t.Errorf("%s: tcp_seq %v, want %#x", call.name, got, call.seq)
		case !call.wantSeq && got != nil:
			t.Errorf("%s: tcp_seq %#x, want none", call.name, *got)
		}
	}
}

// TestStreamSeqUnpaired: a tcp_seq_event whose thread has another call
// pending is an unpaired exit like any other - counted, no event - and
// leaves nothing behind for the thread's next call.
func TestStreamSeqUnpaired(t *testing.T) {
	c, sink := newTest(t)
	now := uint64(100)
	f := feeder{t, c, 100, 101, &now}
	f.handle(&types.FdEvent{EventType: types.ENTER_FD_EVENT, TraceId: types.SYS_ENTER_CONNECT,
		Time: f.tick(), Pid: 100, Tid: 101, Fd: 6})
	f.handle(tcp(f.tick(), 0xA, 100, 6, synSent, tcpEstablished, cli, srv))
	f.exit(types.SYS_EXIT_CONNECT, 0)
	f.handle(&types.FdEvent{EventType: types.ENTER_FD_EVENT, TraceId: types.SYS_ENTER_READ,
		Time: f.tick(), Pid: 100, Tid: 101, Fd: 6})
	f.handle(&types.TcpSeqEvent{EventType: types.TCP_SEQ_EVENT, TraceId: types.SYS_EXIT_WRITE,
		Time: f.tick(), Ret: 5, Sk: 0xA, Pid: 100, Tid: 101, Seq: 1})
	f.write(6, 5)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if c.Unpaired() != 1 {
		t.Errorf("%d unpaired exits, want 1", c.Unpaired())
	}
	writes := byKind(sink.events, schema.KindWrite)
	if len(writes) != 1 || writes[0].Args.TCPSeq != nil {
		t.Errorf("writes %+v, want one without tcp_seq", writes)
	}
}
