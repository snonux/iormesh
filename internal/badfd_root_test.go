package internal_test

// Root test of task mf4 on the real BPF object and the correlator: a call
// on fd -1 in a process and again in its fork-and-exec'd child. On Rocky
// 9.9 NSS fstat(-1)s in a JVM and in the child it spawns, and the
// correlator gave the child's call fd_gen 1 - an event the schema rejects,
// which ended the session. Listed in gatecmd.RootBPFTests; `mage
// bpfRootTest` runs it.

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"testing"

	"golang.org/x/sys/unix"

	"iormesh/internal/schema"
)

// badFDChildEnv makes the test binary a child of TestNegativeFdAcrossExec
// (TestMain dispatches it): "parent" or "exec".
const badFDChildEnv = "IORMESH_BADFD_CHILD"

// badFDChildMain is the traced pair. As "parent" it waits for a line on
// stdin, fstat(-1)s (EBADF), forks and execs itself as "exec", which
// fstat(-1)s too, and prints that child's pid once it exited.
func badFDChildMain(role string) int {
	var st unix.Stat_t
	if role == "exec" {
		if unix.Fstat(-1, &st) != unix.EBADF {
			return 1
		}
		return 0
	}
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		return 1
	}
	if unix.Fstat(-1, &st) != unix.EBADF {
		return 1
	}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), badFDChildEnv+"=exec")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "badfd exec child:", err)
		return 1
	}
	fmt.Println(cmd.Process.Pid)
	return 0
}

// TestNegativeFdAcrossExec: the exec'd child's fstat(-1) is a valid event
// without an fd - fd -1, gen 0, EBADF - and so is every other event of the
// trace (recordLog.events validates them all).
func TestNegativeFdAcrossExec(t *testing.T) {
	requireRoot(t)
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), badFDChildEnv+"=parent")
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start parent: %v", err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Wait() })
	tr := openTracer(t, uint32(cmd.Process.Pid))
	log := collectRecords(tr)
	if _, err := stdin.Write([]byte("go\n")); err != nil {
		t.Fatal(err)
	}
	var child uint32
	if _, err := fmt.Fscan(stdout, &child); err != nil {
		t.Fatalf("parent %d named no child: %v", cmd.Process.Pid, err)
	}
	var stats []schema.Event
	for _, ev := range log.events(t, child, schema.NoFD) {
		if ev.Event == schema.KindNewfstat && ev.Err == "EBADF" {
			stats = append(stats, ev)
		}
	}
	if len(stats) == 0 {
		t.Fatalf("no fstat(-1) event of the exec'd child %d", child)
	}
	for _, ev := range stats {
		if ev.FDGen != 0 {
			t.Errorf("child %d fstat(-1): fd_gen %d, want 0", child, ev.FDGen)
		}
	}
}
