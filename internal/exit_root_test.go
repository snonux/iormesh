package internal_test

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"iormesh/internal/types"
)

// exitChildEnv makes the test binary the child of TestProcessExitGroupDead
// (TestMain dispatches it).
const exitChildEnv = "IORMESH_EXIT_CHILD"

// init locks the child's main goroutine to the main thread. Only an init
// function runs on the main thread for certain: by the time TestMain calls
// exitChildMain the runtime may have moved the main goroutine to another
// thread, and the thread the child then lets exit could be the main thread,
// whose tid is the pid (nf4 review).
func init() {
	if os.Getenv(exitChildEnv) != "" {
		runtime.LockOSThread()
	}
}

// exitChildMain is a multithreaded process whose threads exit one by one:
// on the first line on stdin a goroutine locks itself to a fresh OS thread,
// prints that thread's tid and returns still locked, which makes the Go
// runtime end the thread while the process lives on; on the second line (or
// EOF) the process exits. The main goroutine is locked to the main thread
// (init), so the exiting goroutine never runs there (a locked main thread is
// parked, not ended).
func exitChildMain() int {
	in := bufio.NewReader(os.Stdin)
	if _, err := in.ReadString('\n'); err != nil {
		return 1
	}
	done := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		fmt.Println(unix.Gettid())
		close(done)
	}()
	<-done
	_, _ = in.ReadString('\n')
	return 0
}

const (
	// exitWait bounds how long exitsOf waits for the record it is after.
	exitWait = 5 * time.Second
	// exitQuiet is how long exitsOf keeps collecting once it has that
	// record: a duplicate or a stray record of pid arrives within it, as
	// the kernel writes all of a process's exit records before the parent's
	// wait returns.
	exitQuiet = 200 * time.Millisecond
)

// exitsOf collects the process exit records of pid until done accepts
// them, then for exitQuiet more, or until exitWait has passed.
func exitsOf(records <-chan []any, pid uint32, done func([]*types.ProcessExitEvent) bool) []*types.ProcessExitEvent {
	var exits []*types.ProcessExitEvent
	deadline := time.After(exitWait)
	var quiet <-chan time.Time
	for {
		select {
		case batch, ok := <-records:
			if !ok {
				return exits
			}
			for _, rec := range batch {
				if ev, isExit := rec.(*types.ProcessExitEvent); isExit && ev.Pid == pid {
					cp := *ev
					exits = append(exits, &cp)
				}
			}
			if quiet == nil && done(exits) {
				quiet = time.After(exitQuiet)
			}
		case <-quiet:
			return exits
		case <-deadline:
			return exits
		}
	}
}

// groupDeadCount counts the exit records with group_dead 1.
func groupDeadCount(exits []*types.ProcessExitEvent) int {
	dead := 0
	for _, ev := range exits {
		if ev.GroupDead == 1 {
			dead++
		}
	}
	return dead
}

// TestProcessExitGroupDead (task nf4): a traced process's thread that exits
// while its siblings live yields an exit record with group_dead 0 and leaves
// the process in target_map; the process's own exit yields a group_dead
// record and removes it. On a kernel whose sched_process_exit carries
// group_dead (upstream 6.16+) that is the field, set on exactly one record;
// on one without (RHEL 9's 5.14) it is ior_exit_group_dead's
// task->signal->live fallback (exec.c), which a wrong answer turns into an
// untraced live process or a target_map entry outliving its process.
func TestProcessExitGroupDead(t *testing.T) {
	requireRoot(t)
	hasField := exitHasGroupDead(t)
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), exitChildEnv+"=1")
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
		t.Fatalf("start child: %v", err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Wait() })
	pid := uint32(cmd.Process.Pid)
	tr := openTracer(t, pid)

	if _, err := stdin.Write([]byte("go\n")); err != nil {
		t.Fatal(err)
	}
	var tid uint32
	if _, err := fmt.Fscan(stdout, &tid); err != nil || tid == pid {
		t.Fatalf("child %d: thread tid %d (%v)", pid, tid, err)
	}
	exits := exitsOf(tr.Records(), pid, func(e []*types.ProcessExitEvent) bool { return len(e) > 0 })
	if len(exits) != 1 || exits[0].Tid != tid || exits[0].GroupDead != 0 {
		t.Fatalf("thread %d's exit: want one record with group_dead 0, got %s", tid, describeExits(exits))
	}
	if traced, err := tr.Targeted(pid); err != nil || !traced {
		t.Fatalf("pid %d left target_map after a thread exit (%v, %v)", pid, traced, err)
	}

	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("child: %v", err)
	}
	exits = exitsOf(tr.Records(), pid, func(e []*types.ProcessExitEvent) bool { return groupDeadCount(e) > 0 })
	t.Logf("process %d's exit records (tid:group_dead): %s; sched_process_exit has group_dead: %v",
		pid, describeExits(exits), hasField)
	switch dead := groupDeadCount(exits); {
	case dead == 0:
		t.Fatalf("process %d's exit: no group_dead record in %s", pid, describeExits(exits))
	case hasField && dead != 1:
		// The kernel's own field is set by the one thread whose decrement
		// of signal->live reached 0.
		t.Fatalf("process %d's exit: %d group_dead records from the tracepoint's field, want 1: %s",
			pid, dead, describeExits(exits))
	}
	// More than one is possible only on the fallback: two threads both saw
	// live 0, which iormesh_forget_target's single successful delete
	// absorbs.
	if traced, err := tr.Targeted(pid); err != nil || traced {
		t.Fatalf("exited pid %d still in target_map (%v, %v)", pid, traced, err)
	}
}

// exitHasGroupDead reports whether this kernel's sched_process_exit record
// carries group_dead, read from its tracefs format file.
func exitHasGroupDead(t *testing.T) bool {
	t.Helper()
	for _, root := range []string{"/sys/kernel/tracing", "/sys/kernel/debug/tracing"} {
		_, err := formatOffset(filepath.Join(root, "events", "sched", "sched_process_exit", "format"), "group_dead")
		if err == nil {
			return true
		}
		if errors.Is(err, os.ErrNotExist) {
			if _, statErr := os.Stat(filepath.Join(root, "events", "sched", "sched_process_exit")); statErr == nil {
				return false
			}
			continue
		}
		t.Fatalf("sched_process_exit format: %v", err)
	}
	t.Fatal("sched_process_exit: no tracefs format file")
	return false
}

// describeExits renders exit records as tid:group_dead pairs.
func describeExits(exits []*types.ProcessExitEvent) string {
	s := "["
	for i, ev := range exits {
		if i > 0 {
			s += " "
		}
		s += fmt.Sprintf("%d:%d", ev.Tid, ev.GroupDead)
	}
	return s + "]"
}
