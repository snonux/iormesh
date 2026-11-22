package correlate

import (
	"testing"

	"iormesh/internal/agentspool"
)

// pollAt moves the test clock to now and polls.
func (jt *joinTest) pollAt(now uint64) {
	jt.t.Helper()
	jt.now = now
	if err := jt.j.Poll(); err != nil {
		jt.t.Fatal(err)
	}
}

// TestJavaJoinPrunesVirtualThreads (task 2j4): a virtual thread's entry
// goes once the watermark left its last bracket Retain behind — a
// virtual-thread-per-request service otherwise keeps one per request
// for the session. Platform threads, and a virtual thread still active,
// stay.
func TestJavaJoinPrunesVirtualThreads(t *testing.T) {
	jt := newJoinTest(t)
	at := jt.now + 1000
	const carrier, n = 90, 1000
	recs := []agentspool.Record{agentspool.Thread{ID: 2, KTID: carrier,
		Name: "ForkJoinPool-1-worker-1"}}
	for i := range uint64(n) {
		id := 100 + i
		recs = append(recs, agentspool.Thread{ID: id, KTID: carrier, Virtual: true},
			jt.bracket(1, carrier, at+i*10, at+i*10+5, 10, id))
	}
	recs = append(recs, agentspool.Thread{ID: 99, KTID: carrier, Virtual: true, Name: "a"},
		agentspool.Thread{ID: 99, KTID: carrier, Virtual: true, Name: "b"}) // a rename
	late := at + 2_000_000
	recs = append(recs, agentspool.Thread{ID: 5000, KTID: carrier, Virtual: true},
		jt.bracket(1, carrier, late, late+5, 10, 5000),
		jt.watermark(at+defaultRetain+1_000_000))
	jt.ring(recs...)
	jt.event(jvmPID, mainTID, "write", at)
	v := jt.j.jvms[jvmPID]
	if v == nil {
		t.Fatal("ring not accepted")
	}
	if len(v.threads) != 3 || len(v.renames) != 0 {
		t.Errorf("%d threads kept (want main, the carrier and the active one), %d renames",
			len(v.threads), len(v.renames))
	}
	for _, id := range []uint64{1, 2, 5000} {
		if _, ok := v.threads[id]; !ok {
			t.Errorf("thread %d dropped", id)
		}
	}
	if len(jt.tables.threads) != n+5 {
		t.Errorf("%d thread rows, want each written once", len(jt.tables.threads))
	}
	jt.close()
}

// exitTest is a JVM with a bracket and a held write, and an exit of it.
func exitTest(t *testing.T) (*joinTest, *fakeRing, uint64) {
	jt := newJoinTest(t)
	at := jt.now + 1000
	r := jt.ring(jt.bracket(1, mainTID, at-50, at+500, 10, 1))
	jt.event(jvmPID, mainTID, "write", at)
	exit := at + 1000
	jt.j.Exited(jvmPID, exit)
	return jt, r, exit
}

// TestJavaJoinRetiresExitedJVM (task 2j4): once its process's group died
// and every event it may still have had upstream is decided, a JVM's
// ring is drained one last time, closed, its file removed and the JVM
// forgotten — before, it stayed mapped and drained for the session.
func TestJavaJoinRetiresExitedJVM(t *testing.T) {
	jt, r, exit := exitTest(t)
	jt.pollAt(exit + defaultFuseWindowNS)
	if jt.j.jvms[jvmPID] == nil || r.closed || r.removed {
		t.Fatal("retired before its upstream events could have arrived")
	}
	r.queue = []agentspool.Record{agentspool.Thread{ID: 7, KTID: 80, Name: "late"}}
	jt.pollAt(exit + defaultFuseWindowNS + defaultTimeout)
	if jt.j.jvms[jvmPID] != nil || !r.closed || !r.removed {
		t.Fatalf("exited JVM kept: jvm %v, closed %v, removed %v", jt.j.jvms[jvmPID] != nil,
			r.closed, r.removed)
	}
	if len(r.queue) != 0 || jt.tables.threads[len(jt.tables.threads)-1].Name != "late" {
		t.Error("no last drain")
	}
	if len(jt.sink.events) != 1 || jt.sink.events[0].Attribution != "java" {
		t.Errorf("events %+v", jt.sink.events)
	}
	if jt.j.Stats().Lost.DroppedBrackets != 1 {
		t.Errorf("losses %+v", jt.j.Stats().Lost)
	}
	jt.close()
	if jt.j.Stats().Lost.DroppedBrackets != 1 {
		t.Errorf("losses counted twice: %+v", jt.j.Stats().Lost)
	}
}

