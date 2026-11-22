package correlate

import (
	"testing"

	"iormesh/internal/agentspool"
	"iormesh/internal/schema"
)

// strangerPID is a process the session does not trace: no event names it.
const strangerPID = 4242

// attach sends the attach event of pid, incarnation inc, as the
// correlator emits it when the session takes the process on.
func (jt *joinTest) attach(pid uint32, inc uint64) {
	jt.t.Helper()
	jt.seq++
	ev := schema.Event{SchemaVersion: schema.Version, Project: "p", Seq: jt.seq, BootNS: jt.now,
		HostID: "h", PID: pid, PIDInc: inc, TID: pid, Comm: "java", Event: schema.KindAttach,
		FD: schema.NoFD, Attribution: schema.AttributionNone}
	if err := jt.j.Event(&ev); err != nil {
		jt.t.Fatal(err)
	}
}

// loudRing is a ring with everything that reaches the trace or the
// tracer: the hook table (and so an attach), a thread row, an http and a
// dns event.
func (jt *joinTest) loudRing(pid uint32, at uint64) *fakeRing {
	r := jt.ring(jt.request(at+1000, "/transfer", "127.0.0.1:40000", "127.0.0.1:8080"),
		agentspool.DNS{ExitMono: jt.mono(at + 1000), StackID: 10, ThreadID: 1, KTID: mainTID,
			Host: "db.internal", Addrs: []string{"6.6.6.6"}},
		jt.watermark(at+20_000_000))
	r.hdr.PID = uint64(pid)
	return r
}

// probes counts the join's AttachMarks calls.
func (jt *joinTest) probes() *[]uint32 {
	var pids []uint32
	jt.j.cfg.AttachMarks = func(pid uint32, _ []MarkedNative) ([]string, error) {
		pids = append(pids, pid)
		return nil, nil
	}
	return &pids
}

// TestJavaJoinIgnoresUntracedRing (task yi4): a ring of a process no
// event names — another user's forged file, or a JVM outside the target
// that inherited JAVA_TOOL_OPTIONS — puts no http or dns event, no table
// row into the trace and gets no uprobe attached; it is closed at the end.
func TestJavaJoinIgnoresUntracedRing(t *testing.T) {
	jt := newJoinTest(t)
	probed := jt.probes()
	at := jt.now + 1000
	r := jt.loudRing(strangerPID, at)
	jt.event(jvmPID, mainTID, "write", at)
	jt.now += 100_000_000
	if err := jt.j.Poll(); err != nil {
		t.Fatal(err)
	}
	jt.event(999, 1000, "write", at+30_000_000)
	jt.close()
	for _, ev := range jt.sink.events {
		if ev.PID == strangerPID || ev.Event == schema.KindHTTP || ev.Event == schema.KindDNS {
			t.Errorf("event %s of pid %d from an untraced ring", ev.Event, ev.PID)
		}
	}
	if len(jt.tables.threads)+len(jt.tables.methods)+len(jt.tables.classes) != 0 {
		t.Errorf("table rows %+v %+v %+v", jt.tables.threads, jt.tables.methods, jt.tables.classes)
	}
	if len(*probed) != 0 {
		t.Errorf("probed pids %v", *probed)
	}
	if s := jt.j.Stats(); s.Rings != 0 || s.HTTP != 0 || s.DNS != 0 {
		t.Errorf("stats %+v", s)
	}
	if !r.closed {
		t.Error("held ring not closed")
	}
}

// TestJavaJoinHoldsRingUntilTraced (task yi4): a ring that appears before
// its process is traced (the agent loaded at exec by JAVA_TOOL_OPTIONS,
// mapped before the session's attach event passes) is held unread — no
// probe, no row — and read once an event of that very incarnation
// passes; an event of another incarnation under the pid does not count.
func TestJavaJoinHoldsRingUntilTraced(t *testing.T) {
	jt := newJoinTest(t)
	probed := jt.probes()
	at := jt.now + 1000
	r := jt.loudRing(jvmPID, at)
	for range 2 {
		if err := jt.j.Poll(); err != nil {
			t.Fatal(err)
		}
	}
	jt.attach(jvmPID, jvmStart+1) // a former process under the pid
	if err := jt.j.Poll(); err != nil {
		t.Fatal(err)
	}
	if len(*probed) != 0 || len(jt.tables.threads) != 0 || len(r.queue) == 0 {
		t.Fatalf("untraced ring read: probed %v, threads %+v, %d records left",
			*probed, jt.tables.threads, len(r.queue))
	}
	jt.attach(jvmPID, jvmStart)
	if err := jt.j.Poll(); err != nil {
		t.Fatal(err)
	}
	if len(*probed) != 1 || len(jt.tables.threads) != 1 {
		t.Errorf("traced ring not read: probed %v, threads %+v", *probed, jt.tables.threads)
	}
	jt.close()
	if s := jt.j.Stats(); s.Rings != 1 || s.HTTP != 1 || s.DNS != 1 {
		t.Errorf("stats %+v", s)
	}
}
