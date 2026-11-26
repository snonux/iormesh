package agenttrial

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestShellRefusesLocal: a host that is not one of the VMs — the laptop
// by any name, a path, an ssh option smuggled in as the host, a host with
// a shell tail — is refused before ssh starts, and logged as refused.
func TestShellRefusesLocal(t *testing.T) {
	s, log := fakeShell(t)
	var cmdLog bytes.Buffer
	s.Log = &cmdLog
	for _, host := range []string{"localhost", "127.0.0.1", "", "/bin/sh", "../iom1", "./iom1",
		"-oProxyCommand=touch /tmp/x", "iom1;sh", "iom1 sh", "IOM1", "iom3"} {
		_, _, err := s.Run(context.Background(), Call{Host: host, Command: "true", By: ByAgent})
		if !errors.Is(err, ErrRefused) {
			t.Errorf("host %q: err %v, want ErrRefused", host, err)
		}
	}
	for _, cmd := range []string{"", "  ", "true\x00rm -rf ~"} {
		if _, _, err := s.Run(context.Background(), Call{Host: "iom1", Command: cmd}); !errors.Is(err, ErrRefused) {
			t.Errorf("command %q: err %v, want ErrRefused", cmd, err)
		}
	}
	if calls := sshCalls(t, log); len(calls) != 0 {
		t.Errorf("ssh ran %d times for refused calls: %v", len(calls), calls)
	}
	if n := strings.Count(cmdLog.String(), `"exit":-1`); n != 14 {
		t.Errorf("%d refused calls logged, want 14:\n%s", n, cmdLog.String())
	}
}

// TestShellMetacharactersStayRemote: whatever shell syntax a command
// holds — a separator, a substitution, backquotes, a redirection to a
// laptop path — reaches ssh as one remote-command argument after the
// host, and nothing of it runs on the laptop (the markers stay absent).
func TestShellMetacharactersStayRemote(t *testing.T) {
	s, log := fakeShell(t)
	dir := t.TempDir()
	marker := func(n string) string { return filepath.Join(dir, n) }
	cmds := []string{
		"true; touch " + marker("semicolon"),
		"$(touch " + marker("subst") + ")",
		"`touch " + marker("backquote") + "`",
		"echo x > " + marker("redirect"),
		"true && touch " + marker("and") + " | cat",
		"cat ~/.ssh/id_rsa /etc/shadow",
	}
	for _, c := range cmds {
		if _, code, err := s.Run(context.Background(), Call{Host: "iom1", Command: c}); err != nil || code != 0 {
			t.Fatalf("%q: exit %d, %v", c, code, err)
		}
	}
	for _, n := range []string{"semicolon", "subst", "backquote", "redirect", "and"} {
		if _, err := os.Stat(marker(n)); !os.IsNotExist(err) {
			t.Errorf("marker %s exists: the command ran on the laptop", n)
		}
	}
	calls := sshCalls(t, log)
	if len(calls) != len(cmds) {
		t.Fatalf("%d ssh calls, want %d", len(calls), len(cmds))
	}
	for i, argv := range calls {
		sep := slices.Index(argv, "--")
		if sep < 0 || !slices.Equal(argv[sep+1:], []string{"iom1", cmds[i]}) {
			t.Errorf("argv %q: want ... -- iom1 %q", argv, cmds[i])
		}
	}
}

// TestShellArgv: BatchMode, no escape character, no tty, nothing
// forwarded, the options before "--".
func TestShellArgv(t *testing.T) {
	s := &Shell{SSH: "ssh", Options: []string{"-F", "cfg"}, Hosts: []string{"iom1"}}
	got := s.Argv("iom1", "uname -a")
	want := []string{"ssh", "-F", "cfg", "-T", "-e", "none", "-o", "BatchMode=yes", "-o", "ForwardAgent=no",
		"-o", "ForwardX11=no", "-o", "PermitLocalCommand=no", "-o", "ClearAllForwardings=yes",
		"--", "iom1", "uname -a"}
	if !slices.Equal(got, want) {
		t.Errorf("argv %q, want %q", got, want)
	}
}

// TestShellRunLogsAndCaps: the exit status comes back, the output is
// capped and says so, and each call is logged with who ran it, its step
// and its command — or LogAs in place of a secret.
func TestShellRunLogsAndCaps(t *testing.T) {
	s, _ := fakeShell(t)
	var cmdLog bytes.Buffer
	s.Log, s.MaxBytes = &cmdLog, 10
	if _, code, err := s.Run(context.Background(), Call{Host: "iom2", Command: "false"}); err != nil || code != 1 {
		t.Errorf("false: exit %d, %v; want 1, nil", code, err)
	}
	out, _, _ := s.Run(context.Background(), Call{Host: "iom1", Command: "echo secret-pw", By: ByAgent,
		Step: 3, LogAs: "echo <password>"})
	if !strings.HasPrefix(out, "ran on iom") || !strings.Contains(out, "[output cut at 10 bytes]") {
		t.Errorf("output %q: want 10 bytes and the cut note", out)
	}
	var e CommandLog
	lines := strings.Split(strings.TrimSpace(cmdLog.String()), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &e); err != nil {
		t.Fatal(err)
	}
	if e.By != ByAgent || e.Step != 3 || e.Host != "iom1" || e.Command != "echo <password>" {
		t.Errorf("log entry %+v", e)
	}
	if strings.Contains(cmdLog.String(), "secret-pw") {
		t.Error("the log holds the secret LogAs replaced")
	}
}
