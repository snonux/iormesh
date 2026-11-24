package federate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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

// TestSSHRunArgv: the options decision #12's security rules need are on
// every call, the host follows "--", and the remote command is one
// quoted string.
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
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-o", "ForwardAgent=no",
		"-o", "ClearAllForwardings=yes",
		"-o", "ForwardX11=no",
		"-o", "ForwardX11Trusted=no",
		"--", "ops@app1",
		"'sudo' '/opt/iormesh/bin/iormesh' 'status' '-project' 'shop'"}
	if !slices.Equal(rec.calls[0], want) {
		t.Errorf("argv\n got %q\nwant %q", rec.calls[0], want)
	}
	assertNoInsecureOptions(t, rec.calls[0])
}

// assertNoInsecureOptions fails when argv weakens host-key checking or
// opens a forwarding, and when it misses an option that switches one off.
// ClearAllForwardings does not cover X11, so ForwardX11 needs its own -o.
func assertNoInsecureOptions(t *testing.T, argv []string) {
	t.Helper()
	joined := strings.Join(argv, " ")
	for _, bad := range []string{"StrictHostKeyChecking=no", "UserKnownHostsFile=/dev/null",
		"StrictHostKeyChecking=accept-new", "ForwardAgent=yes", "ForwardX11=yes",
		"ForwardX11Trusted=yes"} {
		if strings.Contains(joined, bad) {
			t.Errorf("argv carries %s: %q", bad, argv)
		}
	}
	for _, need := range []string{"ForwardAgent=no", "ClearAllForwardings=yes", "ForwardX11=no",
		"ForwardX11Trusted=no"} {
		if !strings.Contains(joined, need) {
			t.Errorf("argv misses %s: %q", need, argv)
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
	want := []string{"rsync", "-rt", "--timeout=60", "--fsync", "--remove-source-files", "--max-size=1073741824", "--exclude=.*", "--include=*.parquet",
		"--exclude=*", "-e",
		"ssh '-o' 'BatchMode=yes' '-o' 'StrictHostKeyChecking=yes' '-o' 'ControlMaster=auto'" +
			" '-o' 'ControlPath=/ctl/%C' '-o' 'ControlPersist=60s' '-o' 'ConnectTimeout=15'" +
			" '-o' 'ServerAliveInterval=15' '-o' 'ServerAliveCountMax=3'" +
			" '-o' 'ForwardAgent=no' '-o' 'ClearAllForwardings=yes'" +
			" '-o' 'ForwardX11=no' '-o' 'ForwardX11Trusted=no'",
		"--", "app1:/opt/iormesh/traces/shop/ready/", local + "/"}
	if !slices.Equal(argv, want) {
		t.Errorf("argv\n got %q\nwant %q", argv, want)
	}
	assertNoInsecureOptions(t, argv)
	assertMode(t, local, 0o700)
}

func TestSSHPushArgv(t *testing.T) {
	s, rec := newTestSSH(t, "ops@app1")
	if err := s.Push(context.Background(), "/home/ops/.iormesh/staging", "/dist/iormesh-linux-amd64",
		"/dist/iormesh-linux-amd64.minisig"); err != nil {
		t.Fatal(err)
	}
	argv := rec.calls[0]
	want := []string{"rsync", "-ct", "--timeout=60", "-e",
		"ssh '-o' 'BatchMode=yes' '-o' 'StrictHostKeyChecking=yes' '-o' 'ControlMaster=auto'" +
			" '-o' 'ControlPath=/ctl/%C' '-o' 'ControlPersist=60s' '-o' 'ConnectTimeout=15'" +
			" '-o' 'ServerAliveInterval=15' '-o' 'ServerAliveCountMax=3'" +
			" '-o' 'ForwardAgent=no' '-o' 'ClearAllForwardings=yes'" +
			" '-o' 'ForwardX11=no' '-o' 'ForwardX11Trusted=no'",
		"--", "/dist/iormesh-linux-amd64", "/dist/iormesh-linux-amd64.minisig", "ops@app1:/home/ops/.iormesh/staging/"}
	if !slices.Equal(argv, want) {
		t.Errorf("argv\n got %q\nwant %q", argv, want)
	}
	assertNoInsecureOptions(t, argv)
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
	for _, dir := range []string{"-e", "../x", "a/../../b", "a b", "a;b", "$(id)", "a:b", ""} {
		if err := s.Push(ctx, dir, "/dist/f"); !errors.Is(err, ErrTransport) {
			t.Errorf("Push(%q) = %v, want ErrTransport", dir, err)
		}
	}
	if err := s.Push(ctx, ".iormesh/staging"); !errors.Is(err, ErrTransport) {
		t.Errorf("Push() with no file = %v, want ErrTransport", err)
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

// TestRunLocalDeadline: a command still running at the context's
// deadline is killed, runLocal returns soon after, and the error is the
// deadline's, with what the command had said on stderr.
func TestRunLocalDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := runLocal(ctx, "sh", "-c", "echo 'Timeout, server not responding.' >&2; exec sleep 30")
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("runLocal returned after %s", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "server not responding") {
		t.Errorf("err = %v, want the deadline with stderr", err)
	}
}

// TestRunLocalCutsStderrOnARune: the stderrLimit cut never splits a
// multi-byte rune, which would leave an invalid tail in the error and
// get the whole message quoted when it is printed (termsafe.String).
func TestRunLocalCutsStderrOnARune(t *testing.T) {
	// stderrLimit-1 a's, then é (2 bytes) straddling the limit.
	script := fmt.Sprintf("head -c %d /dev/zero | tr '\\0' a >&2; printf 'é' >&2; exit 1", stderrLimit-1)
	_, err := runLocal(context.Background(), "sh", "-c", script)
	if err == nil {
		t.Fatal("no error")
	}
	if !utf8.ValidString(err.Error()) {
		t.Errorf("error is not valid UTF-8: %q", err.Error()[len(err.Error())-8:])
	}
	if !strings.HasSuffix(err.Error(), strings.Repeat("a", 8)+"…") {
		t.Errorf("error tail = %q, want the a's then …", err.Error()[len(err.Error())-12:])
	}
}

// localRsync is an execFunc that runs the real rsync, minus the remote
// shell: -e and its value are dropped and "app1:" is cut off the source,
// so rsync copies between two local directories with exactly the flags
// Fetch chose.
func localRsync(t *testing.T) execFunc {
	t.Helper()
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not installed")
	}
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		var local []string
		for i := 0; i < len(args); i++ {
			switch {
			case args[i] == "-e":
				i++
			case strings.HasPrefix(args[i], "app1:"):
				local = append(local, strings.TrimPrefix(args[i], "app1:"))
			default:
				local = append(local, args[i])
			}
		}
		return runLocal(ctx, name, local...)
	}
}

// TestFetchDistrustsServerModes runs real rsync against a "server" dir
// that is 0755 and holds a 0666 segment, a symlink to /etc/passwd and a
// segment over the size cap: the local dir stays 0700, the symlink is not
// recreated, the segment moves, and the oversized one stays on the server
// (task gi4). maxFetchSize is lowered to 1 KiB so the oversized file is
// a small sparse file, not a GiB written to disk.
func TestFetchDistrustsServerModes(t *testing.T) {
	defer func(old int64) { maxFetchSize = old }(maxFetchSize)
	maxFetchSize = 1 << 10
	remote := filepath.Join(t.TempDir(), "ready")
	seg := filepath.Join(remote, "h-s-20261120T000000Z-000000.parquet")
	big := filepath.Join(remote, "h-s-20261120T000000Z-000001.parquet")
	mustDo(t, os.Mkdir(remote, 0o700), os.Chmod(remote, 0o755),
		os.WriteFile(seg, []byte("PAR1"), 0o600), os.Chmod(seg, 0o666),
		os.Symlink("/etc/passwd", filepath.Join(remote, "lnk")),
		os.WriteFile(big, nil, 0o600), os.Truncate(big, maxFetchSize+1))
	s, err := newSSH("app1", "/ctl", localRsync(t))
	if err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(t.TempDir(), "shop")
	if err := s.Fetch(context.Background(), remote, local); err != nil {
		t.Fatal(err)
	}
	assertMode(t, local, 0o700)
	if _, err := os.Lstat(filepath.Join(local, "lnk")); !os.IsNotExist(err) {
		t.Errorf("symlink recreated locally: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(local, filepath.Base(seg))); err != nil || string(got) != "PAR1" {
		t.Errorf("segment not fetched: %q, %v", got, err)
	}
	if _, err := os.Stat(seg); !os.IsNotExist(err) {
		t.Errorf("remote segment not removed after transfer: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(local, filepath.Base(big))); !os.IsNotExist(err) {
		t.Errorf("oversized segment fetched: %v", err)
	}
	if _, err := os.Stat(big); err != nil {
		t.Errorf("oversized segment not left on the server: %v", err)
	}
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

// TestControlDirTooLong: a base dir that leaves no room for ssh's socket
// name is refused up front, not with ssh's own obscure bind error.
func TestControlDirTooLong(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("darwin's control dir is under $HOME, not the cache dir")
	}
	t.Setenv("XDG_CACHE_HOME", "/"+strings.Repeat("c", sunPathMax))
	if _, err := ControlDir(); !errors.Is(err, ErrTransport) {
		t.Errorf("ControlDir = %v, want ErrTransport", err)
	}
}
