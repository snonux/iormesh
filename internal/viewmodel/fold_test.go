package viewmodel

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"iormesh/internal/schema"
)

// The checkout under a real host's noise (task vg4): on Rocky 9.9 the
// spread's checkout had 1696 steps, most of them the serving threads'
// reads of the JDK's runtime image while they loaded classes on their
// first request, plus name-service lookups. noisyCheckout rebuilds that
// pattern, trimmed to its shape — the run lengths of the real
// conversation's reads of lib/modules, the lookups, /dev/urandom
// breaking a run — with the declared story inside it: orders calls
// inventory and billing, each server writes its record by
// create-tmp/write/rename, then orders writes its own. Synthetic paths
// below /srv; the JDK paths are the distribution's.

// Roles of the fixture, by pid: each its own process (and host), so
// each is named "pid N" by the empty namer.
const (
	pidGateway   = 1
	pidOrders    = 2
	pidInventory = 3
	pidBilling   = 4
	jdkModules   = "/usr/lib/jvm/java-25-openjdk/lib/modules"
)

// fixture writes one role's events: an http event per request, an open
// per file and the calls on its fd, in wall-clock order on its thread.
type fixture struct {
	host string
	pid  uint32
	at   uint64
	fd   int32
	fds  map[string]int32
	evs  []*schema.Event
}

func newFixture(pid uint32, at uint64) *fixture {
	return &fixture{host: fmt.Sprint("h", pid), pid: pid, at: at, fd: 10, fds: map[string]int32{}}
}

// ev appends one event of the fixture's thread, a microsecond after the
// one before.
func (f *fixture) ev(kind string, fd int32, ret int64, args schema.Args) *schema.Event {
	f.at += 1000
	e := &schema.Event{Seq: uint64(len(f.evs) + 1), HostID: f.host, PID: f.pid, PIDInc: 1, TID: f.pid * 10,
		WallNS: f.at, Event: kind, FD: fd, FDGen: 1, Ret: ret, Args: args,
		Attribution: schema.AttributionJava, Java: &schema.Java{Thread: "HTTP-Dispatcher", Stack: []uint64{7}}}
	f.evs = append(f.evs, e)
	return e
}

// reads appends n pread64 calls on path, opening it first if needed.
func (f *fixture) reads(path string, n int) {
	fd, ok := f.fds[path]
	if !ok {
		f.fd++
		fd, f.fds[path] = f.fd, f.fd
		f.ev(schema.KindOpenat, fd, int64(fd), schema.Args{Path: ptr(path)})
	}
	for i := range n {
		f.ev(schema.KindPread64, fd, 4096, schema.Args{Offset: ptr(int64(i) * 4096)}).Bytes = 4096
	}
}

// probe appends a failed open.
func (f *fixture) probe(path string) {
	f.ev(schema.KindOpenat, schema.NoFD, -2, schema.Args{Path: ptr(path)}).Err = "ENOENT"
}

// lookup appends the name-service lookup the real threads made.
func (f *fixture) lookup() {
	for _, p := range []string{"/etc/host.conf", "/etc/resolv.conf", "/etc/hosts"} {
		f.reads(p, 2)
	}
	f.probe("/usr/lib/jvm/java-25-openjdk/bin/libnss_myhostname.so.2")
	f.probe("/usr/lib/jvm/java-25-openjdk/bin/../lib/libnss_myhostname.so.2")
	f.reads("/lib64/libnss_myhostname.so.2", 1)
}

// record appends the declared create-tmp/write/rename of path.
func (f *fixture) record(path string) {
	f.fd++
	f.ev(schema.KindOpenat, f.fd, int64(f.fd), schema.Args{Path: ptr(path + ".tmp"), Flags: ptr(uint32(oCreat | 1))})
	f.ev(schema.KindWrite, f.fd, 36, schema.Args{}).Bytes = 36
	f.ev(schema.KindRename, schema.NoFD, 0, schema.Args{Path: ptr(path + ".tmp"), NewPath: ptr(path)})
}

