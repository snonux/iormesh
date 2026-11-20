package federate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// recorder is an execFunc that records each command instead of running
// it, so no test here reaches a host.
type recorder struct {
	calls [][]string
}

func (r *recorder) exec(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	return []byte("ok\n"), nil
}

func newTestSSH(t *testing.T, host string) (SSH, *recorder) {
	t.Helper()
	rec := &recorder{}
	s, err := newSSH(host, "/ctl", rec.exec)
	if err != nil {
		t.Fatal(err)
	}
	return s, rec
}

// TestSSHRunArgv: the options PLAN's security rules need are on every
// call, the host follows "--", and the remote command is one quoted
// string.
func TestSSHRunArgv(t *testing.T) {
	s, rec := newTestSSH(t, "ops@app1")
	out, err := s.Run(context.Background(), "sudo", "/opt/iormesh/bin/iormesh", "status", "-project", "shop")
	if err != nil || string(out) != "ok\n" {
		t.Fatalf("Run = %q, %v", out, err)
	}
	want := []string{"ssh",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=/ctl/%C",
		"-o", "ControlPersist=60s",
		"-o", "ConnectTimeout=15",
		"--", "ops@app1",
		"'sudo' '/opt/iormesh/bin/iormesh' 'status' '-project' 'shop'"}
	if !slices.Equal(rec.calls[0], want) {
		t.Errorf("argv\n got %q\nwant %q", rec.calls[0], want)
	}
	assertNoInsecureOptions(t, rec.calls[0])
}

func assertNoInsecureOptions(t *testing.T, argv []string) {
	t.Helper()
	joined := strings.Join(argv, " ")
	for _, bad := range []string{"StrictHostKeyChecking=no", "UserKnownHostsFile=/dev/null",
		"StrictHostKeyChecking=accept-new", "ForwardAgent=yes"} {
		if strings.Contains(joined, bad) {
			t.Errorf("argv carries %s: %q", bad, argv)
		}
	}
}

// TestQuoteArgvThroughShell runs quoteArgv's output through a local POSIX
// shell — what the remote login shell does with ssh's command string —
// and checks every argument comes out whole and unexpanded.
func TestQuoteArgvThroughShell(t *testing.T) {
	argv := []string{"printf", `%s\n`, "plain", "two words", "it's", `"dq"`, "$(id)", "`id`",
		"a;b", "x|y", "*", "~", "back\\slash", "$HOME", "", "new\nline"}
	out, err := exec.Command("sh", "-c", quoteArgv(argv)).Output()
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join(argv[2:], "\n") + "\n"
	if string(out) != want {
		t.Errorf("shell saw\n%q\nwant\n%q", out, want)
	}
}

func TestSSHFetchArgv(t *testing.T) {
	s, rec := newTestSSH(t, "app1")
	local := filepath.Join(t.TempDir(), "traces", "shop")
	if err := s.Fetch(context.Background(), "/opt/iormesh/traces/shop/ready", local); err != nil {
		t.Fatal(err)
	}
	argv := rec.calls[0]
	want := []string{"rsync", "-a", "--remove-source-files", "-e",
		"ssh '-o' 'BatchMode=yes' '-o' 'StrictHostKeyChecking=yes' '-o' 'ControlMaster=auto'" +
			" '-o' 'ControlPath=/ctl/%C' '-o' 'ControlPersist=60s' '-o' 'ConnectTimeout=15'",
		"--", "app1:/opt/iormesh/traces/shop/ready/", local + "/"}
	if !slices.Equal(argv, want) {
		t.Errorf("argv\n got %q\nwant %q", argv, want)
	}
	assertNoInsecureOptions(t, argv)
	assertMode(t, local, 0o700)
}

// TestTransportRejects: arguments refused before anything runs.
func TestTransportRejects(t *testing.T) {
	s, rec := newTestSSH(t, "app1")
	ctx := context.Background()
	for _, dir := range []string{"relative/ready", "/opt/../etc", "/opt/a b", "/opt/$(id)",
		"/opt/a;b", "/opt/'q'", ""} {
		if err := s.Fetch(ctx, dir, t.TempDir()); !errors.Is(err, ErrTransport) {
			t.Errorf("Fetch(%q) = %v, want ErrTransport", dir, err)
		}
	}
	if _, err := s.Run(ctx); !errors.Is(err, ErrTransport) {
		t.Errorf("Run() = %v, want ErrTransport", err)
	}
	for _, host := range []string{"-oProxyCommand=x", "a b", "a:b", ""} {
		if _, err := newSSH(host, "/ctl", rec.exec); !errors.Is(err, ErrTransport) {
			t.Errorf("newSSH(%q) = %v, want ErrTransport", host, err)
		}
	}
	if _, err := newSSH("app1", "/it's", rec.exec); !errors.Is(err, ErrTransport) {
		t.Errorf("quoted control dir = %v, want ErrTransport", err)
	}
	if len(rec.calls) != 0 {
		t.Errorf("rejected calls ran: %q", rec.calls)
	}
}

func TestCheckPrivateDir(t *testing.T) {
	base := t.TempDir()
	private := filepath.Join(base, "private")
	open := filepath.Join(base, "open")
	link := filepath.Join(base, "link")
	for _, d := range []string{private, open} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(private, link); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivateDir(private); err != nil {
		t.Errorf("private: %v", err)
	}
	for _, d := range []string{open, link} {
		if err := checkPrivateDir(d); !errors.Is(err, ErrTransport) {
			t.Errorf("%s: %v, want ErrTransport", d, err)
		}
	}
}

// TestRunLocalCarriesStderr: a failing command's error says why, bounded.
func TestRunLocalCarriesStderr(t *testing.T) {
	_, err := runLocal(context.Background(), "sh", "-c", "echo 'Host key verification failed.' >&2; exit 255")
	if err == nil || !strings.Contains(err.Error(), "Host key verification failed.") {
		t.Errorf("err = %v", err)
	}
	out, err := runLocal(context.Background(), "sh", "-c", "echo hi")
	if err != nil || string(out) != "hi\n" {
		t.Errorf("runLocal = %q, %v", out, err)
	}
}
