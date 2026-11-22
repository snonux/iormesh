package site

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"iormesh/internal/schema"
	"iormesh/internal/viewmodel"
)

// hostile is trace text written to break out of every context the page
// has: the data block (a closing tag, a comment opener, JavaScript's
// line terminators), HTML, an attribute, and the terminal (an escape
// sequence, a bidi override), plus a byte that is not UTF-8.
const hostile = "</script><script>alert(1)</script><!--<img src=x onerror=alert(2)>\"'&" +
	"\u2028\u2029\x1b[31m\u202e\xff"

// sampleModel is a small model touching every list the page renders,
// with s in each kind of string. As in a real run, the store's host
// directories (run hosts, sessions' and processes' Host) are not the
// FQDNs that steps, lifelines and threads carry: the sessions link the
// two, and the selftest's host filter check needs them to (task 4i4).
// Host directories h2 and h3 share one FQDN (a cloned VM, or a host
// that only knows itself as localhost): conversation 0002 and its
// thread are h3's alone, which h2's filter must not show; only the
// steps' and entries' refs, <project>/<host dir>/..., tell the two
// apart (task mj4). The services pin the mesh's host filter: pay is on
// h1 by its FQDN, db on h3 by its loopback scope and on no host by
// h20, which h2 merely prefixes.
func sampleModel(s string) viewmodel.Model {
	t0 := time.Date(2025, 11, 20, 0, 53, 20, 0, time.UTC)
	lanes := []viewmodel.Lifeline{{Service: "gw" + s, Host: "h1.example.org" + s, PID: 10, TID: 11, Thread: "http-1" + s},
		{Service: "orders" + s, Host: "h2.example.org", PID: 20, TID: 21, Thread: "w-3", Carrier: true}}
	steps := []viewmodel.Step{
		{Kind: viewmodel.StepRequest, Service: lanes[0].Service, Host: lanes[0].Host, Thread: lanes[0].Thread,
			To: lanes[1].Service, Label: "GET /x" + s, Ordering: viewmodel.OrderCausal, Hop: viewmodel.HopPaired,
			Stack: []string{"a.B#c(B.java:1)" + s}, Ref: "shop/h1" + s + "/h1.example.org" + s + "-s" + s + "/5",
			Lane: 0, ToLane: 1},
		{Kind: viewmodel.StepFile, Service: lanes[1].Service, Host: "h2.example.org", Thread: "w-3", Label: "open /etc/" + s,
			Ordering: viewmodel.OrderNone, Stack: []string{}, Ref: "shop/h2/h2.example.org-s2/9", Lane: 1, ToLane: -1},
		// A folded run (task vg4): the selftest checks its detail.
		{Kind: viewmodel.StepFile, Service: lanes[1].Service, Host: "h2.example.org", Thread: "w-3",
			Label: "read /usr/lib/modules" + s + " (3 calls)", Count: 3, Bytes: 12, Latency: 3000,
			Ordering: viewmodel.OrderCausal, Stack: []string{"a.B#c(B.java:2)"}, Ref: "shop/h2/h2.example.org-s2/10",
			LastRef: "shop/h2/h2.example.org-s2/12" + s, RunQuery: "iormeshctl query -thread 21 -file '" + s + "'",
			Lane: 1, ToLane: -1},
	}
	h3Step := viewmodel.Step{Kind: viewmodel.StepFile, Service: lanes[1].Service, Host: "h2.example.org",
		Thread: "w-3", Label: "open /srv", Ordering: viewmodel.OrderNone, Stack: []string{},
		Ref: "shop/h3/h2.example.org-s3/4", Lane: 0, ToLane: -1}
	return viewmodel.Model{
		Run: viewmodel.Run{Projects: []string{"shop"}, Hosts: []string{"h1" + s, "h2", "h3"}, Sessions: 3,
			First: t0, Last: t0.Add(time.Second)},
		Health: viewmodel.Health{Sessions: []viewmodel.Session{
			{Project: "shop", Host: "h1" + s, FQDN: "h1.example.org" + s, ID: "s" + s, End: s},
			{Project: "shop", Host: "h2", FQDN: "h2.example.org", ID: "s2"},
			{Project: "shop", Host: "h3", FQDN: "h2.example.org", ID: "s3"}},
			BadSegments: []viewmodel.BadSegment{{Name: s, Error: s}}, Ignored: []string{s},
			RouteFlags: []viewmodel.RouteFlag{{Kind: "route_contradicted", Subject: "shop.example.org" + s,
				Detail: s, Conns: 1}, {Kind: s, Subject: "__proto__", Detail: strings.Repeat("d", 3000)}}},
		// The routed edges: the ingress route (its FQDN the flag's
		// subject), and one whose every name is s, long and hostile.
		Routes: []viewmodel.Route{{From: "gw" + s, FQDN: "shop.example.org" + s, Ingress: "traefik",
			Service: "prod/backend", Workload: "prod/deployment/backend", Basis: "route", Conns: 3, First: t0, Last: t0},
			{From: s, Service: s + strings.Repeat("s", 3000), Pod: s, Basis: s, Endpoints: 2}},
		Services: []viewmodel.Service{{Name: "gw" + s},
			{Name: "orders" + s, Endpoints: []string{"h2.example.org:8080", "orders" + s + ":8080"},
				TopPaths: []viewmodel.PathStat{{Method: "GET", Path: "/x" + s, Count: 2}}},
			{Name: "pay", Endpoints: []string{"h1.example.org" + s + ":7000"}},
			{Name: "db", Endpoints: []string{"h20:5432", "127.0.0.1:5432@h3"}},
			{Name: "__proto__"}, {Name: "constructor"}},
		Edges: []viewmodel.Edge{{From: "gw" + s, To: "orders" + s, State: viewmodel.StateFused, Conns: 1},
			{From: "gw" + s, To: "203.0.113.9:5432", State: viewmodel.StateUnresolved, Conns: 1},
			// A script keying a plain object by service name would get
			// the prototype back for these (nb4 review).
			{From: "__proto__", To: "constructor", State: viewmodel.StateFused, Conns: 1},
			{From: "constructor", To: "toString", State: viewmodel.StateUnresolved, Conns: 1}},
		Conversations: []viewmodel.Conversation{{ID: "0001", Start: t0, Entry: "gw" + s, Label: "GET /x" + s,
			Hops: 1, Services: []string{"gw" + s, "orders" + s}, Lifelines: lanes, Steps: steps},
			{ID: "0002", Start: t0, Entry: "orders" + s, Label: "open /srv", Services: []string{"orders" + s},
				Lifelines: lanes[1:], Steps: []viewmodel.Step{h3Step}}},
		Threads: []viewmodel.ThreadStory{{Lifeline: lanes[0], Entries: []viewmodel.ThreadEntry{{Conversation: "0001",
			Step: 1, Act: viewmodel.ActSend, Label: "GET /x" + s, Peer: "orders" + s, Ordering: "causal",
			Ref: steps[0].Ref}}},
			{Lifeline: lanes[1], Entries: []viewmodel.ThreadEntry{{Conversation: "0002", Step: 1,
				Act: viewmodel.ActFile, Label: h3Step.Label, Ordering: "causal", Ref: h3Step.Ref}}}},
		Processes: []viewmodel.Process{{Host: "h1" + s, FQDN: "h1.example.org" + s, PID: 10, Comm: "java" + s, Service: "gw" + s,
			Threads: []viewmodel.ProcThread{{Name: "http-1" + s, TID: 11}},
			Files:   []viewmodel.ProcFile{{Path: "/etc/" + s, Ops: []string{"open"}}},
			Sockets: []viewmodel.ProcSocket{{ID: "c1" + s, FD: 7, Role: "client", Service: "orders" + s}}}},
		// A rollup past its tracking cap: the selftest renders the
		// processes screen's partial line (task 2i4).
		Untracked: 84,
	}
}

