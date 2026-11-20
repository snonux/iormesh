package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeSystemd stands in for systemd-run and systemctl: the unit tests
// never start, stop or list a real unit. It records every argv; show
// answers from units (absent: not-found), list-units from the loaded
// units, and onRun runs when systemd-run is called (a test's stand-in for
// the unit's run writing its state).
type fakeSystemd struct {
	calls [][]string
	units map[string]unitState
	onRun func(args []string)
	fail  map[string]error // by subcommand: "systemd-run", "stop", ...
}

func newFakeSystemd() *fakeSystemd {
	return &fakeSystemd{units: map[string]unitState{}, fail: map[string]error{}}
}

func (f *fakeSystemd) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	sub := name
	if name == "systemctl" {
		sub = args[0]
	}
	if err := f.fail[sub]; err != nil {
		return nil, err
	}
	switch sub {
	case "systemd-run":
		if f.onRun != nil {
			f.onRun(args)
		}
	case "show":
		return f.show(args[len(args)-1]), nil
	case "list-units":
		return f.list(), nil
	case "stop":
		delete(f.units, args[len(args)-1])
	}
	return nil, nil
}

func (f *fakeSystemd) show(unit string) []byte {
	u, ok := f.units[unit]
	if !ok {
		u = unitState{LoadState: "not-found", ActiveState: "inactive", SubState: "dead", MainPID: "0"}
	}
	return []byte("LoadState=" + u.LoadState + "\nActiveState=" + u.ActiveState + "\nSubState=" +
		u.SubState + "\nMainPID=" + u.MainPID + "\nInvocationID=" + u.InvocationID + "\n")
}

func (f *fakeSystemd) list() []byte {
	var b strings.Builder
	for name, u := range f.units {
		// A failed unit's line starts with a status mark.
		if u.ActiveState == "failed" {
			b.WriteString("● ")
		}
		b.WriteString(name + " " + u.LoadState + " " + u.ActiveState + " " + u.SubState + " iormesh tracer\n")
	}
	// Names not of our shape are skipped (a glob match is not enough).
	b.WriteString("iormesh-.service loaded active running not ours\n")
	b.WriteString("iormesh-a b.service loaded active running not ours\n")
	return []byte(b.String())
}

// subcommands lists the calls as "systemctl stop" / "systemd-run".
func (f *fakeSystemd) subcommands() []string {
	var out []string
	for _, c := range f.calls {
		if c[0] == "systemctl" {
			out = append(out, "systemctl "+c[1])
		} else {
			out = append(out, c[0])
		}
	}
	return out
}

// called reports whether argv was run.
func (f *fakeSystemd) called(argv ...string) bool {
	return slices.ContainsFunc(f.calls, func(c []string) bool { return slices.Equal(c, argv) })
}

var failedUnit = unitState{LoadState: "loaded", ActiveState: "failed", SubState: "failed", MainPID: "0"}

var running = unitState{LoadState: "loaded", ActiveState: "active", SubState: "running",
	MainPID: "4242", InvocationID: "inv1"}

// testControl is a control over sys whose clock advances with every
// sleep, so the start wait's timeout passes instantly.
func testControl(sys commander, env map[string]string) (control, *bytes.Buffer) {
	now := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	out := &bytes.Buffer{}
	return control{sys: sys, self: "/opt/iormesh/bin/iormesh", stdout: out, stderr: &bytes.Buffer{},
		env:   func(k string) string { return env[k] },
		now:   func() time.Time { return now },
		sleep: func(_ context.Context, d time.Duration) error { now = now.Add(d); return nil },
	}, out
}

// liveState is a running state of this test process, so processAlive
// holds.
func liveState(project, inv string) sessionState {
	pid := os.Getpid()
	return sessionState{Project: project, Session: "1a2b3c4d", FQDN: "app1.example.org", PID: pid,
		PIDStartNS: processStart(uint32(pid)), InvocationID: inv, State: stateRunning,
		Targets: `pattern "*java*"`, StartedAt: time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC),
		EndsBy: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
}

