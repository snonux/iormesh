package viewmodel

import (
	"testing"

	"iormesh/internal/schema"
)

// The review's guards against guessed nesting (task mb4): a span nests
// calls and owns file work only when its thread worked on that request
// alone (request.owned).

// TestNoNestingOnSharedThreads: a Netty event loop's overlapping
// requests (not held), two virtual threads on one carrier, and a
// request whose thread is unknown (no java object) nest nothing and own
// no file work, though the spans hold the calls and the file event.
func TestNoNestingOnSharedThreads(t *testing.T) {
	loop := func(e *schema.Event) *schema.Event { e.HTTP.Held = false; return e }
	carrier := func(e *schema.Event) *schema.Event { e.Java.Carrier = true; return e }
	unknown := func(e *schema.Event) *schema.Event {
		e.Java, e.Attribution = nil, schema.AttributionNone
		return e
	}
	for _, c := range []struct {
		name string
		mark func(*schema.Event) *schema.Event
	}{{"netty event loop", loop}, {"virtual threads on one carrier", carrier}, {"unknown thread", unknown}} {
		evs := []*schema.Event{
			c.mark(httpEv("a", 7, base, base+10*ms, "", false, 0)),
			c.mark(httpEv("a", 7, base+ms, base+9*ms, "", false, 0)),
			c.mark(httpEv("a", 7, base+2*ms, base+3*ms, "d", true, 0)),
		}
		file := &schema.Event{Seq: 9, HostID: "a", PID: 1, PIDInc: 1, TID: 7, WallNS: base + 4*ms,
			Event: schema.KindUnlink, FD: schema.NoFD, Args: schema.Args{Path: ptr("/order.json")}}
		r := record(nil, evs, []*schema.Event{file})
		if n := len(r.links.roots); n != 3 {
			t.Errorf("%s: %d roots, want 3 (nothing nested)", c.name, n)
		}
		for _, conv := range r.conversations(newNamer(schema.Graph{}, nil, nil)) {
			for _, s := range conv.Steps {
				if s.Kind == StepFile {
					t.Errorf("%s: file work pinned on a shared span: %+v", c.name, s)
				}
			}
		}
	}
}

// TestNoJoinNearTheCut: once records were cut, a request starting
// before the oldest kept completion (plus the widest clock offset) joins
// no hop — its partner, or a duplicate of it, may be among the cut.
func TestNoJoinNearTheCut(t *testing.T) {
	evs := []*schema.Event{
		httpEv("a", 1, base, base+10*ms, "c", true, 1),
		httpEv("b", 2, base+ms, base+2*ms, "c", false, 1),
		httpEv("a", 3, base+20*ms, base+21*ms, "", false, 0),
		httpEv("a", 4, base-100*ms, base-99*ms, "", false, 0),
	}
	for _, c := range []struct {
		max    int
		cut    uint64
		paired bool
	}{{4, 0, true}, {3, 1, false}} {
		r := NewRecorder(nil)
		r.maxRequests = c.max
		for _, ev := range evs {
			r.HTTP(Origin{Session: "s/" + ev.HostID}, ev)
		}
		r.Plan(nil)
		paired := false
		for _, h := range r.links.hop {
			paired = paired || h == HopPaired
		}
		if r.cut != c.cut || paired != c.paired {
			t.Errorf("max %d: cut %d, paired %v; want %d, %v", c.max, r.cut, paired, c.cut, c.paired)
		}
	}
}
