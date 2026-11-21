package report

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"iormesh/internal/federate"
	"iormesh/internal/schema"
	"iormesh/internal/servicedb"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func ptr[T any](v T) *T { return &v }

// trace builds a host's events the way a session records them.
type trace struct {
	host   string
	seq    uint64
	events []schema.Event
}

// add appends one event of pid/tid; java names the thread and stack
// when the event is Java-attributed.
func (tr *trace) add(pid, tid uint32, kind string, fd int32, ret int64, conn string, args schema.Args,
	java *schema.Java) *schema.Event {
	tr.seq++
	ev := schema.Event{SchemaVersion: schema.Version, Project: "shop", Seq: tr.seq, WallNS: 1e18 + tr.seq,
		HostID: tr.host, PID: pid, PIDInc: 7, TID: tid, Comm: "java", Event: kind, FD: fd, FDGen: 1,
		Ret: ret, ConnID: conn, Args: args, Attribution: schema.AttributionNone}
	if java != nil {
		ev.Attribution, ev.Java = schema.AttributionJava, java
	}
	if ret < 0 {
		ev.Err = "ENOENT"
	}
	if ret > 0 && kind != schema.KindOpenat && kind != schema.KindAccept4 {
		ev.Bytes = uint64(ret)
	}
	tr.events = append(tr.events, ev)
	return &tr.events[len(tr.events)-1]
}

func (tr *trace) http(pid, tid uint32, conn, method, path string, status uint16, java *schema.Java) {
	ev := tr.add(pid, tid, schema.KindHTTP, schema.NoFD, 0, conn, schema.Args{}, java)
	ev.HTTP = &schema.HTTP{Method: method, Scheme: "http", Path: path, Status: status}
}

// methods is the session's method table: two scenario frames and the
// JDK frame a write runs through (never the causing method).
var methods = map[uint64]schema.Method{
	1: {MethodID: 1, Class: "shop.DataStore", Method: "save"},
	2: {MethodID: 2, Class: "shop.ServiceConfig", Method: "load"},
	3: {MethodID: 3, Class: "java.io.FileOutputStream", Method: "write"},
	4: {MethodID: 4, Class: "shop.HttpFetch", Method: "call"},
	5: {MethodID: 5, Class: "shop.HttpServe", Method: "handle"},
}

// meshTrace is a gateway (pid 10) calling orders (pid 20) over a
// loopback pair its host fused (c1, merged as m-c1), and orders, while
// serving, reading its config, writing a record create-tmp-then-rename,
// failing to open a missing file and calling an untraced inventory
// (c2).
func meshTrace() *trace {
	tr := &trace{host: "h1"}
	gw := &schema.Java{Thread: "main", Stack: []uint64{4}}
	srv := &schema.Java{Thread: "http-1", Stack: []uint64{5}}
	save := &schema.Java{Thread: "http-1", Stack: []uint64{3, 1}}
	load := &schema.Java{Thread: "http-1", Stack: []uint64{2}}
	tr.add(10, 10, schema.KindAttach, schema.NoFD, 0, "", schema.Args{Pattern: ptr("gateway*")}, nil)
	tr.add(20, 20, schema.KindExec, schema.NoFD, 0, "", schema.Args{Argv: []string{"java", "-jar", "w.jar",
		"--role", "orders"}}, nil)
	tr.add(10, 11, schema.KindConnect, 5, 0, "c1", schema.Args{RemoteAddr: ptr("127.0.0.1"),
		RemotePort: ptr(uint16(8001))}, gw)
	tr.add(20, 21, schema.KindAccept4, 6, 6, "c1", schema.Args{LocalPort: ptr(uint16(8001))}, srv)
	tr.add(10, 11, schema.KindWrite, 5, 36, "c1", schema.Args{}, gw)
	tr.add(20, 21, schema.KindRead, 6, 36, "c1", schema.Args{}, srv)
	tr.add(20, 21, schema.KindOpenat, 7, 7, "", schema.Args{Path: ptr("/o/orders.conf"), Flags: ptr(uint32(0))}, load)
	tr.add(20, 21, schema.KindRead, 7, 39, "", schema.Args{}, load)
	tr.add(20, 21, schema.KindClose, 7, 0, "", schema.Args{}, load)
	tr.add(20, 21, schema.KindOpenat, -1, -2, "", schema.Args{Path: ptr("/o/missing")}, load)
	tr.add(20, 21, schema.KindConnect, 8, 0, "c2", schema.Args{RemoteAddr: ptr("127.0.0.1"),
		RemotePort: ptr(uint16(8002))}, srv)
	tr.http(20, 21, "c2", "POST", "/inventory/reserve", 200, &schema.Java{Thread: "http-1", Stack: []uint64{4}})
	tr.add(20, 21, schema.KindOpenat, 7, 7, "", schema.Args{Path: ptr("/o/order-42.json.tmp"),
		Flags: ptr(uint32(577))}, save)
	tr.add(20, 21, schema.KindWrite, 7, 36, "", schema.Args{}, save)
	tr.add(20, 21, schema.KindClose, 7, 0, "", schema.Args{}, save)
	tr.add(20, 21, schema.KindWrite, 7, 99, "", schema.Args{}, save) // closed: no file's
	tr.add(20, 21, schema.KindRename, schema.NoFD, 0, "", schema.Args{Path: ptr("/o/order-42.json.tmp"),
		NewPath: ptr("/o/order-42.json")}, save)
	tr.add(20, 21, schema.KindNewstat, schema.NoFD, 0, "", schema.Args{Path: ptr("/o/order-42.json")}, save)
	tr.add(20, 21, schema.KindWrite, 6, 33, "c1", schema.Args{}, srv)
	tr.http(20, 21, "c1", "POST", "/orders", 201, srv)
	tr.add(10, 11, schema.KindRead, 5, 33, "c1", schema.Args{}, gw)
	tr.http(10, 11, "c1", "POST", "/orders", 201, gw)
	tr.add(0, 0, schema.KindDrop, schema.NoFD, 0, "", schema.Args{Lost: ptr(uint64(3))}, nil)
	return tr
}

