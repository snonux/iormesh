package viewmodel

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"iormesh/internal/report"
	"iormesh/internal/schema"
)

// stories builds r's conversations and thread stories with an empty
// namer.
func stories(r *Recorder) ([]Conversation, []ThreadStory) {
	return r.conversations(newNamer(schema.Graph{}, nil, nil))
}

// TestCarrierLifeline: a virtual thread's request, named by its carrier
// (java.carrier), sits on a lifeline of its own — never the carrier's
// platform lifeline, which its own request uses — and the carrier's
// story of its two interleaved virtual threads is all unordered, with
// no one-line chain — virtual threads migrate between carriers, so
// nothing proves their order on this one (task 0f4 review); no span of it is
// the thread's alone.
func TestCarrierLifeline(t *testing.T) {
	virt := httpEv("a", 5, base, base+4*ms, "c1", true, 1)
	virt.Java.Carrier, virt.HTTP.Held = true, false
	virt2 := httpEv("a", 5, base+ms, base+3*ms, "c3", true, 1)
	virt2.Java.Carrier, virt2.HTTP.Held = true, false
	own := httpEv("a", 5, base+10*ms, base+12*ms, "c2", true, 1)
	evs := []*schema.Event{virt, httpEv("b", 7, base+ms, base+2*ms, "c1", false, 1), virt2,
		own, httpEv("b", 8, base+10*ms+1, base+11*ms, "c2", false, 1)}
	cs, ts := stories(record(nil, evs, nil))
	carrierLanes, platformLanes := 0, 0
	for _, c := range cs {
		for _, l := range c.Lifelines {
			if l.TID == 5 && l.Carrier {
				carrierLanes++
			} else if l.TID == 5 {
				platformLanes++
			}
		}
		if lane := c.Lifelines[c.Steps[0].Lane]; lane.TID != 5 {
			t.Errorf("conversation %s: first step on %+v", c.ID, lane)
		}
	}
	if carrierLanes != 2 || platformLanes != 1 {
		t.Fatalf("lanes: %d carrier, %d platform; want 2 and 1", carrierLanes, platformLanes)
	}
	var carrier, platform *ThreadStory
	for i := range ts {
		switch {
		case ts[i].TID == 5 && ts[i].Carrier:
			carrier = &ts[i]
		case ts[i].TID == 5:
			platform = &ts[i]
		}
	}
	if carrier == nil || platform == nil || len(carrier.Entries) != 4 || len(platform.Entries) != 2 {
		t.Fatalf("stories: carrier %+v, platform %+v", carrier, platform)
	}
	for _, e := range carrier.Entries {
		if e.Ordering != OrderNone {
			t.Errorf("carrier entry %+v is %s, want unordered", e, e.Ordering)
		}
	}
	var out bytes.Buffer
	if err := WriteThreads(&out, []ThreadStory{*carrier}); err != nil ||
		!strings.Contains(out.String(), "not its own work") ||
		!strings.Contains(out.String(), "in no provable order") || strings.Contains(out.String(), " => ") {
		t.Errorf("carrier story text (%v):\n%s", err, &out)
	}
}

// TestIncarnationsApart: a restarted process reusing its pid and tid is
// another lifeline — its steps are never one story with the earlier
// incarnation's, ordered across both processes.
func TestIncarnationsApart(t *testing.T) {
	first := httpEv("a", 7, base, base+ms, "c1", true, 1)
	second := httpEv("a", 7, base+5*ms, base+6*ms, "c2", true, 1)
	second.PIDInc = 2
	_, ts := stories(record(nil, []*schema.Event{first, second}, nil))
	if len(ts) != 2 || ts[0].Incarnation == ts[1].Incarnation || len(ts[0].Entries) != 2 ||
		len(ts[1].Entries) != 2 {
		t.Fatalf("stories: %+v", ts)
	}
}

// TestStoryOrder: a thread's entries are causal only inside one owned
// span; across spans the thread's own clock orders them when it moved
// on, and a tie is unordered.
func TestStoryOrder(t *testing.T) {
	ms := []storyMark{{wall: 10, span: 1}, {wall: 20, span: 1}, {wall: 20, span: 2}, {wall: 20, span: -1},
		{wall: 30, span: -1}, {wall: 30, span: -1}}
	want := []string{OrderTime, OrderCausal, OrderNone, OrderNone, OrderTime, OrderNone}
	for i, w := range want {
		if got := storyOrder(ms, i); got != w {
			t.Errorf("mark %d: %s, want %s", i, got, w)
		}
	}
}