// call appends a client request to (conn, seq) spanning [start, end].
func (f *fixture) call(conn string, start, end uint64, client bool) *schema.Event {
	e := &schema.Event{Seq: uint64(len(f.evs) + 1), HostID: f.host, PID: f.pid, PIDInc: 1, TID: f.pid * 10,
		Event: schema.KindHTTP, WallNS: start, LatencyNS: end - start, ConnID: conn,
		Attribution: schema.AttributionJava, Java: &schema.Java{Thread: "HTTP-Dispatcher"},
		HTTP: &schema.HTTP{Method: "POST", Path: "/" + conn, Status: 200, ReqSeq: 1, Held: true}}
	if client {
		e.HTTP.Host = conn
	}
	f.evs = append(f.evs, e)
	return e
}

// serve is a server's work on (conn): its noise and its record, as one
// server request spanning it.
func serve(pid uint32, at uint64, conn, rec string, before, after int) []*schema.Event {
	f := newFixture(pid, at)
	f.reads(jdkModules, before)
	f.record(rec)
	f.reads(jdkModules, after)
	f.call(conn, at, f.at+1000, false)
	return f.evs
}

// noisyCheckout is the fixture: its http events and its file events.
func noisyCheckout() (https, files []*schema.Event) {
	o := newFixture(pidOrders, base)
	o.reads(jdkModules, 2)
	o.lookup()
	for _, n := range []int{420, 134, 13} { // /dev/urandom breaks the runs, as it did
		o.reads(jdkModules, n)
		o.reads("/dev/urandom", 1)
	}
	o.reads(jdkModules, 5) // right up to the call, and on right after it
	inv := serve(pidInventory, o.at+2000, "inventory", "/srv/inventory.d/reservation-42.json", 8, 92)
	invStart, invEnd := o.at+1000, inv[len(inv)-1].WallNS+inv[len(inv)-1].LatencyNS+1000
	o.call("inventory", invStart, invEnd, true)
	o.at = invEnd
	o.reads(jdkModules, 27)
	bil := serve(pidBilling, o.at+2000, "billing", "/srv/billing.d/invoice-42.json", 429, 82)
	bilStart, bilEnd := o.at+1000, bil[len(bil)-1].WallNS+bil[len(bil)-1].LatencyNS+1000
	o.call("billing", bilStart, bilEnd, true)
	o.at = bilEnd
	o.record("/srv/orders.d/order-42.json")
	o.reads(jdkModules, 82)
	gw := newFixture(pidGateway, base-2000)
	gw.call("orders", base-1000, o.at+2000, true)
	o.call("orders", base, o.at+1000, false)
	for _, evs := range [][]*schema.Event{gw.evs, o.evs, inv, bil} {
		for _, e := range evs {
			if e.Event == schema.KindHTTP {
				https = append(https, e)
			} else {
				files = append(files, e)
			}
		}
	}
	return https, files
}

