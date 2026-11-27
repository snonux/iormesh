package ctl

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"iormesh/internal/schema"
	"iormesh/internal/segment"
)

// queryProjects are the build fixture's two projects.
var queryProjects = []string{"-project", "shop", "-project", "billing"}

// queryLines decodes query's output: the events strictly, and the
// truncation record, if any, as the last line.
func queryLines(t *testing.T, out string) (events []schema.Event, truncated string) {
	t.Helper()
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		if strings.Contains(sc.Text(), `"event":"truncated"`) {
			truncated = sc.Text()
			continue
		}
		if truncated != "" {
			t.Fatalf("a line after the truncation record: %s", sc.Text())
		}
		ev, err := schema.DecodeEvent(sc.Bytes())
		if err != nil {
			t.Fatalf("not a schema event: %v\n%s", err, sc.Text())
		}
		events = append(events, ev)
	}
	return events, truncated
}

// TestQueryGolden: each filter over the collected store matches its
// golden and decodes as schema events; rerunning gives the same bytes.
// The window golden is unchanged by tasks ik4 and 1l4: a windowed query
// reads the whole store by default, and its time of day lands on the
// store's one UTC date (a store spanning several dates refuses a bare
// time of day, task 1l4).
func TestQueryGolden(t *testing.T) {
	f := newBuildFixture(t)
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"query_file.golden", []string{"-file", "/etc/*.conf"}},
		{"query_host.golden", []string{"-host", "app2", "-thread", "200"}},
		{"query_window.golden", []string{"-from", "00:53:20.001", "-to", "00:53:20.002"}},
		{"query_app.golden", []string{"-app", "java", "-from", "2025-11-20T00:53:20.009Z"}},
		{"query_capped.golden", []string{"-max-bytes", "1500"}},
	} {
		args := slices.Concat([]string{"query"}, queryProjects, tc.args)
		out := f.mustRun(t, args...)
		golden(t, tc.name, out)
		queryLines(t, out)
		if again := f.mustRun(t, args...); again != out {
			t.Errorf("%s: a rerun wrote other bytes", tc.name)
		}
	}
	if dirExists(f.builds) {
		t.Error("query wrote build outputs")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.db), hostPinsFileName)); err == nil {
		t.Error("query pinned hosts")
	}
}

// TestQueryConnFollow: the merged conn id selects both ends of the fused
// connection; -follow adds the threads' other events while on it (a
// second session of app1's process writes a file mid-connection).
func TestQueryConnFollow(t *testing.T) {
	f := newBuildFixture(t)
	side := &hostTrace{host: "hostA", project: "shop", session: "b2c3d4e5"}
	side.header()
	side.add(100, schema.KindOpenat, 5, 5, 5000, "", schema.Args{Path: bptr("/tmp/reply.json")})
	writeStoreSegments(t, f.store, "app1", side, 10)
	evs, _ := queryLines(t, f.mustRun(t, slices.Concat([]string{"query"}, queryProjects,
		[]string{"-host", "app2", "-thread", "200", "-from", "00:53:20.0015"})...))
	conn := evs[0].ConnID
	if conn == "" || conn == "s1" {
		t.Fatalf("app2's connection id %q, want the merged one", conn)
	}
	args := slices.Concat([]string{"query"}, queryProjects, []string{"-conn", conn})
	plain, _ := queryLines(t, f.mustRun(t, args...))
	hosts := map[string]bool{}
	for _, ev := range plain {
		hosts[ev.HostID] = true
		if ev.ConnID != conn {
			t.Errorf("seq %d of %s on %q", ev.Seq, ev.HostID, ev.ConnID)
		}
	}
	if !hosts["hostA"] || !hosts["hostB"] {
		t.Errorf("hosts %v, want both ends", hosts)
	}
	out := f.mustRun(t, append(args, "-follow")...)
	golden(t, "query_follow.golden", out)
	if followed, _ := queryLines(t, out); len(followed) <= len(plain) {
		t.Errorf("-follow gave %d events, -conn alone %d", len(followed), len(plain))
	}
}