// collect feeds tr to a collector with the session's methods.
func collect(tr *trace, limit int) *Collector {
	c := NewCollector(map[string]map[uint64]schema.Method{"s1": methods})
	if limit > 0 {
		c.limit = limit
	}
	for i := range tr.events {
		c.Add(Source{Project: "shop", Host: "app1", FQDN: "h1.example.org", Session: "s1"}, &tr.events[i])
	}
	return c
}

// meshInput renames the loopback pair and names the orders endpoint.
func meshInput() Input {
	return Input{Renames: map[federate.LocalConn]string{{Host: "h1", ConnID: "c1"}: "m-c1"},
		Endpoints: []servicedb.Endpoint{{IP: "127.0.0.1", Port: 8001, Service: "orders"}}}
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs (go test ./internal/report -update to accept):\n%s", path, got)
	}
}

func render(t *testing.T, r Report) (js, text []byte) {
	t.Helper()
	js, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := WriteText(&b, r); err != nil {
		t.Fatal(err)
	}
	return append(js, '\n'), b.Bytes()
}

// TestReportGolden: the mesh's rollup, as JSON and as text, matches its
// goldens, and building it twice gives the same bytes.
func TestReportGolden(t *testing.T) {
	js, text := render(t, Build(collect(meshTrace(), 0), meshInput()))
	golden(t, "mesh.json.golden", js)
	golden(t, "mesh.txt.golden", text)
	js2, text2 := render(t, Build(collect(meshTrace(), 0), meshInput()))
	if !bytes.Equal(js, js2) || !bytes.Equal(text, text2) {
		t.Error("a second build differs")
	}
}

// TestReportCaps: past the tracking cap events are counted, not
// tracked, and the text says the rollup is partial; past a list's cap
// the rest is counted in its More field.
func TestReportCaps(t *testing.T) {
	r := Build(collect(meshTrace(), 3), meshInput())
	if r.Untracked == 0 {
		t.Fatal("a collector at its cap tracked everything")
	}
	_, text := render(t, r)
	if !bytes.HasPrefix(text, []byte("partial    ")) {
		t.Errorf("a partial rollup does not say so:\n%s", text)
	}
	tr := &trace{host: "h1"}
	for i := range MaxFiles + 3 {
		tr.add(30, 30, schema.KindUnlink, schema.NoFD, 0, "", schema.Args{Path: ptr("/f/" + itoa(uint32(i)))}, nil)
	}
	r = Build(collect(tr, 0), Input{})
	if p := r.Processes[0]; len(p.Files) != MaxFiles || p.MoreFiles != 3 {
		t.Errorf("%d files, %d more; want %d and 3", len(p.Files), p.MoreFiles, MaxFiles)
	}
	if _, text := render(t, r); !bytes.Contains(text, []byte("...      3 more files")) {
		t.Errorf("the cut is not shown:\n%s", text)
	}
}

