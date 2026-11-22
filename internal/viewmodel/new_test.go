package viewmodel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"iormesh/internal/federate"
	"iormesh/internal/schema"
	"iormesh/internal/servicedb"
)

func ptr[T any](v T) *T { return &v }

// TestTallySortsDrops: each drop reason lands in its own count, the end
// reason is kept, and sessions come out sorted with their sums.
func TestTallySortsDrops(t *testing.T) {
	tl := NewTally()
	b := SessionKey{Project: "shop", Host: "app2", FQDN: "b.example", ID: "2"}
	a := SessionKey{Project: "shop", Host: "app1", FQDN: "a.example", ID: "1"}
	drop := func(reason *string, n uint64) *schema.Event {
		return &schema.Event{HostID: "ha", Event: schema.KindDrop, WallNS: 5,
			Args: schema.Args{Lost: ptr(n), DropReason: reason}}
	}
	tl.Add(b, "s/b/0", &schema.Event{HostID: "hb", Event: schema.KindRead, WallNS: 9})
	tl.Add(a, "s/a/0", drop(nil, 1))
	tl.Add(a, "s/a/0", drop(ptr(schema.DropSkippedRuns), 10))
	tl.Add(a, "s/a/1", drop(ptr(schema.DropUnattributed), 100))
	tl.Add(a, "s/a/1", drop(ptr(schema.DropAttachGap), 1000))
	tl.Add(a, "s/a/1", &schema.Event{HostID: "ha", Event: schema.KindEnd, WallNS: 7,
		Args: schema.Args{EndReason: ptr(schema.EndDiskLimit)}})
	tl.Segment(a, "s/a/2")
	m := New(Input{Tally: tl})
	h := m.Health
	if h.Lost != 1 || h.SkippedRuns != 10 || h.Unattributed != 100 || h.AttachGap != 1000 {
		t.Errorf("health %+v", h)
	}
	if len(h.Sessions) != 2 || h.Sessions[0].Host != "app1" || h.Sessions[0].Segments != 3 ||
		h.Sessions[0].End != schema.EndDiskLimit || h.Sessions[1].End != "" {
		t.Errorf("sessions %+v", h.Sessions)
	}
	if m.Run.Events != 6 || m.Run.Segments != 4 || m.Run.First != time.Unix(0, 5).UTC() ||
		m.Run.Last != time.Unix(0, 9).UTC() || strings.Join(m.Run.Hosts, ",") != "app1,app2" {
		t.Errorf("run %+v", m.Run)
	}
}

// TestClocksSummary: windows per pair, empty and one-sided counted
// apart, widths over the rest, hosts by FQDN.
func TestClocksSummary(t *testing.T) {
	tl := NewTally()
	tl.Add(SessionKey{FQDN: "a.example"}, "x", &schema.Event{HostID: "ha"})
	bounds := []federate.ClockBound{
		{HostA: "ha", HostB: "hb", Lower: -10, Upper: 30, HasLower: true, HasUpper: true, Drift: 5},
		{HostA: "ha", HostB: "hb", Start: 60, Lower: 0, Upper: 10, HasLower: true, HasUpper: true},
		{HostA: "ha", HostB: "hb", Start: 120, Lower: 50, Upper: 10, HasLower: true, HasUpper: true},
		{HostA: "ha", HostB: "hb", Start: 180, Lower: 3, HasLower: true},
		{HostA: "hb", HostB: "hc", Lower: 1, Upper: 2, HasLower: true, HasUpper: true},
	}
	got := New(Input{Tally: tl, Merge: federate.Result{Clocks: bounds}}).Health.Clocks
	want := []Clock{{HostA: "a.example", HostB: "hb", Windows: 4, Empty: 1, OneSided: 1,
		MinWidth: 10, MaxWidth: 50}, {HostA: "hb", HostB: "hc", Windows: 1, MinWidth: 1, MaxWidth: 1}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("clocks\n got %+v\nwant %+v", got, want)
	}
}

// TestServicesAndEdges: endpoints group by label, client-only names
// become services, paths are capped with the rest counted, and an
// edge's state says whether its connections fused.
func TestServicesAndEdges(t *testing.T) {
	var reqs []servicedb.EndpointRequest
	for i := range MaxTopPaths + 2 {
		reqs = append(reqs, servicedb.EndpointRequest{Method: "GET", Path: fmt.Sprintf("/p%02d", i), Count: int64(i + 1)})
	}
	eps := []servicedb.Endpoint{
		{Hostname: "orders-1", Port: 8443, Service: "orders", Conns: 2, TLS: "tls", Requests: reqs},
		{IP: "10.0.0.2", Port: 8443, Service: "orders", Conns: 1, Unresolved: 1, TLS: "plain"},
		{IP: "::1", Port: 5432, Conns: 1},
	}
	ins := []servicedb.Interaction{{From: "gateway", To: "orders", Conns: 3, Unresolved: 1},
		{From: "orders", To: "[::1]:5432", Conns: 1, Unresolved: 1}, {From: "x", To: "orders", Conns: 2}}
	m := New(Input{Endpoints: eps, Interactions: ins})
	var names []string
	for _, s := range m.Services {
		names = append(names, s.Name)
	}
	if strings.Join(names, " ") != "[::1]:5432 gateway orders x" {
		t.Fatalf("services %v", names)
	}
	o := m.Services[2]
	if !o.Named || o.Conns != 3 || o.TLS != "mixed" || len(o.TopPaths) != MaxTopPaths || o.MorePaths != 2 ||
		o.TopPaths[0].Path != "/p11" || strings.Join(o.Endpoints, " ") != "10.0.0.2:8443 orders-1:8443" {
		t.Errorf("orders %+v", o)
	}
	var states []string
	for _, e := range m.Edges {
		states = append(states, e.State)
	}
	if strings.Join(states, " ") != "mixed unresolved fused" {
		t.Errorf("edge states %v", states)
	}
}

