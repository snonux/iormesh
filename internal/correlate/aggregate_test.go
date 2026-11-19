package correlate

import (
	"testing"

	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// folded hands the correlator a rw_aggregate_event of count calls on fd by the feeder's
// thread, running from first to last (boot ns).
func (f feeder) folded(id types.TraceId, fd int32, count uint32, bytes, first, last uint64) {
	f.t.Helper()
	f.handle(&types.RwAggregateEvent{EventType: types.RW_AGGREGATE_EVENT, TraceId: id,
		Time: first, EndTime: last, TotalBytes: bytes, LatencyNs: 7, Pid: f.pid, Tid: f.tid,
		Fd: fd, Count: count})
}

// TestAggregateEvents: a folded record becomes one read or write event with
// the sums and args.count; a single call becomes a plain event, the same
// as one sent unfolded.
func TestAggregateEvents(t *testing.T) {
	c, sink := newTest(t)
	now := uint64(0)
	f := feeder{t, c, 10, 11, &now}
	f.open("/tmp/a.dat", 577, 5)
	f.folded(types.SYS_ENTER_WRITE, 5, 1000, 4000, 100, 900)
	f.folded(types.SYS_ENTER_READ, 5, 1, 3, 1000, 1010)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	w, r := byKind(sink.events, schema.KindWrite), byKind(sink.events, schema.KindRead)
	if len(w) != 1 || len(r) != 1 {
		t.Fatalf("events %+v: want one write and one read", sink.events)
	}
	if w[0].Args.Count == nil || *w[0].Args.Count != 1000 || w[0].Bytes != 4000 || w[0].Ret != 4000 ||
		w[0].LatencyNS != 7 || w[0].BootNS != 100 || w[0].FDGen != 1 || w[0].TID != 11 {
		t.Fatalf("folded write %+v", w[0])
	}
	if r[0].Args.Count != nil || r[0].Bytes != 3 || r[0].FDGen != 1 {
		t.Fatalf("single read %+v: want no count", r[0])
	}
}

// TestAggregateLateAfterReuse: a record flushed after another thread closed
// the fd and reopened the number names the instance its calls ran on — by
// time — not the number's current one; one no instance explains is counted
// and dropped, never put on the wrong file.
func TestAggregateLateAfterReuse(t *testing.T) {
	c, sink := newTest(t)
	now := uint64(0)
	reader := feeder{t, c, 10, 11, &now}
	other := feeder{t, c, 10, 12, &now}
	reader.open("/tmp/a.dat", 0, 5) // gen 1, born at the open's exit
	first := now + 5
	now += 100
	other.close(5)
	other.open("/tmp/b.dat", 0, 5) // gen 2
	reader.folded(types.SYS_ENTER_READ, 5, 50, 50, first, first+20)
	reader.folded(types.SYS_ENTER_READ, 5, 2, 2, now+5, now+8) // on gen 2
	other.close(5)
	other.open("/tmp/c.dat", 0, 5) // gen 3: gen 1 is no longer the past one
	reader.folded(types.SYS_ENTER_READ, 5, 9, 9, first, first+20)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	reads := byKind(sink.events, schema.KindRead)
	if len(reads) != 2 || reads[0].FDGen != 1 || reads[1].FDGen != 2 {
		t.Fatalf("reads %+v: want gen 1 (late) then gen 2", reads)
	}
	if c.Unattributed() != 1 {
		t.Fatalf("unattributed %d, want 1", c.Unattributed())
	}
	assertUnattributedDrop(t, sink.events, 9, first)
}

// assertUnattributedDrop requires exactly one unattributed drop, of lost
// calls, naming the reader thread at the record's first call.
func assertUnattributedDrop(t *testing.T, events []schema.Event, lost, at uint64) {
	t.Helper()
	drops := byKind(events, schema.KindDrop)
	if len(drops) != 1 {
		t.Fatalf("drops %+v: want one", drops)
	}
	d := drops[0]
	if d.Args.DropReason == nil || *d.Args.DropReason != schema.DropUnattributed || *d.Args.Lost != lost ||
		d.PID != 10 || d.TID != 11 || d.BootNS != at {
		t.Fatalf("drop %+v (reason %v, lost %d): want unattributed, lost %d, tid 11 at %d",
			d, d.Args.DropReason, *d.Args.Lost, lost, at)
	}
}

// TestAggregateStraddlingAReuse: another thread's dup2 or close+open over
// the number while the loop ran makes one record span two instances (the
// kernel keys by number); it is an unattributed drop, not a sum credited to
// either instance.
func TestAggregateStraddlingAReuse(t *testing.T) {
	c, sink := newTest(t)
	now := uint64(0)
	reader := feeder{t, c, 10, 11, &now}
	other := feeder{t, c, 10, 12, &now}
	reader.open("/tmp/a.dat", 0, 5)
	first := now + 5
	now += 100
	other.close(5)
	other.open("/tmp/b.dat", 0, 5)
	reader.folded(types.SYS_ENTER_READ, 5, 40, 40, first, now+50)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if reads := byKind(sink.events, schema.KindRead); len(reads) != 0 {
		t.Fatalf("reads %+v: a straddling record must not be credited", reads)
	}
	assertUnattributedDrop(t, sink.events, 40, first)
}

// TestAggregateOnClosedNumber: a record of calls made before the fd's close
// arrives after it and names the closed instance; calls after the close
// came on a number an untraced call reused (use()'s new instance).
func TestAggregateOnClosedNumber(t *testing.T) {
	c, sink := newTest(t)
	now := uint64(0)
	f := feeder{t, c, 10, 11, &now}
	other := feeder{t, c, 10, 12, &now}
	f.open("/tmp/a.dat", 0, 5)
	first := now + 1
	other.close(5)
	f.folded(types.SYS_ENTER_READ, 5, 3, 3, first, first+2)
	f.folded(types.SYS_ENTER_READ, 5, 3, 3, now+50, now+60)
	f.folded(types.SYS_ENTER_READ, 7, 3, 3, now+70, now+80) // never seen: predates the trace
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	reads := byKind(sink.events, schema.KindRead)
	if len(reads) != 3 || reads[0].FDGen != 1 || reads[1].FDGen != 2 || reads[2].FDGen != 0 {
		t.Fatalf("reads %+v: want gen 1, then 2 (reused), then 0", reads)
	}
}

// TestInstanceAtFork: a forked child keeps the parent's instance times, so
// a late record of the child's calls on an inherited fd still resolves.
func TestInstanceAtFork(t *testing.T) {
	c, _ := newTest(t)
	now := uint64(0)
	f := feeder{t, c, 10, 10, &now}
	f.open("/tmp/a.dat", 0, 5)
	at := now + 1
	f.fork(20)
	child := c.proc(20)
	if gen, _, ok := child.instanceAt(5, at, at+1); !ok || gen != 1 {
		t.Fatalf("child instanceAt = %d, %v; want gen 1", gen, ok)
	}
	if _, _, ok := child.instanceAt(5, 0, 1); ok {
		// Before the open: neither the live instance nor a past one.
		t.Fatal("a time before the fd's creation resolved")
	}
}
