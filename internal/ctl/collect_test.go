package ctl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"iormesh/internal/federate"
)

// readyOf is the remote ready/ of project.
func readyOf(project string) string {
	return "/opt/iormesh/traces/" + project + "/ready"
}

// collectLog is one project's collect calls on host.
func (f *deployFixture) collectLog(host, project string) [][]string {
	return [][]string{
		call(host, "run", "test", "-d", readyOf(project)),
		call(host, "fetch", readyOf(project), filepath.Join(f.store, project, host)),
	}
}

// probeCall is the reachability probe.
func probeCall(host string) []string { return call(host, "run", "true") }

// TestCollectArgv: collect without HOST walks the whole registry, every
// project of every host, ready/ only, into <store>/<project>/<host>; run
// again it makes exactly the same calls (no state, nothing to clean up).
func TestCollectArgv(t *testing.T) {
	f := newDeployFixture(t)
	want := slices.Concat([][]string{probeCall("app1")}, f.collectLog("app1", "shop"),
		f.collectLog("app1", "billing"), [][]string{probeCall("app2")}, f.collectLog("app2", "shop"))
	for range 2 {
		if code := f.run("collect"); code != exitOK {
			t.Fatalf("exit %d: %s", code, f.stderr)
		}
		checkLog(t, f.log, want)
	}
	for _, dir := range []string{f.store, filepath.Join(f.store, "shop"), filepath.Join(f.store, "shop", "app2")} {
		assertDirMode(t, dir, 0o700)
	}
	if !strings.Contains(f.stdout.String(), "app2: project shop: 0 new files in "+f.store) {
		t.Errorf("stdout: %s", f.stdout)
	}
}

// TestCollectSelect: -project narrows (repeatable), HOSTs narrow, and a
// host or project the registry lacks fails before any host is contacted.
func TestCollectSelect(t *testing.T) {
	f := newDeployFixture(t)
	f.mustRun(t, "collect", "-project", "billing")
	checkLog(t, f.log, [][]string{probeCall("app1")}, f.collectLog("app1", "billing"))
	f.mustRun(t, "collect", "-store", f.store, "-project", "shop", "-project", "billing", "app2", "app1", "app2")
	checkLog(t, f.log, [][]string{probeCall("app2")}, f.collectLog("app2", "shop"),
		[][]string{probeCall("app1")}, f.collectLog("app1", "shop"), f.collectLog("app1", "billing"))
	for _, c := range []struct {
		args []string
		msg  string
	}{
		{[]string{"collect", "nope"}, "host not registered"},
		{[]string{"collect", "app1", "nope"}, "host not registered"},
		{[]string{"collect", "-project", "nope"}, `"nope" on no selected target`},
		{[]string{"collect", "-project", "billing", "app2"}, "app2 carries none of billing"},
		{[]string{"collect", "-project", "shop", "-project", "billing", "app2"}, `"billing" on no selected target`},
	} {
		if code := f.run(c.args...); code != exitError || !strings.Contains(f.stderr.String(), c.msg) {
			t.Errorf("%v: exit %d, stderr %s", c.args, code, f.stderr)
		}
		if len(f.log) != 0 {
			t.Errorf("%v contacted hosts: %q", c.args, f.log)
		}
	}
}

// exitStatus is the error a Transport's Run returns for a remote command
// that exited with code: a real *exec.ExitError, as SSH.Run wraps it.
func exitStatus(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != code {
		t.Fatalf("sh exit %d: %v", code, err)
	}
	return fmt.Errorf("ssh app test: %w", err)
}

