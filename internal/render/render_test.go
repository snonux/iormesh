package render

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"testing"

	"iormesh/internal/schema"
	"iormesh/internal/viewmodel"
)

// hostileGraph is a small graph whose names carry XML specials and
// escape sequences.
func hostileGraph() schema.Graph {
	g := schema.Graph{SchemaVersion: schema.Version, Nodes: []schema.Node{
		{Type: schema.NodeHost, ID: schema.HostNodeID("h1")},
		{Type: schema.NodeProcess, ID: schema.ProcessNodeID("h1", 7, 1), Attrs: map[string]string{"comm": "a<b>&\x1b[2J"}},
		{Type: schema.NodeFile, ID: schema.FileNodeID("h1", `/tmp/"q'`)},
		{Type: schema.NodeSocket, ID: schema.SocketNodeID("c1")},
	}, Edges: []schema.Edge{
		{Type: schema.EdgeConnected, From: schema.ProcessNodeID("h1", 7, 1), To: schema.SocketNodeID("c1"),
			State: schema.StateUnresolved},
		{Type: schema.EdgeOpened, From: schema.ProcessNodeID("h1", 7, 1), To: schema.FileNodeID("h1", `/tmp/"q'`)},
	}}
	g.Sort()
	return g
}

// wellFormed parses an SVG document as XML.
func wellFormed(t *testing.T, doc []byte) {
	t.Helper()
	d := xml.NewDecoder(bytes.NewReader(doc))
	for {
		if _, err := d.Token(); err == io.EOF {
			return
		} else if err != nil {
			t.Fatalf("not well-formed XML: %v\n%s", err, doc)
		}
	}
}

// TestDeterministicAndEscaped: each export rendered twice is the same
// bytes, parses as XML, and carries no raw control character.
func TestDeterministicAndEscaped(t *testing.T) {
	conv := viewmodel.Conversation{ID: "0001", Entry: "gw", Label: "GET /<x>",
		Services: []string{"gw", "orders&co"}, MoreSteps: 2, Steps: []viewmodel.Step{
			{Kind: viewmodel.StepRequest, Service: "gw", To: "orders&co", Label: "GET /\x1b[31m",
				Ordering: viewmodel.OrderCausal},
			{Kind: viewmodel.StepFile, Service: "orders&co", Thread: "w", Label: "read /a", Ordering: viewmodel.OrderCausal},
			{Kind: viewmodel.StepRequest, Service: "orders&co", To: "orders&co", Label: "self",
				Ordering: viewmodel.OrderNone},
			{Kind: viewmodel.StepResponse, Service: "orders&co", To: "gw", Label: "200", Ordering: viewmodel.OrderTime},
		}}
	for name, render := range map[string]func() []byte{
		"graph":    func() []byte { return Graph(hostileGraph(), Names{}) },
		"sequence": func() []byte { return Sequence(conv) },
	} {
		a, b := render(), render()
		if !bytes.Equal(a, b) {
			t.Errorf("%s: two renders differ", name)
		}
		wellFormed(t, a)
		if bytes.ContainsAny(a, "\x1b\x07") {
			t.Errorf("%s: raw control character", name)
		}
	}
	seq := string(Sequence(conv))
	for _, want := range []string{`class="unordered"`, `class="time-ordered"`, "? self", "truncated: 2 more steps"} {
		if !strings.Contains(seq, want) {
			t.Errorf("sequence lacks %q", want)
		}
	}
	if !strings.Contains(string(Graph(hostileGraph(), Names{})), `class="unresolved"`) {
		t.Error("graph: the unresolved connection is not marked")
	}
	conv.Steps[0].Ordering = `x" onload="alert(1)`
	if out := string(Sequence(conv)); strings.Contains(out, "onload") || !strings.Contains(out, `class="other"`) {
		t.Errorf("a class from the model is written raw:\n%s", out)
	}
}

