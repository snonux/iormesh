package correlate

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"iormesh/internal/redact"
	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// incarnations counts the graph's incarnation edges from app.
func incarnations(g schema.Graph, app string) []string {
	var to []string
	for _, e := range g.Edges {
		if e.Type == schema.EdgeIncarnation && e.From == app {
			to = append(to, e.To)
		}
	}
	return to
}

// TestIncarnationLinksRestarts: two incarnations of one app under two
// PIDs hang off one app node, and the graph validates.
func TestIncarnationLinksRestarts(t *testing.T) {
	c, _ := newTest(t)
	app := schema.AppNodeID("p", "*RestartApp*")
	for _, pid := range []uint32{40, 41} {
		if err := c.Incarnation("*RestartApp*", pid, testIdentity(pid), 0); err != nil {
			t.Fatal(err)
		}
	}
	g := c.Graph()
	if err := g.Validate(); err != nil {
		t.Fatalf("graph invalid: %v", err)
	}
	got := incarnations(g, app)
	want := []string{schema.ProcessNodeID("h1", 40, 1040), schema.ProcessNodeID("h1", 41, 1041)}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("incarnation edges to %v, want %v", got, want)
	}
}

// TestIncarnationGapDrop: a non-zero attach gap becomes one drop event
// naming the process, right after its attach and ahead of its first
// traced event; a zero gap emits nothing.
func TestIncarnationGapDrop(t *testing.T) {
	c, sink := newTest(t)
	if err := c.Incarnation("*x*", 40, testIdentity(40), 0); err != nil {
		t.Fatal(err)
	}
	if err := c.Incarnation("*x*", 41, testIdentity(41), 6); err != nil {
		t.Fatal(err)
	}
	now := uint64(100)
	feeder{t, c, 41, 41, &now}.open("/tmp/a", 0, 3)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	drops := byKind(sink.events, schema.KindDrop)
	if len(drops) != 1 {
		t.Fatalf("drops %+v, want exactly the one of pid 41", drops)
	}
	d := drops[0]
	if d.PID != 41 || d.PIDInc != 1041 || *d.Args.Lost != 6 || *d.Args.DropReason != schema.DropAttachGap {
		t.Fatalf("drop %+v (args %+v), want pid 41 lost 6 attach_gap", d, d.Args)
	}
	kinds := []string{}
	for _, e := range sink.events {
		kinds = append(kinds, e.Event)
	}
	want := []string{schema.KindAttach, schema.KindAttach, schema.KindDrop, schema.KindOpenat}
	if !slices.Equal(kinds, want) {
		t.Fatalf("events %v, want %v: the drop follows the attach and precedes the first syscall", kinds, want)
	}
}

// TestNewTaskRegistersChild: a forked child is registered at its
// task_newtask record (incarnation read then) with a forked edge from its
// creator, a new thread is not a process, and a PID reused by a new child
// is a new incarnation.
func TestNewTaskRegistersChild(t *testing.T) {
	c, _ := newTest(t)
	info := map[uint32]uint64{50: 7}
	c.cfg.ProcInfo = func(pid uint32) (string, uint64) { return "child", info[pid] }
	handle := func(rec any) {
		if err := c.Handle(rec); err != nil {
			t.Fatal(err)
		}
	}
	handle(&types.TaskNewtaskEvent{EventType: types.TASK_NEWTASK_EVENT, Time: 10, Pid: 50, Tid: 50, CreatorPid: 1})
	handle(&types.TaskNewtaskEvent{EventType: types.TASK_NEWTASK_EVENT, Time: 20, Pid: 50, Tid: 51,
		CreatorPid: 50, CloneFlags: cloneThread})
	handle(&types.ProcessExitEvent{EventType: types.PROCESS_EXIT_EVENT, Time: 30, Pid: 50, Tid: 50, GroupDead: 1})
	info[50] = 9 // the PID is reused by a later child
	handle(&types.TaskNewtaskEvent{EventType: types.TASK_NEWTASK_EVENT, Time: 40, Pid: 50, Tid: 50, CreatorPid: 1})
	var procs []string
	for _, n := range c.Graph().Nodes {
		if n.Type == schema.NodeProcess {
			procs = append(procs, n.ID)
		}
	}
	parent := schema.ProcessNodeID("h1", 1, 0)
	want := []string{parent, schema.ProcessNodeID("h1", 50, 7), schema.ProcessNodeID("h1", 50, 9)}
	if !slices.Equal(procs, want) {
		t.Fatalf("process nodes %v, want %v", procs, want)
	}
	for _, child := range want[1:] {
		if !hasEdge(c.Graph(), schema.EdgeForked, parent, child, "") {
			t.Errorf("no forked edge %s → %s", parent, child)
		}
	}
}