// TestCollectFailures: an unreachable host or a failed transfer is
// reported by host and fails the command, but every other host and
// project is still collected; a project with no ready/ yet (test exit 1)
// is skipped, while a test that fails any other way (ssh's 255 on a
// dropped connection) is a failure, never "nothing to collect".
func TestCollectFailures(t *testing.T) {
	f := newDeployFixture(t)
	f.answers["app1 true"] = reply{err: errors.New("ssh: connect to host app1: Connection refused")}
	if code := f.run("collect"); code != exitError {
		t.Fatalf("exit %d", code)
	}
	checkLog(t, f.log, [][]string{probeCall("app1"), probeCall("app2")}, f.collectLog("app2", "shop"))
	if !strings.Contains(f.stderr.String(), "app1: host unreachable") ||
		!strings.Contains(f.stderr.String(), "collect failed on 1 of 2 hosts: app1") {
		t.Errorf("stderr: %s", f.stderr)
	}

	delete(f.answers, "app1 true")
	f.fetchErrs["app1 "+readyOf("shop")] = errors.New("rsync: connection unexpectedly closed")
	f.answers["app2 test -d"] = reply{err: exitStatus(t, 1)}
	if code := f.run("collect"); code != exitError {
		t.Fatalf("exit %d", code)
	}
	checkLog(t, f.log, [][]string{probeCall("app1")}, f.collectLog("app1", "shop"),
		f.collectLog("app1", "billing"), [][]string{probeCall("app2"),
			call("app2", "run", "test", "-d", readyOf("shop"))})
	if !strings.Contains(f.stderr.String(), "app1: project shop: rsync: connection unexpectedly closed") ||
		!strings.Contains(f.stderr.String(), "collect failed on 1 of 2 hosts: app1\n") {
		t.Errorf("stderr: %s", f.stderr)
	}
	if !strings.Contains(f.stdout.String(), "app2: project shop: no "+readyOf("shop")+" yet") {
		t.Errorf("stdout: %s", f.stdout)
	}

	f.answers["app2 test -d"] = reply{err: exitStatus(t, 255)}
	if code := f.run("collect", "app2"); code != exitError ||
		!strings.Contains(f.stderr.String(), "app2: project shop: ssh app test: exit status 255") {
		t.Errorf("test -d exit 255: exit %d, stderr %s", code, f.stderr)
	}
}

// hangingTransport is a server that accepted the session and then
// stalled: every call blocks until its context is done, except the
// reachability probe when answerProbe is set.
type hangingTransport struct {
	fakeTransport
	answerProbe bool
}

func (h hangingTransport) Run(ctx context.Context, argv ...string) ([]byte, error) {
	if h.answerProbe && slices.Equal(argv, []string{"true"}) {
		return h.fakeTransport.Run(ctx, argv...)
	}
	*h.log = append(*h.log, slices.Concat([]string{h.host, "run"}, argv))
	<-ctx.Done()
	return nil, ctx.Err()
}

func (h hangingTransport) Fetch(ctx context.Context, remote, local string) error {
	*h.log = append(*h.log, []string{h.host, "fetch", remote, local})
	<-ctx.Done()
	return ctx.Err()
}

// hangOn makes host hang (hangingTransport, answering the probe when
// answerProbe is set) and bounds each host at timeout.
func (f *deployFixture) hangOn(host string, timeout time.Duration, answerProbe bool) {
	f.e.hostTimeout = timeout
	f.e.transport = func(h string) (federate.Transport, error) {
		if h == host {
			return hangingTransport{f.transport(h), answerProbe}, nil
		}
		return f.transport(h), nil
	}
}

// runBounded is run with a watchdog: a command still running after
// limit fails the test instead of hanging it.
func (f *deployFixture) runBounded(t *testing.T, limit time.Duration, args ...string) int {
	t.Helper()
	done := make(chan int, 1)
	go func() { done <- f.run(args...) }()
	select {
	case code := <-done:
		return code
	case <-time.After(limit):
		t.Fatalf("%v did not return within %s: a hung host blocks the others", args, limit)
		return 0
	}
}

// TestCollectHungHost: a host that accepts the session and then stalls
// is given up on after the per-host bound and reported as failed; the
// hosts after it are still collected (task bi4).
func TestCollectHungHost(t *testing.T) {
	f := newDeployFixture(t)
	f.hangOn("app1", 50*time.Millisecond, false)
	if code := f.runBounded(t, 10*time.Second, "collect"); code != exitError {
		t.Fatalf("exit %d: %s", code, f.stderr)
	}
	checkLog(t, f.log, [][]string{probeCall("app1"), probeCall("app2")}, f.collectLog("app2", "shop"))
	if !strings.Contains(f.stderr.String(), "app1: host unreachable") ||
		!strings.Contains(f.stderr.String(), "gave up on the host after 50ms") ||
		!strings.Contains(f.stderr.String(), "collect failed on 1 of 2 hosts: app1") {
		t.Errorf("stderr: %s", f.stderr)
	}
}

