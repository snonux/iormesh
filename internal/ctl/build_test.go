package ctl

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"iormesh/internal/federate"
	"iormesh/internal/schema"
	"iormesh/internal/segment"
	"iormesh/internal/servicedb"
	"iormesh/internal/viewmodel"
)

// siteRun matches a site folder's name in build's output: it carries a
// hash of the page, so it changes with the explorer's code, which the
// summary's golden need not follow (internal/site tests the page).
var siteRun = regexp.MustCompile(`sites/[0-9]{8}T[0-9]{6}Z-[0-9a-f]{12}/`)

var update = flag.Bool("update", false, "rewrite testdata/*.golden from the current output")

// buildWall0 is the synthetic traces' first wall-clock instant.
const buildWall0 = uint64(1_763_600_000) * uint64(time.Second)

func bptr[T any](v T) *T { return &v }

// hostTrace builds one synthetic host session the way the tracer records
// it: every event complete and valid, so the segment writer takes it.
type hostTrace struct {
	host, project string
	// session names the run; "" is a1b2c3d4.
	session string
	seq     uint64
	events  []schema.Event
}

// add appends one event of pid at wall offset at (µs after buildWall0).
func (h *hostTrace) add(pid uint32, kind string, fd int32, ret int64, at uint64, conn string,
	args schema.Args) *schema.Event {
	h.seq++
	ns := at * uint64(time.Microsecond)
	ev := schema.Event{SchemaVersion: schema.Version, Project: h.project, Seq: h.seq,
		BootNS: ns + 1, WallNS: buildWall0 + ns, HostID: h.host, PID: pid, PIDInc: 1, TID: pid,
		Comm: "java", Event: kind, FD: fd, Ret: ret, ConnID: conn, Args: args,
		Attribution: schema.AttributionNone}
	if ret < 0 {
		ev.Err = "EINPROGRESS"
	}
	h.events = append(h.events, ev)
	return &h.events[len(h.events)-1]
}

// header and end are the session's first and last records.
func (h *hostTrace) header() {
	ev := h.add(0, schema.KindSession, schema.NoFD, 0, 0, "", schema.Args{})
	ev.PIDInc, ev.TID, ev.Comm = 0, 0, ""
	ev.Session = &schema.Session{BootID: "b-" + h.host, FQDN: h.host + ".example.org",
		Kernel: "6.12.0", Arch: "x86_64"}
}

func (h *hostTrace) end(at uint64, reason string) {
	ev := h.add(0, schema.KindEnd, schema.NoFD, 0, at, "", schema.Args{EndReason: bptr(reason)})
	ev.PIDInc, ev.TID, ev.Comm = 0, 0, ""
}

// tcp is a tcp_state record with the four tcp_sock fields of the join
// key (docs/schema.md).
func (h *hostTrace) tcp(pid uint32, fd int32, at uint64, conn, local, peer, state string,
	sndUna, rcvNxt uint32, acked, received uint64) {
	a := tupleArgs(local, peer)
	a.SockState, a.SndUna, a.RcvNxt = bptr(state), bptr(sndUna), bptr(rcvNxt)
	a.BytesAcked, a.BytesReceived = bptr(acked), bptr(received)
	h.add(pid, schema.KindTCPState, fd, 0, at, conn, a)
}

func (h *hostTrace) http(pid uint32, at uint64, conn, reqHost, path string) {
	ev := h.add(pid, schema.KindHTTP, schema.NoFD, 0, at, conn, schema.Args{})
	ev.LatencyNS = 1000
	ev.HTTP = &schema.HTTP{Method: "GET", Scheme: "http", Host: reqHost, Path: path, Status: 200,
		Version: "HTTP/1.1"}
}

// data is a write or read of n stream bytes from sequence number seq: the
// records the merge pairs into the clock bound's messages.
func (h *hostTrace) data(pid uint32, kind string, fd int32, at uint64, conn string, seq uint32, n int64) {
	ev := h.add(pid, kind, fd, n, at, conn, schema.Args{TCPSeq: bptr(seq)})
	ev.Bytes = uint64(n)
}

