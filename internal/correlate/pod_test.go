package correlate

import (
	"fmt"
	"strings"
	"testing"

	"iormesh/internal/agentspool"
	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// Containers of the test pod: the app's and the sidecar's (task sh4).
var (
	appCtr     = strings.Repeat("a1", 32)
	sidecarCtr = strings.Repeat("b2", 32)
)

const podHostID = "pod-3b1c9a52-7e0d-4c1f-9a8e-2f6d5b4c3a21"

// newPodTest is newTest for an in-pod tracer: pid 1 and its children run
// in the app container, pid 7 in the sidecar, pid 9 in none (the
// tracer's own); reads counts the cgroup reads per pid.
func newPodTest(t *testing.T) (*Correlator, *memSink, map[uint32]int) {
	sink := &memSink{t: t}
	reads := map[uint32]int{}
	containers := map[uint32]string{1: appCtr, 50: appCtr, 7: sidecarCtr, 60: sidecarCtr}
	c := New(Config{Host: Host{ID: podHostID, Project: "p"}, Sink: sink,
		ProcInfo: func(pid uint32) (string, uint64) { return fmt.Sprintf("proc%d", pid), 1000 + uint64(pid) },
		Pod:      &PodConfig{Namespace: "prod", Name: "web-7d9f"},
		ContainerOf: func(pid uint32) string {
			reads[pid]++
			return containers[pid]
		}})
	return c, sink, reads
}

// TestPodFieldsOnEvents: every event of an in-pod tracer names the pod,
// a process's events also its container — read once per process from
// its cgroup when the correlator meets it (attach, first record), and
// inherited by a forked child without a read; host-level events and a
// process in no container carry the pod alone.
func TestPodFieldsOnEvents(t *testing.T) {
	c, sink, reads := newPodTest(t)
	if err := c.Session(schema.Session{BootID: "b", FQDN: "web-7d9f.prod.3b1c9a52", Kernel: "k",
		Arch: "x86_64"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Seed(1, testIdentity(1)); err != nil {
		t.Fatal(err)
	}
	now := uint64(0)
	app := feeder{t, c, 1, 1, &now}
	app.open("/tmp/a", 0, 5)
	app.read(5, 10)
	app.handle(&types.TaskNewtaskEvent{EventType: types.TASK_NEWTASK_EVENT, Time: app.tick(), Pid: 50,
		Tid: 50, CreatorPid: 1})
	feeder{t, c, 50, 50, &now}.close(5)
	side := feeder{t, c, 7, 7, &now}
	side.open("/tmp/b", 0, 3)
	side.close(3)
	feeder{t, c, 9, 9, &now}.open("/tmp/c", 0, 4)
	if err := c.RecordLosses(2); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	for _, e := range sink.events {
		want := map[uint32]string{1: appCtr, 50: appCtr, 7: sidecarCtr}[e.PID]
		if e.Pod == nil || e.Pod.Namespace != "prod" || e.Pod.Name != "web-7d9f" || e.Pod.ContainerID != want {
			t.Errorf("%s of pid %d: pod %+v, want prod/web-7d9f container %q", e.Event, e.PID, e.Pod, want)
		}
	}
	if want := map[uint32]int{1: 1, 7: 1, 9: 1}; fmt.Sprint(reads) != fmt.Sprint(want) {
		t.Errorf("cgroup reads %v, want %v (once per process, none for the forked child)", reads, want)
	}
}

// TestJavaJoinDNSCarriesPod: a JVM's dns event, which the Java join
// builds itself, carries the pod object of the JVM's joined events — its
// container included — like every other event of an in-pod tracer.
func TestJavaJoinDNSCarriesPod(t *testing.T) {
	jt := newJoinTest(t)
	jt.pod = &schema.Pod{Namespace: "prod", Name: "web-7d9f", ContainerID: appCtr}
	at := jt.now + 1000
	jt.ring(agentspool.DNS{ExitMono: jt.mono(at + 1000), StackID: 10, ThreadID: 1, KTID: mainTID,
		Host: "localhost", Addrs: []string{"127.0.0.1"}}, jt.watermark(at+20_000_000))
	jt.event(jvmPID, mainTID, "read", at+500)
	jt.event(jvmPID, mainTID, "write", at+2000)
	jt.close()
	dns := byKind(jt.sink.events, schema.KindDNS)
	if len(dns) != 1 || dns[0].Pod == nil || *dns[0].Pod != *jt.pod {
		t.Fatalf("dns events %+v: want one carrying %+v", dns, jt.pod)
	}
}

// TestNoPodOutsideAPod: without Config.Pod no event has a pod object and
// no cgroup is read.
func TestNoPodOutsideAPod(t *testing.T) {
	sink := &memSink{t: t}
	reads := 0
	c := New(Config{Host: Host{ID: "h1", Project: "p"}, Sink: sink,
		ProcInfo:    func(pid uint32) (string, uint64) { return "x", 1000 },
		ContainerOf: func(uint32) string { reads++; return appCtr }})
	now := uint64(0)
	feeder{t, c, 1, 1, &now}.open("/tmp/a", 0, 5)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	for _, e := range sink.events {
		if e.Pod != nil {
			t.Errorf("%s: pod %+v outside a pod", e.Event, e.Pod)
		}
	}
	if reads != 0 {
		t.Errorf("%d cgroup reads outside a pod", reads)
	}
}
