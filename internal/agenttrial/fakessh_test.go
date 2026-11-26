package agenttrial

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeSSHEnv makes the test binary act as ssh: the fake "VM" target the
// tests drive the harness against. Its value is the file each call's
// argv is appended to, one JSON array per line.
const fakeSSHEnv = "AGENTTRIAL_FAKE_SSH_LOG"

// fakeEdges is what the fake controller's `iormeshctl show edges` prints:
// the guide's chapter 7 table, a fused edge to each called service.
const fakeEdges = `FROM     TO         STATE  CONNS  UNRESOLVED  FIRST                LAST
gateway  orders     fused  1      0           2026-11-24 09:24:22  2026-11-24 09:24:24
orders   inventory  fused  2      0           2026-11-24 09:24:23  2026-11-24 09:24:24
orders   billing    fused  1      0           2026-11-24 09:24:23  2026-11-24 09:24:24
`

func TestMain(m *testing.M) {
	if log := os.Getenv(fakeSSHEnv); log != "" {
		os.Exit(fakeSSH(log, os.Args[1:], os.Stdin, os.Stdout))
	}
	os.Exit(m.Run())
}

// fakeSSH logs its argv and answers like a VM would, without running
// anything: the command is only ever compared, never executed, so a test
// can tell that whatever it holds never ran on the laptop.
func fakeSSH(log string, args []string, stdin io.Reader, stdout io.Writer) int {
	f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return 255
	}
	line, _ := json.Marshal(args)
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
	sep := slices.Index(args, "--")
	if sep < 0 || sep+1 >= len(args) {
		return 255
	}
	if slices.Contains(args[:sep], "-N") {
		time.Sleep(time.Hour) // the release tunnel: held until killed
		return 0
	}
	host, command := args[sep+1], strings.Join(args[sep+2:], " ")
	switch {
	case strings.Contains(command, "cat > "):
		_, _ = io.Copy(io.Discard, stdin)
	case strings.Contains(command, "id_ed25519.pub"):
		_, _ = fmt.Fprintln(stdout, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFake user@"+host)
	case strings.Contains(command, "show edges"):
		_, _ = fmt.Fprint(stdout, fakeEdges)
	case command == "false":
		return 1
	default:
		_, _ = fmt.Fprintf(stdout, "ran on %s: %s\n", host, command)
	}
	return 0
}

// fakeShell is a Shell whose ssh is the fake, logging to a temp file,
// with the trial's two VMs.
func fakeShell(t *testing.T) (*Shell, string) {
	t.Helper()
	log := filepath.Join(t.TempDir(), "ssh.log")
	t.Setenv(fakeSSHEnv, log)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return &Shell{SSH: exe, Hosts: []string{"iom1", "iom2"}, Timeout: 30 * time.Second}, log
}

// sshCalls reads the fake's argv log.
func sshCalls(t *testing.T, log string) [][]string {
	t.Helper()
	f, err := os.Open(log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var calls [][]string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var argv []string
		if err := json.Unmarshal(sc.Bytes(), &argv); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, argv)
	}
	return calls
}
