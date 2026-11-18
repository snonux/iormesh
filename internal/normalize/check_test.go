package normalize

import (
	"slices"
	"strings"
	"testing"

	"iormesh/internal/schema"
)

// TestGoldensPass checks synthetic traces shaped like real runs against
// the shipped M0 goldens: noise threads, fd reuse, startup I/O outside the
// bracket, extra open flags and IPv4-mapped addresses must all pass.
func TestGoldensPass(t *testing.T) {
	tests := []struct {
		scenario string
		build    func(run) Trace
	}{
		{"file-basic", fileBasic},
		{"tcp-loopback", tcpLoopback},
		{"noop", noop},
	}
	for _, tt := range tests {
		t.Run(tt.scenario, func(t *testing.T) {
			for _, r := range []run{runA(), runB()} {
				raw := tt.build(r)
				if err := raw.Graph.Validate(); err != nil {
					t.Fatalf("fixture graph invalid: %v", err)
				}
				got := normalized(t, raw, r, tt.scenario)
				if vs := Check(got, golden(t, tt.scenario)); len(vs) > 0 {
					t.Fatalf("violations: %v", vs)
				}
			}
		})
	}
}

// mutation changes a raw trace or an expectation to break one rule.
type mutation struct {
	name     string
	scenario string
	trace    func(*Trace)
	want     func(*Expectation)
	kind     string // the one violation kind expected
	contains string // substring of its message
}

// TestCheckViolations: each mutation must yield exactly one violation of
// the expected kind — a check that reports nothing, or everything, would
// pass a broken tracer or hide the real cause.
func TestCheckViolations(t *testing.T) {
	for _, m := range checkMutations() {
		t.Run(m.name, func(t *testing.T) {
			r := runA()
			raw := fileBasic(r)
			if m.scenario == "tcp-loopback" {
				raw = tcpLoopback(r)
			}
			want := golden(t, m.scenario)
			if m.trace != nil {
				m.trace(&raw)
			}
			if m.want != nil {
				m.want(&want)
			}
			vs := Check(normalized(t, raw, r, m.scenario), want)
			if len(vs) != 1 || vs[0].Kind != m.kind || !strings.Contains(vs[0].Msg, m.contains) {
				t.Fatalf("want one %s violation containing %q, got %v", m.kind, m.contains, vs)
			}
		})
	}
}

func checkMutations() []mutation {
	return []mutation{
		{name: "missing event", scenario: "file-basic", kind: ViolationEvent,
			trace:    dropWhere(func(e schema.Event) bool { return e.Event == schema.KindRead && e.FD == 7 }),
			contains: "declared event 5 (read fd=$fd2 bytes=4096)"},
		{name: "wrong per-thread order", scenario: "file-basic", kind: ViolationEvent,
			trace:    moveAfter(isWrite(7, 1), isClose(7, 1)),
			contains: "declared event 3 (close fd=$fd1)"},
		{name: "event on another thread", scenario: "file-basic", kind: ViolationEvent,
			trace:    editWhere(isWrite(7, 1), func(e *schema.Event) { e.TID++ }),
			contains: "declared event 2 (write"},
		{name: "fd symbol bound to two instances", scenario: "file-basic", kind: ViolationEvent,
			want:     func(x *Expectation) { x.Events[4].FD = &Ref{"$fd1"} },
			contains: "declared event 5 (read fd=$fd1"},
		{name: "two fd symbols on one instance", scenario: "file-basic", kind: ViolationEvent,
			trace: editWhere(func(e schema.Event) bool { return e.FD == 7 && e.FDGen == 2 },
				func(e *schema.Event) { e.FDGen = 1 }),
			contains: "declared event 4 (openat fd=$fd2"},
		{name: "create bits missing from open flags", scenario: "file-basic", kind: ViolationEvent,
			trace:    editWhere(isOpen(7, 1), func(e *schema.Event) { e.Args.Flags = u32p(1) }),
			contains: "declared event 1 (openat"},
		{name: "missing edge", scenario: "file-basic", kind: ViolationEdge,
			trace:    dropEdges(schema.EdgeOpened),
			contains: "declared edge 1 (opened $p1 → file:$h1:$tmp/file-basic.dat)"},
		{name: "planted secret in a path", scenario: "file-basic", kind: ViolationMustNotAppear,
			want: forbid("Bearer s3cret"),
			trace: editWhere(func(e schema.Event) bool { return e.Seq == 4 },
				func(e *schema.Event) { e.Args.Path = strp("/srv/q?auth=Bearer s3cret") }),
			contains: `"Bearer s3cret" appears 1 time(s), first in event[seq=2].args.path`},
		{name: "planted secret in a graph attr", scenario: "file-basic", kind: ViolationMustNotAppear,
			want: forbid("hunter2"),
			trace: func(tr *Trace) {
				tr.Graph.Nodes[1].Attrs = map[string]string{"argv": "--password=hunter2"}
			},
			contains: "graph.nodes[1].attrs[argv]"},
		{name: "unresolved instead of fused", scenario: "tcp-loopback", kind: ViolationEdge,
			trace: func(tr *Trace) {
				i := slices.IndexFunc(tr.Graph.Edges,
					func(e schema.Edge) bool { return e.Type == schema.EdgeConnected })
				tr.Graph.Edges[i].State = schema.StateUnresolved
			},
			contains: "declared edge 3 (connected $p1 → conn:$c1, fused)"},
		{name: "client connects to another port", scenario: "tcp-loopback", kind: ViolationEvent,
			trace: editWhere(func(e schema.Event) bool { return e.Event == schema.KindConnect },
				func(e *schema.Event) { e.Args.RemotePort = u16p(*e.Args.RemotePort + 1) }),
			contains: "declared event 7 (connect process=$p2 fd=$fd6 remote_port=$port1)"},
	}
}