// TestQueryTruncation: the cap holds, the last line says how much is
// missing, and emitted + remaining is the uncut count.
func TestQueryTruncation(t *testing.T) {
	f := newBuildFixture(t)
	all, tr := queryLines(t, f.mustRun(t, slices.Concat([]string{"query"}, queryProjects)...))
	if tr != "" {
		t.Fatalf("uncut query truncated: %s", tr)
	}
	out := f.mustRun(t, slices.Concat([]string{"query"}, queryProjects, []string{"-max-bytes", "1500"})...)
	cut, tr := queryLines(t, out)
	if len(out) > 1500 || tr == "" {
		t.Fatalf("%d bytes, record %q", len(out), tr)
	}
	want := `{"schema_version":1,"event":"truncated","args":{"emitted":` +
		strconv.Itoa(len(cut)) + `,"remaining":` + strconv.Itoa(len(all)-len(cut)) + `}}`
	if tr != want {
		t.Errorf("record %s, want %s", tr, want)
	}
}

// TestQueryInvalid: a filter that cannot be applied is a usage error,
// reported before any segment is read.
func TestQueryInvalid(t *testing.T) {
	f := newBuildFixture(t)
	for _, tc := range []struct {
		args []string
		msg  string
	}{
		{[]string{"-file", "/etc/["}, "syntax error in pattern"},
		{[]string{"-method", "[x"}, "syntax error in pattern"},
		{[]string{"-follow"}, "-follow needs -conn"},
		{[]string{"-from", "noon"}, "want RFC 3339"},
		{[]string{"-from", "00:53:21", "-to", "00:53:20"}, "is empty"},
		{[]string{"-max-bytes", "10"}, "below"},
		{[]string{"extra"}, "takes no arguments"},
		{[]string{"-project", "-bad"}, "invalid project"},
		{[]string{"-event", "opennat"}, "unknown event kind \"opennat\""},
		{[]string{"-event", "opennat"}, "openat"},
		{[]string{"-errno", "ENOPE"}, "-errno \"ENOPE\""},
		{[]string{"-errno", "-5"}, "-errno \"-5\""},
		{[]string{"-pid", "abc"}, "-pid \"abc\""},
		{[]string{"-pid", "4294967296"}, "-pid \"4294967296\""},
		{[]string{"-otel-trace", "xyz"}, "32 lowercase hex"},
	} {
		code := f.run(slices.Concat([]string{"query"}, tc.args)...)
		if code != exitUsage || !strings.Contains(f.stderr.String(), tc.msg) || f.stdout.Len() != 0 {
			t.Errorf("%v: exit %d, stdout %q, stderr %s", tc.args, code, f.stdout, f.stderr)
		}
	}
}

// TestQueryBadSegment: a damaged segment is left out and named on
// stderr, the rest is still sliced, and the command exits 1.
func TestQueryBadSegment(t *testing.T) {
	f := newBuildFixture(t)
	bad := f.app2Files[len(f.app2Files)-1]
	if err := os.WriteFile(bad, []byte("not parquet"), 0o600); err != nil {
		t.Fatal(err)
	}
	code := f.run(slices.Concat([]string{"query"}, queryProjects, []string{"-host", "app1"})...)
	if code != exitError || !strings.Contains(f.stderr.String(), "left out billing/app2/"+filepath.Base(bad)) {
		t.Fatalf("exit %d, stderr %s", code, f.stderr)
	}
	if evs, _ := queryLines(t, f.stdout.String()); len(evs) == 0 {
		t.Error("the good segments were not sliced")
	}
}

// javaTrace is one host session of Java-attributed events whose stacks
// name a methods table: DataStore#save writes a file, Http#send a
// socket.
func javaTrace() (*hostTrace, []schema.Method) {
	h := &hostTrace{host: "hostJ", project: "shop", session: "j1"}
	h.header()
	open := h.add(300, schema.KindOpenat, 3, 3, 10, "", schema.Args{Path: bptr("/data/order-42.json")})
	open.Attribution, open.Java = schema.AttributionJava, &schema.Java{Thread: "worker-1", Stack: []uint64{1, 3}}
	send := h.add(300, schema.KindWrite, 4, 10, 20, "", schema.Args{})
	send.Attribution, send.Java = schema.AttributionJava, &schema.Java{Thread: "http-1", Stack: []uint64{2, 3}}
	h.end(30, schema.EndStop)
	var methods []schema.Method
	for i, frame := range []string{"com.shop.DataStore#save(DataStore.java:42)",
		"com.shop.Http#send(Http.java:7)", "com.shop.Main#run(Main.java:3)"} {
		m, err := schema.ParseFrame(frame)
		if err != nil {
			panic(err)
		}
		m.SchemaVersion, m.MethodID = schema.Version, uint64(i+1)
		methods = append(methods, m)
	}
	return h, methods
}

