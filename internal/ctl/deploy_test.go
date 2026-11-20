package ctl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"iormesh/internal/federate"
	"iormesh/internal/release"
)

// fakeTransport records every call as one line, "HOST run ARGV..." or
// "HOST push DIR FILE...", into a log shared by all hosts, and answers Run
// through respond. No test here reaches a host.
type fakeTransport struct {
	host    string
	log     *[]string
	respond func(host string, argv []string) ([]byte, error)
}

var _ federate.Transport = fakeTransport{}

func (f fakeTransport) Run(_ context.Context, argv ...string) ([]byte, error) {
	*f.log = append(*f.log, f.host+" run "+strings.Join(argv, " "))
	return f.respond(f.host, argv)
}

func (f fakeTransport) Fetch(context.Context, string, string) error {
	return errors.New("deploy must not fetch")
}

func (f fakeTransport) Push(_ context.Context, dir string, files ...string) error {
	names := make([]string, len(files))
	for i, p := range files {
		names[i] = filepath.Base(p)
	}
	*f.log = append(*f.log, f.host+" push "+dir+" "+strings.Join(names, " "))
	return nil
}

// deployFixture is a temp registry, pinned key, signed dist/ and deploy
// record, plus the fake transport's log and responses.
type deployFixture struct {
	e              env
	stdout, stderr *bytes.Buffer
	dist, registry string
	log            []string
	// answers overrides a Run's reply by "HOST ARGV..." prefix.
	answers map[string]reply
}

type reply struct {
	out string
	err error
}

func newDeployFixture(t *testing.T) *deployFixture {
	t.Helper()
	pub, priv := genKey(t)
	cfg := t.TempDir()
	f := &deployFixture{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, dist: t.TempDir(),
		registry: filepath.Join(cfg, "servers.yaml"), answers: map[string]reply{}}
	keyPath := filepath.Join(cfg, keyFileName)
	text, _ := pub.MarshalText()
	writeFile(t, keyPath, text, 0o600)
	signed(t, priv, filepath.Join(f.dist, "iormesh-linux-amd64"))
	signed(t, priv, filepath.Join(f.dist, "iormesh-linux-arm64"))
	record := filepath.Join(t.TempDir(), "share", deployedFileName)
	f.e = env{stdout: f.stdout, stderr: f.stderr,
		registry:   func() (string, error) { return f.registry, nil },
		releaseKey: func() (string, error) { return keyPath, nil },
		deployed:   func() (string, error) { return record, nil },
		transport: func(host string) (federate.Transport, error) {
			return fakeTransport{host: host, log: &f.log, respond: f.respond}, nil
		}}
	f.mustRun(t, "add", "-project", "shop", "-pattern", "*orders*", "app1", "-duration", "1h")
	f.mustRun(t, "add", "-project", "billing", "-listen-port", "8443", "app1")
	f.mustRun(t, "add", "-project", "shop", "-pattern", "*orders*", "app2")
	return f
}

// respond answers like a healthy amd64 host with nothing running, unless
// answers has a reply for the call.
func (f *deployFixture) respond(host string, argv []string) ([]byte, error) {
	call := host + " " + strings.Join(argv, " ")
	for prefix, r := range f.answers {
		if strings.HasPrefix(call, prefix) {
			return []byte(r.out), r.err
		}
	}
	switch argv[0] {
	case "uname":
		return []byte("x86_64\n"), nil
	case "systemctl":
		return []byte("inactive\n"), nil
	}
	return nil, nil
}

func (f *deployFixture) run(args ...string) int {
	f.log = nil
	f.stdout.Reset()
	f.stderr.Reset()
	return run(args, f.e)
}

func (f *deployFixture) mustRun(t *testing.T, args ...string) {
	t.Helper()
	if code := f.run(args...); code != exitOK {
		t.Fatalf("%v: exit %d: %s", args, code, f.stderr)
	}
}

const sudoTr = "sudo -n /opt/iormesh/bin/iormesh "

// preflightLog is the calls up to and including the upgrade on host.
func preflightLog(host, arch string) []string {
	staged := ".iormesh/staging/iormesh-linux-" + arch
	return []string{
		host + " run uname -m",
		host + " run command -v rsync",
		host + " run command -v systemd-run",
		host + " run test -x /opt/iormesh/bin/iormesh",
		host + " run mkdir -p -m 0700 .iormesh/staging",
		host + " push .iormesh/staging iormesh-linux-" + arch + " iormesh-linux-" + arch + ".minisig",
		host + " run " + sudoTr + "upgrade " + staged,
	}
}

// startLog is a project's calls when its unit is not live.
func startLog(host, project string, start ...string) []string {
	return []string{
		host + " run systemctl show --property=ActiveState --value -- iormesh-" + project + ".service",
		host + " run " + sudoTr + strings.Join(start, " "),
		host + " run " + sudoTr + "status -project " + project,
	}
}

