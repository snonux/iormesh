package integrationtests

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"iormesh/internal/render"
	"iormesh/internal/schema"
	"iormesh/internal/viewmodel"
)

// The SVG goldens of the spread (docs/milestones.md M5 "Deterministic SVG
// export": "M0 scenarios get visual goldens"), beside its expect-spread.json.
// IORMESH_UPDATE_SVG=1 rewrites them from the run.
const (
	graphGolden    = "graph-spread.svg"
	checkoutGolden = "sequence-checkout-spread.svg"
	updateSVGEnv   = "IORMESH_UPDATE_SVG"
)

// assertSVGExports: a rebuild over the same store writes the same bytes
// (graph.svg, every sequence SVG, model.json, report.json) into the same
// site folder; graph.svg names the hosts and the named services; each
// sequence SVG is its conversation of the model; and the mesh and the
// checkout, normalized as goldens are (no run's ids, the scenario's own
// files), render to the goldens.
func assertSVGExports(t *testing.T, b *viewsBuild, c viewmodel.Conversation) {
	before, sites := readExports(t, b.dir), siteFolders(t, b.dir)
	b.build(t)
	after := readExports(t, b.dir)
	if !maps.EqualFunc(before, after, bytes.Equal) {
		t.Errorf("a rebuild changed the exports: %q before, %q after", slices.Sorted(maps.Keys(before)),
			slices.Sorted(maps.Keys(after)))
	}
	if got := siteFolders(t, b.dir); !slices.Equal(got, sites) || len(got) != 1 {
		t.Errorf("site folders %q, then %q: want one, the same", sites, got)
	}
	for _, cv := range b.model.Conversations {
		if !bytes.Equal(after["sequence/"+cv.ID+".svg"], render.Sequence(cv)) {
			t.Errorf("sequence/%s.svg is not its conversation's", cv.ID)
		}
	}
	wantInOrder(t, "graph.svg", string(after["graph.svg"]), ">host hosta<", ">inventory: java ",
		">host hostb<", ">orders: java ", ">host hostc<", ">billing: java ")
	matchSVGGolden(t, graphGolden, render.Graph(canonicalGraph(t, b)))
	matchSVGGolden(t, checkoutGolden, render.Sequence(b.canonicalCheckout(c)))
}

