package ctl

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"iormesh/internal/viewmodel"
)

// TestThreadsAndProcessesGolden builds the microservices mesh: `show
// thread` lists the conversation's threads, `show thread
// orders:8080/worker-3` prints orders' worker as one story — received
// the checkout's call, read its config, called inventory, wrote and
// renamed the order, charged billing, replied — and `show processes`
// prints each process with its threads, files and sockets; all match
// their goldens.
func TestThreadsAndProcessesGolden(t *testing.T) {
	f := newMicroFixture(t, true)
	golden(t, "thread_list.golden", f.mustRun(t, "show", "thread"))
	story := f.mustRun(t, "show", "thread", "orders:8080/worker-3")
	golden(t, "thread_story.golden", story)
	golden(t, "processes.golden", f.mustRun(t, "show", "processes"))
	var ts []viewmodel.ThreadStory
	if err := json.Unmarshal([]byte(f.mustRun(t, "show", "thread", "orders:8080/worker-3", "-json")), &ts); err != nil {
		t.Fatal(err)
	}
	if len(ts) != 1 || ts[0].Thread != "worker-3" || len(ts[0].Entries) == 0 {
		t.Fatalf("thread json: %+v", ts)
	}
	// One owned span on one thread: every entry after the first is in
	// the thread's program order.
	for i, e := range ts[0].Entries[1:] {
		if e.Ordering != viewmodel.OrderCausal {
			t.Errorf("entry %d: %s, want causal", i+2, e.Ordering)
		}
	}
	if code := f.run("show", "thread", "orders:8080/nobody"); code == exitOK {
		t.Error("show thread of an unknown thread succeeded")
	}
	if !bytes.Contains(f.stderr.Bytes(), []byte("no such thread")) {
		t.Errorf("stderr: %s", f.stderr)
	}
	if !strings.Contains(story, "received GET /orders/42 from java") {
		t.Errorf("story:\n%s", story)
	}
}