// TestModelDeterministic: equal inputs, equal JSON — and the summary
// renders from the model alone.
func TestModelDeterministic(t *testing.T) {
	in := func() Input {
		tl := NewTally()
		tl.Add(SessionKey{Project: "p", Host: "h", FQDN: "f", ID: "1"}, "s", &schema.Event{HostID: "x", WallNS: 1})
		return Input{Projects: []string{"q", "p"}, Tally: tl,
			Interactions: []servicedb.Interaction{{From: "a", To: "b", Conns: 1}},
			BadSegments:  []federate.BadSegment{{Name: "p/h/x.parquet", Err: fmt.Errorf("broken")}}}
	}
	j1, err := json.Marshal(New(in()))
	if err != nil {
		t.Fatal(err)
	}
	j2, _ := json.Marshal(New(in()))
	if !bytes.Equal(j1, j2) {
		t.Fatal("two models of one input differ")
	}
	var b bytes.Buffer
	if err := WriteSummary(&b, New(in())); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"built      p+q: 1 hosts, 1 sessions, 1 segments, 1 events",
		"session    p/h 1: no end recorded", "bad        p/h/x.parquet: broken", "no clock bounds"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("summary lacks %q:\n%s", want, b.String())
		}
	}
}

// TestSummaryNamesEndReason: the summary names a session the disk
// watchdog (or an error) ended, with its reason — PLAN.md M4 and the
// acceptance check M4-13 read the reason from the plain summary — and
// leaves sessions that ran their course out.
func TestSummaryNamesEndReason(t *testing.T) {
	tl := NewTally()
	end := func(id, reason string) {
		k := SessionKey{Project: "m4disk", Host: "iom2", FQDN: "iom2.example", ID: id}
		tl.Add(k, "s/"+id, &schema.Event{HostID: "x", WallNS: 1, Event: schema.KindEnd,
			Args: schema.Args{EndReason: ptr(reason)}})
	}
	end("1", schema.EndDiskLimit)
	end("2", schema.EndError)
	end("3", schema.EndDuration)
	end("4", schema.EndStop)
	end("5", schema.EndTargetsExited)
	var b bytes.Buffer
	if err := WriteSummary(&b, New(Input{Tally: tl})); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	for _, want := range []string{
		"session    m4disk/iom2 1: ended disk_limit (the disk watchdog stopped the tracer)\n",
		"session    m4disk/iom2 2: ended error (the tracer failed; the trace is incomplete)\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q:\n%s", want, got)
		}
	}
	for _, normal := range []string{schema.EndDuration, schema.EndStop, schema.EndTargetsExited} {
		if strings.Contains(got, "ended "+normal) {
			t.Errorf("summary names the normal end %q:\n%s", normal, got)
		}
	}
}

// TestTextIsTerminalSafe: an escape sequence in any server-supplied
// string (a comm, a hostname, a segment file name, an error) never
// reaches the terminal raw from any text view.
func TestTextIsTerminalSafe(t *testing.T) {
	const esc = "\x1b]0;pwned\x07\x1b[2J"
	tl := NewTally()
	tl.Add(SessionKey{Project: "p", Host: "h", FQDN: "f" + esc, ID: "1"}, "s",
		&schema.Event{HostID: "x", WallNS: 1, Event: schema.KindEnd, Args: schema.Args{EndReason: ptr(esc)}})
	m := New(Input{Tally: tl, Ignored: []string{"p/h/" + esc},
		BadSegments: []federate.BadSegment{{Name: "p/h/" + esc, Err: fmt.Errorf("bad %s", esc)}},
		Endpoints:   []servicedb.Endpoint{{Hostname: "db" + esc, Port: 1, Conns: 1}},
		Interactions: []servicedb.Interaction{{From: "java" + esc, To: "db" + esc + ":1", Conns: 1,
			Unresolved: 1}}})
	for name, write := range map[string]func(*bytes.Buffer) error{
		"summary":  func(b *bytes.Buffer) error { return WriteSummary(b, m) },
		"services": func(b *bytes.Buffer) error { return WriteServices(b, m) },
		"edges":    func(b *bytes.Buffer) error { return WriteEdges(b, m, false) },
		"health":   func(b *bytes.Buffer) error { return WriteHealth(b, m) },
	} {
		var b bytes.Buffer
		if err := write(&b); err != nil {
			t.Fatal(err)
		}
		if strings.ContainsAny(b.String(), "\x1b\x07") || !strings.Contains(b.String(), `\x1b`) {
			t.Errorf("%s:\n%s", name, b.String())
		}
	}
}

// TestEndpointStringScoped: a loopback or link-local endpoint's address
// ends in "@host", as `iormeshctl services` shows it; a routable one
// does not, and a service name never replaces the address.
func TestEndpointStringScoped(t *testing.T) {
	for _, c := range []struct {
		ep   servicedb.Endpoint
		want string
	}{
		{servicedb.Endpoint{IP: "127.0.0.1", Port: 5432, Host: "hA", Service: "db"}, "127.0.0.1:5432@hA"},
		{servicedb.Endpoint{IP: "::1", Port: 5432, Host: "hB"}, "[::1]:5432@hB"},
		{servicedb.Endpoint{Hostname: "orders-1", IP: "10.0.0.2", Port: 8443, Service: "orders"},
			"orders-1:8443"},
	} {
		if got := endpointString(c.ep); got != c.want {
			t.Errorf("endpointString(%+v) = %q, want %q", c.ep, got, c.want)
		}
	}
}