func isOpen(fd int32, gen uint32) func(schema.Event) bool  { return isKind(schema.KindOpenat, fd, gen) }
func isWrite(fd int32, gen uint32) func(schema.Event) bool { return isKind(schema.KindWrite, fd, gen) }
func isClose(fd int32, gen uint32) func(schema.Event) bool { return isKind(schema.KindClose, fd, gen) }

func isKind(kind string, fd int32, gen uint32) func(schema.Event) bool {
	return func(e schema.Event) bool { return e.Event == kind && e.FD == fd && e.FDGen == gen }
}

func dropWhere(f func(schema.Event) bool) func(*Trace) {
	return func(tr *Trace) { tr.Events = slices.DeleteFunc(tr.Events, f) }
}

func editWhere(f func(schema.Event) bool, edit func(*schema.Event)) func(*Trace) {
	return func(tr *Trace) {
		for i := range tr.Events {
			if f(tr.Events[i]) {
				edit(&tr.Events[i])
			}
		}
	}
}

// moveAfter moves the first event matching what to just after the first
// event matching anchor.
func moveAfter(what, anchor func(schema.Event) bool) func(*Trace) {
	return func(tr *Trace) {
		i := slices.IndexFunc(tr.Events, what)
		e := tr.Events[i]
		tr.Events = slices.Delete(tr.Events, i, i+1)
		j := slices.IndexFunc(tr.Events, anchor)
		tr.Events = slices.Insert(tr.Events, j+1, e)
	}
}

func dropEdges(edgeType string) func(*Trace) {
	return func(tr *Trace) {
		tr.Graph.Edges = slices.DeleteFunc(tr.Graph.Edges,
			func(e schema.Edge) bool { return e.Type == edgeType })
	}
}

func forbid(s string) func(*Expectation) {
	return func(x *Expectation) { x.MustNotAppear = append(x.MustNotAppear, s) }
}

// TestCheckReportsEveryKind: one broken trace yields an event, an edge
// and a must_not_appear violation together, not just the first.
func TestCheckReportsEveryKind(t *testing.T) {
	r := runA()
	raw := fileBasic(r)
	dropWhere(func(e schema.Event) bool { return e.Event == schema.KindWrite })(&raw)
	dropEdges(schema.EdgeOpened)(&raw)
	want := golden(t, "file-basic")
	want.MustNotAppear = []string{"java"}
	var kinds []string
	for _, v := range Check(normalized(t, raw, r, "file-basic"), want) {
		kinds = append(kinds, v.Kind)
	}
	if !slices.Equal(kinds, []string{ViolationEvent, ViolationEdge, ViolationMustNotAppear}) {
		t.Fatalf("violation kinds %v", kinds)
	}
}

// TestBindings covers the bijection and undo the matcher relies on.
func TestBindings(t *testing.T) {
	b := newBindings()
	first := b.bind("$fd1", "x")
	if !first || !b.bind("$fd1", "x") || b.bind("$fd1", "y") {
		t.Fatal("a symbol must keep its first value")
	}
	if b.bind("$fd2", "x") {
		t.Fatal("two symbols of one kind must not share a value")
	}
	if !b.bind("$port1", "x") {
		t.Fatal("kinds are independent")
	}
	m := b.mark()
	if !b.matchTemplate("file:$h1:$tmp/a.dat", "file:$h3:$tmp/a.dat") || b.sym["$h1"] != "$h3" {
		t.Fatalf("template did not bind $h1: %v", b.sym)
	}
	if b.matchTemplate("conn:$c1", "proc:x") {
		t.Fatal("template prefix ignored")
	}
	b.undo(m)
	if _, ok := b.sym["$h1"]; ok || !b.bind("$h2", "$h3") {
		t.Fatal("undo must release symbol and value")
	}
}

// TestCheckIgnoresOutsideBracket: what happens outside the bracket can
// neither satisfy a declaration nor violate must_not_appear.
func TestCheckIgnoresOutsideBracket(t *testing.T) {
	r := runA()
	b := &traceBuilder{r: r}
	p := r.server
	b.open(p, p.tid, "/srv/q?auth=Bearer s3cret", 0, 5, 1)
	b.fileWork(p, "file-basic.dat") // the declared work, but too early
	b.marker(p, "file-basic", "begin")
	b.jvmNoise(p)
	b.marker(p, "file-basic", "end")
	raw := Trace{Events: b.events, Graph: r.graph([]proc{p}, []string{"file-basic.dat"}, false)}
	want := golden(t, "file-basic")
	want.MustNotAppear = []string{"Bearer s3cret"}
	var kinds []string
	for _, v := range Check(normalized(t, raw, r, "file-basic"), want) {
		kinds = append(kinds, v.Kind)
	}
	if !slices.Equal(kinds, []string{ViolationEvent, ViolationEdge}) {
		t.Fatalf("want one event and one edge violation, got kinds %v", kinds)
	}
}

// TestCheckWrongProcess: a declared client event that ran in the server
// process does not count for the client.
func TestCheckWrongProcess(t *testing.T) {
	r := runA()
	raw := tcpLoopback(r)
	editWhere(func(e schema.Event) bool {
		return e.PID == r.client.pid && e.Event == schema.KindWrite && e.Bytes == 4096
	}, func(e *schema.Event) {
		e.PID, e.PIDInc, e.TID = r.server.pid, r.server.inc, r.server.tid
	})(&raw)
	vs := Check(normalized(t, raw, r, "tcp-loopback"), golden(t, "tcp-loopback"))
	if len(vs) != 1 || !strings.Contains(vs[0].Msg, "thread $t2: declared event 2 (write process=$p2 fd=$fd4") {
		t.Fatalf("want the client's missing write, got %v", vs)
	}
}