// inline returns the text of the page's element opened by open and
// closed by the next "</script>" or "</style>".
func inline(t *testing.T, page []byte, open, closing string) []byte {
	t.Helper()
	i := bytes.Index(page, []byte(open))
	if i < 0 {
		t.Fatalf("page has no %s", open)
	}
	rest := page[i+len(open):]
	return rest[:bytes.Index(rest, []byte(closing))]
}

// TestPageDeterministic: the same model renders the same bytes, and the
// files — so the folder name — too.
func TestPageDeterministic(t *testing.T) {
	m := sampleModel(hostile)
	a, err := Files(schema.Graph{}, m)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Files(schema.Graph{}, m)
	for i := range a {
		if a[i].Path != b[i].Path || !bytes.Equal(a[i].Data, b[i].Data) {
			t.Fatalf("%s differs between two renders", a[i].Path)
		}
	}
	if RunName(m, a) != RunName(m, b) || !strings.HasPrefix(RunName(m, a), "20251120T005321Z-") {
		t.Errorf("run name %s", RunName(m, a))
	}
}

// TestPageCSP: the policy is default-src 'none' and allows exactly the
// inline script and stylesheet the page carries, by hash.
func TestPageCSP(t *testing.T) {
	page, err := Page(sampleModel(""))
	if err != nil {
		t.Fatal(err)
	}
	policy := string(inline(t, page, `<meta http-equiv="Content-Security-Policy" content="`, `"`))
	script := inline(t, page, "<script>", "</script>")
	style := inline(t, page, "<style>", "</style>")
	for _, want := range []string{"default-src 'none'", "script-src '" + hash(string(script)) + "';",
		"style-src '" + hash(string(style)) + "';", "connect-src 'none'", "img-src 'none'", "base-uri 'none'",
		"form-action 'none'"} {
		if !strings.Contains(policy, want) {
			t.Errorf("policy %q lacks %q", policy, want)
		}
	}
	for _, bad := range []string{"unsafe", "http", "*", "data:", "'self'"} {
		if strings.Contains(policy, bad) {
			t.Errorf("policy %q allows %q", policy, bad)
		}
	}
	if bytes.Count(page, []byte("<script")) != 2 || bytes.Count(page, []byte("<style")) != 1 {
		t.Error("page has another script or style element the policy would have to allow")
	}
}