// TestReportFilesPastTheCap: a process traced from its exec (task vg4)
// probes and opens many library paths before its own work. Past
// MaxFiles the files kept are the written or changed ones, then the
// read ones, then those only opened, and the failed probes go first —
// listed in first-seen order, the cut counted.
func TestReportFilesPastTheCap(t *testing.T) {
	tr := &trace{host: "h1"}
	for i := range MaxFiles { // the loader's probes: failed opens only
		tr.add(30, 30, schema.KindOpenat, schema.NoFD, -2, "", schema.Args{Path: ptr("/lib/probe" + itoa(uint32(i)))}, nil)
	}
	open := func(path string, fd int32, flags uint32) {
		tr.add(30, 30, schema.KindOpenat, fd, int64(fd), "", schema.Args{Path: ptr(path), Flags: ptr(flags)}, nil)
	}
	open("/etc/ld.so.cache", 3, 0) // opened, nothing read (mapped): the last slot, above the probes
	want := []string{"/etc/ld.so.cache"}
	for i := range MaxFiles - 4 { // libraries read: with the config and the record, one slot short of the cap
		want = append(want, "/lib/lib"+itoa(uint32(i))+".so")
		open("/lib/lib"+itoa(uint32(i))+".so", 4, 0)
		tr.add(30, 30, schema.KindRead, 4, 832, "", schema.Args{}, nil)
		tr.add(30, 30, schema.KindClose, 4, 0, "", schema.Args{}, nil)
	}
	open("/proc/self/task", 3, 0) // opened only too, seen later: cut
	open("/srv/orders.conf", 5, 0)
	tr.add(30, 30, schema.KindRead, 5, 39, "", schema.Args{}, nil)
	open("/srv/order-42.json.tmp", 6, 577)
	tr.add(30, 30, schema.KindRename, schema.NoFD, 0, "", schema.Args{Path: ptr("/srv/order-42.json.tmp"),
		NewPath: ptr("/srv/order-42.json")}, nil)
	tr.add(30, 30, schema.KindUnlink, schema.NoFD, -2, "", schema.Args{Path: ptr("/srv/gone")}, nil)
	p := Build(collect(tr, 0), Input{}).Processes[0]
	var paths []string
	for _, f := range p.Files {
		paths = append(paths, f.Path)
	}
	if p.MoreFiles != MaxFiles+2 {
		t.Errorf("%d more files; want %d", p.MoreFiles, MaxFiles+2)
	}
	want = append(want, "/srv/orders.conf", "/srv/order-42.json.tmp", "/srv/order-42.json")
	if !slices.Equal(paths, want) {
		t.Errorf("files kept\n%q\nwant\n%q", paths, want)
	}
}

// TestReportTerminalSafe: strings from the trace reach the text
// escaped; the JSON keeps them as they were.
func TestReportTerminalSafe(t *testing.T) {
	tr := &trace{host: "h1"}
	tr.add(40, 40, schema.KindUnlink, schema.NoFD, 0, "", schema.Args{Path: ptr("/x\x1b]0;pwned\a")}, nil)
	tr.events[0].Comm = "ev\x1b[2Jil"
	r := Build(collect(tr, 0), Input{})
	js, text := render(t, r)
	if bytes.ContainsAny(text, "\x1b\a") {
		t.Errorf("text carries raw control bytes: %q", text)
	}
	var back Report
	if err := json.Unmarshal(js, &back); err != nil || back.Processes[0].Files[0].Path != "/x\x1b]0;pwned\a" {
		t.Errorf("JSON lost the path: %v %+v", err, back)
	}
}

// TestReportNoMethodWithoutTable: a Java stack whose ids the session's
// table lacks names no method — never a guess — and an unknown
// session's events none either.
func TestReportNoMethodWithoutTable(t *testing.T) {
	c := NewCollector(nil)
	tr := meshTrace()
	for i := range tr.events {
		c.Add(Source{Project: "shop", Host: "app1", Session: "other"}, &tr.events[i])
	}
	for _, p := range Build(c, meshInput()).Processes {
		if len(p.Methods) != 0 {
			t.Errorf("%s names methods without a table: %+v", p.ID, p.Methods)
		}
		for _, th := range p.Threads {
			if th.Carried != 0 {
				t.Errorf("%s/%s: %d carried without a virtual thread", p.ID, th.Name, th.Carried)
			}
		}
		for _, f := range p.Files {
			if len(f.Methods) != 0 {
				t.Errorf("%s: %+v", f.Path, f.Methods)
			}
		}
	}
}

// TestReportCarrier: a virtual thread's I/O, named by its carrier
// (java.carrier), is counted on the carrier's thread as carried and its
// request flagged — never left to read as the carrier's own work.
func TestReportCarrier(t *testing.T) {
	tr := &trace{host: "h1"}
	virt := &schema.Java{Thread: "carrier-1", Carrier: true}
	tr.add(20, 25, schema.KindAccept4, 6, 6, "c1", schema.Args{LocalPort: ptr(uint16(8001))}, nil)
	tr.add(20, 25, schema.KindRead, 6, 36, "c1", schema.Args{}, virt)
	tr.http(20, 25, "c1", "GET", "/v", 200, virt)
	tr.http(20, 25, "c1", "GET", "/own", 200, &schema.Java{Thread: "carrier-1"})
	r := Build(collect(tr, 0), meshInput())
	th := r.Processes[0].Threads[0]
	if th.Carried != 2 || len(th.Calls) != 2 || !th.Calls[0].Carrier || th.Calls[1].Carrier {
		t.Fatalf("carrier thread: %+v", th)
	}
	_, text := render(t, r)
	for _, want := range []string{"2 of them virtual threads' I/O it carried", "[virtual thread on this carrier]"} {
		if !bytes.Contains(text, []byte(want)) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
}