// writeJavaSegment writes h and its methods as one segment (events and
// methods table) into store/<project>/<host>/.
func writeJavaSegment(t *testing.T, store, host string, h *hostTrace, methods []schema.Method) []string {
	t.Helper()
	t0 := time.Unix(0, int64(buildWall0)).UTC()
	srv := filepath.Join(t.TempDir(), h.project)
	w, err := segment.New(segment.Config{Dir: srv, FQDN: h.host + ".example.org", Session: h.session,
		Project: h.project, Now: func() time.Time { return t0 }})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range methods {
		if err := w.Method(m); err != nil {
			t.Fatal(err)
		}
	}
	for i := range h.events {
		if err := w.Event(&h.events[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return moveInto(t, filepath.Join(srv, segment.ReadyDir), filepath.Join(store, h.project, host))
}

// TestQueryMethod: -method selects the events whose Java stack holds a
// matching frame, through the session's checked methods table; a
// damaged table is left out and named, and the command exits 1.
func TestQueryMethod(t *testing.T) {
	f := newBuildFixture(t)
	h, methods := javaTrace()
	files := writeJavaSegment(t, f.store, "appJ", h, methods)
	args := []string{"query", "-project", "shop", "-method", "*DataStore#save"}
	out := f.mustRun(t, args...)
	golden(t, "query_method.golden", out)
	evs, _ := queryLines(t, out)
	if len(evs) != 1 || evs[0].Args.Path == nil || *evs[0].Args.Path != "/data/order-42.json" {
		t.Errorf("events %+v, want the openat of order-42.json", evs)
	}
	main, _ := queryLines(t, f.mustRun(t, "query", "-project", "shop", "-method", "com.shop.Main#run(Main.java:3)"))
	if len(main) != 2 {
		t.Errorf("%d events under Main#run, want 2", len(main))
	}
	table := ""
	for _, p := range files {
		if strings.HasSuffix(p, ".methods.parquet") {
			table = p
		}
	}
	if err := os.WriteFile(table, []byte("PAR1 damaged PAR1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := f.run(args...); code != exitError ||
		!strings.Contains(f.stderr.String(), "left out shop/appJ/"+filepath.Base(table)) {
		t.Errorf("damaged table: exit %d, stderr %s", code, f.stderr)
	}
}

// TestQueryFrames: -frames ends each Java event's line with java_frames,
// its stack's frames from the session's checked methods table, an id
// the table lacks as method#<id> (decision #42, task an4); without it
// the lines stay schema events. A damaged table is left out and named,
// its ids stay method#<id>, and the command exits 1.
func TestQueryFrames(t *testing.T) {
	f := newBuildFixture(t)
	h, methods := javaTrace()
	h.events[2].Java.Stack = []uint64{2, 4} // 4: in no table
	files := writeJavaSegment(t, f.store, "appJ", h, methods)
	args := []string{"query", "-project", "shop", "-host", "appJ", "-frames"}
	out := f.mustRun(t, args...)
	golden(t, "query_frames.golden", out)
	plain := f.mustRun(t, "query", "-project", "shop", "-host", "appJ")
	if evs, _ := queryLines(t, plain); len(evs) != 4 || strings.Contains(plain, "java_frames") {
		t.Errorf("without -frames: %d events, output %s", len(evs), plain)
	}
	for _, p := range files {
		if strings.HasSuffix(p, ".methods.parquet") {
			if err := os.WriteFile(p, []byte("PAR1 damaged PAR1"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	code := f.run(args...)
	if code != exitError || !strings.Contains(f.stderr.String(), "left out shop/appJ/") ||
		!strings.Contains(f.stdout.String(), `"java_frames":["method#1","method#3"]`) {
		t.Errorf("damaged table: exit %d, stdout %s, stderr %s", code, f.stdout, f.stderr)
	}
	// -method read the table first: it is named once, not twice.
	code = f.run(slices.Concat(args, []string{"-method", "*Main#run"})...)
	if n := strings.Count(f.stderr.String(), "left out shop/appJ/"); code != exitError || n != 1 {
		t.Errorf("damaged table with -method: exit %d, named %d times: %s", code, n, f.stderr)
	}
}

// TestQueryKindPidErrnoOtel: -event, -pid, -errno (a name or a number)
// and -otel-trace (any case) select through the store and AND with each
// other (task wh4). The fixture's app1 (pid 100) has two connects
// failing EINPROGRESS; javaTrace's write (pid 300) runs in a span.
func TestQueryKindPidErrnoOtel(t *testing.T) {
	f := newBuildFixture(t)
	h, methods := javaTrace()
	h.events[2].Otel = &schema.Otel{TraceID: "4bf92f3577b34da6a3ce929d0e0e4736", SpanID: "00f067aa0ba902b7"}
	writeJavaSegment(t, f.store, "appJ", h, methods)
	for _, tc := range []struct {
		args []string
		want int
	}{
		{[]string{"-event", "connect", "-pid", "100", "-errno", "EINPROGRESS"}, 2},
		{[]string{"-event", "connect", "-errno", "115"}, 2},
		{[]string{"-errno", "einprogress", "-pid", "200"}, 0},
		{[]string{"-event", "accept4", "-pid", "200"}, 1},
		{[]string{"-otel-trace", "4BF92F3577B34DA6A3CE929D0E0E4736"}, 1},
		{[]string{"-otel-trace", "4bf92f3577b34da6a3ce929d0e0e4736", "-event", "openat"}, 0},
	} {
		evs, _ := queryLines(t, f.mustRun(t, slices.Concat([]string{"query"}, queryProjects, tc.args)...))
		if len(evs) != tc.want {
			t.Errorf("%v: %d events, want %d", tc.args, len(evs), tc.want)
		}
		for _, ev := range evs {
			if tc.args[0] == "-event" && ev.Event != tc.args[1] {
				t.Errorf("%v: a %s event", tc.args, ev.Event)
			}
		}
	}
}

// TestQueryFileBaseHint: a -file glob that matches no path but the last
// element of one ('*' stops at '/') still exits 0 with an empty slice,
// and stderr says why and which glob would match (decision #41, task
// 0n4). A glob that matches, or matches nothing at all, adds no hint.
func TestQueryFileBaseHint(t *testing.T) {
	f := newBuildFixture(t)
	for _, tc := range []struct {
		glob, hint string
	}{
		{"*shop.conf*", "try -file '/etc/*shop.conf*'"},
		{"/etc/*.conf", ""},
		{"*nothing-like-it*", ""},
	} {
		out := f.mustRun(t, slices.Concat([]string{"query"}, queryProjects, []string{"-file", tc.glob})...)
		stderr := f.stderr.String()
		if tc.hint == "" {
			if strings.Contains(stderr, "stops at '/'") {
				t.Errorf("%s: unwanted hint: %s", tc.glob, stderr)
			}
			continue
		}
		if out != "" || !strings.Contains(stderr, tc.hint) || !strings.Contains(stderr, "stops at '/'") {
			t.Errorf("%s: stdout %q, stderr %s; want the hint %q", tc.glob, out, stderr, tc.hint)
		}
	}
}

// TestCheckFileRefusesOtherTables: the child checks events files and
// methods tables only.
func TestCheckFileRefusesOtherTables(t *testing.T) {
	_, err := checkFile(t.Context(), filepath.Join(t.TempDir(), "h-s1-20251120T005320Z-000001.threads.parquet"))
	if err == nil || !strings.Contains(err.Error(), "no check for threads tables") {
		t.Errorf("err %v", err)
	}
	if _, err := checkFile(t.Context(), "/tmp/not-a-segment"); err == nil {
		t.Error("a non-segment name was checked")
	}
}
