package normalize

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"iormesh/internal/schema"
)

// TestNormalizeDeterministic: the same trace normalized twice is
// identical, and two runs differing in every run-specific value (host,
// PIDs, TIDs, incarnations, fds, ports, conn id, times, tmp dir) diff
// empty inside the bracket — PLAN.md's "Deterministic exports".
func TestNormalizeDeterministic(t *testing.T) {
	for _, tt := range []struct {
		scenario string
		build    func(run) Trace
	}{{"file-basic", fileBasic}, {"tcp-loopback", tcpLoopback}} {
		t.Run(tt.scenario, func(t *testing.T) {
			a := encode(t, normalized(t, tt.build(runA()), runA(), tt.scenario))
			again := encode(t, normalized(t, tt.build(runA()), runA(), tt.scenario))
			b := encode(t, normalized(t, tt.build(runB()), runB(), tt.scenario))
			if a != again {
				t.Fatal("normalizing one trace twice differs")
			}
			if a != b {
				t.Fatalf("two runs differ after normalization:\n%s\n---\n%s", a, b)
			}
		})
	}
}

func encode(t *testing.T, tr Trace) string {
	t.Helper()
	var sb strings.Builder
	for _, e := range tr.Events {
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		sb.Write(line)
		sb.WriteByte('\n')
	}
	g, err := json.Marshal(tr.Graph)
	if err != nil {
		t.Fatal(err)
	}
	return sb.String() + string(g)
}

// TestNormalizeRewrites pins the normalized form of each run-specific
// field, and that the input is left untouched.
func TestNormalizeRewrites(t *testing.T) {
	r := runA()
	raw := tcpLoopback(r)
	before := encode(t, raw)
	got := normalized(t, raw, r, "tcp-loopback")
	if encode(t, raw) != before {
		t.Fatal("Normalize modified its input")
	}
	// The server's accept is its process's and thread's first (1), the
	// eighth fd instance in the bracket (3 + 7), and the client's
	// connect, seen first, already numbered both ports: its ephemeral
	// local port 1024, the server port 1025.
	accept := got.Events[findEvent(t, got.Events, schema.KindAccept)]
	want := map[string]any{
		"host_id": "$h1", "pid": uint32(1), "pid_incarnation": uint64(1), "tid": uint32(1),
		"fd": int32(firstDynamicFD + 7), "ret": int64(firstDynamicFD + 7), "fd_gen": uint32(1),
		"conn_id": "$c1", "local_addr": "127.0.0.1", "local_port": uint16(firstDynamicPort + 1),
		"remote_port": uint16(firstDynamicPort), "latency_ns": uint64(0),
	}
	have := map[string]any{
		"host_id": accept.HostID, "pid": accept.PID, "pid_incarnation": accept.PIDInc,
		"tid": accept.TID, "fd": accept.FD, "ret": accept.Ret, "fd_gen": accept.FDGen,
		"conn_id":    accept.ConnID,
		"local_addr": *accept.Args.LocalAddr, "local_port": *accept.Args.LocalPort,
		"remote_port": *accept.Args.RemotePort, "latency_ns": accept.LatencyNS,
	}
	if !reflect.DeepEqual(have, want) {
		t.Fatalf("accept normalized to %v, want %v", have, want)
	}
	for i, e := range got.Events {
		if r := uint64(i + 1); e.Seq != r || e.BootNS != r || e.WallNS != r {
			t.Fatalf("event %d: seq/boot/wall %d/%d/%d, want rank %d", i, e.Seq, e.BootNS, e.WallNS, r)
		}
	}
	checkNormalizedGraph(t, got.Graph)
}

// checkNormalizedGraph: node ids use the events' symbols and the graph
// stays valid and sorted.
func checkNormalizedGraph(t *testing.T, g schema.Graph) {
	t.Helper()
	if err := g.Validate(); err != nil {
		t.Fatalf("normalized graph invalid: %v", err)
	}
	ids := map[string]bool{}
	for _, n := range g.Nodes {
		ids[n.ID] = true
	}
	for _, id := range []string{"host:$h1", "proc:$h1/1@1", "proc:$h1/2@2",
		"file:$h1:$tmp/tcp-loopback-server.dat", "conn:$c1"} {
		if !ids[id] {
			t.Errorf("node %s missing; have %v", id, ids)
		}
	}
}

