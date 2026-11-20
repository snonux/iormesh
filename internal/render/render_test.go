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
		"graph":    func() []byte { return Graph(hostileGraph()) },
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
	if !strings.Contains(string(Graph(hostileGraph())), `class="unresolved"`) {
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
// hostile model) is drawn on the first lifeline, never trusted.
func TestSequenceLifelines(t *testing.T) {
	conv := viewmodel.Conversation{ID: "0001", Services: []string{"gw", "orders"},
		Lifelines: []viewmodel.Lifeline{{Service: "gw", Thread: "http-1"},
			{Service: "orders", Thread: "carrier-1", Carrier: true}, {Service: "orders", Thread: "w-3"}},
		Steps: []viewmodel.Step{
			{Kind: viewmodel.StepRequest, To: "orders", Label: "GET /a", Lane: 0, ToLane: 1,
				Ordering: viewmodel.OrderCausal},
			{Kind: viewmodel.StepFile, Label: "read /x", Lane: 2, ToLane: -1, Ordering: viewmodel.OrderCausal},
			{Kind: viewmodel.StepResponse, To: "gw", Label: "200", Lane: 99, ToLane: 42,
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
	if !strings.Contains(out, `<line class="step" x1="120" y1="150" x2="160" y2="150"`) {
		t.Errorf("the out-of-range step is not drawn on the first lifeline:\n%s", out)
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
	out := string(Graph(g))
	if !strings.Contains(out, "+6 more") || strings.Count(out, `<rect class="file"`) != maxColumnNodes-1 {
		t.Errorf("cap: %d file boxes", strings.Count(out, `<rect class="file"`))
	}
	if got := short(strings.Repeat("x", 100)); len([]rune(got)) != maxLabel || !strings.HasSuffix(got, "…") {
		t.Errorf("short: %q", got)
	}
}