// readExports reads a build's exports by their path below its directory.
func readExports(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, name := range []string{"graph.svg", "model.json", "report.json", "graph.json"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		out[name] = data
	}
	err := filepath.WalkDir(filepath.Join(dir, "sequence"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		out["sequence/"+d.Name()] = data
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// siteFolders names the run folders below the build's sites/.
func siteFolders(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "sites"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	return out
}

// matchSVGGolden compares got with the golden, or rewrites it.
func matchSVGGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join(scenariosDir, "microservices", name)
	if os.Getenv(updateSVGEnv) == "1" {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		chownToSudoUser(t, []string{path})
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (%s=1 writes it)", err, updateSVGEnv)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s does not match its golden %s:\n%s", name, path, got)
	}
}

// canonicalCheckout is the checkout as its golden draws it: the
// conversation's lifelines without their run's ids, its story's steps
// (hops and the scenario's files, $tmp for the harness's directory)
// without durations, refs or stacks. The lanes keep their indices: the
// lifelines are the run's, in first-appearance order.
func (b *viewsBuild) canonicalCheckout(c viewmodel.Conversation) viewmodel.Conversation {
	out := viewmodel.Conversation{ID: "checkout", Entry: c.Entry, Label: c.Label, Hops: c.Hops,
		Services: c.Services}
	for _, l := range c.Lifelines {
		l.PID, l.Incarnation, l.TID = 0, 0, 0
		out.Lifelines = append(out.Lifelines, l)
	}
	for _, s := range c.Steps {
		if s.Kind == viewmodel.StepFile && !strings.Contains(s.Label, b.tmp) {
			continue
		}
		s.Label, s.Host = strings.ReplaceAll(s.Label, b.tmp, "$tmp"), ""
		s.Latency, s.Ref, s.Stack = 0, "", nil
		out.Steps = append(out.Steps, s)
	}
	return out
}

// canonicalGraph is the build's graph.json as its golden draws it:
// hosts by name, processes by role, the scenario's files below $tmp,
// the fused connections by the roles at their ends — the run's machine
// ids, PIDs and connection ids gone, so is the machine's own file work.
func canonicalGraph(t *testing.T, b *viewsBuild) (schema.Graph, render.Names) {
	t.Helper()
	var g schema.Graph
	data, err := os.ReadFile(filepath.Join(b.dir, "graph.json"))
	if err == nil {
		err = json.Unmarshal(data, &g)
	}
	if err != nil {
		t.Fatalf("graph.json: %v", err)
	}
	names := render.NamesOf(b.model)
	ids := b.canonicalIDs(g, names)
	out := schema.Graph{SchemaVersion: g.SchemaVersion}
	canon := render.Names{Services: map[string]string{}}
	for _, n := range g.Nodes {
		if id, ok := ids[n.ID]; ok {
			out.Nodes = append(out.Nodes, schema.Node{Type: n.Type, ID: id, Attrs: n.Attrs})
			if svc := names.Services[n.ID]; svc != "" {
				canon.Services[id] = svc
			}
		}
	}
	for _, e := range g.Edges {
		from, okF := ids[e.From]
		to, okT := ids[e.To]
		if okF && okT {
			out.Edges = append(out.Edges, schema.Edge{Type: e.Type, From: from, To: to, State: e.State})
		}
	}
	out.Sort()
	return out, canon
}

// canonicalIDs maps the graph's kept node ids to their canonical ones.
func (b *viewsBuild) canonicalIDs(g schema.Graph, names render.Names) map[string]string {
	ids := map[string]string{}
	host := func(id string) string { return names.Hosts[id] }
	for _, n := range g.Nodes {
		switch n.Type {
		case schema.NodeHost:
			ids[n.ID] = schema.HostNodeID(host(strings.TrimPrefix(n.ID, schema.HostNodeID(""))))
		case schema.NodeProcess:
			h, rest, _ := strings.Cut(strings.TrimPrefix(n.ID, "proc:"), "/")
			pid, _ := strconv.ParseUint(strings.Split(rest, "@")[0], 10, 32)
			ids[n.ID] = "proc:" + host(h) + "/" + b.roles[uint32(pid)]
		case schema.NodeFile:
			h, path, _ := strings.Cut(strings.TrimPrefix(n.ID, "file:"), ":")
			if strings.HasPrefix(path, b.tmp+"/") {
				ids[n.ID] = schema.FileNodeID(host(h), "$tmp"+strings.TrimPrefix(path, b.tmp))
			}
		}
	}
	for conn, name := range connNames(fusedConnRoles(g, ids)) {
		ids[conn] = schema.SocketNodeID(name)
	}
	return ids
}

// connNames names each fused connection by its role pair, "a+b"; a
// second connection of one pair (a keep-alive reconnect) is "a+b#2", and
// so on, so two never share a node id. Within a pair the ordinals follow
// the connection ids: any order draws the same, since the connections of
// one pair have the same ends.
func connNames(roles map[string][]string) map[string]string {
	byPair := map[string][]string{}
	for conn, r := range roles {
		pair := strings.Join(r, "+")
		byPair[pair] = append(byPair[pair], conn)
	}
	names := map[string]string{}
	for pair, conns := range byPair {
		slices.Sort(conns)
		for i, conn := range conns {
			names[conn] = pair
			if i > 0 {
				names[conn] = pair + "#" + strconv.Itoa(i+1)
			}
		}
	}
	return names
}

// fusedConnRoles maps each fused connection node to the sorted roles of
// the processes at its ends (ids holds their canonical ids).
func fusedConnRoles(g schema.Graph, ids map[string]string) map[string][]string {
	roles := map[string][]string{}
	for _, e := range g.Edges {
		if e.Type == schema.EdgeConnected && e.State == schema.StateFused {
			_, role, _ := strings.Cut(ids[e.From], "/")
			roles[e.To] = append(roles[e.To], role)
		}
	}
	for _, r := range roles {
		slices.Sort(r)
	}
	return roles
}