// TestSeedOutlivesPIDReuse (task jd4): the session reads a target's
// identity when it takes the target on; by the time the correlator
// handles the target's first record, the target may be gone and its PID
// reused. Each case seeds pid 60 (or not), then lets the fake /proc show
// the squatter that took the PID, then hands over the target's open (at
// 600, after the target's start 500 and before the squatter's 900).
// Unseeded, the target's event would carry the squatter's identity.
func TestSeedOutlivesPIDReuse(t *testing.T) {
	target := Identity{Comm: "target", StartNS: 500}
	cases := []struct {
		name string
		seed func(c *Correlator) error
	}{
		{"pid target (Seed)", func(c *Correlator) error { return c.Seed(60, target) }},
		{"pattern adoption (Incarnation)", func(c *Correlator) error {
			return c.Incarnation("*target*", 60, target, 0)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, sink := newTest(t)
			proc := target
			c.cfg.ProcInfo = func(uint32) (string, uint64) { return proc.Comm, proc.StartNS }
			if err := tc.seed(c); err != nil {
				t.Fatal(err)
			}
			proc = Identity{Comm: "squatter", StartNS: 900} // exit, then PID reuse
			now := uint64(599)
			feeder{t, c, 60, 60, &now}.open("/tmp/t", 0, 3)
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			ev := byKind(sink.events, schema.KindOpenat)[0]
			if ev.PIDInc != target.StartNS || ev.Comm != target.Comm {
				t.Fatalf("open has incarnation %d comm %q, want the seeded %d %q",
					ev.PIDInc, ev.Comm, target.StartNS, target.Comm)
			}
		})
	}
}

// TestNewTaskChildGoneBeforeRecord: a lagging correlator reads a forked
// child's /proc entry after the child exited and a later process took its
// PID. That process started after the fork record, so it cannot be the
// child: the child keeps an unknown incarnation and its inherited comm
// from the record, never the stranger's identity. A start at the record's
// time (tick rounding) is still the child's.
func TestNewTaskChildGoneBeforeRecord(t *testing.T) {
	cases := []struct {
		name     string
		start    uint64
		wantInc  uint64
		wantComm string
	}{
		{"reused after the fork", 901, 0, "parent"},
		{"gone, /proc unreadable", 0, 0, "parent"},
		{"started at the fork", 900, 900, "squatter"},
		{"started before the fork", 850, 850, "squatter"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTest(t)
			c.cfg.ProcInfo = func(pid uint32) (string, uint64) {
				if pid == 70 {
					if tc.start == 0 {
						return "", 0
					}
					return "squatter", tc.start
				}
				return "parent", 100
			}
			rec := &types.TaskNewtaskEvent{EventType: types.TASK_NEWTASK_EVENT, Time: 900,
				Pid: 70, Tid: 70, CreatorPid: 1}
			copy(rec.Comm[:], "parent")
			if err := c.Handle(rec); err != nil {
				t.Fatal(err)
			}
			if p := c.procs[70]; p.inc != tc.wantInc || p.comm != tc.wantComm {
				t.Fatalf("child %d %q, want %d %q", p.inc, p.comm, tc.wantInc, tc.wantComm)
			}
		})
	}
}