// TestSequenceLifelines: one column per thread lifeline, its head naming
// service and thread; a carrier's virtual-thread lifeline is drawn as a
// "carrier" lane marked "(virtual)"; a lane index outside the list (a
// hostile model) is not drawn at all — no guessed column — and counted.
func TestSequenceLifelines(t *testing.T) {
	conv := viewmodel.Conversation{ID: "0001", Services: []string{"gw", "orders"},
		Lifelines: []viewmodel.Lifeline{{Service: "gw", Thread: "http-1"},
			{Service: "orders", Thread: "carrier-1", Carrier: true}, {Service: "orders", Thread: "w-3"}},
		Steps: []viewmodel.Step{
			{Kind: viewmodel.StepRequest, To: "orders", Label: "GET /a", Lane: 0, ToLane: 1,
				Ordering: viewmodel.OrderCausal},
			{Kind: viewmodel.StepFile, Label: "read /x", Lane: 2, ToLane: -1, Ordering: viewmodel.OrderCausal},
			{Kind: viewmodel.StepResponse, To: "gw", Label: "hostile-a", Lane: 99, ToLane: 0,
				Ordering: viewmodel.OrderCausal},
			{Kind: viewmodel.StepResponse, To: "gw", Label: "hostile-b", Lane: 1, ToLane: 42,
				Ordering: viewmodel.OrderCausal},
		}}
	out := string(Sequence(conv))
	wellFormed(t, []byte(out))
	for _, want := range []string{`<rect class="carrier"`, "carrier-1 (virtual)", ">w-3<", ">http-1<"} {
		if !strings.Contains(out, want) {
			t.Errorf("sequence lacks %q:\n%s", want, out)
		}
	}
	if n := strings.Count(out, `class="life"`); n != 3 {
		t.Errorf("%d lifelines, want 3", n)
	}
	if strings.Contains(out, "hostile-") || strings.Count(out, `<line class="step"`) != 1 ||
		!strings.Contains(out, "2 steps with invalid lanes not drawn") {
		t.Errorf("out-of-range steps drawn or not counted:\n%s", out)
	}
}

// TestGraphColumnCap: a host with more nodes than a column holds shows
// the first ones and counts the rest.
func TestGraphColumnCap(t *testing.T) {
	g := schema.Graph{SchemaVersion: schema.Version}
	g.Nodes = append(g.Nodes, schema.Node{Type: schema.NodeHost, ID: schema.HostNodeID("h")})
	for i := range maxColumnNodes + 5 {
		g.Nodes = append(g.Nodes, schema.Node{Type: schema.NodeFile, ID: schema.FileNodeID("h", fmt.Sprintf("/f%04d", i))})
	}
	g.Sort()
	out := string(Graph(g, Names{}))
	if !strings.Contains(out, "+6 more") || strings.Count(out, `<rect class="file"`) != maxColumnNodes-1 {
		t.Errorf("cap: %d file boxes", strings.Count(out, `<rect class="file"`))
	}
	if got := short(strings.Repeat("x", 100)); len([]rune(got)) != maxLabel || !strings.HasSuffix(got, "…") {
		t.Errorf("short: %q", got)
	}
}

// TestGraphNames: with the model's names, host columns are titled and
// ordered by host name — not by the random machine id — a server
// carries the service it serves (its lifeline name, without an app, is
// only its comm) and a client its app; a process with neither, and a
// host the model never named, keep their ids
// (task rb4: graph.svg said "java PID" on a machine id while every
// other surface said "orders" on "hostb").
func TestGraphNames(t *testing.T) {
	g := schema.Graph{SchemaVersion: schema.Version}
	for _, h := range []string{"ffff", "aaaa", "zzzz"} {
		g.Nodes = append(g.Nodes, schema.Node{Type: schema.NodeHost, ID: schema.HostNodeID(h)},
			schema.Node{Type: schema.NodeProcess, ID: schema.ProcessNodeID(h, 7, 1),
				Attrs: map[string]string{"comm": "java"}})
	}
	g.Sort()
	m := viewmodel.Model{Processes: []viewmodel.Process{
		{ID: schema.ProcessNodeID("ffff", 7, 1), Host: "hosta", Comm: "java", Service: "java", Serves: []string{"orders"}},
		{ID: schema.ProcessNodeID("aaaa", 7, 1), Host: "hostb", Comm: "java", Service: "java"},
		{ID: schema.ProcessNodeID("zzzz", 7, 1), Comm: "java", Service: "edge/gw"},
	}}
	out := string(Graph(g, NamesOf(m)))
	wellFormed(t, []byte(out))
	a, b, c := strings.Index(out, ">host hosta<"), strings.Index(out, ">host hostb<"), strings.Index(out, ">host zzzz<")
	if a < 0 || b < 0 || c < 0 || !(a < b && b < c) {
		t.Errorf("host columns not titled and ordered by name (hosta %d, hostb %d, zzzz %d):\n%s", a, b, c, out)
	}
	for _, want := range []string{">orders: java 7@1<", ">hosta ffff<", ">java 7@1<", ">edge/gw: java 7@1<"} {
		if !strings.Contains(out, want) {
			t.Errorf("graph lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "java: java") {
		t.Errorf("a comm-only service is repeated as a name:\n%s", out)
	}
}
