package viewmodel

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"iormesh/internal/federate"
	"iormesh/internal/schema"
)

// ms is a millisecond in ns; base an instant well inside the test
// bounds' window.
const (
	ms   = uint64(time.Millisecond)
	base = uint64(1_000_000) * ms
)

// skewBound says host b's clock runs 50 ms ahead of host a's, known to
// ±10 µs plus 60 µs drift, over [base−1 s, base+60 s) on a's clock.
func skewBound(width int64) federate.ClockBound {
	off := int64(50 * ms)
	return federate.ClockBound{HostA: "a", HostB: "b", Start: base - 1000*ms, End: base + 60_000*ms,
		Lower: off - width, Upper: off + width, HasLower: true, HasUpper: true, Drift: 60_000}
}

// httpEv is an http event of platform thread tid on host spanning
// [start, end] ns, held by its thread as a synchronous library's;
// client sets http.host.
func httpEv(host string, tid uint32, start, end uint64, conn string, client bool, seq uint32) *schema.Event {
	ev := &schema.Event{Seq: uint64(start/1000) + uint64(tid), HostID: host, PID: 1, PIDInc: 1, TID: tid,
		Event: schema.KindHTTP, WallNS: start, LatencyNS: end - start, ConnID: conn,
		Attribution: schema.AttributionJava, Java: &schema.Java{Thread: fmt.Sprint("t", tid)},
		HTTP: &schema.HTTP{Method: "GET", Path: "/x", Status: 200, ReqSeq: seq, Held: end > start}}
	if client {
		ev.HTTP.Host = "svc"
	}
	return ev
}

// record runs a recorder over evs (http pass) and files (file pass).
func record(bounds []federate.ClockBound, evs []*schema.Event, files []*schema.Event) *Recorder {
	r := NewRecorder(nil)
	for _, ev := range evs {
		r.HTTP(Origin{Session: "s/" + ev.HostID, FQDN: ev.HostID + ".example"}, ev)
	}
	r.Plan(bounds)
	for _, ev := range files {
		r.File(Origin{Session: "s/" + ev.HostID, FQDN: ev.HostID + ".example"}, ev)
	}
	return r
}

// convs builds r's conversations with an empty namer.
func convs(r *Recorder) []Conversation {
	cs, _ := r.conversations(newNamer(schema.Graph{}, nil, nil))
	return cs
}