// TestExitDropsPendingEnter: a thread that dies inside a syscall leaves
// no enter behind for a later thread reusing its tid to pair with.
func TestExitDropsPendingEnter(t *testing.T) {
	c, sink := newTest(t)
	now := uint64(0)
	f := feeder{t, c, 60, 61, &now}
	f.handle(&types.FdEvent{EventType: types.ENTER_FD_EVENT, TraceId: types.SYS_ENTER_READ,
		Time: f.tick(), Pid: 60, Tid: 61, Fd: 3})
	f.handle(&types.ProcessExitEvent{EventType: types.PROCESS_EXIT_EVENT, Time: f.tick(), Pid: 60, Tid: 61})
	f.exit(types.SYS_EXIT_READ, 8)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if len(byKind(sink.events, schema.KindRead)) != 0 || c.Unpaired() != 1 {
		t.Fatalf("events %v, unpaired %d: the dead thread's enter paired", sink.events, c.Unpaired())
	}
}

func TestIncarnationAfterEnd(t *testing.T) {
	c, _ := newTest(t)
	if err := c.End(schema.EndStop); err != nil {
		t.Fatal(err)
	}
	if err := c.Incarnation("*x*", 1, testIdentity(1), 0); !errors.Is(err, ErrEnded) {
		t.Fatalf("Incarnation after End: %v, want ErrEnded", err)
	}
}

// TestExecRecordsRedactedArgv: an exec event carries the executable and
// the argv with every planted secret masked, and nothing it emits — event
// or graph — holds the secret (decision #9).
func TestExecRecordsRedactedArgv(t *testing.T) {
	c, sink := newTest(t)
	argv := []string{"/usr/bin/java", "-Dpassword=hunter2", "-cp", "/a.jar", "Main", "--token", "hunter2"}
	if err := c.Exec(ExecInfo{PID: 40, TID: 40, BootNS: 5, Exe: "/usr/bin/java", Argv: argv}); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	execs := byKind(sink.events, schema.KindExec)
	if len(execs) != 1 {
		t.Fatalf("%d exec events, want 1", len(execs))
	}
	ev := execs[0]
	want := []string{"/usr/bin/java", "-Dpassword=***", "-cp", "/a.jar", "Main", "--token", "***"}
	if !slices.Equal(ev.Args.Argv, want) || ev.Args.Path == nil || *ev.Args.Path != "/usr/bin/java" {
		t.Fatalf("exec args %+v, want path /usr/bin/java and argv %q", ev.Args, want)
	}
	if ev.PID != 40 || ev.PIDInc != 1040 || ev.FD != schema.NoFD || ev.BootNS != 5 {
		t.Fatalf("exec envelope %+v", ev)
	}
	if argv[1] != "-Dpassword=hunter2" {
		t.Fatal("Exec modified the caller's argv")
	}
	if b, _ := json.Marshal(c.Graph()); strings.Contains(string(b), "hunter2") {
		t.Fatalf("graph holds the secret: %s", b)
	}
}