// TestNoisyCheckoutFolds: under the real host's noise the checkout keeps
// its whole declared story inside MaxSteps — every hop, each server's
// create-tmp/write/rename in its place, every service named — because
// repeated calls fold into one step each, counted: the steps' calls add
// up to every file event of the spans, and their bytes to every byte.
// A run never folds across a call the thread made (the reads right
// before and after the inventory hop stay two steps), and a call that
// differs (another file, an error, another path) starts a new step.
func TestNoisyCheckoutFolds(t *testing.T) {
	https, files := noisyCheckout()
	cs := convs(record(nil, https, files))
	if len(cs) != 1 {
		t.Fatalf("%d conversations, want 1", len(cs))
	}
	c := cs[0]
	if c.MoreSteps != 0 || c.Hops != 3 || len(c.Steps) > MaxSteps {
		t.Fatalf("%d steps, %d more, %d hops; want all kept and 3 hops", len(c.Steps), c.MoreSteps, c.Hops)
	}
	if want := []string{"pid 1", "pid 2", "pid 3", "pid 4"}; !slices.Equal(c.Services, want) {
		t.Errorf("services %q, want %q", c.Services, want)
	}
	var story []string
	calls, bytes, events, wantBytes := 0, uint64(0), 0, uint64(0)
	for _, s := range c.Steps {
		if s.Kind != StepFile {
			story = append(story, s.Service+" "+s.Label)
			continue
		}
		calls, bytes = calls+max(s.Count, 1), bytes+s.Bytes
		if strings.HasPrefix(s.Label, "create /srv") || strings.HasPrefix(s.Label, "rename /srv") ||
			strings.HasPrefix(s.Label, "write /srv") {
			story = append(story, s.Service+" "+s.Label)
		}
		if (s.Count > 1) != strings.HasSuffix(s.Label, fmt.Sprintf(" (%d calls)", s.Count)) {
			t.Errorf("step %q: count %d, label disagrees", s.Label, s.Count)
		}
		if s.Count > 1 && strings.Contains(s.Label, " @") {
			t.Errorf("step %q: a folded run names one call's offset", s.Label)
		}
	}
	for _, e := range files {
		events++
		wantBytes += e.Bytes
	}
	if calls != events || bytes != wantBytes {
		t.Errorf("steps hold %d calls and %d B, the spans %d events and %d B", calls, bytes, events, wantBytes)
	}
	wantStory := []string{
		"pid 1 POST /orders",
		"pid 2 POST /inventory",
		"pid 3 create /srv/inventory.d/reservation-42.json.tmp",
		"pid 3 write /srv/inventory.d/reservation-42.json.tmp",
		"pid 3 rename /srv/inventory.d/reservation-42.json.tmp -> /srv/inventory.d/reservation-42.json",
		"pid 3 200",
		"pid 2 POST /billing",
		"pid 4 create /srv/billing.d/invoice-42.json.tmp",
		"pid 4 write /srv/billing.d/invoice-42.json.tmp",
		"pid 4 rename /srv/billing.d/invoice-42.json.tmp -> /srv/billing.d/invoice-42.json",
		"pid 4 200",
		"pid 2 create /srv/orders.d/order-42.json.tmp",
		"pid 2 write /srv/orders.d/order-42.json.tmp",
		"pid 2 rename /srv/orders.d/order-42.json.tmp -> /srv/orders.d/order-42.json",
		"pid 2 200",
	}
	if !slices.Equal(story, wantStory) {
		t.Errorf("story\n%s\nwant\n%s", strings.Join(story, "\n"), strings.Join(wantStory, "\n"))
	}
	assertRuns(t, c)
}

// assertRuns: the runs around the inventory hop are their own steps —
// 5 reads before it, 27 after — and the long class-loading run is one.
func assertRuns(t *testing.T, c Conversation) {
	t.Helper()
	hop := slices.IndexFunc(c.Steps, func(s Step) bool { return s.Label == "POST /inventory" })
	back := slices.IndexFunc(c.Steps, func(s Step) bool { return s.Label == "200" && s.Service == "pid 3" })
	if hop < 1 || back < 0 || back+1 >= len(c.Steps) {
		t.Fatalf("no inventory hop in %d steps", len(c.Steps))
	}
	want := "read " + jdkModules
	if s := c.Steps[hop-1]; s.Label != want+" (5 calls)" || s.Count != 5 {
		t.Errorf("before the hop: %q (count %d), want the 5 reads", s.Label, s.Count)
	}
	if s := c.Steps[back+1]; s.Label != want+" (27 calls)" || s.Count != 27 {
		t.Errorf("after the hop: %q (count %d), want the 27 reads", s.Label, s.Count)
	}
	if !slices.ContainsFunc(c.Steps, func(s Step) bool { return s.Label == want+" (420 calls)" }) {
		t.Errorf("the 420 reads are not one step")
	}
	if i := slices.IndexFunc(c.Steps, func(s Step) bool { return strings.Contains(s.Label, "(ENOENT)") }); i < 0 ||
		c.Steps[i].Count != 0 || c.Steps[i+1].Count != 0 || !strings.Contains(c.Steps[i+1].Label, "(ENOENT)") {
		t.Errorf("two failed opens of two paths must stay two steps")
	}
}

