package ctl

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

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
		{[]string{"collect", "-project", "nope"}, `"nope" on no selected host`},
		{[]string{"collect", "-project", "billing", "app2"}, "app2 carries none of billing"},
		{[]string{"collect", "-project", "shop", "-project", "billing", "app2"}, `"billing" on no selected host`},
	} {
		if code := f.run(c.args...); code != exitError || !strings.Contains(f.stderr.String(), c.msg) {
			t.Errorf("%v: exit %d, stderr %s", c.args, code, f.stderr)
		}
		if len(f.log) != 0 {
			t.Errorf("%v contacted hosts: %q", c.args, f.log)
		}
	}
}

// TestCollectFailures: an unreachable host or a failed transfer is
// reported by host and fails the command, but every other host and
// project is still collected; a project with no ready/ yet is skipped.
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
	f.answers["app2 test -d"] = reply{err: errors.New("exit status 1")}
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
}

// TestCollectRefusesOpenStore: a store others can write is refused, not
// repaired, and nothing is fetched into it.
func TestCollectRefusesOpenStore(t *testing.T) {
	f := newDeployFixture(t)
	mustMkdir(t, f.store, 0o777)
	if code := f.run("collect", "app2"); code != exitError || !strings.Contains(f.stderr.String(), "writable by others") {
		t.Fatalf("exit %d, stderr %s", code, f.stderr)
	}
	for _, c := range f.log {
		if c[1] == "fetch" {
			t.Errorf("fetched into an open store: %q", c)
		}
	}
	assertDirMode(t, f.store, 0o777)
}

// TestCollectLiveSession runs real rsync against a server tree laid out
// as a live session leaves it: the active segment at the project
// directory's top, published segments in ready/. Each collect moves what
// ready/ holds and nothing else; the active file is never pulled or
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
	mustMkdir(t, ready, 0o755)
	writeFile(t, active, []byte("partial"), 0o644)
	writeFile(t, filepath.Join(ready, seg("000000")+".methods.parquet"), []byte("PAR1m"), 0o644)
	writeFile(t, filepath.Join(ready, seg("000000")+".parquet"), []byte("PAR1e"), 0o644)
	f.e.transport = func(host string) (federate.Transport, error) {
		return localServer{root: server, respond: f.respond}, nil
	}
	local := filepath.Join(f.store, "shop", "app2")

	f.mustRun(t, "collect", "app2")
	assertFiles(t, local, seg("000000")+".methods.parquet", seg("000000")+".parquet")
	assertFiles(t, ready)
	assertFiles(t, proj, filepath.Base(active), "ready")

	// Rotation: the active segment is published, the next one opened.
	mustDo(t, os.Rename(active, filepath.Join(ready, seg("000001")+".parquet")),
		os.WriteFile(active, []byte("partial2"), 0o644))
	f.mustRun(t, "collect", "app2")
	assertFiles(t, local, seg("000000")+".methods.parquet", seg("000000")+".parquet", seg("000001")+".parquet")
	assertFiles(t, ready)
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
// tree; Fetch runs real rsync with federate.SSH.Fetch's flags (-rt
// --remove-source-files, no -l or -p), minus ssh.
type localServer struct {
	root    string
	respond func(host string, argv []string) ([]byte, error)
}

func (s localServer) Run(_ context.Context, argv ...string) ([]byte, error) {
	if argv[0] == "test" && argv[1] == "-d" {
		info, err := os.Stat(filepath.Join(s.root, argv[2]))
		if err != nil || !info.IsDir() {
			return nil, errors.New("exit status 1")
		}
		return nil, nil
	}
	return s.respond("local", argv)
}

func (s localServer) Fetch(ctx context.Context, remote, local string) error {
	if err := os.MkdirAll(local, 0o700); err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, "rsync", "-rt", "--remove-source-files", "--",
		filepath.Join(s.root, remote)+"/", local+"/").CombinedOutput()
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