// TestExecCustomPolicyAndEmptyArgv: Config.Redaction replaces the default
// policy, and an exec whose argv could not be read has no argv at all.
func TestExecCustomPolicyAndEmptyArgv(t *testing.T) {
	sink := &memSink{t: t}
	policy := redact.Policy{ArgvDeny: []string{"color"}}
	c := New(Config{Host: Host{ID: "h1", Project: "p"}, Sink: sink, Redaction: &policy,
		ProcInfo: func(uint32) (string, uint64) { return "x", 1 }})
	if err := c.Exec(ExecInfo{PID: 7, TID: 7, BootNS: 1, Argv: []string{"ls", "--color=auto", "--password=p"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(ExecInfo{PID: 7, TID: 7, BootNS: 2}); err != nil {
		t.Fatal(err)
	}
	if err := c.End(schema.EndStop); err != nil {
		t.Fatal(err)
	}
	execs := byKind(sink.events, schema.KindExec)
	if len(execs) != 2 {
		t.Fatalf("%d exec events, want 2", len(execs))
	}
	if want := []string{"ls", "--color=***", "--password=p"}; !slices.Equal(execs[0].Args.Argv, want) {
		t.Errorf("argv %q, want %q", execs[0].Args.Argv, want)
	}
	if execs[1].Args.Argv != nil || execs[1].Args.Path != nil {
		t.Errorf("unreadable exec has args %+v", execs[1].Args)
	}
	if err := c.Exec(ExecInfo{PID: 7}); !errors.Is(err, ErrEnded) {
		t.Errorf("Exec after End: %v, want ErrEnded", err)
	}
}

// TestExecCutArgvAndExe: a cut argv's partial last element is masked
// whole — the policy cannot judge a fragment — and the executable path
// goes through the policy like any argument.
func TestExecCutArgvAndExe(t *testing.T) {
	c, sink := newTest(t)
	x := ExecInfo{PID: 40, TID: 40, BootNS: 5, Exe: "/x/https://u:pw@h/app",
		Argv: []string{"app", "--url", "https://u:secr"}, Cut: true}
	if err := c.Exec(x); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	ev := byKind(sink.events, schema.KindExec)[0]
	if want := []string{"app", "--url", "***"}; !slices.Equal(ev.Args.Argv, want) {
		t.Errorf("argv %q, want %q", ev.Args.Argv, want)
	}
	if *ev.Args.Path != "/x/https://u:***@h/app" {
		t.Errorf("exe %q", *ev.Args.Path)
	}
}

// execRecord is the sched_process_exec record of f's process with the
// kernel's post-exec comm.
func (f feeder) execRecord(comm string) {
	rec := &types.ProcessExecEvent{EventType: types.PROCESS_EXEC_EVENT, Time: f.tick(),
		Pid: f.pid, Tid: f.tid}
	copy(rec.Comm[:], comm)
	f.handle(rec)
}

// processComm returns the comm attr of pid's process node.
func processComm(t *testing.T, g schema.Graph, pid uint32, inc uint64) string {
	t.Helper()
	id := schema.ProcessNodeID("h1", pid, inc)
	for _, n := range g.Nodes {
		if n.ID == id {
			return n.Attrs["comm"]
		}
	}
	t.Fatalf("no process node %s", id)
	return ""
}

// TestExecRefreshesComm: a successful exec renames the process for every
// later event, its exec event and its graph node; events before it keep
// the old comm. An empty record comm falls back to the executable's
// basename cut like the kernel's. A failed exec (no sched_process_exec
// record, no Exec) and an Exec without a record (a pattern session's
// adoption, whose comm /proc read after the exec) rename nothing.
func TestExecRefreshesComm(t *testing.T) {
	cases := []struct {
		name     string
		record   bool   // the exec record arrived: the exec succeeded
		recComm  string // its comm
		callExec bool
		exe      string
		want     string
	}{
		{"kernel comm", true, "echopeer", true, "/bin/echopeer", "echopeer"},
		{"kernel comm wins over exe", true, "jspawnhelper", true, "/x/other", "jspawnhelper"},
		{"empty record comm uses exe", true, "", true, "/opt/a-very-long-program-name", "a-very-long-pro"},
		{"empty record comm, unreadable exe", true, "", true, "", "proc10"},
		{"failed exec", false, "", false, "", "proc10"},
		{"exec without record", false, "", true, "/bin/other", "proc10"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, sink := newTest(t)
			f := feeder{t, c, 10, 10, new(uint64)}
			f.open("/before", 0, 3)
			if tc.record {
				f.execRecord(tc.recComm)
			}
			if tc.callExec {
				if err := c.Exec(ExecInfo{PID: 10, TID: 10, BootNS: f.tick(), Exe: tc.exe}); err != nil {
					t.Fatal(err)
				}
			}
			f.open("/after", 0, 4)
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			opens := byKind(sink.events, schema.KindOpenat)
			if opens[0].Comm != "proc10" || opens[1].Comm != tc.want {
				t.Errorf("open comms %q, %q; want proc10, %q", opens[0].Comm, opens[1].Comm, tc.want)
			}
			if execs := byKind(sink.events, schema.KindExec); tc.callExec && execs[0].Comm != tc.want {
				t.Errorf("exec event comm %q, want %q", execs[0].Comm, tc.want)
			}
			if got := processComm(t, c.Graph(), 10, 1010); got != tc.want {
				t.Errorf("node comm %q, want %q", got, tc.want)
			}
		})
	}
}

// renameRecord is the task_rename record of f's thread with comm.
func (f feeder) renameRecord(comm string) {
	rec := &types.TaskRenameEvent{EventType: types.TASK_RENAME_EVENT, Time: f.tick(),
		Pid: f.pid, Tid: f.tid}
	copy(rec.Comm[:], comm)
	f.handle(rec)
}

// TestRenameRefreshesComm (task 0g4): the leader's prctl(PR_SET_NAME)
// renames the process for later events and its graph node, events before
// it keep the old name; a worker thread naming itself, and an empty name,
// rename nothing.
func TestRenameRefreshesComm(t *testing.T) {
	cases := []struct {
		name string
		tid  uint32
		comm string
		want string
	}{
		{"leader", 10, "renamed", "renamed"},
		{"worker thread", 11, "worker-1", "proc10"},
		{"empty name", 10, "", "proc10"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, sink := newTest(t)
			f := feeder{t, c, 10, 10, new(uint64)}
			f.open("/before", 0, 3)
			feeder{t, c, 10, tc.tid, f.now}.renameRecord(tc.comm)
			f.open("/after", 0, 4)
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			opens := byKind(sink.events, schema.KindOpenat)
			if opens[0].Comm != "proc10" || opens[1].Comm != tc.want {
				t.Errorf("open comms %q, %q; want proc10, %q", opens[0].Comm, opens[1].Comm, tc.want)
			}
			if got := processComm(t, c.Graph(), 10, 1010); got != tc.want {
				t.Errorf("node comm %q, want %q", got, tc.want)
			}
		})
	}
}

// TestExeComm: the kernel's naming of an exec'd process — the basename,
// cut to 15 bytes, here without a split UTF-8 character.
func TestExeComm(t *testing.T) {
	for exe, want := range map[string]string{
		"":                          "",
		"/":                         "",
		"echopeer":                  "echopeer",
		"/usr/bin/fifteen-bytes-x":  "fifteen-bytes-x",
		"/usr/bin/sixteen-bytes-xy": "sixteen-bytes-x",
		"/opt/abcdefghijklmnä":      "abcdefghijklmn", // ä's 2 bytes straddle 15
	} {
		if got := exeComm(exe); got != want {
			t.Errorf("exeComm(%q) = %q, want %q", exe, got, want)
		}
	}
}

// TestForkAndAttachEvents (task ze4): a new child is a fork event on the
// child, naming its parent (none when the record names no creator, no
// event for a thread); a -pid target is an attach without a pattern, a
// pattern's incarnation one with it.
func TestForkAndAttachEvents(t *testing.T) {
	c, sink := newTest(t)
	if err := c.Seed(1, testIdentity(1)); err != nil {
		t.Fatal(err)
	}
	if err := c.Incarnation("*app*", 2, testIdentity(2), 0); err != nil {
		t.Fatal(err)
	}
	for _, r := range []*types.TaskNewtaskEvent{
		{EventType: types.TASK_NEWTASK_EVENT, Time: 5000, Pid: 50, Tid: 50, CreatorPid: 1},
		{EventType: types.TASK_NEWTASK_EVENT, Time: 5010, Pid: 50, Tid: 51, CreatorPid: 50, CloneFlags: cloneThread},
		{EventType: types.TASK_NEWTASK_EVENT, Time: 5020, Pid: 60, Tid: 60},
	} {
		if err := c.Handle(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, e := range sink.events {
		a, _ := json.Marshal(e.Args)
		got = append(got, fmt.Sprintf("%s %d@%d %s %s", e.Event, e.PID, e.PIDInc, e.Comm, a))
	}
	want := []string{
		`attach 1@1001 proc1 {}`,
		`attach 2@1002 proc2 {"pattern":"*app*"}`,
		`fork 50@1050 proc50 {"parent_pid":1,"parent_incarnation":1001}`,
		`fork 60@1060 proc60 {}`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("events\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