// TestServicesPastTheCap: a conversation cut at MaxSteps still names the
// services its later hops reached, and counts their hops.
func TestServicesPastTheCap(t *testing.T) {
	o := newFixture(pidOrders, base)
	for i := range MaxSteps + 10 { // distinct paths: nothing folds
		o.ev(schema.KindUnlink, schema.NoFD, 0, schema.Args{Path: ptr(fmt.Sprint("/srv/f", i))})
	}
	inv := serve(pidInventory, o.at+2000, "inventory", "/srv/inventory.d/r.json", 0, 0)
	end := inv[len(inv)-1].WallNS + inv[len(inv)-1].LatencyNS + 1000
	o.call("inventory", o.at+1000, end, true)
	o.call("orders", base-1000, end+1000, false)
	var https, files []*schema.Event
	for _, e := range append(o.evs, inv...) {
		if e.Event == schema.KindHTTP {
			https = append(https, e)
		} else {
			files = append(files, e)
		}
	}
	c := convs(record(nil, https, files))[0]
	if c.MoreSteps == 0 || c.Hops != 2 || !slices.Contains(c.Services, "pid 3") {
		t.Errorf("%d steps, %d more, %d hops, services %q: want a cut, 2 hops and pid 3 named",
			len(c.Steps), c.MoreSteps, c.Hops, c.Services)
	}
}

