package ctl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"iormesh/internal/federate"
	"iormesh/internal/hostlayout"
)

// The bootstrap tests run on deploy's fixture: a registered app1, a
// signed dist/ and a pinned key, and the fake transport. No test reaches
// a host.

// newBootstrapFixture is deploy's fixture answering like a healthy amd64
// host with NOPASSWD sudo for ops and no sudoers rule yet.
func newBootstrapFixture(t *testing.T) *deployFixture {
	t.Helper()
	f := newDeployFixture(t)
	f.answers["app1 id -un"] = reply{out: "ops\n"}
	f.answers["app1 sudo -n test -e "+hostlayout.SudoersFile] = reply{err: exitStatus(t, 1)}
	return f
}

// sha256Hex is the hex SHA-256 of data.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// pinnedKeyText is the fixture's pinned key as bootstrap stages it.
func pinnedKeyText(t *testing.T, f *deployFixture) []byte {
	t.Helper()
	path, _ := f.e.releaseKey()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

// sudoRun is an expected run of argv under the operator's own sudo -n.
func sudoRun(host string, argv ...string) []string {
	return call(host, "run", slices.Concat([]string{"sudo", "-n"}, argv)...)
}

// placeLog is the calls installing one staged file at dest, checked.
func placeLog(host, staged, dest, mode, sum string, visudo bool) [][]string {
	tmp := filepath.Dir(dest) + "/." + filepath.Base(dest) + ".new"
	log := [][]string{
		sudoRun(host, "install", "-o", "root", "-g", "root", "-m", mode, staged, tmp),
		sudoRun(host, "sh", "-c", hashCheck, "sh", tmp, sum),
	}
	if visudo {
		log = append(log, sudoRun(host, "visudo", "-cf", tmp))
	}
	return append(log, sudoRun(host, "mv", "-f", "-T", tmp, dest))
}

// TestBootstrapApplyArgv pins task qh4's -apply: the exact call sequence,
// every privileged step under the operator's own sudo -n, each installed
// file checked against the SHA-256 of the bytes the controller verified
// before it is renamed into place, the tracer's own upgrade check before
// the rule goes live, and the rule proved through itself last.
func TestBootstrapApplyArgv(t *testing.T) {
	f := newBootstrapFixture(t)
	f.mustRun(t, "bootstrap", "-apply", "-dist", f.dist, "app1")
	bin := staging + "/iormesh-linux-amd64"
	want := slices.Concat([][]string{
		call("app1", "run", "uname", "-m"),
		call("app1", "run", "command", "-v", "rsync"),
		call("app1", "run", "command", "-v", "systemd-run"),
		sudoRun("app1", "true"),
		call("app1", "run", "id", "-un"),
		sudoRun("app1", "test", "-e", hostlayout.SudoersFile),
		call("app1", "run", "pwd"),
		call("app1", "run", "mkdir", "-p", "-m", "0700", staging),
		call("app1", "push", staging, "iormesh-linux-amd64", "iormesh-linux-amd64.minisig", "minisign.pub",
			"iormesh.sudoers"),
		sudoRun("app1", "install", "-d", "-o", "root", "-g", "root", "-m", "0755", "/opt/iormesh",
			"/opt/iormesh/bin", "/opt/iormesh/etc"),
	},
		placeLog("app1", bin, hostlayout.Binary, "0755", sha256Hex([]byte("iormesh-linux-amd64")), false),
		placeLog("app1", staging+"/minisign.pub", hostlayout.Key, "0644", sha256Hex(pinnedKeyText(t, f)), false),
		[][]string{sudoRun("app1", hostlayout.Binary, "upgrade", bin)},
		placeLog("app1", staging+"/iormesh.sudoers", hostlayout.SudoersFile, "0440",
			sha256Hex([]byte(hostlayout.SudoersContent("ops"))), true),
		[][]string{sudoRun("app1", hostlayout.Binary, "status")},
	)
	checkLog(t, f.log, want)
}

// TestBootstrapApplyStagesVerifiedBytes: what -apply pushes is exactly
// the verified build, its signature, the pinned key and the generated
// rule — read back from the files the fake transport was handed.
func TestBootstrapApplyStagesVerifiedBytes(t *testing.T) {
	f := newBootstrapFixture(t)
	got := map[string][]byte{}
	f.e.transport = func(host string) (federate.Transport, error) {
		return pushCapture{fakeTransport: f.transport(host), got: got}, nil
	}
	f.mustRun(t, "bootstrap", "-apply", "-dist", f.dist, "app1")
	sig, err := os.ReadFile(filepath.Join(f.dist, "iormesh-linux-amd64.minisig"))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]byte{
		"iormesh-linux-amd64":         []byte("iormesh-linux-amd64"),
		"iormesh-linux-amd64.minisig": sig,
		"minisign.pub":                pinnedKeyText(t, f),
		"iormesh.sudoers":             []byte(hostlayout.SudoersContent("ops")),
	} {
		if string(got[name]) != string(want) {
			t.Errorf("staged %s = %q, want %q", name, got[name], want)
		}
	}
}

