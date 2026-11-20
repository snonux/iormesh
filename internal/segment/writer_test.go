package segment

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	parquetgo "github.com/parquet-go/parquet-go"

	"iormesh/internal/schema"
)

// t0 is the fake clock's start; segment names carry it.
var t0 = time.Date(2026, 11, 20, 6, 30, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

// hostEvent is a valid host-level (pid 0) event: the shape of session,
// drop and end events.
func hostEvent(seq uint64, kind string) schema.Event {
	e := schema.Event{SchemaVersion: schema.Version, Project: "shop", Seq: seq, BootNS: 10 + seq,
		WallNS: 20 + seq, HostID: "3f2a", Event: kind, FD: schema.NoFD,
		Attribution: schema.AttributionNone}
	switch kind {
	case schema.KindSession:
		e.Session = &schema.Session{BootID: "b1d2", FQDN: "app-1.example.org", Kernel: "6.12.0",
			Arch: "x86_64"}
	case schema.KindEnd:
		e.Args.EndReason = ptr(schema.EndDiskLimit)
	}
	return e
}

// burstEvent is a 1-byte read, the tracer's hottest event shape
// (decision #25's syscall burst).
func burstEvent(seq uint64) schema.Event {
	return schema.Event{SchemaVersion: schema.Version, Project: "shop", Seq: seq,
		BootNS: 9_876_543_210_123, WallNS: 1_763_500_000_123_456_789,
		HostID: "0123456789abcdef0123456789abcdef", PID: 4242, PIDInc: 123_456_789, TID: 4243,
		Comm: "java", Event: schema.KindRead, FD: 7, FDGen: 1, Ret: 1, LatencyNS: 700, Bytes: 1,
		Attribution: schema.AttributionNone}
}

// richEvents is a stream that fills every nested shape the parquet
// mapping has: the session header, args with a list, a java-attributed
// connect, an http event and the end.
func richEvents() []schema.Event {
	exec := burstEvent(2)
	exec.Event, exec.FD, exec.FDGen, exec.Ret, exec.Bytes = schema.KindExec, schema.NoFD, 0, 0, 0
	exec.Args = schema.Args{Path: ptr("/usr/bin/java"), Argv: []string{"java", "-Dpassword=***"}}
	conn := burstEvent(3)
	conn.Event, conn.FD, conn.FDGen, conn.Ret, conn.Err, conn.Bytes = schema.KindConnect, 6, 2, -115,
		"EINPROGRESS", 0
	conn.Args = schema.Args{RemoteAddr: ptr("::ffff:127.0.0.1"), RemotePort: ptr(uint16(8080)),
		LocalAddr: ptr("::ffff:127.0.0.1"), LocalPort: ptr(uint16(40000))}
	conn.ConnID, conn.Attribution = "0123456789abcdef", schema.AttributionJava
	conn.Java = &schema.Java{Thread: "main", Stack: []uint64{3, 1}}
	conn.StackID = ptr(uint32(5))
	conn.Otel = &schema.Otel{TraceID: "a3ce929d0e0e47364bf92f3577b34d01", SpanID: "c11e0000000000a1"}
	return []schema.Event{hostEvent(1, schema.KindSession), exec, conn, httpEvent(conn, 4),
		dnsEvent(conn, 5), tcpStateEvent(6), burstEvent(7), hostEvent(8, schema.KindEnd)}
}

// tcpStateEvent carries sock_state and the four tcp_sock fields of the
// ISN-pair join key (task fb4).
func tcpStateEvent(seq uint64) schema.Event {
	e := burstEvent(seq)
	e.Event, e.Ret, e.Bytes, e.LatencyNS = schema.KindTCPState, 0, 0, 0
	e.Args = schema.Args{SockState: ptr("ESTABLISHED"), LocalAddr: ptr("::ffff:127.0.0.1"),
		LocalPort: ptr(uint16(40000)), RemoteAddr: ptr("::ffff:127.0.0.1"),
		RemotePort: ptr(uint16(8080)), SndUna: ptr(uint32(1_000_001)), RcvNxt: ptr(uint32(7)),
		BytesAcked: ptr(uint64(0)), BytesReceived: ptr(uint64(4096))}
	e.ConnID = "0123456789abcdef"
	return e
}

// httpEvent and dnsEvent derive the Java join's two event kinds from a
// java-attributed connect, every optional nested field set.
func httpEvent(conn schema.Event, seq uint64) schema.Event {
	e := conn
	e.Seq, e.Event, e.FD, e.FDGen, e.Ret, e.Err, e.Args = seq, schema.KindHTTP, schema.NoFD, 0, 0, "",
		schema.Args{}
	e.HTTP = &schema.HTTP{Method: "GET", Scheme: "http", Host: "127.0.0.1",
		Path: "/login?token=***", Status: 200, ReqSeq: 1, Version: "HTTP/1.1",
		ReqHeaders:  []string{"authorization: ***", "x-request-id: 7"},
		RespHeaders: []string{"set-cookie: ***"}, ReqBytes: new(uint64), RespBytes: ptr(uint64(7))}
	return e
}

func dnsEvent(conn schema.Event, seq uint64) schema.Event {
	e := conn
	e.Seq, e.Event, e.FD, e.FDGen, e.Ret, e.Err, e.Args = seq, schema.KindDNS, schema.NoFD, 0, 0, "",
		schema.Args{}
	e.Otel = nil
	e.DNS = &schema.DNS{Host: "localhost", Addrs: []string{"127.0.0.1", "0:0:0:0:0:0:0:1"}}
	return e
}

// newTestWriter opens a Writer in a temporary project directory with a
// fake clock at t0.
func newTestWriter(t *testing.T, mod func(*Config)) (*Writer, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "shop")
	cfg := Config{Dir: dir, FQDN: "app-1.example.org", Session: "a1b2c3d4", Project: "shop",
		Now: func() time.Time { return t0 }}
	if mod != nil {
		mod(&cfg)
	}
	w, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return w, dir
}