// TestDeployHungHost: deploy gives up on a stalled host the same way
// and still deploys the next one.
func TestDeployHungHost(t *testing.T) {
	f := newDeployFixture(t)
	f.hangOn("app1", 50*time.Millisecond, false)
	if code := f.runBounded(t, 10*time.Second, "deploy", "-dist", f.dist, "app1", "app2"); code != exitError {
		t.Fatalf("exit %d: %s", code, f.stderr)
	}
	if !slices.ContainsFunc(f.log, func(c []string) bool { return c[0] == "app2" && c[1] == "push" }) {
		t.Errorf("app2 not deployed: %q", f.log)
	}
	if !strings.Contains(f.stderr.String(), "gave up on the host after 50ms") ||
		!strings.Contains(f.stderr.String(), "deploy failed on 1 of 2 hosts: app1") {
		t.Errorf("stderr: %s", f.stderr)
	}
}

// TestRmHungHost: rm of a host that answers the probe and then stalls
// mid-teardown returns within the bound and leaves the registry alone,
// as any failed teardown does (a hung probe reads as unreachable, which
// TestRmUnreachable covers).
func TestRmHungHost(t *testing.T) {
	f := newDeployFixture(t)
	f.hangOn("app1", 50*time.Millisecond, true)
	if code := f.runBounded(t, 10*time.Second, "rm", "app1"); code != exitError {
		t.Fatalf("exit %d: %s", code, f.stderr)
	}
	if !strings.Contains(f.stderr.String(), "gave up on the host after 50ms") ||
		!strings.Contains(f.stderr.String(), "registry unchanged") {
		t.Errorf("stderr: %s", f.stderr)
	}
	if got := f.hosts(t); len(got) != 3 {
		t.Errorf("registry changed: %v", got)
	}
	assertNoUninstall(t, f.log)
}

// TestCollectRefusesStore: a store or project directory others can
// write, or a store that is a file, is refused, not repaired, and nothing
// is fetched into it; the store is checked before any host is contacted.
func TestCollectRefusesStore(t *testing.T) {
	cases := map[string]struct {
		setup     func(f *deployFixture)
		msg       string
		contacted bool
	}{
		"open store": {func(f *deployFixture) { mustMkdir(t, f.store, 0o777) }, "writable by others", false},
		"store is a file": {func(f *deployFixture) {
			mustMkdir(t, filepath.Dir(f.store), 0o700)
			writeFile(t, f.store, nil, 0o600)
		}, "is not a directory", false},
		"open project dir": {func(f *deployFixture) {
			mustMkdir(t, f.store, 0o700)
			mustMkdir(t, filepath.Join(f.store, "shop"), 0o777)
		}, "writable by others", true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newDeployFixture(t)
			c.setup(f)
			if code := f.run("collect", "app2"); code != exitError || !strings.Contains(f.stderr.String(), c.msg) {
				t.Fatalf("exit %d, stderr %s", code, f.stderr)
			}
			if (len(f.log) > 0) != c.contacted {
				t.Errorf("calls %q", f.log)
			}
			for _, call := range f.log {
				if call[1] == "fetch" {
					t.Errorf("fetched into a refused store: %q", call)
				}
			}
		})
	}
}