func findEvent(t *testing.T, events []schema.Event, kind string) int {
	t.Helper()
	for i, e := range events {
		if e.Event == kind {
			return i
		}
	}
	t.Fatalf("no %s event", kind)
	return -1
}

// TestNormalizeKeptValues: stdio fds, well-known ports and paths outside
// the tmp dir are part of what a scenario means and stay verbatim; a
// path that merely shares the tmp dir's string prefix is not under it.
func TestNormalizeKeptValues(t *testing.T) {
	r := runA()
	b := &traceBuilder{r: r}
	p := r.server
	b.io(p, p.tid, schema.KindWrite, 1, 0, 3)
	b.add(p, p.tid, schema.KindConnect, 8, 1, func(e *schema.Event) {
		e.Args.RemotePort, e.Args.RemoteAddr = u16p(443), strp("::1")
	})
	b.open(p, p.tid, r.tmp+"x/a", 0, 9, 1)
	b.open(p, p.tid, r.tmp, 0, 10, 1)
	got, err := Normalize(Trace{Events: b.events}, Options{TmpPrefix: r.tmp + "/"})
	if err != nil {
		t.Fatal(err)
	}
	e := got.Events
	if e[0].FD != 1 || *e[1].Args.RemotePort != 443 || *e[1].Args.RemoteAddr != "::1" ||
		*e[2].Args.Path != r.tmp+"x/a" || *e[3].Args.Path != "$tmp" {
		t.Fatalf("kept values rewritten: %s", encode(t, got))
	}
}

// TestBracket: events outside the bracket are dropped per process, and
// every bracket defect is an error.
func TestBracket(t *testing.T) {
	r := runA()
	got := normalized(t, tcpLoopback(r), r, "tcp-loopback")
	for _, e := range got.Events {
		if e.Args.Path != nil && strings.HasPrefix(*e.Args.Path, "/usr/lib/jvm") {
			t.Fatalf("startup I/O outside the bracket kept: %+v", e)
		}
	}
	first, last := got.Events[0], got.Events[len(got.Events)-1]
	if *first.Args.Marker != "tcp-loopback/begin" || *last.Args.Marker != "tcp-loopback/end" {
		t.Fatalf("output not delimited by the markers: %+v … %+v", first, last)
	}
	all, err := Normalize(fileBasic(r), Options{})
	if err != nil || len(all.Events) != len(fileBasic(r).Events) {
		t.Fatalf("empty Bracket must keep everything: %v", err)
	}
}

func TestBracketErrors(t *testing.T) {
	r := runA()
	p := r.server
	tests := []struct {
		name    string
		markers []string // marker values, in order, all in process p
		want    error
	}{
		{"missing", []string{"other/begin", "other/end"}, ErrNoBracket},
		{"unterminated", []string{"s/begin"}, ErrUnterminatedBracket},
		{"end before begin", []string{"s/end", "s/begin"}, ErrMalformedBracket},
		{"second begin", []string{"s/begin", "s/end", "s/begin"}, ErrMalformedBracket},
		{"second end", []string{"s/begin", "s/end", "s/end"}, ErrMalformedBracket},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &traceBuilder{r: r}
			for _, m := range tt.markers {
				scenario, edge, _ := strings.Cut(m, "/")
				b.marker(p, scenario, edge)
			}
			if _, err := Normalize(Trace{Events: b.events}, Options{Bracket: "s"}); !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
		})
	}
}