func writeAll(t *testing.T, w *Writer, evs []schema.Event) {
	t.Helper()
	for i := range evs {
		if err := w.Event(&evs[i]); err != nil {
			t.Fatal(err)
		}
	}
}

// readyFiles lists ready/'s file names, sorted.
func readyFiles(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(dir, ReadyDir))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names
}

// readEvents reads a published events file back.
func readEvents(t *testing.T, path string) []schema.Event {
	t.Helper()
	evs, err := parquetgo.ReadFile[schema.Event](path)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

// sameNDJSON compares events by their NDJSON lines: the contract's own
// equality (an absent list and an empty one are the same record).
func sameNDJSON(t *testing.T, got, want []schema.Event) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d", len(got), len(want))
	}
	for i := range want {
		g, wnt := string(schema.AppendEvent(nil, &got[i])), string(schema.AppendEvent(nil, &want[i]))
		if g != wnt {
			t.Errorf("event %d:\n got %s\nwant %s", i, g, wnt)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	w, dir := newTestWriter(t, nil)
	want := richEvents()
	writeAll(t, w, want)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	name := "app-1.example.org-a1b2c3d4-20261120T063000Z-000000.parquet"
	if got := readyFiles(t, dir); !slices.Equal(got, []string{name}) {
		t.Fatalf("ready/ = %v, want [%s]", got, name)
	}
	sameNDJSON(t, readEvents(t, filepath.Join(dir, ReadyDir, name)), want)
	assertZstd(t, filepath.Join(dir, ReadyDir, name))
	assertNoActive(t, dir)
}

// assertZstd checks every column chunk is zstd-compressed (decision #16:
// parquet-internal zstd, never a whole-file .zst).
func assertZstd(t *testing.T, path string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	pf, err := parquetgo.OpenFile(f, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	for _, rg := range pf.Metadata().RowGroups {
		for _, c := range rg.Columns {
			if c.MetaData.Codec.String() != "ZSTD" {
				t.Fatalf("column %v: codec %v, want ZSTD", c.MetaData.PathInSchema, c.MetaData.Codec)
			}
		}
	}
	if got := metaValue(pf, "iormesh.session"); got != "a1b2c3d4" {
		t.Errorf("footer iormesh.session = %q", got)
	}
}

func metaValue(pf *parquetgo.File, key string) string {
	v, _ := pf.Lookup(key)
	return v
}

func assertNoActive(t *testing.T, dir string) {
	t.Helper()
	stale, err := filepath.Glob(filepath.Join(dir, activePrefix+"*"))
	if err != nil || len(stale) != 0 {
		t.Fatalf("active files left: %v %v", stale, err)
	}
}

func TestRotationBoundary(t *testing.T) {
	w, dir := newTestWriter(t, nil)
	evs := richEvents()
	writeAll(t, w, evs[:2])
	for _, tc := range []struct {
		at    time.Duration
		ready int
	}{
		{0, 0}, {59*time.Second + 999*time.Millisecond, 0}, {time.Minute, 1},
	} {
		if err := w.Tick(t0.Add(tc.at)); err != nil {
			t.Fatal(err)
		}
		if got := len(readyFiles(t, dir)); got != tc.ready {
			t.Fatalf("after %v: %d ready files, want %d", tc.at, got, tc.ready)
		}
	}
	writeAll(t, w, evs[2:])
	// The second segment opened at the rotation, a minute in: it is due
	// at two minutes, not a minute after the first one was due.
	if err := w.Tick(t0.Add(2*time.Minute - time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got := readyFiles(t, dir)
	want := []string{"app-1.example.org-a1b2c3d4-20261120T063000Z-000000.parquet",
		"app-1.example.org-a1b2c3d4-20261120T063100Z-000001.parquet"}
	if !slices.Equal(got, want) {
		t.Fatalf("ready/ = %v, want %v", got, want)
	}
	sameNDJSON(t, readEvents(t, filepath.Join(dir, ReadyDir, want[0])), evs[:2])
	sameNDJSON(t, readEvents(t, filepath.Join(dir, ReadyDir, want[1])), evs[2:])
}

func TestSizeCapRotates(t *testing.T) {
	w, dir := newTestWriter(t, func(c *Config) { c.MaxBytes = 1 })
	evs := richEvents()
	// The size estimate counts the open row group's buffered columns, so
	// one event is over a 1-byte cap at once.
	writeAll(t, w, evs[:1])
	if err := w.Tick(t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := len(readyFiles(t, dir)); got != 1 {
		t.Fatalf("%d ready files after the size cap, want 1", got)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestIdleSegmentIsNotPublished(t *testing.T) {
	w, dir := newTestWriter(t, nil)
	if err := w.Tick(t0.Add(3 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	evs := richEvents()
	writeAll(t, w, evs)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// The segment's clock restarted at the idle rotation, so its name says
	// when the minute of its first row began, and the seq has no hole.
	want := []string{"app-1.example.org-a1b2c3d4-20261120T063300Z-000000.parquet"}
	if got := readyFiles(t, dir); !slices.Equal(got, want) {
		t.Fatalf("ready/ = %v, want %v", got, want)
	}
}

func TestCloseWithoutEventsPublishesNothing(t *testing.T) {
	w, dir := newTestWriter(t, nil)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if got := readyFiles(t, dir); len(got) != 0 {
		t.Fatalf("ready/ = %v, want empty", got)
	}
	assertNoActive(t, dir)
	if err := w.Event(ptr(burstEvent(1))); !errors.Is(err, errClosed) {
		t.Fatalf("Event after Close = %v, want errClosed", err)
	}
}

// TestPublishIsARename proves the segment reaches ready/ by rename, not
// by a copy a collector could catch half-written: the published file is
// the very inode the writer wrote, and nothing of the active segment is
// in ready/ before.
func TestPublishIsARename(t *testing.T) {
	w, dir := newTestWriter(t, nil)
	writeAll(t, w, richEvents()[:2])
	active, err := os.Stat(w.activePath("events"))
	if err != nil {
		t.Fatal(err)
	}
	if got := readyFiles(t, dir); len(got) != 0 {
		t.Fatalf("ready/ holds %v while the segment is active", got)
	}
	if err := w.Tick(t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	names := readyFiles(t, dir)
	if len(names) != 1 {
		t.Fatalf("ready/ = %v", names)
	}
	published, err := os.Stat(filepath.Join(dir, ReadyDir, names[0]))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(active, published) {
		t.Fatal("published file is not the active file renamed")
	}
	if published.Mode().Perm() != 0o644 {
		t.Errorf("published mode %v, want 0644", published.Mode().Perm())
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTablesPublishedWithTheirSegment(t *testing.T) {
	w, dir := newTestWriter(t, nil)
	m, err := schema.ParseFrame("demo.Main#run(Main.java:12)")
	if err != nil {
		t.Fatal(err)
	}
	m.SchemaVersion, m.MethodID = schema.Version, 1
	if err := w.Method(m); err != nil {
		t.Fatal(err)
	}
	st := schema.Stack{SchemaVersion: schema.Version, StackID: 1, PID: 4242, PIDInc: 7,
		Frames: []schema.StackFrame{{Addr: "0x10", Module: "/usr/lib/libc.so.6", Offset: ptr(uint64(16))},
			{Addr: "0x20"}}}
	if err := w.Stack(st); err != nil {
		t.Fatal(err)
	}
	writeAll(t, w, richEvents())
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	base := "app-1.example.org-a1b2c3d4-20261120T063000Z-000000"
	want := []string{base + ".methods.parquet", base + ".parquet", base + ".stacks.parquet"}
	if got := readyFiles(t, dir); !slices.Equal(got, want) {
		t.Fatalf("ready/ = %v, want %v", got, want)
	}
	stacks, err := parquetgo.ReadFile[schema.Stack](filepath.Join(dir, ReadyDir, want[2]))
	if err != nil {
		t.Fatal(err)
	}
	if len(stacks) != 1 || len(stacks[0].Frames) != 2 || *stacks[0].Frames[0].Offset != 16 ||
		stacks[0].Frames[1].Offset != nil {
		t.Fatalf("stacks read back as %+v", stacks)
	}
	methods, err := parquetgo.ReadFile[schema.Method](filepath.Join(dir, ReadyDir, want[0]))
	if err != nil || len(methods) != 1 || methods[0].Frame != m.Frame {
		t.Fatalf("methods read back as %+v, %v", methods, err)
	}
}

func TestInvalidRowsAreRejected(t *testing.T) {
	w, _ := newTestWriter(t, nil)
	defer func() { _ = w.Close() }()
	bad := burstEvent(1)
	bad.Attribution = ""
	if err := w.Event(&bad); err == nil {
		t.Error("event without attribution accepted")
	}
	if err := w.Stack(schema.Stack{SchemaVersion: schema.Version}); err == nil {
		t.Error("stack with id 0 accepted")
	}
}

func TestStaleActiveFilesAreSwept(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shop")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, activePrefix+"events"+Ext)
	if err := os.WriteFile(stale, []byte("no footer"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := New(Config{Dir: dir, FQDN: "h", Session: "s1", Project: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale active file still there: %v", err)
	}
}

func TestReadyOwnerAndSymlink(t *testing.T) {
	// Chown to oneself needs no privilege, so the owner path runs here.
	w, dir := newTestWriter(t, func(c *Config) { c.Owner = &Owner{UID: os.Getuid(), GID: os.Getgid()} })
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ReadyDir)); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "shop")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(other, ReadyDir)); err != nil {
		t.Fatal(err)
	}
	_, err := New(Config{Dir: other, FQDN: "h", Session: "s1", Project: "shop"})
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("ready/ as a symlink: %v, want rejected", err)
	}
}

func TestConfigNames(t *testing.T) {
	for name, cfg := range map[string]Config{
		"no dir":          {FQDN: "h", Session: "s", Project: "p"},
		"dash in session": {Dir: "d", FQDN: "h", Session: "s-1", Project: "p"},
		"slash in fqdn":   {Dir: "d", FQDN: "h/x", Session: "s", Project: "p"},
		"dot project":     {Dir: "d", FQDN: "h", Session: "s", Project: ".."},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New(cfg); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// TestSecondWriterIsRefused: two runs of one project on one trace dir
// must not share it — the second would sweep the first's live active
// files and the two would publish each other's segments. The second New
// fails fast; the first publishes untouched, and once it closed a new
// run may start.
func TestSecondWriterIsRefused(t *testing.T) {
	w, dir := newTestWriter(t, nil)
	writeAll(t, w, richEvents())
	_, err := New(Config{Dir: dir, FQDN: "app-1.example.org", Session: "b2", Project: "shop"})
	if err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("second writer: %v, want refused", err)
	}
	if _, err := os.Stat(w.activePath("events")); err != nil {
		t.Fatalf("first run's active file gone: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	sameNDJSON(t, readEvents(t, filepath.Join(dir, ReadyDir, readyFiles(t, dir)[0])), richEvents())
	w2, err := New(Config{Dir: dir, FQDN: "app-1.example.org", Session: "b2", Project: "shop"})
	if err != nil {
		t.Fatalf("after the first closed: %v", err)
	}
	if err := w2.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestUntrustedDirsStopTheStart: a project or trace dir another user
// could write would let them swap ready/ for a symlink after the check,
// so group- or other-writable ones stop the start.
func TestUntrustedDirsStopTheStart(t *testing.T) {
	for name, which := range map[string]func(trace, project string) string{
		"trace dir":   func(trace, _ string) string { return trace },
		"project dir": func(_, project string) string { return project },
	} {
		t.Run(name, func(t *testing.T) {
			trace := filepath.Join(t.TempDir(), "traces")
			project := filepath.Join(trace, "shop")
			if err := os.MkdirAll(project, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(which(trace, project), 0o777); err != nil {
				t.Fatal(err)
			}
			_, err := New(Config{Dir: project, FQDN: "h", Session: "s1", Project: "shop"})
			if err == nil || !strings.Contains(err.Error(), "writable") {
				t.Fatalf("New = %v, want refused", err)
			}
		})
	}
}

// TestExistingDirsAreNotChmoded: a trace dir that already exists keeps
// its mode — pointed at an existing root-owned tree (-trace-dir /), the
// tracer must not loosen it — while the directories it creates are 0755.
func TestExistingDirsAreNotChmoded(t *testing.T) {
	trace := filepath.Join(t.TempDir(), "traces")
	if err := os.Mkdir(trace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(trace, 0o711); err != nil {
		t.Fatal(err)
	}
	w, err := New(Config{Dir: filepath.Join(trace, "shop"), FQDN: "h", Session: "s1", Project: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	for d, want := range map[string]os.FileMode{trace: 0o711, filepath.Join(trace, "shop"): 0o755,
		filepath.Join(trace, "shop", ReadyDir): 0o755} {
		if fi, err := os.Stat(d); err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s: %v, %v; want %v", d, fi.Mode().Perm(), err, want)
		}
	}
	if err := MakeDirs(filepath.Join(trace, "shop", ReadyDir, "x")); err != nil {
		t.Fatal(err)
	}
}

// TestDirsAre0755UnderAnyUmask: under umask 077 the collector still gets
// into the project directory and ready/.
func TestDirsAre0755UnderAnyUmask(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	w, dir := newTestWriter(t, nil)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{dir, filepath.Join(dir, ReadyDir)} {
		fi, err := os.Stat(d)
		if err != nil || fi.Mode().Perm() != 0o755 {
			t.Fatalf("%s: %v, %v; want 0755", d, fi.Mode().Perm(), err)
		}
	}
}
