package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"iormesh/internal/schema"
	"iormesh/internal/target"
)

// fakeProcs makes processAlive answer from a fixed set for the test, and
// processStart report one start time for every pid.
func fakeProcs(t *testing.T, pids ...uint32) {
	t.Helper()
	live := map[uint32]bool{}
	for _, p := range pids {
		live[p] = true
	}
	origAlive, origStart := processAlive, processStart
	processAlive = func(pid uint32, _ uint64) bool { return live[pid] }
	processStart = func(uint32) uint64 { return 1 }
	t.Cleanup(func() { processAlive, processStart = origAlive, origStart })
}

func TestParseRunFlags(t *testing.T) {
	fakeProcs(t, 10, 20)
	cfg, err := parseRunFlags([]string{"-pid", "10, 20", "-project", "shop", "-out", "/tmp/t",
		"-duration", "90s", "-ready", "/tmp/r"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.spec.PIDs) != 2 || cfg.spec.PIDs[1] != 20 || cfg.project != "shop" ||
		cfg.duration != 90*time.Second || cfg.ready != "/tmp/r" {
		t.Fatalf("cfg %+v", cfg)
	}
	cfg, err = parseRunFlags([]string{"-pid", "10", "-project", "p", "-out", "o"}, &bytes.Buffer{})
	if err != nil || cfg.duration != defaultDuration {
		t.Fatalf("default duration: %v, %v", cfg.duration, err)
	}
	cfg, err = parseRunFlags([]string{"-pattern", "*RestartApp*", "-project", "p", "-out", "o"}, &bytes.Buffer{})
	if err != nil || cfg.spec.Mode() != target.ModePattern || cfg.spec.Pattern != "*RestartApp*" {
		t.Fatalf("pattern: %+v, %v", cfg.spec, err)
	}
	cfg, err = parseRunFlags([]string{"-listen-port", "8080", "-project", "p", "-out", "o"}, &bytes.Buffer{})
	if err != nil || cfg.spec.Mode() != target.ModeListenPort || cfg.spec.ListenPort != 8080 {
		t.Fatalf("listen port: %+v, %v", cfg.spec, err)
	}
}

func TestParseRunFlagsRejects(t *testing.T) {
	fakeProcs(t, 10)
	ok := []string{"-project", "p", "-out", "o"}
	cases := map[string][]string{
		"no target":          ok,
		"pid and pattern":    append([]string{"-pid", "10", "-pattern", "*x*"}, ok...),
		"pattern and port":   append([]string{"-pattern", "*x*", "-listen-port", "80"}, ok...),
		"pid and port":       append([]string{"-pid", "10", "-listen-port", "80"}, ok...),
		"pattern too broad":  append([]string{"-pattern", "*"}, ok...),
		"pattern bad escape": append([]string{"-pattern", `x\`}, ok...),
		"port too big":       append([]string{"-listen-port", "70000"}, ok...),
		"pid not number":     append([]string{"-pid", "abc"}, ok...),
		"pid zero":           append([]string{"-pid", "0"}, ok...),
		"pid negative":       append([]string{"-pid", "-5"}, ok...),
		"pid empty entry":    append([]string{"-pid", "10,"}, ok...),
		"pid twice":          append([]string{"-pid", "10,10"}, ok...),
		"no such process":    append([]string{"-pid", "99"}, ok...),
		"bad duration":       {"-pid", "10", "-project", "p", "-out", "o", "-duration", "soon"},
		"zero duration":      {"-pid", "10", "-project", "p", "-out", "o", "-duration", "0s"},
		"neg duration":       {"-pid", "10", "-project", "p", "-out", "o", "-duration", "-1m"},
		"missing project":    {"-pid", "10", "-out", "o"},
		"bad project":        {"-pid", "10", "-project", "../etc", "-out", "o"},
		"missing out":        {"-pid", "10", "-project", "p"},
		"stray argument":     {"-pid", "10", "-project", "p", "-out", "o", "extra"},
		"unknown flag":       {"-pid", "10", "-project", "p", "-out", "o", "-bogus"},
	}
	for name, args := range cases {
		if _, err := parseRunFlags(args, &bytes.Buffer{}); !errors.Is(err, errUsage) {
			t.Errorf("%s: err %v, want a usage error", name, err)
		}
	}
}

func TestMainDispatch(t *testing.T) {
	var out, errOut bytes.Buffer
	for _, c := range []struct {
		args []string
		want int
	}{
		{nil, exitUsage},
		{[]string{"bogus"}, exitUsage},
		{[]string{"help"}, exitOK},
		{[]string{"run", "-pid", "abc"}, exitUsage},
		{[]string{"normalize"}, exitUsage},
		{[]string{"normalize", "/nonexistent/trace"}, exitError},
	} {
		if got := Main(c.args, &out, &errOut); got != c.want {
			t.Errorf("Main(%q) = %d, want %d", c.args, got, c.want)
		}
	}
}

// writeTrace writes a small marker-bracketed trace like a run would.
func writeTrace(t *testing.T, path string) string {
	t.Helper()
	dir := t.TempDir()
	marker := func(seq uint64, edge string) schema.Event {
		m := "s/" + edge
		return schema.Event{SchemaVersion: schema.Version, Project: "p", Seq: seq, HostID: "h",
			PID: 7, PIDInc: 70, TID: 7, Event: schema.KindMarker, FD: schema.NoFD, Ret: -2,
			Err: "ENOENT", Attribution: schema.AttributionNone, Args: schema.Args{Marker: &m}}
	}
	flags := uint32(0)
	open := schema.Event{SchemaVersion: schema.Version, Project: "p", Seq: 2, HostID: "h",
		PID: 7, PIDInc: 70, TID: 7, Event: schema.KindOpenat, FD: 3, FDGen: 1, Ret: 3,
		Attribution: schema.AttributionNone, Args: schema.Args{Path: &path, Flags: &flags}}
	f, err := os.Create(filepath.Join(dir, EventsFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeEvents(schema.NewNDJSONSink(f), []schema.Event{marker(1, "begin"), open, marker(3, "end")}); err != nil {
		t.Fatal(err)
	}
	proc, host := schema.ProcessNodeID("h", 7, 70), schema.HostNodeID("h")
	g := schema.Graph{SchemaVersion: schema.Version,
		Nodes: []schema.Node{{Type: schema.NodeHost, ID: host}, {Type: schema.NodeProcess, ID: proc}},
		Edges: []schema.Edge{{Type: schema.EdgeRunsOn, From: proc, To: host}}}
	if err := schema.WriteGraphFile(filepath.Join(dir, GraphFile), g); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeExpect(t *testing.T, path string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "expect.json")
	body := `{"scenario": "s", "schema_version": 1, "events": [
	  {"thread": "$t1", "event": "openat", "args": {"path": "` + path + `"}, "fd": "$fd1"}],
	 "edges": [], "must_not_appear": []}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNormalizeCheck(t *testing.T) {
	dir := writeTrace(t, "/run/x/a.dat")
	var out bytes.Buffer
	pass := writeExpect(t, "$tmp/a.dat")
	if code := Main([]string{"normalize", "-bracket", "s", "-tmp", "/run/x", "-check", pass, dir}, &out, &out); code != exitOK {
		t.Fatalf("matching check: exit %d\n%s", code, out.String())
	}
	out.Reset()
	fail := writeExpect(t, "$tmp/other.dat")
	if code := Main([]string{"normalize", "-bracket", "s", "-tmp", "/run/x", "-check", fail, dir}, &out, &out); code != exitViolations {
		t.Fatalf("mismatching check: exit %d, want %d\n%s", code, exitViolations, out.String())
	}
	if !strings.Contains(out.String(), "event") {
		t.Fatalf("violation not printed: %q", out.String())
	}
	if code := Main([]string{"normalize", "-bracket", "nosuch", dir}, &out, &out); code != exitError {
		t.Fatalf("missing bracket: exit %d, want %d", code, exitError)
	}
	if code := Main([]string{"normalize", "-check", pass, "-out", dir, dir}, &out, &out); code != exitUsage {
		t.Fatalf("-check with -out: exit %d, want %d", code, exitUsage)
	}
}

func TestNormalizeWrites(t *testing.T) {
	dir := writeTrace(t, "/run/x/a.dat")
	var stdout, stderr bytes.Buffer
	if code := Main([]string{"normalize", "-bracket", "s", "-tmp", "/run/x", dir}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	events, err := schema.ReadNDJSON(&stdout)
	if err != nil || len(events) != 3 || *events[1].Args.Path != "$tmp/a.dat" || events[1].HostID != "$h1" {
		t.Fatalf("normalized stdout: %d events, err %v", len(events), err)
	}
	out := filepath.Join(t.TempDir(), "norm")
	if code := Main([]string{"normalize", "-out", out, dir}, &stdout, &stderr); code != exitOK {
		t.Fatalf("-out: exit %d: %s", code, stderr.String())
	}
	if _, err := readTrace(out); err != nil {
		t.Fatalf("normalized trace dir does not read back: %v", err)
	}
}