// TestBracketKeepsHostEvents: a ring-buffer drop (PID 0) inside the
// scenario window stays visible; one before it does not.
func TestBracketKeepsHostEvents(t *testing.T) {
	r := runA()
	b := &traceBuilder{r: r}
	host := proc{}
	b.add(host, 0, schema.KindDrop, schema.NoFD, 0, nil)
	b.marker(r.server, "s", "begin")
	b.add(host, 0, schema.KindDrop, schema.NoFD, 0, nil)
	b.marker(r.server, "s", "end")
	got, err := Normalize(Trace{Events: b.events}, Options{Bracket: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != 3 || got.Events[1].Event != schema.KindDrop {
		t.Fatalf("want begin, drop, end; got %s", encode(t, got))
	}
	// A host-level event stays PID 0 and takes no process ordinal, so
	// the process after it is still 1.
	drop, end := got.Events[1], got.Events[2]
	if drop.PID != 0 || drop.PIDInc != 0 || drop.TID != 0 || end.PID != 1 || end.TID != 1 {
		t.Fatalf("host event renumbered: %s", encode(t, got))
	}
}

func TestParseExpectationErrors(t *testing.T) {
	ev := `{"thread":"$t1","event":"read"}`
	tests := map[string]string{
		"unknown key":       `{"scenario":"s","schema_version":1,"events":[],"edges":[],"must_not_appear":[],"extra":1}`,
		"wrong version":     `{"scenario":"s","schema_version":2}`,
		"no scenario":       `{"schema_version":1}`,
		"trailing data":     `{"scenario":"s","schema_version":1} {}`,
		"unknown event":     `{"scenario":"s","schema_version":1,"events":[{"thread":"$t1","event":"frob"}]}`,
		"bad thread":        `{"scenario":"s","schema_version":1,"events":[{"thread":"t1","event":"read"}]}`,
		"fd of wrong kind":  `{"scenario":"s","schema_version":1,"events":[{"thread":"$t1","event":"read","fd":"$p1"}]}`,
		"thread in 2 procs": `{"scenario":"s","schema_version":1,"events":[` + strings.Replace(ev, `{`, `{"process":"$p1",`, 1) + `,` + strings.Replace(ev, `{`, `{"process":"$p2",`, 1) + `]}`,
		"empty forbidden":   `{"scenario":"s","schema_version":1,"must_not_appear":[""]}`,
		"edge without to":   `{"scenario":"s","schema_version":1,"edges":[{"type":"opened","from":"$p1"}]}`,
		"bad attribution":   `{"scenario":"s","schema_version":1,"events":[{"thread":"$t1","event":"read","attribution":"jvm"}]}`,
		"fd as app symbol":  `{"scenario":"s","schema_version":1,"events":[{"thread":"$t1","event":"read","fd":"$a1"}]}`,
	}
	for name, doc := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseExpectation([]byte(doc)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// TestNormalizePrunesGraph: with a bracket, the graph keeps only what
// bracketed events touched — not a JVM startup file opened before the
// begin marker, nor a process that never entered the scenario.
func TestNormalizePrunesGraph(t *testing.T) {
	r := runA()
	raw := fileBasic(r)
	stray := proc{pid: 999, inc: 1, tid: 999}
	g := r.graph([]proc{r.server, stray}, []string{"file-basic.dat", "other.dat"}, false)
	pid := schema.ProcessNodeID(r.host, r.server.pid, r.server.inc)
	jvm := schema.FileNodeID(r.host, "/usr/lib/jvm/lib/modules")
	g.Nodes = append(g.Nodes, schema.Node{Type: schema.NodeFile, ID: jvm})
	g.Edges = append(g.Edges, schema.Edge{Type: schema.EdgeOpened, From: pid, To: jvm})
	g.Sort()
	raw.Graph = g
	got := normalized(t, raw, r, "file-basic")
	want := normalized(t, fileBasic(r), r, "file-basic").Graph
	if !reflect.DeepEqual(got.Graph, want) {
		t.Fatalf("graph not pruned to the bracket:\n%+v\nwant\n%+v", got.Graph, want)
	}
	if err := got.Graph.Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestNormalizeArgv: an exec's argv elements under the temp directory
// become $tmp paths like args.path; everything else, including a path
// that only shares the prefix's string, stays verbatim, and the input
// argv is not written through.
func TestNormalizeArgv(t *testing.T) {
	n := normalizer{opt: Options{TmpPrefix: "/tmp/run1/"}}
	in := []string{"/jdk/bin/java", "-Dpassword=***", "/tmp/run1", "/tmp/run1/work", "/tmp/run10", "x=/tmp/run1/a"}
	keep := slices.Clone(in)
	got := n.args(schema.Args{Argv: in}).Argv
	want := []string{"/jdk/bin/java", "-Dpassword=***", "$tmp", "$tmp/work", "/tmp/run10", "x=/tmp/run1/a"}
	if !slices.Equal(got, want) {
		t.Errorf("argv %q, want %q", got, want)
	}
	if !slices.Equal(in, keep) {
		t.Errorf("input argv modified: %q", in)
	}
	if n.args(schema.Args{}).Argv != nil {
		t.Error("absent argv became non-nil")
	}
}
