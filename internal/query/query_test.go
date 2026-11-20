package query

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"iormesh/internal/federate"
	"iormesh/internal/schema"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden from the current output")

// wall0 is the fixture's first wall-clock instant, 2025-11-20T00:53:20Z.
const wall0 = uint64(1_763_600_000) * uint64(time.Second)

func ptr[T any](v T) *T { return &v }

// trace builds one host session's events, each valid.
type trace struct {
	t       *testing.T
	project string
	host    string
	seq     uint64
	events  []schema.Event
}

// add appends an event of pid/tid at at ms after wall0.
func (tr *trace) add(pid, tid uint32, kind string, fd int32, gen uint32, at uint64,
	args schema.Args) *schema.Event {
	tr.seq++
	ev := schema.Event{SchemaVersion: schema.Version, Project: tr.project, Seq: tr.seq,
		BootNS: at + 1, WallNS: wall0 + at*uint64(time.Millisecond), HostID: tr.host, PID: pid,
		PIDInc: 1, TID: tid, Comm: "java", Event: kind, FD: fd, FDGen: gen, Args: args,
		Attribution: schema.AttributionNone}
	tr.events = append(tr.events, ev)
	return &tr.events[len(tr.events)-1]
}

// java attributes ev to a Java thread with a stack of method ids.
func java(ev *schema.Event, thread string, stack ...uint64) {
	ev.Attribution = schema.AttributionJava
	ev.Java = &schema.Java{Thread: thread, Stack: stack}
}

// source turns the trace into a Source, checking every event.
func (tr *trace) source(host, fqdn string) Source {
	for i := range tr.events {
		if err := tr.events[i].Validate(); err != nil {
			tr.t.Fatalf("fixture event %d: %v", i, err)
		}
	}
	return Source{Host: host, FQDN: fqdn, Session: tr.project + "/" + host + "/s",
		Events: func(_ context.Context, fn func(*schema.Event) error) error {
			for _, ev := range tr.events {
				if err := fn(&ev); err != nil { // a copy: Run renames conn ids
					return err
				}
			}
			return nil
		}}
}

// fixture is a client host (app1, pattern "orders") calling a server
// host (app2) over one fused connection, with file I/O around it.
func fixture(t *testing.T) Input {
	a := &trace{t: t, project: "shop", host: "mA"}
	a.add(100, 101, schema.KindOpenat, 3, 1, 1, schema.Args{Path: ptr("/etc/orders.conf")}).Ret = 3
	java(a.add(100, 101, schema.KindRead, 3, 1, 2, schema.Args{}), "http-1", 7, 8)
	a.add(100, 101, schema.KindClose, 3, 1, 3, schema.Args{})
	a.add(100, 101, schema.KindRead, 3, 2, 4, schema.Args{}) // another fd instance
	java(a.add(100, 101, schema.KindWrite, 7, 1, 10, schema.Args{}), "http-1", 9)
	a.events[len(a.events)-1].ConnID = "c1"
	a.add(100, 102, schema.KindWrite, 7, 1, 11, schema.Args{}).ConnID = "c1"
	java(a.add(100, 101, schema.KindOpenat, 4, 1, 12, schema.Args{Path: ptr("/tmp/order-42.json")}),
		"http-1", 8)
	a.events[len(a.events)-1].Ret = 4
	a.add(100, 103, schema.KindWrite, 9, 1, 13, schema.Args{}).ConnID = "c9"
	a.add(100, 101, schema.KindRead, 7, 1, 14, schema.Args{}).ConnID = "c1" // the reply
	java(a.add(100, 101, schema.KindWrite, 4, 1, 20, schema.Args{}), "http-1", 7)
	b := &trace{t: t, project: "shop", host: "mB"}
	b.add(200, 201, schema.KindRead, 5, 1, 10, schema.Args{}).ConnID = "c1"
	ev := b.add(200, 201, schema.KindOpenat, 6, 1, 15, schema.Args{Path: ptr("/var/x\x1b[2J\u009b\u202e")})
	ev.Ret, ev.Comm = 6, "inv\u0085"
	b.add(200, 201, schema.KindWrite, 5, 1, 16, schema.Args{}).ConnID = "c1"
	return Input{
		Sources: []Source{a.source("app1", "app1.example.org"), b.source("app2", "app2.example.org")},
		Renames: map[federate.LocalConn]string{{Host: "mA", ConnID: "c1"}: "conn:f1",
			{Host: "mB", ConnID: "c1"}: "conn:f1"},
		Graph: schema.Graph{Edges: []schema.Edge{{Type: schema.EdgeIncarnation,
			From: schema.AppNodeID("shop", "orders"), To: schema.ProcessNodeID("mA", 100, 1)}}},
		Methods: map[string]map[uint64]bool{"shop/app1/s": {9: true}},
	}
}