// TestStoryCap: a story keeps its newest MaxThreadEntries entries in
// clock order and counts the older ones and those of cut steps.
func TestStoryCap(t *testing.T) {
	b := newStoryBook()
	lane := Lifeline{Service: "s", Thread: "t", TID: 1}
	for i := range MaxThreadEntries + 5 {
		// Marked newest first, as conversations are emitted.
		b.mark("0001", i+1, Step{Kind: StepFile, Label: "f"}, end{lane: lane,
			wall: uint64(MaxThreadEntries + 5 - i), span: -1}, noEnd)
	}
	b.cut(lane, Lifeline{Service: "peer"})
	st := b.stories()
	if len(st) != 1 || len(st[0].Entries) != MaxThreadEntries || st[0].MoreEntries != 6 {
		t.Fatalf("cap: %d stories, %d entries, %d more", len(st), len(st[0].Entries), st[0].MoreEntries)
	}
	if first := st[0].Entries[0]; first.Step != MaxThreadEntries {
		t.Errorf("the oldest kept entry is step %d, want %d", first.Step, MaxThreadEntries)
	}
}

// TestProcessesPartial (task 2i4): a rollup that hit its tracking cap
// carries its untracked count into the model, and the processes view
// says the list is partial; a complete rollup prints no such line.
func TestProcessesPartial(t *testing.T) {
	for _, untracked := range []uint64{0, 84} {
		m := New(Input{Report: &report.Report{Untracked: untracked, Processes: []report.Process{
			{ID: "process:h/7@1", Host: "h", PID: 7, Incarnation: 1, Comm: "java"}}}})
		if m.Untracked != untracked {
			t.Errorf("model untracked %d, want %d", m.Untracked, untracked)
		}
		var out bytes.Buffer
		if err := WriteProcesses(&out, m); err != nil {
			t.Fatal(err)
		}
		want := "partial    84 events past the rollup's tracking cap are not in it"
		if got := strings.Contains(out.String(), want); got != (untracked > 0) {
			t.Errorf("untracked %d: partial line shown %v:\n%s", untracked, got, &out)
		}
	}
}

// TestProcessesProjection: the processes view projects the rollup —
// incarnations of one pid counted, a carrier's virtual-thread I/O kept
// apart and said so, every printed string terminal-safe — and a build
// without a rollup has an empty list, not null.
func TestProcessesProjection(t *testing.T) {
	r := &report.Report{MoreProcesses: 3, Processes: []report.Process{
		{ID: "process:h/7@1", Host: "h", PID: 7, Incarnation: 1, Comm: "java\x1b[2J"},
		{ID: "process:h/7@2", Host: "h", PID: 7, Incarnation: 2, Comm: "java", Threads: []report.Thread{
			{Name: "carrier-1", TID: 9, Events: 5, Carried: 4, Calls: []report.Call{{Carrier: true}, {}}}},
			// Connected to itself: both ends in one process, one per fd.
			Conns: []report.Conn{{ID: "m-c1", FD: 5, Role: report.RoleClient},
				{ID: "m-c1", FD: 6, Role: report.RoleServer}}},
	}}
	ps, more := processes(r, newNamer(schema.Graph{}, nil, nil))
	if len(ps) != 2 || more != 3 || ps[0].Incarnations != 2 || ps[1].Service != "pid 7" {
		t.Fatalf("processes: %+v, %d more", ps, more)
	}
	if th := ps[1].Threads[0]; th.Carried != 4 || th.CarriedCalls != 1 || th.Calls != 2 {
		t.Errorf("carrier thread: %+v", th)
	}
	var out bytes.Buffer
	if err := WriteProcesses(&out, Model{Processes: ps, MoreProcesses: more}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"m-c1 fd 5 client", "m-c1 fd 6 server", "2 incarnations of pid 7 on this host (restart or reused pid)", "not its own", "truncated: 3 more processes"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("text lacks %q:\n%s", want, &out)
		}
	}
	if bytes.ContainsRune(out.Bytes(), 0x1b) {
		t.Error("a raw escape reached the text")
	}
	if ps, _ := processes(nil, namer{}); ps == nil {
		t.Error("no rollup: nil processes")
	}
}

// TestThreadViewsTerminalSafe: names and labels from the traces reach
// the thread views escaped, and an unknown SERVICE/THREAD is an error.
func TestThreadViewsTerminalSafe(t *testing.T) {
	st := ThreadStory{Lifeline: Lifeline{Service: "s\x1b]0;x\x07", Thread: "t\x1b[2J"},
		Entries: []ThreadEntry{{Conversation: "0001", Step: 1, Act: ActReceive, Label: "GET /\x1b[31m",
			Peer: "p\x9b", Ordering: OrderTime, Ref: "r"}}}
	var out bytes.Buffer
	if err := WriteThreads(&out, []ThreadStory{st}); err != nil {
		t.Fatal(err)
	}
	if err := WriteThreadList(&out, Model{Threads: []ThreadStory{st}}); err != nil {
		t.Fatal(err)
	}
	if bytes.ContainsAny(out.Bytes(), "\x1b\x07\u009b") {
		t.Errorf("a raw control character reached the text:\n%q", &out)
	}
	if _, err := FindThreads(Model{Threads: []ThreadStory{st}}, "s/t"); !errors.Is(err, ErrNoThread) {
		t.Errorf("unknown thread: %v", err)
	}
}
