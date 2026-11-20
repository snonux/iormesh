package types

import "testing"

// TestNewTcpSeqEventFastMatchesSlow holds the fast decoder to the
// reflective one on the kernel's layout, on a longer payload, and on a
// short one (both nil).
func TestNewTcpSeqEventFastMatchesSlow(t *testing.T) {
	want := TcpSeqEvent{EventType: TCP_SEQ_EVENT, TraceId: 9, Time: 1 << 41, Ret: -5,
		Pid: 4242, Tid: 4243, Sk: 0xffff8881_2345_6780, Seq: 0xfffffff0, Reserved: 1}
	raw, err := want.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != tcpSeqEventSize {
		t.Fatalf("binary.Write size %d, want %d", len(raw), tcpSeqEventSize)
	}
	for _, payload := range [][]byte{raw, append(append([]byte(nil), raw...), 0, 0, 0, 0)} {
		fast, slow := NewTcpSeqEventFast(payload), NewTcpSeqEvent(payload)
		if fast == nil || slow == nil || *fast != *slow || *fast != want {
			t.Fatalf("len %d: fast %+v, slow %+v, want %+v", len(payload), fast, slow, want)
		}
		fast.Recycle()
		slow.Recycle()
	}
	if NewTcpSeqEventFast(raw[:47]) != nil || NewTcpSeqEvent(raw[:47]) != nil {
		t.Fatal("short payload decoded")
	}
}
