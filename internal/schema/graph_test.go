package schema

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// loopbackGraph is the shape tcp-loopback must produce: two processes on
// one host, each with its file, and exactly one fused connection that both
// point at. Built out of order on purpose so the test exercises Sort.
func loopbackGraph() Graph {
	const host = "3f2a"
	server := ProcessNodeID(host, 100, 1000)
	client := ProcessNodeID(host, 200, 2000)
	conn := SocketNodeID("0123456789abcdef")
	g := Graph{
		SchemaVersion: Version,
		Nodes: []Node{
			{Type: NodeSocket, ID: conn, Attrs: map[string]string{"local": "::1:8080", "remote": "::1:40000"}},
			{Type: NodeProcess, ID: client, Attrs: map[string]string{"comm": "java"}},
			{Type: NodeProcess, ID: server, Attrs: map[string]string{"comm": "java"}},
			{Type: NodeFile, ID: FileNodeID(host, "/tmp/server.dat")},
			{Type: NodeFile, ID: FileNodeID(host, "/tmp/client.dat")},
			{Type: NodeHost, ID: HostNodeID(host)},
		},
		Edges: []Edge{
			{Type: EdgeConnected, From: server, To: conn, State: StateFused},
			{Type: EdgeConnected, From: client, To: conn, State: StateFused},
			{Type: EdgeOpened, From: server, To: FileNodeID(host, "/tmp/server.dat")},
			{Type: EdgeOpened, From: client, To: FileNodeID(host, "/tmp/client.dat")},
			{Type: EdgeRunsOn, From: server, To: HostNodeID(host)},
			{Type: EdgeRunsOn, From: client, To: HostNodeID(host)},
		},
	}
	g.Sort()
	return g
}