// at is the wall_ns ms after wall0.
func at(ms uint64) uint64 { return wall0 + ms*uint64(time.Millisecond) }

// TestRunGolden runs each filter over the fixture: the output matches
// its golden, every event line decodes strictly as a schema event, and
// a second run writes the same bytes.
func TestRunGolden(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    Filter
		max  int64
	}{
		{"all", Filter{}, 1 << 20},
		{"window", Filter{From: at(3), To: at(11)}, 1 << 20},
		{"host", Filter{Host: "app2"}, 1 << 20},
		{"host-fqdn", Filter{Host: "app1.example.*"}, 1 << 20},
		{"app-pattern", Filter{App: "ord*"}, 1 << 20},
		{"app-comm", Filter{App: "inv*"}, 1 << 20},
		{"thread-name", Filter{Thread: "http-*"}, 1 << 20},
		{"thread-tid", Filter{Thread: "102"}, 1 << 20},
		{"conn", Filter{Conn: "conn:f1"}, 1 << 20},
		{"conn-follow", Filter{Conn: "conn:f1", Follow: true}, 1 << 20},
		{"file", Filter{File: "/etc/*.conf"}, 1 << 20},
		{"method", Filter{Method: "x"}, 1 << 20},
		{"combined", Filter{Host: "app1", Thread: "http-1", From: at(2)}, 1 << 20},
		{"truncated", Filter{}, 1200},
		{"truncated-all", Filter{}, MinMaxBytes()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := runOK(t, fixture(t), tc.f, tc.max)
			golden(t, tc.name, got)
			if int64(len(got)) > tc.max {
				t.Errorf("%d bytes, over the cap %d", len(got), tc.max)
			}
			checkLines(t, got)
			if again := runOK(t, fixture(t), tc.f, tc.max); again != got {
				t.Error("a second run wrote other bytes")
			}
		})
	}
}

func runOK(t *testing.T, in Input, f Filter, max int64) string {
	t.Helper()
	var buf bytes.Buffer
	if _, err := Run(context.Background(), &buf, in, f, max); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// checkLines decodes every line: events strictly, the truncation record
// as the last line only; no raw byte a terminal could act on.
func checkLines(t *testing.T, out string) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	for i, l := range lines {
		if l == "" {
			continue
		}
		if strings.Contains(l, `"event":"truncated"`) {
			if i != len(lines)-1 {
				t.Errorf("truncation record on line %d of %d", i+1, len(lines))
			}
			continue
		}
		if _, err := schema.DecodeEvent([]byte(l)); err != nil {
			t.Errorf("line %d: %v", i+1, err)
		}
		if bytes.ContainsFunc([]byte(l), func(r rune) bool { return r == 0x1b || r == 0x9b || r == 0x202e }) {
			t.Errorf("line %d holds a raw control: %q", i+1, l)
		}
	}
}

// golden compares got with testdata/<name>.golden (-update rewrites).
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s differs (go test ./internal/query -update to accept):\n%s", path, got)
	}
}

// TestTruncationStats: the stats agree with the record, and a cap that
// holds everything adds none.
func TestTruncationStats(t *testing.T) {
	var buf bytes.Buffer
	st, err := Run(context.Background(), &buf, fixture(t), Filter{}, 1200)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Truncated || st.Emitted+st.Remaining != 13 {
		t.Errorf("stats %+v, want truncated with 13 events in all", st)
	}
	if want := truncatedLine(st.Emitted, st.Remaining); !strings.HasSuffix(buf.String(), want) {
		t.Errorf("last line is not %q", want)
	}
	buf.Reset()
	if st, _ := Run(context.Background(), &buf, fixture(t), Filter{}, 1<<20); st.Truncated || st.Emitted != 13 {
		t.Errorf("uncut stats %+v", st)
	}
}