func tupleArgs(local, peer string) schema.Args {
	l, p := netip.MustParseAddrPort(local), netip.MustParseAddrPort(peer)
	return schema.Args{LocalAddr: bptr(l.Addr().String()), LocalPort: bptr(l.Port()),
		RemoteAddr: bptr(p.Addr().String()), RemotePort: bptr(p.Port())}
}

// shopMesh is the build tests' mesh: app1 (project shop) calls app2
// (project billing) over one connection the merge fuses — two GETs, each
// in its own segment once rotated — and dials an untraced database that
// stays unresolved. app1's session has no end yet (still running).
func shopMesh() (app1, app2 *hostTrace) {
	const cli, srv, db = "10.0.1.1:45678", "10.0.2.2:8080", "203.0.113.9:5432"
	const kc, ks = 0x1000, 0x9000
	app1, app2 = &hostTrace{host: "hostA", project: "shop"}, &hostTrace{host: "hostB", project: "billing"}
	app1.header()
	app1.add(100, schema.KindOpenat, 3, 3, 10, "", schema.Args{Path: bptr("/etc/shop.conf")})
	app1.tcp(100, 7, 100, "c1", cli, srv, "SYN_SENT", 0, 0, 0, 0)
	app1.add(100, schema.KindConnect, 7, -115, 100, "c1", tupleArgs(cli, srv))
	app1.tcp(100, 7, 1000, "c1", cli, srv, "ESTABLISHED", kc+1, ks+1, 1, 0)
	app1.data(100, schema.KindWrite, 7, 1200, "c1", kc+1, 20)
	app1.data(100, schema.KindRead, 7, 1900, "c1", ks+1, 300)
	app1.http(100, 2000, "c1", "orders.example", "/orders/1")
	app1.http(100, 3000, "c1", "orders.example", "/orders/2")
	app1.http(100, 3500, "c1", "orders.example", "/orders/2")
	app1.tcp(100, 7, 9000, "c1", cli, srv, "CLOSE", kc+22, ks+302, 22, 301)
	app1.add(100, schema.KindConnect, 8, -115, 9100, "c2", tupleArgs("10.0.1.1:45700", db))
	app1.tcp(100, 8, 9200, "c2", "10.0.1.1:45700", db, "ESTABLISHED", 0x51, 0x61, 1, 0)
	app2.header()
	app2.tcp(200, 9, 1500, "s1", srv, cli, "CLOSE_WAIT", ks+1, kc+22, 0, 21)
	app2.add(200, schema.KindAccept4, 9, 9, 1500, "s1", tupleArgs(srv, cli))
	app2.data(200, schema.KindRead, 9, 1520, "s1", kc+1, 20)
	app2.data(200, schema.KindWrite, 9, 1600, "s1", ks+1, 300)
	app2.http(200, 2100, "s1", "", "/orders/1")
	app2.http(200, 3100, "s1", "", "/orders/2")
	app2.http(200, 3600, "s1", "", "/orders/2")
	app2.end(10_000, schema.EndStop)
	return app1, app2
}