func TestNodeIDs(t *testing.T) {
	cases := map[string]string{
		HostNodeID("h1"):              "host:h1",
		ProcessNodeID("h1", 4711, 99): "proc:h1/4711@99",
		FileNodeID("h1", "/etc/x"):    "file:h1:/etc/x",
		SocketNodeID("abc"):           "conn:abc",
		AppNodeID("shop", "*Main*"):   "app:shop/*Main*",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestGraphSortIsCanonical(t *testing.T) {
	g := loopbackGraph()
	if g.Nodes[0].Type != NodeFile || g.Nodes[len(g.Nodes)-1].Type != NodeSocket {
		t.Fatalf("nodes not sorted by type: %+v", g.Nodes)
	}
	if g.Edges[0].Type != EdgeConnected || g.Edges[0].From > g.Edges[1].From {
		t.Fatalf("edges not sorted by (type, from): %+v", g.Edges)
	}
	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestGraphRoundTrip(t *testing.T) {
	want := loopbackGraph()
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeGraph(data)
	if err != nil {
		t.Fatalf("DecodeGraph: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip changed the graph:\n got %+v\nwant %+v", got, want)
	}
	again, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(data) {
		t.Fatalf("export not byte-stable:\n%s\n%s", again, data)
	}
}

// TestGraphValidateRejects breaks one invariant per case.
func TestGraphValidateRejects(t *testing.T) {
	cases := map[string]func(*Graph){
		"bad schema_version": func(g *Graph) { g.SchemaVersion = 0 },
		"unknown node type":  func(g *Graph) { g.Nodes[0].Type = "pod" },
		"id prefix mismatch": func(g *Graph) { g.Nodes[0].ID = "proc:x" },
		"bare prefix id":     func(g *Graph) { g.Nodes[0].ID = "file:" },
		"file id no host":    func(g *Graph) { g.Nodes[0].ID = "file:/tmp/x" },
		"file id empty host": func(g *Graph) { g.Nodes[0].ID = "file::/tmp/x" },
		"file id no path":    func(g *Graph) { g.Nodes[0].ID = "file:h1:" },
		"duplicate node":     func(g *Graph) { g.Nodes = append(g.Nodes, g.Nodes[len(g.Nodes)-1]) },
		"unsorted nodes": func(g *Graph) {
			g.Nodes[0], g.Nodes[1] = g.Nodes[1], g.Nodes[0]
		},
		"unknown edge type":   func(g *Graph) { g.Edges[0].Type = "talks_to" },
		"dangling edge":       func(g *Graph) { g.Edges[0].To = "conn:missing" },
		"wrong endpoint type": func(g *Graph) { g.Edges[len(g.Edges)-1].To = g.Nodes[0].ID },
		"connected no state":  func(g *Graph) { g.Edges[0].State = "" },
		"connected bad state": func(g *Graph) { g.Edges[0].State = "maybe" },
		"opened with state":   func(g *Graph) { g.Edges[2].State = StateFused },
		"duplicate edge":      func(g *Graph) { g.Edges = append(g.Edges, g.Edges[len(g.Edges)-1]) },
		"unsorted edges": func(g *Graph) {
			g.Edges[0], g.Edges[1] = g.Edges[1], g.Edges[0]
		},
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			g := loopbackGraph()
			breakIt(&g)
			if err := g.Validate(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate() = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestDecodeGraphRejectsMalformed(t *testing.T) {
	data, err := json.Marshal(loopbackGraph())
	if err != nil {
		t.Fatal(err)
	}
	line := string(data)
	cases := map[string]string{
		"missing nodes":   `{"schema_version":1,"edges":[]}`,
		"missing edges":   `{"schema_version":1,"nodes":[]}`,
		"missing version": `{"nodes":[],"edges":[]}`,
		"unknown key":     strings.Replace(line, `"nodes"`, `"extra":1,"nodes"`, 1),
		"unknown node key": strings.Replace(line, `"type":"file"`,
			`"type":"file","label":"x"`, 1),
		"trailing data": line + `x`,
		"null nodes":    `{"schema_version":1,"nodes":null,"edges":[]}`,
		"null edges":    `{"schema_version":1,"nodes":[],"edges":null}`,
		"null node id": strings.Replace(line, `"type":"host","id":"host:3f2a"`,
			`"type":"host","id":null`, 1),
		"case-folded key": strings.Replace(line, `"edges"`, `"Edges"`, 1),
		"case-folded node key": strings.Replace(line, `"type":"host"`,
			`"type":"host","Type":"pod"`, 1),
		"case-folded edge key": strings.Replace(line, `"from"`, `"From"`, 1),
		"version 2":            strings.Replace(line, `"schema_version":1`, `"schema_version":2`, 1),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeGraph([]byte(in)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("DecodeGraph(%s) = %v, want ErrInvalid", in, err)
			}
		})
	}
}

// TestEmptyGraphRoundTrip checks that a zero Graph (nil slices) exports
// as arrays, not null, and reads back.
func TestEmptyGraphRoundTrip(t *testing.T) {
	data, err := json.Marshal(Graph{SchemaVersion: Version})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"schema_version":1,"nodes":[],"edges":[]}`; string(data) != want {
		t.Fatalf("got %s, want %s", data, want)
	}
	if _, err := DecodeGraph(data); err != nil {
		t.Fatal(err)
	}
}

func TestEmptyGraphIsValid(t *testing.T) {
	g, err := DecodeGraph([]byte(`{"schema_version":1,"nodes":[],"edges":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Nodes) != 0 || len(g.Edges) != 0 {
		t.Fatalf("unexpected content: %+v", g)
	}
}

// restartGraph is what a pattern session exports for one app restarted
// once: two process incarnations, each linked from the one app node.
func restartGraph() Graph {
	const host = "3f2a"
	app := AppNodeID("demo", "*RestartApp*")
	one, two := ProcessNodeID(host, 100, 1000), ProcessNodeID(host, 101, 2000)
	g := Graph{SchemaVersion: Version,
		Nodes: []Node{{Type: NodeApp, ID: app}, {Type: NodeProcess, ID: one},
			{Type: NodeProcess, ID: two}, {Type: NodeHost, ID: HostNodeID(host)}},
		Edges: []Edge{{Type: EdgeIncarnation, From: app, To: one},
			{Type: EdgeIncarnation, From: app, To: two},
			{Type: EdgeRunsOn, From: one, To: HostNodeID(host)},
			{Type: EdgeRunsOn, From: two, To: HostNodeID(host)}}}
	g.Sort()
	return g
}

func TestGraphAcceptsAppIncarnations(t *testing.T) {
	if err := restartGraph().Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// TestGraphRejectsBadApps: an app id without its project or pattern, and
// an incarnation edge that does not run app → process, are invalid.
func TestGraphRejectsBadApps(t *testing.T) {
	cases := map[string]func(*Graph){
		"app id without pattern": func(g *Graph) { g.Nodes[0].ID = "app:demo/" },
		"app id without project": func(g *Graph) { g.Nodes[0].ID = "app:/*x*" },
		"app id without slash":   func(g *Graph) { g.Nodes[0].ID = "app:demo" },
		"incarnation reversed": func(g *Graph) {
			g.Edges[0].From, g.Edges[0].To = g.Edges[0].To, g.Edges[0].From
		},
		"incarnation with state": func(g *Graph) { g.Edges[0].State = StateFused },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			g := restartGraph()
			breakIt(&g)
			g.Sort()
			if err := g.Validate(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate = %v, want ErrInvalid", err)
			}
		})
	}
}
