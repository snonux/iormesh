package correlate

import (
	"testing"

	"iormesh/internal/agentspool"
	"iormesh/internal/schema"
)

// TestJavaJoinHTTPThreads (task mb4 review): an http event names its
// thread as a syscall's join does — a platform thread by name; a virtual
// thread's request flagged carrier and named after its carrier, whose
// span other virtual threads share; a thread whose THREAD record never
// came not at all — and says held only for a held record with a start.
func TestJavaJoinHTTPThreads(t *testing.T) {
	jt := newJoinTest(t)
	at := jt.now + 1000
	const carrier = 90
	held := jt.request(at+3000, "/held", "", "")
	held.StartMono, held.Held = jt.mono(at+1000), true
	startless := jt.request(at+3100, "/startless", "", "")
	startless.Held = true
	virt := jt.request(at+3200, "/virtual", "", "")
	virt.ThreadID, virt.KTID, virt.StartMono, virt.Held = 3, carrier, jt.mono(at+1000), true
	lost := jt.request(at+3300, "/lost", "", "")
	lost.ThreadID = 9
	jt.ring(agentspool.Thread{ID: 2, KTID: carrier, Name: "ForkJoinPool-1-worker-1"},
		agentspool.Thread{ID: 3, KTID: carrier, Virtual: true},
		held, startless, virt, lost, jt.watermark(at+20_000_000))
	jt.event(jvmPID, mainTID, "read", at+500) // names the project and host
	jt.close()
	byPath := map[string]schema.Event{}
	for _, ev := range jt.sink.events {
		if ev.HTTP != nil {
			byPath[ev.HTTP.Path] = ev
		}
	}
	for path, want := range map[string]struct {
		thread        string
		carrier, held bool
	}{"/held": {"main", false, true}, "/startless": {"main", false, false},
		"/virtual": {"ForkJoinPool-1-worker-1", true, true}, "/lost": {"", false, false}} {
		ev, ok := byPath[path]
		if !ok || ev.Java == nil {
			t.Fatalf("%s: no java event in %+v", path, jt.sink.events)
		}
		if ev.Java.Thread != want.thread || ev.Java.Carrier != want.carrier || ev.HTTP.Held != want.held {
			t.Errorf("%s: thread %q carrier %v held %v, want %+v", path, ev.Java.Thread,
				ev.Java.Carrier, ev.HTTP.Held, want)
		}
	}
}
