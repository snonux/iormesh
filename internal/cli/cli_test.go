package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"iormesh/internal/correlate"
	"iormesh/internal/schema"
	"iormesh/internal/segment"
	"iormesh/internal/target"
	"iormesh/internal/tracepoints"
)

// fakeProcs makes processAlive answer from a fixed set for the test, and
// processIdentity report one identity (start time 1) for every pid.
func fakeProcs(t *testing.T, pids ...uint32) {
	t.Helper()
	live := map[uint32]bool{}
	for _, p := range pids {
		live[p] = true
	}
	origAlive, origIdentity, origCommand, origEnv := processAlive, processIdentity, procCommand, podEnv
	processAlive = func(pid uint32, _ uint64) bool { return live[pid] }
	processIdentity = func(uint32) correlate.Identity { return correlate.Identity{Comm: "x", StartNS: 1} }
	// No command lines: the real /proc must not lend a fake pid one.
	procCommand = func(uint32, uint64) ([]string, string, bool) { return nil, "", false }
	// No pod: the test's own environment must not supply a -project.
	podEnv = func(string) string { return "" }
	t.Cleanup(func() {
		processAlive, processIdentity, procCommand, podEnv = origAlive, origIdentity, origCommand, origEnv
	})
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
	if !slices.Equal(cfg.families, tracepoints.DefaultFamilies()) {
		t.Fatalf("default families %v", cfg.families)
	}
	cfg, err = parseRunFlags([]string{"-pid", "10", "-project", "p", "-out", "o",
		"-trace-families", "signal,ipc"}, &bytes.Buffer{})
	want := []tracepoints.TraceFamily{tracepoints.FamilyFile, tracepoints.FamilySocket,
		tracepoints.FamilyIPC, tracepoints.FamilySignal}
	if err != nil || !slices.Equal(cfg.families, want) {
		t.Fatalf("-trace-families: %v, %v", cfg.families, err)
	}
	cfg, err = parseRunFlags([]string{"-pid", "10", "-project", "p", "-out", "o",
		"-cpuprofile", "cpu.out"}, &bytes.Buffer{})
	if err != nil || cfg.cpuProfile != "cpu.out" || cfg.nativeStacks {
		t.Fatalf("-cpuprofile: %q, %v (native stacks %v, want off by default)", cfg.cpuProfile, err, cfg.nativeStacks)
	}
	cfg, err = parseRunFlags([]string{"-pid", "10", "-project", "p", "-out", "o", "-native-stacks"}, &bytes.Buffer{})
	if err != nil || !cfg.nativeStacks {
		t.Fatalf("-native-stacks: %v, %v", cfg.nativeStacks, err)
	}
}