// TestOrdering: a step after another is causal only by an edge, and
// otherwise time-ordered only when the clocks prove it — never by
// comparing two hosts' raw stamps.
func TestOrdering(t *testing.T) {
	tl := federate.NewTimeline([]federate.ClockBound{skewBound(10_000)})
	at := func(h string, w uint64) federate.Stamp { return federate.Stamp{Host: h, Wall: w} }
	off := 50 * ms
	for _, c := range []struct {
		name       string
		prev, step federate.Stamp
		causal     bool
		want       string
	}{
		{"causal edge", at("a", base), at("b", base), true, OrderCausal},
		{"one clock, later", at("a", base), at("a", base+1), false, OrderTime},
		{"one clock, same instant", at("a", base), at("a", base), false, OrderNone},
		{"two clocks, proven by the bound", at("a", base), at("b", base+off+ms), false, OrderTime},
		{"two clocks, within the bound's width", at("a", base), at("b", base+off+50_000), false, OrderNone},
		{"raw stamp later, true time earlier", at("a", base), at("b", base+ms), false, OrderNone},
		{"no bound to the host", at("a", base), at("c", base+10*ms), false, OrderNone},
		{"outside the bound's window", at("a", base-2000*ms), at("b", base+off), false, OrderNone},
	} {
		e := emitter{r: &Recorder{tl: tl}, prev: c.prev, started: true}
		if got := e.ordering(c.step, c.causal); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	if got := (&emitter{r: &Recorder{tl: tl}}).ordering(at("a", base), false); got != OrderTime {
		t.Errorf("a first step: %s", got)
	}
}

// TestClockJoin: an unpaired HTTP/2 hop joins its server end only when
// the bound proves the server's span inside the client's and the match
// is unique; a bound too wide, no bound, or two candidates leave it
// unpaired, and the server request is a conversation of its own.
func TestClockJoin(t *testing.T) {
	off := 50 * ms
	client := httpEv("a", 1, base, base+10*ms, "c", true, 0)
	server := httpEv("b", 2, base+off+3*ms, base+off+7*ms, "c", false, 0)
	other := httpEv("b", 3, base+off+4*ms, base+off+6*ms, "c", false, 0)
	for _, c := range []struct {
		name   string
		bounds []federate.ClockBound
		evs    []*schema.Event
		hop    string
		roots  int
	}{
		{"proven", []federate.ClockBound{skewBound(10_000)}, []*schema.Event{client, server}, HopClock, 1},
		{"bound too wide", []federate.ClockBound{skewBound(int64(4 * ms))}, []*schema.Event{client, server},
			HopUnpaired, 2},
		{"no bound", nil, []*schema.Event{client, server}, HopUnpaired, 2},
		{"two candidates", []federate.ClockBound{skewBound(10_000)}, []*schema.Event{client, server, other},
			HopUnpaired, 3},
		// An async request on the connection may be the server's true
		// client: the span that looks like the client's only match is
		// not joined (task mb4 review).
		{"a startless end on the connection", []federate.ClockBound{skewBound(10_000)},
			[]*schema.Event{client, server, httpEv("a", 4, base+5*ms, base+5*ms, "c", true, 0)}, HopUnpaired, 3},
	} {
		r := record(c.bounds, c.evs, nil)
		cs := convs(r)
		if len(cs) != c.roots {
			t.Errorf("%s: %d conversations, want %d", c.name, len(cs), c.roots)
			continue
		}
		for _, conv := range cs {
			if st := conv.Steps[0]; st.Service == "pid 1" && st.Hop != HopAsync && st.Hop != c.hop {
				t.Errorf("%s: hop %s, want %s", c.name, st.Hop, c.hop)
			}
		}
		if c.hop == HopClock && cs[0].Steps[0].Ordering != OrderTime {
			t.Errorf("%s: the joined hop is %s, want time-ordered", c.name, cs[0].Steps[0].Ordering)
		}
	}
}

// TestUnorderedStep: a joined hop whose step before it lies outside
// every bound's window cannot be ordered against the far host's start:
// the step says unordered rather than borrowing a raw timestamp.
func TestUnorderedStep(t *testing.T) {
	b := skewBound(10_000)
	early := b.Start - 5*ms // before the window: no bound covers it
	srv := httpEv("a", 1, early-ms, base+20*ms, "in", false, 0)
	op := &schema.Event{Seq: 1, HostID: "a", PID: 1, PIDInc: 1, TID: 1, Event: schema.KindRename,
		WallNS: early, FD: schema.NoFD, Args: schema.Args{Path: ptr("/d/x.tmp"), NewPath: ptr("/d/x")}}
	client := httpEv("a", 1, base, base+10*ms, "c", true, 0)
	server := httpEv("b", 2, base+50*ms+3*ms, base+50*ms+7*ms, "c", false, 0)
	r := record([]federate.ClockBound{b}, []*schema.Event{srv, client, server}, []*schema.Event{op})
	cs := convs(r)
	if len(cs) != 1 {
		t.Fatalf("%d conversations", len(cs))
	}
	var got []string
	for _, s := range cs[0].Steps {
		got = append(got, s.Kind+":"+s.Ordering+":"+s.Hop)
	}
	want := "request:time-ordered:untraced file:causal: request:unordered:clock " +
		"response:time-ordered: response:causal:"
	if strings.Join(got, " ") != want {
		t.Errorf("steps\n%s\nwant\n%s", strings.Join(got, " "), want)
	}
}

// TestLinkNegatives: what must not be joined is not — a req_seq two
// server ends claim, a carrier thread's request as a container, a
// request without its start, a client request outside the server's span.
func TestLinkNegatives(t *testing.T) {
	carrier := httpEv("a", 5, base, base+10*ms, "", false, 0)
	carrier.Java = &schema.Java{Thread: "carrier-1", Carrier: true}
	async := httpEv("a", 6, base+ms, base+ms, "z", true, 0)
	for _, c := range []struct {
		name  string
		evs   []*schema.Event
		roots int
	}{
		{"duplicate req_seq", []*schema.Event{httpEv("a", 1, base, base+ms, "c", true, 1),
			httpEv("b", 2, base, base+ms, "c", false, 1), httpEv("b", 3, base, base+ms, "c", false, 1)}, 3},
		{"carrier contains nothing", []*schema.Event{carrier,
			httpEv("a", 5, base+ms, base+2*ms, "d", true, 0)}, 2},
		{"async client", []*schema.Event{httpEv("a", 6, base, base+10*ms, "", false, 0), async}, 2},
		{"outside the span", []*schema.Event{httpEv("a", 7, base, base+ms, "", false, 0),
			httpEv("a", 7, base+2*ms, base+3*ms, "e", true, 0)}, 2},
		{"paired and nested", []*schema.Event{httpEv("a", 8, base, base+10*ms, "", false, 0),
			httpEv("a", 8, base+ms, base+3*ms, "f", true, 1), httpEv("b", 9, base, base+ms, "f", false, 1)}, 1},
	} {
		r := record(nil, c.evs, nil)
		if n := len(r.links.roots); n != c.roots {
			t.Errorf("%s: %d roots, want %d", c.name, n, c.roots)
		}
	}
	r := record(nil, []*schema.Event{async}, nil)
	if st := convs(r)[0].Steps; len(st) != 1 || st[0].Hop != HopAsync {
		t.Errorf("async request steps %+v", st)
	}
}

// TestFileWork: only the serving thread's file work inside the
// request's span is its story — not another thread's, not socket I/O,
// not an fd whose open the trace never showed — and a failed call says
// its error.
func TestFileWork(t *testing.T) {
	srv := httpEv("a", 1, base, base+10*ms, "", false, 0)
	fe := func(tid uint32, at uint64, kind string, fd int32, ret int64, args schema.Args) *schema.Event {
		return &schema.Event{HostID: "a", PID: 1, PIDInc: 1, TID: tid, WallNS: at, Event: kind, FD: fd,
			FDGen: 1, Ret: ret, Args: args}
	}
	files := []*schema.Event{
		fe(1, base+ms, schema.KindOpenat, 4, 4, schema.Args{Path: ptr("/etc/app.conf")}),
		fe(1, base+2*ms, schema.KindRead, 4, 10, schema.Args{}),
		fe(1, base+3*ms, schema.KindRead, 9, 10, schema.Args{}), // fd never opened
		fe(2, base+3*ms, schema.KindUnlink, schema.NoFD, 0, schema.Args{Path: ptr("/other")}),
		fe(1, base+4*ms, schema.KindOpen, schema.NoFD, -2, schema.Args{Path: ptr("/missing")}),
		fe(1, base+20*ms, schema.KindUnlink, schema.NoFD, 0, schema.Args{Path: ptr("/after")}),
	}
	files[4].Err = "ENOENT"
	sock := fe(1, base+5*ms, schema.KindWrite, 4, 10, schema.Args{})
	sock.ConnID = "c9"
	r := record(nil, []*schema.Event{srv}, append(files, sock))
	var labels []string
	for _, s := range convs(r)[0].Steps {
		if s.Kind == StepFile {
			labels = append(labels, s.Label)
		}
	}
	want := "open /etc/app.conf|read /etc/app.conf|open /missing (ENOENT)"
	if strings.Join(labels, "|") != want {
		t.Errorf("file steps %q, want %q", strings.Join(labels, "|"), want)
	}
}

// TestConversationCaps: the recorder keeps the newest MaxRequests
// records and counts the rest; the model keeps MaxConversations and
// MaxSteps and says how many more there were.
func TestConversationCaps(t *testing.T) {
	r := NewRecorder(nil)
	r.maxRequests = 3
	for i := range uint64(10) {
		r.HTTP(Origin{Session: "s"}, httpEv("a", uint32(i+1), base+i*ms, base+i*ms+1, "", false, 0))
	}
	r.Plan(nil)
	cs := convs(r)
	if r.cut != 7 || len(cs) != 3 || cs[0].Start != wallTime(base+9*ms) {
		t.Errorf("cut %d, %d conversations, newest %v", r.cut, len(cs), cs[0].Start)
	}
	var evs, files []*schema.Event
	for i := range uint64(MaxConversations + 1) {
		evs = append(evs, httpEv("a", uint32(i+1), base+i*ms, base+i*ms+ms/2, "", false, 0))
	}
	for i := range uint64(MaxSteps + 5) {
		files = append(files, &schema.Event{HostID: "a", PID: 1, PIDInc: 1, TID: uint32(MaxConversations + 1),
			WallNS: base + MaxConversations*ms + i, Event: schema.KindUnlink, FD: schema.NoFD,
			Args: schema.Args{Path: ptr("/f")}})
	}
	r = record(nil, evs, files)
	cs = convs(r)
	if len(cs) != MaxConversations || r.more != 1 {
		t.Fatalf("%d conversations, %d more", len(cs), r.more)
	}
	if c := cs[0]; len(c.Steps) != MaxSteps || c.MoreSteps != 7 {
		t.Errorf("newest: %d steps, %d more (want %d, 7)", len(c.Steps), c.MoreSteps, MaxSteps)
	}
}

// TestSequenceIsTerminalSafe: escape sequences in a thread name, a path
// or a frame never reach the terminal raw.
func TestSequenceIsTerminalSafe(t *testing.T) {
	c := Conversation{ID: "0001", Entry: "gw\x1b[2J", Label: "GET /\x1b]0;x\x07", Services: []string{"gw\x1b[2J"},
		Steps: []Step{{Kind: StepFile, Service: "gw\x1b[2J", Thread: "t\x9b1m", Label: "read /\x1b[31m",
			Ordering: OrderCausal, Stack: []string{"A#b\x1b"}, Ref: "s/1"}}}
	var b bytes.Buffer
	if err := WriteSequence(&b, c); err != nil {
		t.Fatal(err)
	}
	if err := WriteConversations(&b, Model{Conversations: []Conversation{c}}); err != nil {
		t.Fatal(err)
	}
	if bytes.ContainsAny(b.Bytes(), "\x1b\x07") || strings.Contains(b.String(), "\u009b") {
		t.Errorf("raw control characters in\n%q", b.String())
	}
}