// pushCapture reads every pushed file as it is pushed (bootstrap removes
// its temporary copies afterwards).
type pushCapture struct {
	fakeTransport
	got map[string][]byte
}

func (p pushCapture) Push(ctx context.Context, dir string, files ...string) error {
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		p.got[filepath.Base(f)] = data
	}
	return p.fakeTransport.Push(ctx, dir, files...)
}

// TestBootstrapPrintsSteps: without -apply nothing is contacted, and the
// printed steps are -apply's own privileged steps (with sudo, which may
// ask for a password), the staged files' exact content, the verified
// hashes and the generated rule.
func TestBootstrapPrintsSteps(t *testing.T) {
	f := newBootstrapFixture(t)
	f.e.transport = func(string) (federate.Transport, error) {
		t.Fatal("bootstrap without -apply contacted the host")
		return nil, nil
	}
	f.mustRun(t, "bootstrap", "-dist", f.dist, "-user", "ops", "-arch", "arm64", "app1")
	out := f.stdout.String()
	for _, want := range []string{
		"scp " + filepath.Join(f.dist, "iormesh-linux-arm64") + " " +
			filepath.Join(f.dist, "iormesh-linux-arm64.minisig") + " app1:.iormesh/staging/",
		"sudo install -d -o root -g root -m 0755 /opt/iormesh /opt/iormesh/bin /opt/iormesh/etc\n",
		"sudo install -o root -g root -m 0755 ~/.iormesh/staging/iormesh-linux-arm64 " +
			"/opt/iormesh/bin/.iormesh.new\n",
		sha256Hex([]byte("iormesh-linux-arm64")),
		sha256Hex([]byte(hostlayout.SudoersContent("ops"))),
		"'" + hostlayout.Rule("ops") + "'",
		"'" + hostlayout.SudoersMarker + "'",
		"sudo visudo -cf /etc/sudoers.d/.iormesh.new\n",
		"sudo /opt/iormesh/bin/iormesh upgrade ~/.iormesh/staging/iormesh-linux-arm64\n",
		"sudo -n /opt/iormesh/bin/iormesh status\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("printed steps lack %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "sudo -n install") {
		t.Errorf("printed steps use sudo -n for the operator's own sudo:\n%s", out)
	}
}

// TestBootstrapPrintDefaultUserFromHost: without -user, a registry host
// written user@host names its SSH user, so the printed rule grants that
// user — not the controller's own name (review of qh4). -user still wins.
func TestBootstrapPrintDefaultUserFromHost(t *testing.T) {
	f := newBootstrapFixture(t)
	f.mustRun(t, "add", "-project", "shop", "-pattern", "*orders*", "ops@app3")
	f.mustRun(t, "bootstrap", "-dist", f.dist, "ops@app3")
	if out := f.stdout.String(); !strings.Contains(out, "'"+hostlayout.Rule("ops")+"'") {
		t.Errorf("printed rule does not name ops from ops@app3:\n%s", out)
	}
	f.mustRun(t, "bootstrap", "-dist", f.dist, "-user", "deploy", "ops@app3")
	if out := f.stdout.String(); !strings.Contains(out, "'"+hostlayout.Rule("deploy")+"'") {
		t.Errorf("-user deploy not used:\n%s", out)
	}
}