// TestParseSegmentFlags: -out alone writes no segments, no -out writes
// them to the default trace dir, and the watchdog and owner flags parse.
func TestParseSegmentFlags(t *testing.T) {
	fakeProcs(t, 10)
	cfg, err := parseRunFlags([]string{"-pid", "10", "-project", "p", "-out", "o"}, &bytes.Buffer{})
	if err != nil || cfg.traceDir != "" {
		t.Fatalf("-out alone: trace dir %q, %v", cfg.traceDir, err)
	}
	cfg, err = parseRunFlags([]string{"-pid", "10", "-project", "p"}, &bytes.Buffer{})
	if err != nil || cfg.traceDir != defaultTraceDir || cfg.diskLimit != 90 ||
		cfg.diskMinFree != 256<<20 || cfg.readyOwner != nil {
		t.Fatalf("defaults: %+v, %v", cfg, err)
	}
	cfg, err = parseRunFlags([]string{"-pid", "10", "-project", "p", "-out", "o", "-trace-dir", "/srv/t",
		"-disk-limit", "75.5", "-disk-min-free", "2GiB", "-ready-owner", "1000:1001"}, &bytes.Buffer{})
	if err != nil || cfg.traceDir != "/srv/t" || cfg.out != "o" || cfg.diskLimit != 75.5 ||
		cfg.diskMinFree != 2<<30 || *cfg.readyOwner != (segment.Owner{UID: 1000, GID: 1001}) {
		t.Fatalf("segment flags: %+v, %v", cfg, err)
	}
	for in, want := range map[string]uint64{"0": 0, "512": 512, "4KiB": 4096, "1MiB": 1 << 20} {
		if got, err := parseSize(in); err != nil || got != want {
			t.Errorf("parseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
}

// TestCPUProfile: the profile file is created and written on stop; no
// path profiles nothing.
func TestCPUProfile(t *testing.T) {
	stop, err := startCPUProfile("")
	if err != nil || stop() != nil {
		t.Fatalf("no profile: %v", err)
	}
	path := filepath.Join(t.TempDir(), "cpu.out")
	if stop, err = startCPUProfile(path); err != nil {
		t.Fatal(err)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(path); err != nil || st.Size() == 0 {
		t.Fatalf("profile %v, %v: want a non-empty file", st, err)
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
		"disk limit over":    {"-pid", "10", "-project", "p", "-disk-limit", "101"},
		"disk limit neg":     {"-pid", "10", "-project", "p", "-disk-limit", "-1"},
		"disk limit NaN":     {"-pid", "10", "-project", "p", "-disk-limit", "NaN"},
		"min free unit":      {"-pid", "10", "-project", "p", "-disk-min-free", "1TB"},
		"owner by name":      {"-pid", "10", "-project", "p", "-ready-owner", "paul:paul"},
		"owner without gid":  {"-pid", "10", "-project", "p", "-ready-owner", "1000"},
		"stray argument":     {"-pid", "10", "-project", "p", "-out", "o", "extra"},
		"unknown flag":       {"-pid", "10", "-project", "p", "-out", "o", "-bogus"},
		"unknown family":     {"-pid", "10", "-project", "p", "-out", "o", "-trace-families", "FS"},
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
		// ready takes a project, never a path (internal/k8s).
		{[]string{"ready"}, exitUsage},
		{[]string{"ready", "-project", "../etc"}, exitUsage},
		{[]string{"ready", "-project", "shop", "/etc"}, exitUsage},
	} {
		if got := Main(c.args, &out, &errOut); got != c.want {
			t.Errorf("Main(%q) = %d, want %d", c.args, got, c.want)
		}
	}
}

// TestSubcommandHelpExitsZero pins task vh4: `SUBCMD -h` (and --help)
// is a request, not an error. It exits 0 and prints only that
// subcommand's flags — no "flag: help requested" line and no full
// usage after them. Parsing comes first in every subcommand, so none
// of these touches systemd, a JVM or the trace root.
func TestSubcommandHelpExitsZero(t *testing.T) {
	for _, name := range []string{"run", "normalize", "attach", "start", "stop", "status", "uninstall",
		"upgrade", "ready"} {
		for _, h := range []string{"-h", "--help"} {
			var out, errOut bytes.Buffer
			code := Main([]string{name, h}, &out, &errOut)
			all := out.String() + errOut.String()
			if code != exitOK {
				t.Errorf("%s %s: exit %d, want 0; output %q", name, h, code, all)
			}
			if strings.Contains(all, "help requested") || strings.Contains(all, "usage:\n") {
				t.Errorf("%s %s: printed more than the subcommand's help: %q", name, h, all)
			}
			if !strings.Contains(errOut.String(), "Usage of "+name) {
				t.Errorf("%s %s: no subcommand help on stderr: %q", name, h, errOut.String())
			}
		}
	}
	// An unknown flag stays a usage error, with the full usage.
	var out, errOut bytes.Buffer
	if code := Main([]string{"status", "-nope"}, &out, &errOut); code != exitUsage ||
		!strings.Contains(errOut.String(), "usage:\n") {
		t.Errorf("status -nope: exit %d, stderr %q", code, errOut.String())
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
