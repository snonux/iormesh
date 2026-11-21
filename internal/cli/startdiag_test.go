package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSystemdPath puts shell stand-ins for systemd-run, systemctl and
// journalctl first on PATH, so start runs through the real execCommander
// (argv, stderr capture) without touching the host's systemd. systemd-run
// prints runStderr to stderr and exits 1, as el9's does when systemd
// cannot exec the binary; journalctl prints journal; systemctl answers
// show with an unknown unit (a --collect'ed one) and status with status,
// exiting 3 as systemctl does for a failed or missing unit.
func fakeSystemdPath(t *testing.T, runStderr, journal, status string) {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("stderr.txt", runStderr)
	write("journal.txt", journal)
	write("status.txt", status)
	scripts := map[string]string{
		"systemd-run": "#!/bin/sh\ncat '" + dir + "/stderr.txt' >&2\nexit 1\n",
		"journalctl":  "#!/bin/sh\ncat '" + dir + "/journal.txt'\n",
		"systemctl": "#!/bin/sh\ncase \"$1\" in\n" +
			"show) printf 'LoadState=not-found\\nActiveState=inactive\\nSubState=dead\\nMainPID=0\\nInvocationID=\\n' ;;\n" +
			"status) cat '" + dir + "/status.txt'; exit 3 ;;\n" +
			"esac\n",
	}
	for name, body := range scripts {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// el9Journal is what Rocky 9.9's journal holds for a unit whose binary
// sits under /root (seen on iom1, SELinux enforcing).
const el9Journal = `2026-11-21T14:13:57+0000 iom1 systemd[4669]: iormesh-p.service: Failed to locate executable /root/iormesh: Permission denied
2026-11-21T14:13:57+0000 iom1 systemd[4669]: iormesh-p.service: Failed at step EXEC spawning /root/iormesh: Permission denied
2026-11-21T14:13:57+0000 iom1 systemd[1]: iormesh-p.service: Main process exited, code=exited, status=203/EXEC
`

// startWith runs a start under the fake PATH with SELinux reported as
// se, the binary at self.
func startWith(t *testing.T, self string, se selinuxState) error {
	t.Helper()
	c, _, traces := rootedControl(t, execCommander{}, nil)
	c.self = self
	c.selinux = func(string) selinuxState { return se }
	return c.start(context.Background(), []string{"-pattern", "x", "-project", "p", "-trace-dir", traces})
}

func TestStartDiagnosesExecFailure(t *testing.T) {
	fakeSystemdPath(t, "", el9Journal, "")
	err := startWith(t, "/root/iormesh",
		selinuxState{enforcing: true, label: "unconfined_u:object_r:admin_home_t:s0"})
	if err == nil {
		t.Fatal("start succeeded")
	}
	for _, want := range []string{
		"exit status 1", "(no stderr)", "iormesh-p.service logged:",
		"Failed to locate executable /root/iormesh: Permission denied", "status=203/EXEC",
		"SELinux is enforcing and /root/iormesh is labelled admin_home_t", "/usr/local/bin or /opt",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
}

// With nothing in the journal start falls back to `systemctl status`;
// with SELinux permissive there is no hint.
func TestStartDiagnosisFallsBackToStatus(t *testing.T) {
	fakeSystemdPath(t, "Job for iormesh-p.service failed.\n", "",
		"x iormesh-p.service - iormesh tracer\n     Active: failed (Result: exit-code)\n")
	err := startWith(t, "/root/iormesh",
		selinuxState{enforcing: false, label: "unconfined_u:object_r:admin_home_t:s0"})
	if err == nil {
		t.Fatal("start succeeded")
	}
	msg := err.Error()
	for _, want := range []string{"Job for iormesh-p.service failed.", "Active: failed (Result: exit-code)"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
	if strings.Contains(msg, "SELinux is enforcing") {
		t.Errorf("hint with SELinux permissive:\n%v", err)
	}
}

func TestStartSELinuxHint(t *testing.T) {
	for _, tc := range []struct {
		name, journal, label string
		enforcing            bool
		want                 string // "" means no hint
	}{
		{"bin_t, no denial", "Main process exited, status=1/FAILURE\n",
			"system_u:object_r:bin_t:s0", true, ""},
		{"usr_t, no denial", "x\n", "system_u:object_r:usr_t:s0", true, ""},
		{"bin_t, denied", el9Journal, "system_u:object_r:bin_t:s0", true,
			"/opt/x/iormesh is labelled bin_t"},
		{"unlabelled, avc", "avc:  denied  { execute }\n", "", true,
			"SELinux is enforcing: systemd may not exec"},
		{"unlabelled, no denial", "x\n", "", true, ""},
		{"admin_home_t, quiet journal", "", "u:r:admin_home_t:s0", true,
			"is labelled admin_home_t"},
		{"permissive", el9Journal, "u:r:admin_home_t:s0", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeSystemdPath(t, "", tc.journal, "")
			err := startWith(t, "/opt/x/iormesh", selinuxState{enforcing: tc.enforcing, label: tc.label})
			if err == nil {
				t.Fatal("start succeeded")
			}
			has := strings.Contains(err.Error(), "SELinux is enforcing")
			switch {
			case tc.want == "" && has:
				t.Errorf("unexpected hint:\n%v", err)
			case tc.want != "" && !strings.Contains(err.Error(), tc.want):
				t.Errorf("error lacks %q:\n%v", tc.want, err)
			}
		})
	}
}

// The journal and systemd-run's stderr come from the host: an escape
// sequence never reaches the terminal raw, and the diagnosis stays
// bounded however much the unit logged.
func TestStartDiagnosisBoundedAndTerminalSafe(t *testing.T) {
	var journal strings.Builder
	for i := range 200 {
		journal.WriteString("line " + strings.Repeat("y", i*10) + "\n")
	}
	journal.WriteString("last \x1b[2J line\n")
	fakeSystemdPath(t, "Failed to start transient service unit: \x1b]0;pwned\x07\n",
		journal.String(), "")
	err := startWith(t, "/opt/x/iormesh", selinuxState{})
	if err == nil {
		t.Fatal("start succeeded")
	}
	msg := err.Error()
	if strings.ContainsAny(msg, "\x1b\x07") {
		t.Errorf("raw control bytes in error:\n%q", msg)
	}
	for _, want := range []string{`"Failed to start transient service unit: \x1b]0;pwned\a"`,
		`"last \x1b[2J line"`} {
		if !strings.Contains(msg, want) {
			t.Errorf("error lacks %s:\n%q", want, msg)
		}
	}
	_, tail, _ := strings.Cut(msg, "logged:\n")
	if n := strings.Count(tail, "\n") + 1; n > diagLines {
		t.Errorf("%d journal lines, want ≤ %d", n, diagLines)
	}
	if len(tail) > diagBytes {
		t.Errorf("journal tail %d bytes, want ≤ %d", len(tail), diagBytes)
	}
	if strings.Contains(tail, strings.Repeat("y", diagLineBytes+1)) {
		t.Error("a journal line was not cut")
	}
}

// boundedLines keeps the last lines, so the cause (systemd logs it last)
// survives the cut.
func TestBoundedLinesKeepsTheTail(t *testing.T) {
	var in strings.Builder
	for i := range 50 {
		fmt.Fprintf(&in, "l%03d\n\n", i)
	}
	got := boundedLines([]byte(in.String()))
	lines := strings.Split(got, "\n")
	if len(lines) != diagLines || lines[0] != "  l030" || lines[len(lines)-1] != "  l049" {
		t.Errorf("got %q", lines)
	}
}