func mustWriteState(t *testing.T, dir string, st sessionState) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeState(dir, st); err != nil {
		t.Fatal(err)
	}
}

func TestStartUnitArgv(t *testing.T) {
	sys := newFakeSystemd()
	c, _ := testControl(sys, map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1001"})
	t.Chdir(t.TempDir())
	cwd, _ := os.Getwd()
	cfg, err := c.parseStart([]string{"-pattern", "*orders*", "-project", "shop", "-trace-dir", "traces",
		"-duration", "1h", "-cpu-quota", "50%", "-native-stacks", "-wait", "0"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--unit=iormesh-shop.service", "--description=iormesh tracer, project shop",
		"--collect", "--quiet", "--service-type=exec",
		"--property=RuntimeMaxSec=3900s", "--property=CPUQuota=50%", "--property=MemoryMax=1G",
		"--property=KillSignal=SIGTERM", "--property=TimeoutStopSec=30s",
		"--", "/opt/iormesh/bin/iormesh", "run",
		"-duration=1h0m0s", "-native-stacks=true", "-pattern=*orders*", "-project=shop",
		"-trace-dir=" + filepath.Join(cwd, "traces"), "-ready-owner=1000:1001"}
	if got := cfg.unitArgs(c.self); !slices.Equal(got, want) {
		t.Fatalf("argv\n got %q\nwant %q", got, want)
	}
	if cfg.run.traceDir != filepath.Join(cwd, "traces") || cfg.wait != 0 {
		t.Fatalf("cfg %+v", cfg)
	}
}

// TestStartReadyOwner: an explicit -ready-owner wins over sudo's, and
// root's own sudo (or none) leaves ready/ root's.
func TestStartReadyOwner(t *testing.T) {
	for _, tc := range []struct {
		name  string
		env   map[string]string
		args  []string
		owner string
	}{
		{"sudo", map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"}, nil, "-ready-owner=1000:1000"},
		{"explicit", map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"},
			[]string{"-ready-owner", "7:8"}, "-ready-owner=7:8"},
		{"root sudo", map[string]string{"SUDO_UID": "0", "SUDO_GID": "0"}, nil, ""},
		{"no sudo", nil, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := testControl(newFakeSystemd(), tc.env)
			cfg, err := c.parseStart(append([]string{"-pattern", "x", "-project", "p"}, tc.args...))
			if err != nil {
				t.Fatal(err)
			}
			var got string
			for _, a := range cfg.runArgs {
				if strings.HasPrefix(a, "-ready-owner=") {
					got = a
				}
			}
			if got != tc.owner {
				t.Fatalf("owner arg %q, want %q (args %q)", got, tc.owner, cfg.runArgs)
			}
		})
	}
}

func TestStartRejects(t *testing.T) {
	for name, args := range map[string][]string{
		"no project":       {"-pattern", "x"},
		"bad project":      {"-pattern", "x", "-project", "-shop"},
		"slash project":    {"-pattern", "x", "-project", "a/b"},
		"no target":        {"-project", "p"},
		"bad cpu quota":    {"-pattern", "x", "-project", "p", "-cpu-quota", "100"},
		"shell cpu quota":  {"-pattern", "x", "-project", "p", "-cpu-quota", "1%; rm"},
		"bad memory max":   {"-pattern", "x", "-project", "p", "-memory-max", "1 G"},
		"negative wait":    {"-pattern", "x", "-project", "p", "-wait", "-1s"},
		"out, no segments": {"-pattern", "x", "-project", "p", "-out", "/tmp/o"},
		"positional":       {"-pattern", "x", "-project", "p", "extra"},
		"bad sudo uid":     {"-pattern", "x", "-project", "p"},
	} {
		t.Run(name, func(t *testing.T) {
			sys := newFakeSystemd()
			env := map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"}
			if name == "bad sudo uid" {
				env["SUDO_GID"] = "staff"
			}
			c, _ := testControl(sys, env)
			if err := c.start(context.Background(), args); err == nil {
				t.Fatal("start accepted it")
			}
			if len(sys.calls) != 0 {
				t.Fatalf("systemd was called: %v", sys.calls)
			}
		})
	}
}

// TestStartRefusesLiveUnit: flags change by stop and start (decision #19),
// so a live unit is never replaced or doubled.
func TestStartRefusesLiveUnit(t *testing.T) {
	sys := newFakeSystemd()
	sys.units["iormesh-shop.service"] = running
	c, _ := testControl(sys, nil)
	err := c.start(context.Background(), []string{"-pattern", "x", "-project", "shop", "-trace-dir", t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "already active") {
		t.Fatalf("err %v", err)
	}
	if got := sys.subcommands(); !slices.Equal(got, []string{"systemctl show"}) {
		t.Fatalf("calls %v", got)
	}
}

// TestStartWaitsForAttach: start returns once the unit's run wrote its
// running state, recognized by the unit's invocation id; a state of an
// earlier run is not mistaken for it.
func TestStartWaitsForAttach(t *testing.T) {
	traces := t.TempDir()
	dir := filepath.Join(traces, "shop")
	old := liveState("shop", "inv0")
	old.State = stateEnded
	mustWriteState(t, dir, old)
	sys := newFakeSystemd()
	sys.units["iormesh-shop.service"] = unitState{LoadState: "loaded", ActiveState: "failed", SubState: "failed"}
	shows := 0
	sys.onRun = func([]string) { sys.units["iormesh-shop.service"] = running }
	c, out := testControl(sys, nil)
	c.sleep = func(context.Context, time.Duration) error {
		if shows++; shows == 3 { // the run attaches after a few polls
			mustWriteState(t, dir, liveState("shop", "inv1"))
		}
		return nil
	}
	if err := c.start(context.Background(), []string{"-pattern", "*java*", "-project", "shop",
		"-trace-dir", traces}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "started iormesh-shop.service: session 1a2b3c4d") {
		t.Fatalf("output %q", out)
	}
	got := sys.subcommands()
	if !slices.Equal(got[:3], []string{"systemctl show", "systemctl reset-failed", "systemd-run"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestStartWaitFailures(t *testing.T) {
	for _, tc := range []struct {
		name  string
		after unitState // the unit once systemd-run returned
		state string    // what the run writes: "", running, ended
		want  string
	}{
		{"unit exits", unitState{LoadState: "not-found", ActiveState: "inactive"}, "", "exited before"},
		{"never attaches", running, "", "has not attached after 2m0s"},
		{"ends at once", running, stateEnded, "ended at once (targets_exited)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			traces := t.TempDir()
			sys := newFakeSystemd()
			sys.onRun = func([]string) {
				sys.units["iormesh-p.service"] = tc.after
				if tc.state != "" {
					st := liveState("p", "inv1")
					st.State, st.EndReason = tc.state, "targets_exited"
					mustWriteState(t, filepath.Join(traces, "p"), st)
				}
			}
			c, _ := testControl(sys, nil)
			err := c.start(context.Background(), []string{"-pattern", "x", "-project", "p", "-trace-dir", traces})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v, want %q", err, tc.want)
			}
		})
	}
}

func TestStartSystemdRunFails(t *testing.T) {
	sys := newFakeSystemd()
	sys.fail["systemd-run"] = errors.New("Unit iormesh-p.service was already loaded")
	c, _ := testControl(sys, nil)
	err := c.start(context.Background(), []string{"-pattern", "x", "-project", "p", "-trace-dir", t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "already loaded") {
		t.Fatalf("err %v", err)
	}
}

func TestStop(t *testing.T) {
	traces := t.TempDir()
	st := liveState("shop", "inv1")
	st.State, st.EndReason, st.EndedAt = stateEnded, "stop", time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	mustWriteState(t, filepath.Join(traces, "shop"), st)
	sys := newFakeSystemd()
	sys.units["iormesh-shop.service"] = running
	c, out := testControl(sys, nil)
	if err := c.stop(context.Background(), []string{"-project", "shop", "-trace-dir", traces}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "session 1a2b3c4d ended (stop) at 2026-11-20T09:00:00Z") {
		t.Fatalf("output %q", out)
	}
	if !sys.called("systemctl", "stop", "--", "iormesh-shop.service") {
		t.Fatalf("no stop call: %v", sys.calls)
	}
	// Stopped already, or never started: an error, nothing stopped.
	sys.calls = nil
	err := c.stop(context.Background(), []string{"-project", "shop", "-trace-dir", traces})
	if err == nil || !strings.Contains(err.Error(), "is not running (not loaded)") {
		t.Fatalf("second stop: %v", err)
	}
	if got := sys.subcommands(); !slices.Equal(got, []string{"systemctl show"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestStopRejects(t *testing.T) {
	for name, args := range map[string][]string{
		"no project":  {},
		"bad project": {"-project", "a b"},
		"positional":  {"-project", "p", "x"},
	} {
		sys := newFakeSystemd()
		c, _ := testControl(sys, nil)
		if err := c.stop(context.Background(), args); !errors.Is(err, errUsage) {
			t.Errorf("%s: err %v, want usage", name, err)
		}
		if len(sys.calls) != 0 {
			t.Errorf("%s: systemd called: %v", name, sys.calls)
		}
	}
}

func TestStatus(t *testing.T) {
	traces := t.TempDir()
	mustWriteState(t, filepath.Join(traces, "shop"), liveState("shop", "inv1"))
	dead := liveState("billing", "inv2")
	dead.PIDStartNS++ // another incarnation of the pid: the tracer is gone
	mustWriteState(t, filepath.Join(traces, "billing"), dead)
	ready := filepath.Join(traces, "shop", "ready")
	for _, f := range []string{"h-1a2b3c4d-20261120T080000Z-000000.parquet",
		"h-1a2b3c4d-20261120T080000Z-000000.stacks.parquet", "h-1a2b3c4d-20261120T080100Z-000001.parquet"} {
		if err := os.MkdirAll(ready, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ready, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sys := newFakeSystemd()
	sys.units["iormesh-shop.service"] = running
	sys.units["iormesh-x.y.service"] = failedUnit
	c, out := testControl(sys, nil)
	if err := c.status(context.Background(), []string{"-trace-dir", traces, "-json"}); err != nil {
		t.Fatal(err)
	}
	var reports []statusReport
	if err := json.Unmarshal(out.Bytes(), &reports); err != nil {
		t.Fatal(err)
	}
	got := map[string]statusReport{}
	for _, r := range reports {
		got[r.Project] = r
	}
	// x.y comes from list-units only: a unit with no state.
	if len(got) != 3 || got["shop"].State != stateRunning || got["shop"].ReadySegments != 2 ||
		got["shop"].UnitState != "active/running" || got["billing"].State != stateDead ||
		got["x.y"].State != "" || got["x.y"].Error != "" {
		t.Fatalf("reports %+v", reports)
	}
}

func TestStatusText(t *testing.T) {
	traces := t.TempDir()
	mustWriteState(t, filepath.Join(traces, "shop"), liveState("shop", "inv1"))
	sys := newFakeSystemd()
	sys.units["iormesh-shop.service"] = running
	c, out := testControl(sys, nil)
	if err := c.status(context.Background(), []string{"-project", "shop", "-trace-dir", traces}); err != nil {
		t.Fatal(err)
	}
	want := "shop: running (unit iormesh-shop.service active/running)\n" +
		"  session 1a2b3c4d on app1.example.org, pid " + strconv.Itoa(os.Getpid()) + `, tracing pattern "*java*"` + "\n" +
		"  started 2026-11-20T08:00:00Z, ends by 2026-11-20T12:00:00Z\n" +
		"  ready/: 0 segments to collect\n"
	if out.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", out, want)
	}
}

// TestStatusErrors: an unknown project, a corrupt state and a state of
// another project are errors, reported, never shown as a session.
func TestStatusErrors(t *testing.T) {
	traces := t.TempDir()
	if err := os.MkdirAll(filepath.Join(traces, "bad"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(traces, "bad", StateFile), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustWriteState(t, filepath.Join(traces, "other"), liveState("shop", "inv1"))
	empty := liveState("empty", "")
	empty.State = ""
	mustWriteState(t, filepath.Join(traces, "empty"), empty)
	for project, want := range map[string]string{
		"nosuch": "no session",
		"bad":    "corrupt",
		"other":  `names project "shop"`,
		"empty":  "corrupt",
	} {
		c, out := testControl(newFakeSystemd(), nil)
		err := c.status(context.Background(), []string{"-project", project, "-trace-dir", traces})
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(out.String(), "error: ") {
			t.Errorf("%s: err %v, output %q; want %q", project, err, out, want)
		}
	}
	c, _ := testControl(newFakeSystemd(), nil)
	if err := c.status(context.Background(), []string{"-project", "../x"}); !errors.Is(err, errUsage) {
		t.Errorf("invalid project: %v", err)
	}
}

func TestUninstall(t *testing.T) {
	root := filepath.Join(t.TempDir(), "opt-iormesh")
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	sys := newFakeSystemd()
	sys.units["iormesh-shop.service"] = running
	sys.units["iormesh-x.y.service"] = failedUnit
	c, out := testControl(sys, nil)
	if err := c.uninstallFrom(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	for _, unit := range []string{"iormesh-shop.service", "iormesh-x.y.service"} {
		if !sys.called("systemctl", "stop", "--", unit) {
			t.Errorf("%s not stopped: %v", unit, sys.calls)
		}
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("root left: %v", err)
	}
	if !strings.Contains(out.String(), "removed "+root) {
		t.Fatalf("output %q", out)
	}
	// A unit that will not stop keeps the tree.
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	sys.units["iormesh-shop.service"] = running
	sys.fail["stop"] = errors.New("job failed")
	if err := c.uninstallFrom(context.Background(), root); err == nil {
		t.Fatal("uninstall ignored a failed stop")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("root removed after a failed stop: %v", err)
	}
	if err := c.uninstall(context.Background(), []string{"-root", "/"}); !errors.Is(err, errUsage) {
		t.Fatalf("uninstall took a path: %v", err)
	}
}

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	o := &segOutput{dir: dir, session: "1a2b3c4d", fqdn: "h"}
	cfg := runConfig{project: "p", duration: time.Hour}
	now := time.Date(2026, 11, 20, 8, 0, 0, 0, time.FixedZone("x", 3600))
	t.Setenv("INVOCATION_ID", "inv9")
	if err := o.markRunning(cfg, "pid [1]", now); err != nil {
		t.Fatal(err)
	}
	st, err := readState(dir)
	if err != nil || st.State != stateRunning || st.InvocationID != "inv9" || st.PID != os.Getpid() ||
		!st.EndsBy.Equal(now.Add(time.Hour)) || st.StartedAt.Location() != time.UTC ||
		st.effective() != stateRunning {
		t.Fatalf("state %+v, %v", st, err)
	}
	if err := o.markEnded("stop", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if st, err = readState(dir); err != nil || st.State != stateEnded || st.EndReason != "stop" {
		t.Fatalf("ended %+v, %v", st, err)
	}
	if fi, err := os.Stat(filepath.Join(dir, StateFile)); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v, %v", fi, err)
	}
	// Another session's state (the run never attached) is not touched.
	other := &segOutput{dir: dir, session: "ffffffff"}
	if err := other.markEnded("error", now); err != nil {
		t.Fatal(err)
	}
	if st, _ = readState(dir); st.Session != "1a2b3c4d" || st.EndReason != "stop" {
		t.Fatalf("other session changed the state: %+v", st)
	}
	if err := (*segOutput)(nil).markRunning(cfg, "", now); err != nil {
		t.Fatal(err)
	}
}