func checkLog(t *testing.T, got []string, want ...[]string) {
	t.Helper()
	if w := slices.Concat(want...); !slices.Equal(got, w) {
		t.Errorf("calls\n got %s\nwant %s", strings.Join(got, "\n     "), strings.Join(w, "\n     "))
	}
}

// TestDeployArgv: the exact call sequence of a first deploy to two hosts,
// one per arch, every project of the host.
func TestDeployArgv(t *testing.T) {
	f := newDeployFixture(t)
	f.answers["app2 uname"] = reply{out: "aarch64\n"}
	f.answers["app1 "+sudoTr+"upgrade"] = reply{out: "iormesh: upgraded /opt/iormesh/bin/iormesh from 0.3.0 to 0.4.0\n"}
	if code := f.run("deploy", "-dist", f.dist, "app1", "app2", "app1"); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr)
	}
	checkLog(t, f.log,
		preflightLog("app1", "amd64"),
		startLog("app1", "shop", "start", "-project", "shop", "-pattern=*orders*", "-duration", "1h"),
		startLog("app1", "billing", "start", "-project", "billing", "-listen-port=8443"),
		preflightLog("app2", "arm64"),
		startLog("app2", "shop", "start", "-project", "shop", "-pattern=*orders*"))
	for _, want := range []string{"iormesh-linux-amd64 0.4.0", "iormesh-linux-arm64 0.4.0",
		"app1: iormesh: upgraded /opt/iormesh/bin/iormesh from 0.3.0 to 0.4.0\n"} {
		if !strings.Contains(f.stdout.String(), want) {
			t.Errorf("stdout lacks %q:\n%s", want, f.stdout)
		}
	}
	for _, c := range f.log {
		if strings.Contains(c, "allow-downgrade") || strings.Contains(c, "/opt") && strings.Contains(c, "push") {
			t.Errorf("forbidden call %q", c)
		}
		if strings.Contains(c, "sudo") && !strings.Contains(c, sudoTr) {
			t.Errorf("sudo of something else than the installed tracer: %q", c)
		}
	}
}

// TestDeployIdempotent: a re-deploy of the same build and entry to a live
// session upgrades (a no-op on the server) and leaves the session alone;
// a changed entry or -restart stops and starts it.
func TestDeployIdempotent(t *testing.T) {
	f := newDeployFixture(t)
	f.mustRun(t, "deploy", "-dist", f.dist, "-project", "shop", "app2")
	f.answers["app2 systemctl"] = reply{out: "active\n"}
	f.mustRun(t, "deploy", "-dist", f.dist, "-project", "shop", "app2")
	show := "app2 run systemctl show --property=ActiveState --value -- iormesh-shop.service"
	status := "app2 run " + sudoTr + "status -project shop"
	checkLog(t, f.log, preflightLog("app2", "amd64"), []string{show, status})
	if !strings.Contains(f.stdout.String(), "left alone") {
		t.Errorf("stdout: %s", f.stdout)
	}

	restart := []string{show, "app2 run " + sudoTr + "stop -project shop",
		"app2 run " + sudoTr + "start -project shop -pattern=*orders*", status}
	f.mustRun(t, "deploy", "-dist", f.dist, "-restart", "app2")
	checkLog(t, f.log, preflightLog("app2", "amd64"), restart)

	f.mustRun(t, "rm", "app2")
	f.mustRun(t, "add", "-project", "shop", "-pattern", "*orders*", "app2", "-duration", "2h")
	f.mustRun(t, "deploy", "-dist", f.dist, "app2")
	restart[2] += " -duration 2h"
	checkLog(t, f.log, preflightLog("app2", "amd64"), restart)
}

// TestDeployRefusals: what stops a deploy, and how far it got. Every case
// deploys app1 then app2 (unless the command is refused as a whole): a
// failing host never keeps the next one from deploying.
func TestDeployRefusals(t *testing.T) {
	app2 := slices.Concat(preflightLog("app2", "amd64"),
		startLog("app2", "shop", "start", "-project", "shop", "-pattern=*orders*"))
	for _, c := range refusalCases() {
		t.Run(c.name, func(t *testing.T) { c.check(t, app2) })
	}
}

// refusal is one TestDeployRefusals case.
type refusal struct {
	name    string
	args    []string
	answers map[string]reply
	app1    []string // app1's calls; nil with whole=true
	whole   bool     // refused before any host
	stderr  string
}

