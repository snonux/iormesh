package correlate

import (
	"errors"
	"fmt"
	"testing"

	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// fakeStacks hands out kernel id + 100 as the trace id, refuses negative
// ids like internal/stacks.Table, and records the forgotten pids and the
// image tags it was asked about.
type fakeStacks struct {
	forgot []uint32
	tags   []uint64
	fail   error
}

func (s *fakeStacks) ID(_ uint32, _ uint64, kid int32, tag uint64) (uint32, bool, error) {
	s.tags = append(s.tags, tag)
	if s.fail != nil {
		return 0, false, s.fail
	}
	if kid < 0 {
		return 0, false, nil
	}
	return uint32(kid) + 100, true, nil
}

func (s *fakeStacks) Forget(pid uint32) { s.forgot = append(s.forgot, pid) }

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
		StackId: kid, Image: 0x7ffd0000})
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

// TestStackTableForgetsExecAndExit: an exec and a whole process's exit
// let the table drop the process (a thread's exit does not), a dead
// thread's parked stack is dropped, and the record's image tag reaches
// the table.
func TestStackTableForgetsExecAndExit(t *testing.T) {
	st := &fakeStacks{}
	c, _, f := newStackTest(t, st)
	f.stackThenRead(5, 0)
	if len(st.tags) != 1 || st.tags[0] != 0x7ffd0000 {
		t.Fatalf("tags %#x, want the record's", st.tags)
	}
	f.handle(&types.ProcessExecEvent{EventType: types.PROCESS_EXEC_EVENT, Time: f.tick(), Pid: 7})
	f.handle(&types.StackEvent{EventType: types.STACK_EVENT, Time: f.tick(), Pid: 7, Tid: 8, StackId: 1})
	f.handle(&types.ProcessExitEvent{EventType: types.PROCESS_EXIT_EVENT, Time: f.tick(), Pid: 7, Tid: 8})
	if _, parked := c.stacks[8]; parked {
		t.Fatal("a dead thread's stack stays parked")
	}
	f.handle(&types.ProcessExitEvent{EventType: types.PROCESS_EXIT_EVENT, Time: f.tick(), Pid: 7, Tid: 7,
		GroupDead: 1})
	if len(st.forgot) != 2 || st.forgot[0] != 7 || st.forgot[1] != 7 {
		t.Fatalf("forgot %v, want [7 7] (exec, then the group's death)", st.forgot)
	}
}