// TestInvalidFilter: a filter no event can be judged by is rejected
// before anything is read.
func TestInvalidFilter(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    Filter
		max  int64
	}{
		{"bad host glob", Filter{Host: "["}, 1 << 20},
		{"bad app glob", Filter{App: "a[b"}, 1 << 20},
		{"bad thread glob", Filter{Thread: `x\`}, 1 << 20},
		{"bad file glob", Filter{File: "/etc/[a-"}, 1 << 20},
		{"bad method glob", Filter{Method: "["}, 1 << 20},
		{"follow without conn", Filter{Follow: true}, 1 << 20},
		{"reversed window", Filter{From: at(5), To: at(4)}, 1 << 20},
		{"empty window", Filter{From: at(5), To: at(5)}, 1 << 20},
		{"cap below the record", Filter{}, MinMaxBytes() - 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := false
			in := fixture(t)
			in.Sources[0].Events = func(context.Context, func(*schema.Event) error) error {
				read = true
				return nil
			}
			var buf bytes.Buffer
			_, err := Run(context.Background(), &buf, in, tc.f, tc.max)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err %v, want ErrInvalid", err)
			}
			if read || buf.Len() != 0 {
				t.Error("an invalid filter read or wrote something")
			}
		})
	}
}

// TestParseTime: both forms, and the inputs refused.
func TestParseTime(t *testing.T) {
	day := time.Date(2025, 11, 20, 0, 53, 20, 0, time.UTC)
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"2025-11-20T12:00:05Z", "2025-11-20T12:00:05Z"},
		{"2025-11-20T13:00:05.5+01:00", "2025-11-20T12:00:05.5Z"},
		{"12:00:05", "2025-11-20T12:00:05Z"},
		{"00:00:00.000000001", "2025-11-20T00:00:00.000000001Z"},
	} {
		spec, err := ParseTime(tc.in)
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		ns, err := spec.Resolve(day)
		if err != nil {
			t.Fatal(err)
		}
		if got := time.Unix(0, int64(ns)).UTC().Format(time.RFC3339Nano); got != tc.want {
			t.Errorf("%q resolves to %s, want %s", tc.in, got, tc.want)
		}
	}
	for _, bad := range []string{"", "noon", "25:00:00", "12:00", "1969-12-31T23:59:59Z",
		"2300-01-01T00:00:00Z", "1763600000"} {
		if _, err := ParseTime(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: err %v, want ErrInvalid", bad, err)
		}
	}
	spec, _ := ParseTime("12:00:00")
	if _, err := spec.Resolve(time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Error("a time of day in year 9999 resolved")
	}
}

// TestSourceError: a source that fails stops the query with its error
// and no truncation record.
func TestSourceError(t *testing.T) {
	in := fixture(t)
	boom := errors.New("boom")
	in.Sources[1].Events = func(context.Context, func(*schema.Event) error) error { return boom }
	var buf bytes.Buffer
	if _, err := Run(context.Background(), &buf, in, Filter{}, 1<<20); !errors.Is(err, boom) {
		t.Fatalf("err %v, want boom", err)
	}
	if strings.Contains(buf.String(), "truncated") {
		t.Error("a failed query wrote a truncation record")
	}
}

// TestMatchesMethod: by class#method and by the full frame.
func TestMatchesMethod(t *testing.T) {
	m := schema.Method{Frame: "com.shop.DataStore#save(DataStore.java:42)", Class: "com.shop.DataStore",
		Method: "save"}
	for pattern, want := range map[string]bool{"com.shop.DataStore#save": true, "*DataStore#save": true,
		"*#save(DataStore.java:42)": true, "*#load": false, "": false} {
		if got := (Filter{Method: pattern}).MatchesMethod(&m); got != want {
			t.Errorf("%q: %v, want %v", pattern, got, want)
		}
	}
}
