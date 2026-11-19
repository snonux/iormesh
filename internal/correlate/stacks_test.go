package correlate

import (
	"errors"
	"fmt"
	"testing"

	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// fakeStacks hands out kernel id + 100 as the trace id, refuses negative
// ids like internal/stacks.Table, and records execs.
type fakeStacks struct {
	execs []uint32
	fail  error
}

func (s *fakeStacks) ID(_ uint32, _ uint64, kid int32) (uint32, bool, error) {
	if s.fail != nil {
		return 0, false, s.fail
	}
	if kid < 0 {
		return 0, false, nil
	}
	return uint32(kid) + 100, true, nil
}

func (s *fakeStacks) Exec(pid uint32) { s.execs = append(s.execs, pid) }

func newStackTest(t *testing.T, st StackIDs) (*Correlator, *memSink, feeder) {
	sink := &memSink{t: t}
	c := New(Config{Host: Host{ID: "h1", Project: "p"}, Sink: sink, Stacks: st,
		ProcInfo: func(pid uint32) (string, uint64) { return fmt.Sprintf("proc%d", pid), 1000 + uint64(pid) }})
	now := uint64(0)
	return c, sink, feeder{t: t, c: c, pid: 7, tid: 7, now: &now}
}

// stackThenRead sends a stack record stamped at the read's enter time
// (offset: 0 for the real pairing), then the read.
func (f feeder) stackThenRead(kid int32, offset uint64) {
	f.t.Helper()
	at := *f.now + 10 // the read's enter takes the next tick
	f.handle(&types.StackEvent{EventType: types.STACK_EVENT, Time: at + offset, Pid: f.pid, Tid: f.tid,
		StackId: kid})
	*f.now = at - 10
	f.read(3, 1)
}

// TestStackIDOnItsEnter: a stack record names the syscall whose enter has
// its time, and only that one; a failed walk, a stack stamped for another
// enter and a call with no stack record carry no stack_id.
func TestStackIDOnItsEnter(t *testing.T) {
	c, sink, f := newStackTest(t, &fakeStacks{})
	f.stackThenRead(5, 0)   // paired: stack 105
	f.read(3, 1)            // no stack record
	f.stackThenRead(-14, 0) // walk failed
	f.stackThenRead(6, 3)   // stamped for another enter
	f.read(3, 1)            // the stale stack must not leak here
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	reads := byKind(sink.events, schema.KindRead)
	want := []uint32{105, 0, 0, 0, 0}
	if len(reads) != len(want) {
		t.Fatalf("%d reads, want %d", len(reads), len(want))
	}
	for i, r := range reads {
		got := uint32(0)
		if r.StackID != nil {
			got = *r.StackID
		}
		if got != want[i] {
			t.Errorf("read %d: stack_id %d, want %d (0 = none)", i, got, want[i])
		}
	}
}

// TestStacksIgnoredWithoutTable: stack records of a session without a
// table (they never come then, but must not break anything) are dropped.
func TestStacksIgnoredWithoutTable(t *testing.T) {
	c, sink, f := newStackTest(t, nil)
	f.stackThenRead(5, 0)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if r := byKind(sink.events, schema.KindRead); len(r) != 1 || r[0].StackID != nil {
		t.Fatalf("reads %+v, want one without stack_id", r)
	}
}

// TestStackTableErrorEndsHandle: the table's write error is the trace's.
func TestStackTableErrorEndsHandle(t *testing.T) {
	boom := errors.New("disk full")
	c, _, f := newStackTest(t, &fakeStacks{fail: boom})
	err := c.Handle(&types.StackEvent{EventType: types.STACK_EVENT, Time: 1, Pid: f.pid, Tid: f.tid})
	if !errors.Is(err, boom) {
		t.Fatalf("Handle = %v, want %v", err, boom)
	}
}

// TestExecTellsTheStackTable: an exec starts a new image for the table.
func TestExecTellsTheStackTable(t *testing.T) {
	st := &fakeStacks{}
	_, _, f := newStackTest(t, st)
	f.handle(&types.ProcessExecEvent{EventType: types.PROCESS_EXEC_EVENT, Time: f.tick(), Pid: 7})
	if len(st.execs) != 1 || st.execs[0] != 7 {
		t.Fatalf("execs %v, want [7]", st.execs)
	}
}
