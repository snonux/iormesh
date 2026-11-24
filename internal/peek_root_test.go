package internal_test

import (
	"testing"

	"iormesh/internal/schema"
)

// TestPeekCountedOnce (task ok4): the child peeks at one queued chunk with
// recvfrom, recvmsg and recvmmsg (MSG_PEEK each) and then reads it, from
// the real kernel through the seq child (peek_child_test.go). Each peek
// keeps the bytes it copied but is marked args.peek and carries no
// tcp_seq; the read alone carries the chunk's range. The receiver's stream
// sum (StreamBytes, what the merge's clock proof adds up) counts the
// chunk once, not four times. Root only.
func TestPeekCountedOnce(t *testing.T) {
	requireRoot(t)
	child := startSeqChild(t, peekScenario)
	log := collectRecords(openSeqTracer(t, child.pid(), false))
	child.start(t)
	if _, err := child.conn.Write(make([]byte, peekBytes)); err != nil {
		t.Fatalf("send to the child: %v", err)
	}
	r := child.result(t)
	evs := log.events(t, child.pid(), r.Fd)
	_, rcvNxt := established(t, evs)
	checkPeeks(t, evs)
	var sum uint64
	for _, e := range evs {
		if !schema.IsStreamReceive(e.Event) {
			continue
		}
		sum += e.StreamBytes()
		if e.Event != schema.KindRead || e.Bytes == 0 {
			continue
		}
		if e.Args.Peek != nil {
			t.Errorf("read marked peek")
		}
		if s := e.Args.TCPSeq; s == nil || *s != rcvNxt {
			t.Errorf("read: tcp_seq %v, want %#x", s, rcvNxt)
		}
	}
	if sum != peekBytes {
		t.Errorf("receives counted %d bytes, want %d (each byte once)", sum, peekBytes)
	}
}

// checkPeeks requires exactly one event of each peeking kind among evs,
// with the bytes it copied, marked args.peek and without tcp_seq.
func checkPeeks(t *testing.T, evs []schema.Event) {
	t.Helper()
	seen := map[string]int{}
	for _, e := range evs {
		if !schema.IsPeekable(e.Event) {
			continue
		}
		seen[e.Event]++
		if e.Bytes != peekBytes {
			t.Errorf("%s copied %d bytes, want %d", e.Event, e.Bytes, peekBytes)
		}
		if e.Args.Peek == nil || !*e.Args.Peek {
			t.Errorf("%s with MSG_PEEK not marked peek", e.Event)
		}
		if e.Args.TCPSeq != nil {
			t.Errorf("%s with MSG_PEEK carries tcp_seq %#x", e.Event, *e.Args.TCPSeq)
		}
	}
	for _, kind := range []string{schema.KindRecvfrom, schema.KindRecvmsg, schema.KindRecvmmsg} {
		if seen[kind] != 1 {
			t.Errorf("%d %s events, want 1", seen[kind], kind)
		}
	}
}