func refusalCases() []refusal {
	boom := errors.New("exit status 1: boom")
	return []refusal{
		{name: "unregistered host", args: []string{"app1", "app3"}, whole: true, stderr: "host not registered"},
		{name: "unknown project", args: []string{"-project", "nope", "app1"}, whole: true,
			stderr: "project not registered"},
		{name: "unknown arch", answers: map[string]reply{"app1 uname": {out: "riscv64\n"}},
			app1: []string{"app1 run uname -m"}, stderr: `unsupported architecture "riscv64"`},
		{name: "rsync missing", answers: map[string]reply{"app1 command -v rsync": {err: boom}},
			app1: preflightLog("app1", "amd64")[:2], stderr: "rsync not found"},
		{name: "not installed", answers: map[string]reply{"app1 test -x": {err: boom}},
			app1: preflightLog("app1", "amd64")[:4], stderr: "decision #1"},
		{name: "upgrade refused", answers: map[string]reply{"app1 " + sudoTr + "upgrade": {err: boom}},
			app1: preflightLog("app1", "amd64"), stderr: "upgrade refused, nothing started: exit status 1: boom"},
		{name: "start fails", answers: map[string]reply{"app1 " + sudoTr + "start -project shop": {err: boom}},
			app1: slices.Concat(preflightLog("app1", "amd64"), startLog("app1", "shop",
				"start", "-project", "shop", "-pattern=*orders*", "-duration", "1h")[:2],
				startLog("app1", "billing", "start", "-project", "billing", "-listen-port=8443")),
			stderr: "project shop: start: exit status 1: boom"},
	}
}

// check runs the case against a fresh fixture: app1 fails as the case
// says, app2 deploys in full unless the whole command was refused.
func (c refusal) check(t *testing.T, app2 []string) {
	f := newDeployFixture(t)
	if c.answers != nil {
		f.answers = c.answers
	}
	args := append([]string{"deploy", "-dist", f.dist}, c.args...)
	if c.args == nil {
		args = append(args, "app1", "app2")
	}
	if code := f.run(args...); code == exitOK || !strings.Contains(f.stderr.String(), c.stderr) {
		t.Fatalf("exit %d, stderr %s, want %q", code, f.stderr, c.stderr)
	}
	if c.whole {
		checkLog(t, f.log)
		return
	}
	checkLog(t, f.log, c.app1, app2)
}

// TestDeployBadBuild: a missing, unsigned or tampered build for the host's
// arch fails after the preflight — nothing is pushed, nothing sudo'd.
func TestDeployBadBuild(t *testing.T) {
	_, other := genKey(t)
	cases := map[string]func(t *testing.T, bin string){
		"missing binary":    func(t *testing.T, bin string) { rm(t, bin) },
		"missing signature": func(t *testing.T, bin string) { rm(t, bin+release.SigSuffix) },
		"tampered":          func(t *testing.T, bin string) { writeFile(t, bin, []byte("changed"), 0o755) },
		"foreign key":       func(t *testing.T, bin string) { signed(t, other, bin) },
		"other arch renamed": func(t *testing.T, bin string) {
			arm := filepath.Join(filepath.Dir(bin), "iormesh-linux-arm64")
			for _, suffix := range []string{"", release.SigSuffix} {
				data, err := os.ReadFile(arm + suffix)
				if err != nil {
					t.Fatal(err)
				}
				writeFile(t, bin+suffix, data, 0o644)
			}
		},
	}
	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			f := newDeployFixture(t)
			spoil(t, filepath.Join(f.dist, "iormesh-linux-amd64"))
			if code := f.run("deploy", "-dist", f.dist, "app1"); code != exitError ||
				!strings.Contains(f.stderr.String(), "nothing pushed") {
				t.Fatalf("exit %d, stderr %s", code, f.stderr)
			}
			checkLog(t, f.log, preflightLog("app1", "amd64")[:4])
		})
	}
}

// TestDeployRecord: the record holds what was started, keyed by host and
// project, and survives a reload; a corrupt one is an error.
func TestDeployRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d", deployedFileName)
	r, err := loadDeployed(path)
	if err != nil || len(r) != 0 {
		t.Fatalf("missing record = %v, %v", r, err)
	}
	r.set("app1", "shop", []string{"start", "-project", "shop"})
	if err := r.save(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("record mode %v, %v", info.Mode(), err)
	}
	r, err = loadDeployed(path)
	if err != nil || !r.same("app1", "shop", []string{"start", "-project", "shop"}) ||
		r.same("app1", "billing", nil) || r.same("app1", "shop", []string{"start"}) {
		t.Errorf("reloaded record %v, %v", r, err)
	}
	writeFile(t, path, []byte("{"), 0o600)
	if _, err := loadDeployed(path); err == nil {
		t.Error("corrupt record loaded")
	}
}

func TestRemoteArch(t *testing.T) {
	for machine, want := range map[string]string{"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64",
		"riscv64": "", "i686": ""} {
		f := fakeTransport{host: "h", log: &[]string{}, respond: func(string, []string) ([]byte, error) {
			return []byte(machine + "\n"), nil
		}}
		got, err := preflight(context.Background(), f)
		if got != want || (want == "") != (err != nil) {
			t.Errorf("%s: %q, %v; want %q", machine, got, err, want)
		}
	}
}

func rm(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(fmt.Errorf("rm: %w", err))
	}
}