// TestNoNetwork: nothing in the page or the script names a remote
// origin or a way to reach one, and the script writes no markup.
func TestNoNetwork(t *testing.T) {
	page, err := Page(sampleModel(""))
	if err != nil {
		t.Fatal(err)
	}
	// The one URL is the SVG namespace createElementNS takes — a name,
	// never fetched.
	urls := regexp.MustCompile(`(?i)(https?:|ftp:|wss?:)//[^\s'"]*`).FindAll(page, -1)
	for _, u := range urls {
		if string(u) != "http://www.w3.org/2000/svg" {
			t.Errorf("page names %s", u)
		}
	}
	for _, bad := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(",
		"Function(", "fetch(", "XMLHttpRequest", "WebSocket", "EventSource", "import(", "sendBeacon",
		"setAttribute('href'", "setAttribute('src'", ".src =", "localStorage", "</script", "<!--"} {
		if strings.Contains(appJS, bad) {
			t.Errorf("app.js uses %q", bad)
		}
	}
	if regexp.MustCompile(`(?i)\s(src|href|action)=`).Match(page) {
		t.Error("the page's markup loads or links something")
	}
}

// TestHostileEscaped: a model full of hostile strings stays inside its
// data block — two script elements, one comment-free JSON block that
// parses back to the model — and no raw character of it reaches the
// HTML.
func TestHostileEscaped(t *testing.T) {
	m := sampleModel(hostile)
	page, err := Page(m)
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(page, []byte("</script>")); n != 2 {
		t.Errorf("%d closing script tags, want 2", n)
	}
	for _, bad := range []string{"<img", "<!--", "\u2028", "\u2029", "\x1b", "alert(1)</script>"} {
		if bytes.Contains(page, []byte(bad)) {
			t.Errorf("page holds raw %q", bad)
		}
	}
	data := inline(t, page, `<script type="application/json" id="model">`, "</script>")
	var back viewmodel.Model
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("data block does not parse: %v", err)
	}
	if back.Edges[0].From != strings.ToValidUTF8("gw"+hostile, "�") {
		t.Errorf("round trip: %q", back.Edges[0].From)
	}
	if title := inline(t, page, "<title>", "</title>"); bytes.ContainsAny(title, "<>") {
		t.Errorf("title %q", title)
	}
}

// TestWriteKeepsRuns: every build writes a folder; the same model
// rewrites the same one byte for byte, a new model adds a folder and
// leaves the old, and latest follows the newest.
func TestWriteKeepsRuns(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sites")
	m := sampleModel("")
	first, err := Write(root, schema.Graph{}, m)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := os.ReadFile(filepath.Join(first, IndexName))
	again, err := Write(root, schema.Graph{}, m)
	if err != nil || again != first {
		t.Fatalf("rebuild wrote %s (%v), want %s", again, err, first)
	}
	if b, _ := os.ReadFile(filepath.Join(again, IndexName)); !bytes.Equal(b, page) {
		t.Fatal("rebuild changed index.html")
	}
	m.Run.Last = m.Run.Last.Add(time.Minute)
	second, err := Write(root, schema.Graph{}, m)
	if err != nil || second == first {
		t.Fatalf("new run wrote %s (%v)", second, err)
	}
	for _, f := range []string{first, second} {
		for _, name := range []string{IndexName, "graph.svg", "sequence/0001.svg"} {
			if _, err := os.Stat(filepath.Join(f, name)); err != nil {
				t.Error(err)
			}
		}
	}
	if got, err := Latest(root); err != nil || got != filepath.Join(second, IndexName) {
		t.Errorf("latest %s (%v)", got, err)
	}
	if fi, _ := os.Stat(second); fi.Mode().Perm() != 0o700 {
		t.Errorf("run folder mode %v", fi.Mode().Perm())
	}
	for _, name := range []string{IndexName, "graph.svg", "sequence/0001.svg"} {
		if fi, err := os.Stat(filepath.Join(second, name)); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v, want mode 0600", name, err)
		}
	}
}

