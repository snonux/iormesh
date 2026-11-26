package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"iormesh/internal/correlate"
	"iormesh/internal/schema"
	"iormesh/internal/target"
	"iormesh/internal/types"
)

// execRecord is the exec report of pid with argv (NUL-separated, as the
// BPF side copies it) and exe.
func execRecord(pid uint32, at uint64, exe string, argv ...string) *types.ExecArgvEvent {
	ev := &types.ExecArgvEvent{EventType: types.EXEC_ARGV_EVENT, Time: at, Pid: pid, Tid: pid}
	copy(ev.Exe[:], exe)
	n := 0
	for _, a := range argv {
		n += copy(ev.Argv[n:], a)
		n++ // the NUL the zeroed array already holds
	}
	ev.ArgvLen, ev.ArgvTotal = uint32(n), uint32(n)
	return ev
}

// patternSession is a started -pattern session; the pattern matches
// nothing on the test host, so start adopts nobody.
func patternSession(t *testing.T, tr *fakeTracer, c *correlate.Correlator) *session {
	t.Helper()
	cfg := runConfig{spec: target.Spec{Pattern: "*iormesh.test.NoSuchRestartApp*"}, project: "p"}
	s := newSession(tr, c, cfg)
	if err := s.start(nil); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestSessionSeedsOpenSockets (decision #35, task cm4): a started session
// has seeded its targets' open sockets once; a seeding that fails is
// named on stderr and the session still starts — the trace only lacks
// the connections opened before it.
func TestSessionSeedsOpenSockets(t *testing.T) {
	fakeProcs(t, 10)
	tr := newFakeTracer()
	pidSession(t, tr, testCorrelator(t, t.TempDir()), 10)
	if len(tr.seeded) != 1 {
		t.Fatalf("seeded %d times at start, want once", len(tr.seeded))
	}

	tr = newFakeTracer()
	tr.seedErr = errors.New("no iterators")
	cfg := runConfig{spec: target.Spec{PIDs: []uint32{10}}, project: "p"}
	s := newSession(tr, testCorrelator(t, t.TempDir()), cfg)
	var log strings.Builder
	s.log = &log
	if err := s.start([]uint32{10}); err != nil {
		t.Fatalf("start with a failing seeding: %v", err)
	}
	if !strings.Contains(log.String(), "stay unseen: no iterators") {
		t.Fatalf("stderr %q does not name the failed seeding", log.String())
	}
}

// TestPatternSessionJudgesExecs: a matching exec is adopted and linked to
// the app with its gap as an attach_gap drop; a non-matching exec — the
// launcher, or a helper whose comm-like name would fit — is dismissed; a
// match that exited before its verdict is no incarnation. Two
// incarnations under two PIDs share the one app node.
func TestPatternSessionJudgesExecs(t *testing.T) {
	fakeProcs(t)
	dir := t.TempDir()
	tr := newFakeTracer()
	tr.gap = 4
	tr.exited = map[uint32]bool{74: true}
	c := testCorrelator(t, dir)
	s := patternSession(t, tr, c)
	app := "*iormesh.test.NoSuchRestartApp*"
	recs := []any{
		execRecord(70, 10, "/usr/bin/java", "java", "-jar", "w.jar", "restart"),
		execRecord(71, 20, "/jdk/lib/jspawnhelper", "/jdk/lib/jspawnhelper", "3:4"),
		execRecord(71, 30, "/jdk/bin/java", "/jdk/bin/java", "-cp", "w.jar", "iormesh.test.NoSuchRestartApp", "/d"),
		execRecord(72, 40, "/usr/bin/NoSuchRestartApp", "NoSuchRestartApp"),
		execRecord(73, 50, "/jdk/bin/java", "/jdk/bin/java", "-cp", "w.jar", "iormesh.test.NoSuchRestartApp", "/d"),
		// Matches, but exited before the verdict: no incarnation.
		execRecord(74, 60, "/jdk/bin/java", "/jdk/bin/java", "-cp", "w.jar", "iormesh.test.NoSuchRestartApp", "/d"),
	}
	for _, r := range recs {
		if err := s.handle(r); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(tr.adopted, []uint32{71, 73}) || !slices.Equal(tr.ignored, []uint32{70, 71, 72}) {
		t.Fatalf("adopted %v ignored %v; want [71 73] and [70 71 72]", tr.adopted, tr.ignored)
	}
	if s.over() {
		t.Fatal("a pattern session is over: it must wait for the app to re-appear")
	}
	g := c.Graph()
	appID := schema.AppNodeID("p", app)
	var to []string
	for _, e := range g.Edges {
		if e.Type == schema.EdgeIncarnation && e.From == appID {
			to = append(to, e.To)
		}
	}
	if len(to) != 2 || g.Validate() != nil {
		t.Fatalf("incarnation edges %v (graph valid: %v), want 2", to, g.Validate())
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	drops := 0
	for _, e := range readEvents(t, dir) {
		if e.Event == schema.KindDrop && e.Args.DropReason != nil && *e.Args.Lost == 4 {
			drops++
		}
	}
	if drops != 2 {
		t.Fatalf("%d attach_gap drops of 4, want one per incarnation", drops)
	}
}

// TestPIDSessionOver: a -pid session follows its targets' children and
// ends once the target and its child are both gone — by their exit
// records, or by /proc for one whose record was lost.
func TestPIDSessionOver(t *testing.T) {
	exitAll := liveUntilExit(t)
	tr := newFakeTracer()
	c := testCorrelator(t, t.TempDir())
	s, _ := pidSession(t, tr, c, 10)
	child := &types.TaskNewtaskEvent{EventType: types.TASK_NEWTASK_EVENT, Time: 1, Pid: 11, Tid: 11, CreatorPid: 10}
	thread := &types.TaskNewtaskEvent{EventType: types.TASK_NEWTASK_EVENT, Time: 2, Pid: 10, Tid: 12,
		CreatorPid: 10, CloneFlags: cloneThread}
	exit := &types.ProcessExitEvent{EventType: types.PROCESS_EXIT_EVENT, Time: 3, Pid: 10, Tid: 10, GroupDead: 1}
	for _, r := range []any{child, thread, exit} {
		if err := s.handle(r); err != nil {
			t.Fatal(err)
		}
	}
	if s.over() || len(s.live) != 1 || s.live[11] == 0 {
		t.Fatalf("live %v: the followed child must keep the session going", s.live)
	}
	exitAll() // the child's exit record was lost: /proc says it is gone
	if !s.over() {
		t.Fatalf("live %v: the session must be over", s.live)
	}
	_ = c.Close()
}

// TestPIDTargetSeededAtStart (task jd4): a -pid target's identity is the
// one read at session start. The fake /proc then shows a squatter on the
// PID (the target exited, the PID was reused) before the target's first
// record is handled; the record keeps the target's incarnation and comm.
func TestPIDTargetSeededAtStart(t *testing.T) {
	fakeProcs(t, 10)
	processIdentity = func(uint32) correlate.Identity { return correlate.Identity{Comm: "target", StartNS: 500} }
	dir := t.TempDir()
	c := testCorrelator(t, dir)
	s, _ := pidSession(t, newFakeTracer(), c, 10)
	processIdentity = func(uint32) correlate.Identity { return correlate.Identity{Comm: "squatter", StartNS: 900} }
	open := &types.OpenEvent{EventType: types.ENTER_OPEN_EVENT, TraceId: types.SYS_ENTER_OPENAT,
		Time: 600, Pid: 10, Tid: 10}
	copy(open.Filename[:], "/tmp/t")
	ret := &types.RetEvent{EventType: types.EXIT_RET_EVENT, TraceId: types.SYS_EXIT_OPENAT,
		Time: 601, Pid: 10, Tid: 10, Ret: 3}
	if err := s.handleAll([]any{open, ret}); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	opens := 0
	for _, e := range readEvents(t, dir) {
		if e.Event != schema.KindOpenat {
			continue
		}
		opens++
		if e.PIDInc != 500 || e.Comm != "target" {
			t.Fatalf("open has incarnation %d comm %q, want the target's 500 \"target\"", e.PIDInc, e.Comm)
		}
	}
	if opens != 1 {
		t.Fatalf("%d open events, want 1", opens)
	}
}

// TestChildStartAfterForkIsGone: a followed child whose /proc entry shows
// a start after its task_newtask record is another process on a reused
// PID; the child's start stays unknown, so over() takes it for gone.
func TestChildStartAfterForkIsGone(t *testing.T) {
	fakeProcs(t, 10, 11)
	s, _ := pidSession(t, newFakeTracer(), testCorrelator(t, t.TempDir()), 10)
	processIdentity = func(uint32) correlate.Identity { return correlate.Identity{Comm: "squatter", StartNS: 900} }
	for at, want := range map[uint64]uint64{899: 0, 900: 900} {
		child := &types.TaskNewtaskEvent{EventType: types.TASK_NEWTASK_EVENT, Time: at, Pid: 11, Tid: 11,
			CreatorPid: 10}
		if err := s.handle(child); err != nil {
			t.Fatal(err)
		}
		if s.live[11] != want {
			t.Errorf("fork record at %d: child start %d, want %d", at, s.live[11], want)
		}
	}
}

// TestExecAlive: IgnoreExec's liveness check holds only for the process
// that made the exec — alive, started no later than the exec, readable.
func TestExecAlive(t *testing.T) {
	fakeProcs(t, 10)
	for _, c := range []struct {
		name      string
		pid       uint32
		start, at uint64
		wantAlive bool
	}{
		{"same process", 10, 500, 600, true},
		{"reused PID", 10, 700, 600, false},
		{"unreadable", 10, 0, 600, false},
		{"exited", 11, 500, 600, false},
	} {
		processIdentity = func(uint32) correlate.Identity { return correlate.Identity{StartNS: c.start} }
		if got := execAlive(execRecord(c.pid, c.at, "/bin/sh"))(); got != c.wantAlive {
			t.Errorf("%s: execAlive = %v, want %v", c.name, got, c.wantAlive)
		}
	}
}

// TestUnknownStartTimeIsGone: a target whose start time could not be
// read is not kept alive by whatever process holds its PID now, and is
// not seeded: no empty pid@0 process node without events.
func TestUnknownStartTimeIsGone(t *testing.T) {
	fakeProcs(t, 10)
	processIdentity = func(uint32) correlate.Identity { return correlate.Identity{} }
	c := testCorrelator(t, t.TempDir())
	s, _ := pidSession(t, newFakeTracer(), c, 10)
	if !s.over() {
		t.Fatalf("live %v: an unknown start time kept the session alive", s.live)
	}
	for _, n := range c.Graph().Nodes {
		if n.Type == schema.NodeProcess {
			t.Fatalf("process node %s for a target gone at start", n.ID)
		}
	}
}

// TestStartScanDropsVanished: a scanned match that exits between the
// scan and the insert is removed from target_map again — nothing else
// would ever remove it — and is no incarnation.
func TestStartScanDropsVanished(t *testing.T) {
	fakeProcs(t) // nothing is alive by the time of the re-check
	tr := newFakeTracer()
	s := newSession(tr, testCorrelator(t, t.TempDir()),
		runConfig{spec: target.Spec{Pattern: "*x*"}, project: "p"})
	if err := s.adoptRunning(4242); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tr.added, []uint32{4242}) || !slices.Equal(tr.removed, []uint32{4242}) || len(s.live) != 0 {
		t.Fatalf("added %v removed %v live %v: want the vanished match inserted and removed",
			tr.added, tr.removed, s.live)
	}
}

// TestRunningTargetAttachArgv (task nh4): a process already running when
// the session takes it on — a -pid target at start, a pattern's start-scan
// match — has no exec, so its attach carries the argv and exe /proc shows,
// read against the start time seeded and redacted; the planted secret is
// nowhere in the events file. A pattern's exec adoption reads no
// command line (its exec event follows), and a target whose command
// cannot be read attaches without one.
func TestRunningTargetAttachArgv(t *testing.T) {
	fakeProcs(t, 10, 11, 20, 30)
	processIdentity = func(pid uint32) correlate.Identity { return correlate.Identity{Comm: "java", StartNS: 500} }
	var asked []uint32
	procCommand = func(pid uint32, start uint64) ([]string, string, bool) {
		asked = append(asked, pid)
		if pid == 11 || start != 500 {
			return nil, "", false // unreadable (hidepid, gone, reused)
		}
		return []string{"java", "-Dpassword=hunter2", "-jar", "app.jar"}, "/usr/bin/java", false
	}
	dir := t.TempDir()
	c := testCorrelator(t, dir)
	pidSession(t, newFakeTracer(), c, 10, 11)
	ps := newSession(newFakeTracer(), c,
		runConfig{spec: target.Spec{Pattern: "*iormesh.test.NoSuchRestartApp*"}, project: "p"})
	if err := ps.adoptRunning(20); err != nil {
		t.Fatal(err)
	}
	if err := ps.handle(execRecord(30, 40, "/jdk/bin/java", "/jdk/bin/java",
		"iormesh.test.NoSuchRestartApp")); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, e := range readEvents(t, dir) {
		if b, _ := json.Marshal(e); strings.Contains(string(b), "hunter2") {
			t.Errorf("event %d holds the secret: %s", e.Seq, b)
		}
		if e.Event == schema.KindAttach {
			a, _ := json.Marshal(e.Args)
			got = append(got, fmt.Sprintf("%d %s", e.PID, a))
		}
	}
	argv := `"path":"/usr/bin/java","argv":["java","-Dpassword=***","-jar","app.jar"]`
	want := []string{`10 {` + argv + `}`, `11 {}`,
		`20 {` + argv + `,"pattern":"*iormesh.test.NoSuchRestartApp*"}`,
		`30 {"pattern":"*iormesh.test.NoSuchRestartApp*"}`}
	if !slices.Equal(got, want) {
		t.Fatalf("attach events\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if !slices.Equal(asked, []uint32{10, 11, 20}) {
		t.Fatalf("command read for %v, want the running targets 10 11 20 only", asked)
	}
}

// TestExecArgvRecordedRedacted: a traced process's exec (PROCESS_ARGV_EVENT)
// and the exec a pattern session adopts both become exec events whose
// argv is redacted — the adopted one after its attach and attach_gap
// drop — while a dismissed exec leaves no trace; the planted secret is
// nowhere in the events file (decision #9).
func TestExecArgvRecordedRedacted(t *testing.T) {
	fakeProcs(t)
	dir := t.TempDir()
	tr := newFakeTracer()
	tr.gap = 2
	c := testCorrelator(t, dir)
	s := patternSession(t, tr, c)
	traced := execRecord(80, 10, "/jdk/bin/java", "/jdk/bin/java", "-Dpassword=hunter2", "-cp", "w.jar", "Child")
	traced.EventType = types.PROCESS_ARGV_EVENT
	recs := []any{
		traced,
		execRecord(81, 20, "/usr/bin/grep", "grep", "--token", "hunter2"),
		execRecord(82, 30, "/jdk/bin/java", "/jdk/bin/java", "-cp", "w.jar",
			"iormesh.test.NoSuchRestartApp", "--api-key=hunter2"),
	}
	for _, r := range recs {
		if err := s.handle(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	var argvs [][]string
	for _, e := range readEvents(t, dir) {
		if e.PID != 0 {
			kinds = append(kinds, e.Event)
		}
		if e.Event == schema.KindExec {
			argvs = append(argvs, e.Args.Argv)
		}
		if b, _ := json.Marshal(e); strings.Contains(string(b), "hunter2") {
			t.Errorf("event %d holds the secret: %s", e.Seq, b)
		}
	}
	want := []string{schema.KindExec, schema.KindAttach, schema.KindDrop, schema.KindExec}
	if !slices.Equal(kinds, want) {
		t.Fatalf("process events %v, want %v", kinds, want)
	}
	if argvs[0][1] != "-Dpassword=***" || argvs[1][4] != "--api-key=***" {
		t.Fatalf("argvs %q", argvs)
	}
}

// longCmdlineProc is a /proc tree under dir with pid 10, started at
// tick 500, whose command line is 1.5 MB: a class path no NDJSON line
// (capped at 1 MiB by both readers) could hold whole. It returns the
// command line's elements.
func longCmdlineProc(t *testing.T, dir string) []string {
	t.Helper()
	argv := []string{"java", "-cp", strings.Repeat("/lib/a.jar:", 150_000), "-Dpassword=hunter2", "Main"}
	pdir := filepath.Join(dir, "10")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"stat":    "10 (java) S 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 500 0 0",
		"cmdline": strings.Join(argv, "\x00") + "\x00",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(pdir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("/usr/bin/java", filepath.Join(pdir, "exe")); err != nil {
		t.Fatal(err)
	}
	return argv
}

// TestLongCmdlineBounded (review of task nh4): a command line read from
// /proc — a running target's for its attach, a cut exec record's
// completion — is read up to target.MaxCmdline only, and a cut one has
// its partial last element masked whole, as a cut exec record's is: an
// unbounded read put a 1.5 MB argv into one event line, past the 1 MiB
// the NDJSON readers take, and lost the session.
func TestLongCmdlineBounded(t *testing.T) {
	fakeProcs(t, 10)
	procDir := t.TempDir()
	full := longCmdlineProc(t, procDir)
	fs := target.ProcFS{Root: procDir}
	processIdentity = func(uint32) correlate.Identity { return correlate.Identity{Comm: "java", StartNS: 500 * 10_000_000} }
	procCommand = fs.Command
	origCmdline := procCmdline
	procCmdline = fs.CmdlineBounded
	t.Cleanup(func() { procCmdline = origCmdline })
	dir := t.TempDir()
	c := testCorrelator(t, dir)
	s, _ := pidSession(t, newFakeTracer(), c, 10)
	// The exec record's copy: the block's first bytes, the rest cut.
	rec := execRecord(10, 10, "/usr/bin/java")
	rec.EventType = types.PROCESS_ARGV_EVENT
	block := strings.Join(full, "\x00") + "\x00"
	rec.ArgvLen = uint32(copy(rec.Argv[:], block))
	rec.ArgvTotal = uint32(len(block))
	if err := s.handle(rec); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, EventsFile))
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		if len(line) > 2*target.MaxCmdline {
			t.Fatalf("event line of %d bytes, want at most %d", len(line), 2*target.MaxCmdline)
		}
	}
	kinds := 0
	for _, e := range readEvents(t, dir) {
		if e.Event != schema.KindAttach && e.Event != schema.KindExec {
			continue
		}
		kinds++
		a := e.Args.Argv
		if len(a) != 3 || a[0] != "java" || a[1] != "-cp" || a[2] != "***" {
			t.Errorf("%s argv %.80q, want java -cp and the cut class path masked", e.Event, a)
		}
	}
	if kinds != 2 {
		t.Fatalf("%d attach and exec events, want 2", kinds)
	}
}

// cutExecRecord is a traced exec's record whose argv block was cut after
// n bytes, as the BPF side reports a block longer than it copies.
func cutExecRecord(pid uint32, n int, argv ...string) *types.ExecArgvEvent {
	ev := execRecord(pid, 10, "/bin/app", argv...)
	ev.EventType = types.PROCESS_ARGV_EVENT
	ev.ArgvTotal = ev.ArgvLen
	ev.ArgvLen = uint32(n)
	clear(ev.Argv[n:])
	return ev
}

// TestCutArgvCompletion is review round 1's third leak: a cut argv is
// completed from /proc only when /proc still shows the same command line;
// otherwise the cut copy is recorded with its partial last element masked
// — a URL password cut before its '@' is no longer recognizable as one.
func TestCutArgvCompletion(t *testing.T) {
	argv := []string{"app", "--url", "https://u:Secr3tPw@h/"}
	cutAt := len("app\x00--url\x00https://u:Secr") // inside the password
	cases := []struct {
		name string
		proc func(uint32) ([]string, bool, error)
		want []string
	}{
		{"proc agrees", func(uint32) ([]string, bool, error) { return argv, false, nil },
			[]string{"app", "--url", "https://u:***@h/"}},
		{"proc unreadable", func(uint32) ([]string, bool, error) { return nil, false, os.ErrNotExist },
			[]string{"app", "--url", "***"}},
		{"process exec'd again", func(uint32) ([]string, bool, error) { return []string{"other", "x"}, false, nil },
			[]string{"app", "--url", "***"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			orig := procCmdline
			procCmdline = c.proc
			t.Cleanup(func() { procCmdline = orig })
			dir := t.TempDir()
			cc := testCorrelator(t, dir)
			s, _ := pidSession(t, newFakeTracer(), cc, 90)
			if err := s.handle(cutExecRecord(90, cutAt, argv...)); err != nil {
				t.Fatal(err)
			}
			if err := cc.Close(); err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, e := range readEvents(t, dir) {
				if e.Event == schema.KindExec {
					got = e.Args.Argv
				}
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("argv %q, want %q", got, c.want)
			}
		})
	}
}