// TestFoldKeepsApart: only one thread's repeats of one outcome fold — a
// failed open and a successful one of the same path are two steps, so
// are reads under two Java thread names — and the step after a run is
// judged against the run's last call: a call starting at that instant
// is unordered, not time-ordered by the run's first call.
func TestFoldKeepsApart(t *testing.T) {
	srv := httpEv("a", 1, base, base+10*ms, "", false, 0)
	fe := func(at uint64, kind string, fd int32, ret int64, thread string, args schema.Args) *schema.Event {
		return &schema.Event{HostID: "a", PID: 1, PIDInc: 1, TID: 1, WallNS: at, Event: kind, FD: fd, FDGen: 1,
			Ret: ret, Args: args, Attribution: schema.AttributionJava, Java: &schema.Java{Thread: thread}}
	}
	files := []*schema.Event{
		fe(base+ms, schema.KindOpenat, schema.NoFD, -2, "t1", schema.Args{Path: ptr("/x")}),
		fe(base+2*ms, schema.KindOpenat, 4, 4, "t1", schema.Args{Path: ptr("/x")}),
		fe(base+3*ms, schema.KindRead, 4, 1, "t1", schema.Args{}),
		fe(base+4*ms, schema.KindRead, 4, 1, "v1", schema.Args{}),
		fe(base+5*ms, schema.KindRead, 4, 1, "v1", schema.Args{}),
		fe(base+6*ms, schema.KindRead, 4, 1, "v1", schema.Args{}),
	}
	files[0].Err = "ENOENT"
	call := httpEv("a", 1, base+6*ms, base+7*ms, "z", true, 0)
	var got []string
	for _, s := range convs(record(nil, []*schema.Event{srv, call}, files))[0].Steps[1:6] {
		got = append(got, s.Ordering+" "+s.Label)
	}
	want := []string{"causal open /x (ENOENT)", "causal open /x", "causal read /x", "causal read /x (3 calls)",
		"unordered GET /x"}
	if !slices.Equal(got, want) {
		t.Errorf("steps\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestCapCountsRuns: past a request's cap the calls cut fold as the kept
// ones do, so MoreSteps counts steps — runs of 50 and 51 cut are one
// more each, not 101 — and no call folds into the last kept op across
// the calls cut after it.
func TestCapCountsRuns(t *testing.T) {
	o := newFixture(pidOrders, base)
	for i := range MaxSteps - 1 { // distinct paths: nothing folds
		o.ev(schema.KindUnlink, schema.NoFD, 0, schema.Args{Path: ptr(fmt.Sprint("/srv/f", i))})
	}
	o.reads("/srv/x", 1) // the open is the last kept op, the read the first cut
	o.reads("/srv/a", 50)
	o.reads("/srv/b", 51)
	o.call("inventory", o.at+300, o.at+600, true) // the run of b reads goes on after a call
	o.at += 600
	o.reads("/srv/b", 2)
	o.ev(schema.KindUnlink, schema.NoFD, 0, schema.Args{Path: ptr("/srv/y")})
	o.ev(schema.KindOpenat, 99, 99, schema.Args{Path: ptr("/srv/x")}) // the last kept op again
	o.call("orders", base-1000, o.at+1000, false)
	var https, files []*schema.Event
	for _, e := range o.evs {
		if e.Event == schema.KindHTTP {
			https = append(https, e)
		} else {
			files = append(files, e)
		}
	}
	// The request's own cap, below the conversation's (which also holds
	// the request and response steps): read from the recorder.
	r := record(nil, https, files)
	if len(r.ops) != 1 {
		t.Fatalf("%d server requests with file work, want 1", len(r.ops))
	}
	for srv, ops := range r.ops {
		// Cut: read x; open a, read a ×50; open b, read b ×51; after the
		// call read b ×2; unlink y;
		// open x — each a run of its own: 1 + 2 + 2 + 1 + 1 + 1.
		if len(ops) != MaxSteps || r.moreOps[srv] != 8 {
			t.Errorf("%d ops kept, %d more; want %d and 8", len(ops), r.moreOps[srv], MaxSteps)
		}
		if last := ops[len(ops)-1]; last.label != "open /srv/x" || last.count != 1 {
			t.Errorf("last kept op %q (count %d): a cut call folded into it", last.label, last.count)
		}
	}
}

// TestRunQuery: a folded run's query names its host, kernel thread, file
// and window — from its first call to just past its last — every glob
// escaped to match itself and every word inert in a shell; show
// sequence prints it with the run's last ref and marks its stack and
// ref as the first call's.
func TestRunQuery(t *testing.T) {
	got := runQuery("h1.example", 21, "/srv/a b*[x]'c", base, base+5*ms)
	want := `iormeshctl query -host h1.example -thread 21 -file '/srv/a b\*\[x\]'\''c'` +
		` -from 1970-01-01T00:16:40Z -to 1970-01-01T00:16:40.005000001Z`
	if got != want {
		t.Errorf("run query\n%s\nwant\n%s", got, want)
	}
	https, files := noisyCheckout()
	c := convs(record(nil, https, files))[0]
	i := slices.IndexFunc(c.Steps, func(s Step) bool { return s.Count == 420 })
	if s := c.Steps[i]; s.LastRef == "" || s.LastRef == s.Ref ||
		!strings.Contains(s.RunQuery, "-host h2.example -thread 20 -file "+jdkModules+" -from ") {
		t.Errorf("the 420-call run: last ref %q, ref %q, query %q", s.LastRef, s.Ref, s.RunQuery)
	}
	var b strings.Builder
	if err := WriteSequence(&b, c); err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"(first of 420)", "last " + c.Steps[i].LastRef, "run " + c.Steps[i].RunQuery,
		" (first call)"} {
		if !strings.Contains(b.String(), w) {
			t.Errorf("show sequence lacks %q", w)
		}
	}
}