// TestJavaJoinRetireWaitsForHeld: an exited JVM's event still held
// (here behind another JVM's, which waits for its own ring) keeps the
// JVM until it is decided — with its brackets.
func TestJavaJoinRetireWaitsForHeld(t *testing.T) {
	jt := newJoinTest(t)
	at := jt.now + 1000
	exit := at + 1000
	other := &fakeRing{hdr: agentspool.Header{Version: agentspool.Version, PID: 88,
		CreatedMono: jt.mono(jt.now)}}
	jt.spool = append(jt.spool, other)
	r := jt.ring(jt.bracket(1, mainTID, at-50, at+500, 10, 1))
	jt.unmarked(88, 89, "write", exit+6_000_000_000) // a socket event held upstream
	jt.event(jvmPID, mainTID, "write", at)
	jt.j.Exited(jvmPID, exit)
	jt.pollAt(exit + defaultFuseWindowNS + defaultTimeout + 1)
	if jt.j.jvms[jvmPID] == nil || r.closed {
		t.Fatal("retired with an event still held")
	}
	jt.pollAt(exit + 9_000_000_000)
	if jt.j.jvms[jvmPID] != nil || !r.closed || len(jt.sink.events) != 2 {
		t.Fatalf("jvm kept %v, events %d", jt.j.jvms[jvmPID] != nil, len(jt.sink.events))
	}
	if jt.sink.events[1].Attribution != "java" {
		t.Error("held event decided without its brackets")
	}
	jt.close()
}

// TestJavaJoinExitOfEarlierProcess: an exit stamped before the ring's
// process started is an earlier process's under the pid: the ring stays.
func TestJavaJoinExitOfEarlierProcess(t *testing.T) {
	jt := newJoinTest(t)
	at := jt.now + 1000
	r := jt.ring(jt.bracket(1, mainTID, at-50, at+500, 10, 1))
	jt.event(jvmPID, mainTID, "write", at)
	jt.j.Exited(jvmPID, jvmStart-1)
	jt.pollAt(jt.now + 100*defaultTimeout)
	if jt.j.jvms[jvmPID] == nil || r.closed || r.removed {
		t.Error("an earlier process's exit retired the ring")
	}
	jt.close()
}

// TestJavaJoinRetiresWaitingRing: a ring never traced whose process
// exited is closed and removed too.
func TestJavaJoinRetiresWaitingRing(t *testing.T) {
	jt := newJoinTest(t)
	r := &fakeRing{hdr: agentspool.Header{Version: agentspool.Version, PID: 99,
		CreatedMono: jt.mono(jt.now)}}
	jt.spool = append(jt.spool, r)
	jt.pollAt(jt.now + defaultScanEvery)
	if len(jt.j.waiting) != 1 {
		t.Fatalf("%d waiting", len(jt.j.waiting))
	}
	jt.j.Exited(99, jt.now)
	jt.pollAt(jt.now + defaultFuseWindowNS + defaultTimeout)
	if len(jt.j.waiting) != 0 || !r.closed || !r.removed {
		t.Errorf("waiting %d, closed %v, removed %v", len(jt.j.waiting), r.closed, r.removed)
	}
	jt.close()
}

// TestJavaJoinClosesReplacedRing: a newer ring of the pid (a restarted
// JVM) has the older one closed at once, its losses counted once.
func TestJavaJoinClosesReplacedRing(t *testing.T) {
	jt := newJoinTest(t)
	at := jt.now + 1000
	old := jt.ring()
	jt.event(jvmPID, mainTID, "write", at)
	jt.ring()
	jt.pollAt(jt.now + defaultScanEvery)
	if !old.closed {
		t.Error("replaced ring kept open")
	}
	jt.close()
	if s := jt.j.Stats(); s.Rings != 2 || s.Lost.DroppedBrackets != 2 {
		t.Errorf("stats %+v", s)
	}
}