// TestWriteRefuses: a sites root that is a symlink, and a conversation
// ID that is no number (it names a file), are refused.
func TestWriteRefuses(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "sites")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(link, schema.Graph{}, sampleModel("")); err == nil {
		t.Error("symlinked sites root accepted")
	}
	for _, id := range []string{"../../evil", "0001/x", "", "1.svg"} {
		m := sampleModel("")
		m.Conversations[0].ID = id
		if _, err := Write(filepath.Join(dir, "real"), schema.Graph{}, m); err == nil {
			t.Errorf("conversation ID %q accepted", id)
		}
	}
}

// TestLatestRefusesEscapes: a latest link naming anything but a folder
// directly under the root is refused.
func TestLatestRefusesEscapes(t *testing.T) {
	root := t.TempDir()
	if _, err := Latest(root); err == nil {
		t.Error("no link: no error")
	}
	// A planted symlink beside the runs, pointing elsewhere, is no run.
	elsewhere := t.TempDir()
	if err := os.WriteFile(filepath.Join(elsewhere, IndexName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(root, "planted")); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"../elsewhere", "/etc", "..", "planted"} {
		link := filepath.Join(root, LatestName)
		_ = os.Remove(link)
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := Latest(root); err == nil {
			t.Errorf("latest -> %s accepted", target)
		}
	}
}

// TestRoutedEdges: the routed edges and route flags reach the page's
// data block whole (the script draws them from there) and graph.svg's
// band; hostile names among them stay data — escaped in the block,
// escaped and cut in the SVG — and the script routes and classes them
// only by fixed values.
func TestRoutedEdges(t *testing.T) {
	files, err := Files(schema.Graph{}, sampleModel(hostile))
	if err != nil {
		t.Fatal(err)
	}
	data := inline(t, files[0].Data, `<script type="application/json" id="model">`, "</script>")
	var back viewmodel.Model
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("data block does not parse: %v", err)
	}
	if len(back.Routes) != 2 || len(back.Health.RouteFlags) != 2 || back.Routes[0].Ingress != "traefik" {
		t.Errorf("routes in the data block: %+v, flags %+v", back.Routes, back.Health.RouteFlags)
	}
	svg := string(files[1].Data)
	for _, want := range []string{`<rect class="fqdn"`, `<rect class="ingress"`, ">traefik<", `<line class="routed"`,
		"route_contradicted shop.example.org"} {
		if !strings.Contains(svg, want) {
			t.Errorf("graph.svg lacks %q", want)
		}
	}
	for _, bad := range []string{"<script", "\x1b", "<img", strings.Repeat("s", 100)} {
		if strings.Contains(svg, bad) {
			t.Errorf("graph.svg holds %q", bad)
		}
	}
	for _, want := range []string{"function routedBand", "function routeDetail", "function flagDetail",
		"['mesh', 'route', i]", "['mesh', 'flag', i]", "['fused', 'service', 'route'].indexOf(r.basis)"} {
		if !strings.Contains(appJS, want) {
			t.Errorf("app.js lacks %q", want)
		}
	}
}

// TestRoutelessGraphUnchanged: a model without routes gets the graph.svg
// a routeless renderer drew — no band.
func TestRoutelessGraphUnchanged(t *testing.T) {
	m := sampleModel("")
	m.Routes, m.Health.RouteFlags = nil, nil
	files, err := Files(schema.Graph{}, m)
	if err != nil {
		t.Fatal(err)
	}
	if svg := string(files[1].Data); strings.Contains(svg, "routed") || strings.Contains(svg, "flag") {
		t.Errorf("routeless graph.svg draws a band:\n%s", svg)
	}
}

// TestProcessesPartial (task 2i4): a model whose rollup hit its
// tracking cap carries untracked in the page's data block, and the
// processes screen words the same partial line `show processes` prints.
func TestProcessesPartial(t *testing.T) {
	m := sampleModel("")
	files, err := Files(schema.Graph{}, m)
	if err != nil {
		t.Fatal(err)
	}
	data := inline(t, files[0].Data, `<script type="application/json" id="model">`, "</script>")
	var back viewmodel.Model
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("data block does not parse: %v", err)
	}
	if back.Untracked != 84 {
		t.Errorf("data block untracked %d, want 84", back.Untracked)
	}
	for _, want := range []string{"function partial(", "partial(M.untracked)",
		"' events past the rollup’s tracking cap are not in it'"} {
		if !strings.Contains(appJS, want) {
			t.Errorf("app.js lacks %q", want)
		}
	}
}