// TestBootstrapRefusals: each refusal names its cause and installs
// nothing — no push, no privileged install.
func TestBootstrapRefusals(t *testing.T) {
	for _, c := range []struct {
		name    string
		args    []string
		answers map[string]reply
		prep    func(*deployFixture)
		want    string
		code    int
	}{
		{name: "unregistered", args: []string{"-apply", "nope"}, want: "host not registered"},
		{name: "unregistered print", args: []string{"nope"}, want: "host not registered"},
		{name: "password sudo", args: []string{"-apply", "app1"},
			answers: map[string]reply{"app1 sudo -n true": {err: errors.New("sudo: a password is required")}},
			want:    "without -apply"},
		{name: "rule exists", args: []string{"-apply", "app1"},
			answers: map[string]reply{"app1 sudo -n test -e " + hostlayout.SudoersFile: {}},
			want:    "already exists"},
		{name: "arch differs", args: []string{"-apply", "-arch", "arm64", "app1"}, want: "-arch arm64"},
		{name: "user differs", args: []string{"-apply", "-user", "deploy", "app1"}, want: "-user deploy"},
		{name: "bad user", args: []string{"-apply", "app1"},
			answers: map[string]reply{"app1 id -un": {out: "ops,root\n"}}, want: "user name"},
		{name: "bad -user", args: []string{"-user", "a b", "app1"}, want: "-user", code: exitUsage},
		{name: "-user ALL", args: []string{"-user", "ALL", "app1"}, want: "-user", code: exitUsage},
		{name: "-user alias", args: []string{"-user", "OPS", "app1"}, want: "-user", code: exitUsage},
		{name: "bad -arch", args: []string{"-arch", "mips", "app1"}, want: "-arch", code: exitUsage},
		{name: "two hosts", args: []string{"app1", "app2"}, want: "exactly one HOST", code: exitUsage},
		{name: "unsigned build", args: []string{"-apply", "app1"},
			prep: func(f *deployFixture) { rm(t, filepath.Join(f.dist, "iormesh-linux-amd64.minisig")) },
			want: "nothing pushed"},
		{name: "unsigned build print", args: []string{"-user", "ops", "app1"},
			prep: func(f *deployFixture) { rm(t, filepath.Join(f.dist, "iormesh-linux-amd64.minisig")) },
			want: "minisig"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newBootstrapFixture(t)
			for k, v := range c.answers {
				f.answers[k] = v
			}
			if c.prep != nil {
				c.prep(f)
			}
			code := f.run(append([]string{"bootstrap", "-dist", f.dist}, c.args...)...)
			want := c.code
			if want == 0 {
				want = exitError
			}
			if code != want || !strings.Contains(f.stderr.String(), c.want) {
				t.Errorf("exit %d, stderr %q; want exit %d and %q", code, f.stderr, want, c.want)
			}
			for _, l := range f.log {
				if l[1] == "push" || slices.Contains(l, "install") {
					t.Errorf("refused bootstrap still ran %q", l)
				}
			}
		})
	}
}

// TestHashCheckScript runs hashCheck with the local sh and sha256sum: it
// passes for the file's SHA-256 and fails for any other, including a name
// that would confuse sha256sum's own output.
func TestHashCheckScript(t *testing.T) {
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("no sha256sum on this controller")
	}
	p := filepath.Join(t.TempDir(), "a b\\n")
	writeFile(t, p, []byte("tracer"), 0o644)
	if err := exec.Command("sh", "-c", hashCheck, "sh", p, sha256Hex([]byte("tracer"))).Run(); err != nil {
		t.Errorf("matching hash refused: %v", err)
	}
	if err := exec.Command("sh", "-c", hashCheck, "sh", p, sha256Hex([]byte("other"))).Run(); err == nil {
		t.Error("other hash accepted")
	}
}

// TestBootstrapApplyStopsAtFailedCheck: an installed copy whose hash does
// not match is never renamed into place, and nothing after it runs.
func TestBootstrapApplyStopsAtFailedCheck(t *testing.T) {
	f := newBootstrapFixture(t)
	f.answers["app1 sudo -n sh -c "+hashCheck+" sh /opt/iormesh/bin/.iormesh.new"] = reply{err: exitStatus(t, 1)}
	if code := f.run("bootstrap", "-apply", "-dist", f.dist, "app1"); code != exitError ||
		!strings.Contains(f.stderr.String(), "SHA-256") {
		t.Fatalf("exit %d, stderr %q", code, f.stderr)
	}
	for _, l := range f.log {
		if slices.Contains(l, "mv") || slices.Contains(l, "upgrade") {
			t.Errorf("ran %q after a failed check", l)
		}
	}
}