// writeStoreSegments writes h with the tracer's segment writer, one
// segment per perSeg events, into store/<project>/<host>/ as collect
// leaves it.
func writeStoreSegments(t *testing.T, store, host string, h *hostTrace, perSeg int) []string {
	t.Helper()
	t0 := time.Unix(0, int64(buildWall0)).UTC()
	srv := filepath.Join(t.TempDir(), h.project)
	w, err := segment.New(segment.Config{Dir: srv, FQDN: h.host + ".example.org", Session: cmp.Or(h.session, "a1b2c3d4"),
		Project: h.project, Now: func() time.Time { return t0 }})
	if err != nil {
		t.Fatal(err)
	}
	for i := range h.events {
		if err := w.Event(&h.events[i]); err != nil {
			t.Fatal(err)
		}
		if (i+1)%perSeg == 0 {
			if err := w.Tick(t0.Add(time.Duration(i+1) * time.Minute)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return moveInto(t, filepath.Join(srv, segment.ReadyDir), filepath.Join(store, h.project, host))
}

// moveInto moves every file of dir into dst (created 0700) and returns
// their new paths, sorted.
func moveInto(t *testing.T, dir, dst string) []string {
	t.Helper()
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, en := range ents {
		to := filepath.Join(dst, en.Name())
		if err := os.Rename(filepath.Join(dir, en.Name()), to); err != nil {
			t.Fatal(err)
		}
		out = append(out, to)
	}
	slices.Sort(out)
	return out
}

// buildFixture is a temp store, database and builds directory.
type buildFixture struct {
	e                    env
	stdout, stderr       *bytes.Buffer
	store, db, builds    string
	app1Files, app2Files []string
}

func newBuildFixture(t *testing.T) *buildFixture {
	t.Helper()
	root := t.TempDir()
	f := &buildFixture{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{},
		store: filepath.Join(root, "traces"), db: filepath.Join(root, "share", "services.db"),
		builds: filepath.Join(root, "builds")}
	if err := os.Mkdir(f.store, 0o700); err != nil {
		t.Fatal(err)
	}
	f.e = env{stdout: f.stdout, stderr: f.stderr,
		traces:    func() (string, error) { return f.store, nil },
		servicedb: func() (string, error) { return f.db, nil },
		builds:    func() (string, error) { return f.builds, nil },
		registry:  func() (string, error) { return filepath.Join(root, "servers.yaml"), nil },
	}
	app1, app2 := shopMesh()
	f.app1Files = writeStoreSegments(t, f.store, "app1", app1, 3)
	f.app2Files = writeStoreSegments(t, f.store, "app2", app2, 3)
	return f
}

// run runs iormeshctl with args, the buffers reset first.
func (f *buildFixture) run(args ...string) int {
	f.stdout.Reset()
	f.stderr.Reset()
	return run(args, f.e)
}

func (f *buildFixture) mustRun(t *testing.T, args ...string) string {
	t.Helper()
	if code := f.run(args...); code != exitOK {
		t.Fatalf("%v: exit %d: %s", args, code, f.stderr)
	}
	return f.stdout.String()
}

// output reads one of the build's output files.
func (f *buildFixture) output(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.builds, "billing+shop", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// golden compares got with testdata/<name> (go test -update rewrites it).
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
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
		t.Errorf("%s differs (go test ./internal/ctl -update to accept):\n%s", path, got)
	}
}

// TestBuildGolden builds the two-project mesh: the summary, the merged
// graph export, the model and the rollup (report.json, and show report)
// match their goldens, and a rebuild writes
// the same bytes (offline, deterministic, idempotent on the database).
func TestBuildGolden(t *testing.T) {
	f := newBuildFixture(t)
	summary := f.mustRun(t, "build")
	golden(t, "build_summary.golden", siteRun.ReplaceAllString(strings.ReplaceAll(summary, f.builds, "$BUILDS"),
		"sites/$$RUN/"))
	graph, model, rep := f.output(t, graphFileName), f.output(t, modelFileName), f.output(t, reportFileName)
	golden(t, "build_graph.golden", graph)
	golden(t, "build_model.golden", model)
	golden(t, "build_report.golden", rep)
	golden(t, "show_report.golden", f.mustRun(t, "show", "report"))
	for range 2 {
		if again := f.mustRun(t, "build"); again != summary {
			t.Fatalf("rebuild printed\n%s\nwant\n%s", again, summary)
		}
		if f.output(t, graphFileName) != graph || f.output(t, modelFileName) != model ||
			f.output(t, reportFileName) != rep {
			t.Fatal("rebuild changed the outputs")
		}
	}
	if got := f.mustRun(t, "show"); !strings.HasPrefix(summary, got) {
		t.Errorf("show printed\n%s\nbuild printed\n%s", got, summary)
	}
}

// TestBuildNamesServices: a naming rule shows up by name in the next
// build's model — no file edited by hand.
func TestBuildNamesServices(t *testing.T) {
	f := newBuildFixture(t)
	f.mustRun(t, "build")
	f.mustRun(t, "services", "name", "-db", f.db, "-port", "8080", "orders*", "orders")
	f.mustRun(t, "build")
	out := f.mustRun(t, "show", "edges")
	if !strings.Contains(out, "orders") || strings.Contains(out, "orders.example:8080") {
		t.Errorf("show edges after naming:\n%s", out)
	}
}

// requestCount sums the endpoint requests the database holds.
func (f *buildFixture) requestCount(t *testing.T) int64 {
	t.Helper()
	d, err := servicedb.OpenExisting(context.Background(), f.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	eps, err := d.Endpoints(context.Background(), servicedb.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	var n int64
	for _, e := range eps {
		for _, r := range e.Requests {
			n += r.Count
		}
	}
	return n
}

// TestBuildCountsSurviveDeletedSegments: the request counts are exact
// over repeated builds and stay exact once the operator deletes old
// segments — each segment is its own source in the service database.
func TestBuildCountsSurviveDeletedSegments(t *testing.T) {
	f := newBuildFixture(t)
	for range 2 {
		f.mustRun(t, "build")
		if n := f.requestCount(t); n != 3 {
			t.Fatalf("%d requests after a build, want 3", n)
		}
	}
	// The segments holding app1's and app2's first requests go; the
	// database keeps what they counted.
	for _, p := range []string{f.app1Files[1], f.app2Files[1]} {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		f.mustRun(t, "build")
		if n := f.requestCount(t); n != 3 {
			t.Fatalf("%d requests after deleting segments, want 3", n)
		}
	}
}

// TestBuildBadSegment: a damaged segment is left out and named, the
// outputs of the rest are written, and the command exits 1.
func TestBuildBadSegment(t *testing.T) {
	f := newBuildFixture(t)
	bad := f.app2Files[len(f.app2Files)-1]
	data, err := os.ReadFile(bad)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, data[:len(data)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	if code := f.run("build"); code != exitError {
		t.Fatalf("exit %d", code)
	}
	name := "billing/app2/" + filepath.Base(bad)
	if !strings.Contains(f.stdout.String(), "bad        "+name+": ") ||
		!strings.Contains(f.stderr.String(), "1 bad segments left out") {
		t.Errorf("stdout:\n%s\nstderr:\n%s", f.stdout, f.stderr)
	}
	var m viewmodel.Model
	if err := json.Unmarshal([]byte(f.output(t, modelFileName)), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Health.BadSegments) != 1 || m.Health.BadSegments[0].Name != name {
		t.Errorf("model's bad segments: %+v", m.Health.BadSegments)
	}
}

// TestBuildRefusals: nothing collected, an unknown project, arguments.
func TestBuildRefusals(t *testing.T) {
	f := newBuildFixture(t)
	for _, tc := range []struct {
		args []string
		code int
		msg  string
	}{
		{[]string{"build", "-project", "nosuch"}, exitError, "nothing collected for project nosuch"},
		{[]string{"build", "-project", "-bad"}, exitUsage, "invalid project"},
		{[]string{"build", "extra"}, exitUsage, "takes no arguments"},
		{[]string{"show", "-project", "shop"}, exitError, "no build of shop yet"},
		{[]string{"show", "nosuch", "-project", "billing", "-project", "shop"}, exitUsage, "unknown view"},
	} {
		if code := f.run(tc.args...); code != tc.code || !strings.Contains(f.stderr.String(), tc.msg) {
			t.Errorf("%v: exit %d, stderr %s", tc.args, code, f.stderr)
		}
		if tc.args[0] == "build" && tc.code == exitError {
			f.mustRun(t, "build") // later cases need a build
		}
	}
	empty := newBuildFixture(t)
	if err := os.RemoveAll(empty.store); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(empty.store, 0o700); err != nil {
		t.Fatal(err)
	}
	if code := empty.run("build"); code != exitError || !strings.Contains(empty.stderr.String(), "nothing collected") {
		t.Errorf("empty store: exit %d, %s", code, empty.stderr)
	}
}

// TestShowViews: every view renders from model.json alone, and -json
// gives the model's part for the view.
func TestShowViews(t *testing.T) {
	f := newBuildFixture(t)
	f.mustRun(t, "build")
	if err := os.RemoveAll(f.store); err != nil { // show never reads segments
		t.Fatal(err)
	}
	p := []string{"-project", "shop", "-project", "billing"}
	for view, want := range map[string]string{"services": "orders.example:8080", "edges": "unresolved",
		"health": "end: stop"} {
		if out := f.mustRun(t, slices.Concat([]string{"show", view}, p)...); !strings.Contains(out, want) {
			t.Errorf("show %s:\n%s", view, out)
		}
	}
	var edges []viewmodel.Edge
	out := f.mustRun(t, slices.Concat([]string{"show", "edges", "-json"}, p)...)
	if err := json.Unmarshal([]byte(out), &edges); err != nil || len(edges) != 2 {
		t.Fatalf("show edges -json: %v\n%s", err, out)
	}
	if out := f.mustRun(t, slices.Concat([]string{"show", "edges", "-unresolved"}, p)...); strings.Contains(out, "fused") {
		t.Errorf("show edges -unresolved:\n%s", out)
	}
}

// TestRefresh: refresh collects (a fake transport moves the "servers'"
// segments into the store) and then builds what the store holds.
func TestRefresh(t *testing.T) {
	f := newBuildFixture(t)
	// The servers' ready/ folders hold the segments; the store starts
	// empty.
	servers := t.TempDir()
	for _, host := range []string{"app1", "app2"} {
		for _, proj := range []string{"shop", "billing"} {
			if dir := filepath.Join(f.store, proj, host); dirExists(dir) {
				moveInto(t, dir, filepath.Join(servers, host, proj))
			}
		}
	}
	f.mustRun(t, "add", "-project", "shop", "-pattern", "*java*", "app1")
	f.mustRun(t, "add", "-project", "billing", "-pattern", "*java*", "app2")
	f.e.transport = func(host string) (federate.Transport, error) {
		return fakeTransport{host: host, log: &[][]string{},
			respond: func(string, []string) ([]byte, error) { return nil, nil },
			fetch: func(host, remote, local string) error {
				proj := filepath.Base(filepath.Dir(remote))
				moveInto(t, filepath.Join(servers, host, proj), local)
				return nil
			}}, nil
	}
	out := f.mustRun(t, "refresh")
	if !strings.Contains(out, "built      billing+shop: 2 hosts, 2 sessions") ||
		!strings.Contains(out, "tcp 1 fused") {
		t.Fatalf("refresh printed:\n%s", out)
	}
}

func dirExists(dir string) bool {
	fi, err := os.Stat(dir)
	return err == nil && fi.IsDir()
}

// TestBuildKey: one project names its directory; many are hashed.
func TestBuildKey(t *testing.T) {
	if got := buildKey([]string{"billing", "shop"}); got != "billing+shop" {
		t.Errorf("buildKey = %q", got)
	}
	var many []string
	for i := range 30 {
		many = append(many, fmt.Sprintf("project-%02d", i))
	}
	if got := buildKey(many); !strings.HasPrefix(got, "projects-") || len(got) > maxBuildKey {
		t.Errorf("buildKey(30 projects) = %q", got)
	}
}