// TestCollectLiveSession runs real rsync, with SSH.Fetch's very flags,
// against a server tree laid out as a live session leaves it: the active
// segment at the project directory's top, published segments in ready/,
// plus what a hostile or confused server might plant there — a symlink,
// a subdirectory with a segment-named file, a dot file. Each collect
// moves the published segments and nothing else; the active file is
// never pulled or removed, the planted entries are neither pulled nor
// removed, and after a "rotation" the next collect moves only the new
// segment — between collections the server holds only what is unsynced.
func TestCollectLiveSession(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not installed")
	}
	f := newDeployFixture(t)
	server := t.TempDir()
	proj := filepath.Join(server, "opt", "iormesh", "traces", "shop")
	ready := filepath.Join(proj, "ready")
	active := filepath.Join(proj, ".active-0a1b2c3d-events.parquet")
	seg := func(seq string) string { return "h1.example.org-0a1b2c3d-20261120T120000Z-" + seq }
	mustMkdir(t, filepath.Join(ready, "sub"), 0o755)
	writeFile(t, active, []byte("partial"), 0o644)
	writeFile(t, filepath.Join(ready, seg("000000")+".methods.parquet"), []byte("PAR1m"), 0o644)
	writeFile(t, filepath.Join(ready, seg("000000")+".parquet"), []byte("PAR1e"), 0o644)
	writeFile(t, filepath.Join(ready, "sub", seg("000009")+".parquet"), []byte("PAR1x"), 0o644)
	writeFile(t, filepath.Join(ready, ".hidden.parquet"), []byte("PAR1h"), 0o644)
	mustDo(t, os.Symlink("/etc/passwd", filepath.Join(ready, "lnk.parquet")))
	planted := []string{".hidden.parquet", "lnk.parquet", "sub"}
	f.e.transport = func(host string) (federate.Transport, error) {
		return localServer{root: server, respond: f.respond}, nil
	}
	local := filepath.Join(f.store, "shop", "app2")

	f.mustRun(t, "collect", "app2")
	assertFiles(t, local, seg("000000")+".methods.parquet", seg("000000")+".parquet")
	assertFiles(t, ready, planted...)
	assertFiles(t, filepath.Join(ready, "sub"), seg("000009")+".parquet")
	assertFiles(t, proj, filepath.Base(active), "ready")

	// Rotation: the active segment is published, the next one opened.
	mustDo(t, os.Rename(active, filepath.Join(ready, seg("000001")+".parquet")),
		os.WriteFile(active, []byte("partial2"), 0o644))
	f.mustRun(t, "collect", "app2")
	assertFiles(t, local, seg("000000")+".methods.parquet", seg("000000")+".parquet", seg("000001")+".parquet")
	assertFiles(t, ready, planted...)
	if got, err := os.ReadFile(active); err != nil || string(got) != "partial2" {
		t.Errorf("active file touched: %q, %v", got, err)
	}
	if !strings.Contains(f.stdout.String(), "app2: project shop: 1 new files") {
		t.Errorf("stdout: %s", f.stdout)
	}
	assertDirMode(t, local, 0o700)
}

// localServer is a Transport onto a directory standing in for the server's
// root: Run answers through respond, except `test -d`, which looks at the
// tree; Fetch runs real rsync with federate.FetchFlags, SSH.Fetch's own,
// minus ssh.
type localServer struct {
	root    string
	respond func(host string, argv []string) ([]byte, error)
}

func (s localServer) Run(_ context.Context, argv ...string) ([]byte, error) {
	if argv[0] == "test" && argv[1] == "-d" {
		return nil, exec.Command("test", "-d", filepath.Join(s.root, argv[2])).Run()
	}
	return s.respond("local", argv)
}

func (s localServer) Fetch(ctx context.Context, remote, local string) error {
	if err := os.MkdirAll(local, 0o700); err != nil {
		return err
	}
	args := slices.Concat(federate.FetchFlags(), []string{"--", filepath.Join(s.root, remote) + "/", local + "/"})
	out, err := exec.CommandContext(ctx, "rsync", args...).CombinedOutput()
	if err != nil {
		return errors.New(string(out))
	}
	return os.Chmod(local, 0o700)
}

func (s localServer) Push(context.Context, string, ...string) error {
	return errors.New("collect must not push")
}

// assertFiles checks dir holds exactly names.
func assertFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	sort.Strings(names)
	if !slices.Equal(got, names) {
		t.Errorf("%s holds %q, want %q", dir, got, names)
	}
}

func assertDirMode(t *testing.T, dir string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s mode %v, want %v", dir, got, want)
	}
}

// mustMkdir creates dir (and parents) with exactly mode, umask aside.
func mustMkdir(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	mustDo(t, os.MkdirAll(dir, 0o700), os.Chmod(dir, mode))
}

// mustDo fails the test on the first setup error; the calls run in
// argument order before it is entered.
func mustDo(t *testing.T, errs ...error) {
	t.Helper()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}
