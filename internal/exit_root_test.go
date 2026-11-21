package internal_test

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"iormesh/internal/types"
)

// exitChildEnv makes the test binary the child of TestProcessExitGroupDead
// (TestMain dispatches it).
const exitChildEnv = "IORMESH_EXIT_CHILD"

// exitChildMain is a multithreaded process whose threads exit one by one:
// on the first line on stdin a goroutine locks itself to a fresh OS thread,
// prints that thread's tid and returns still locked, which makes the Go
// runtime end the thread while the process lives on; on the second line (or
// EOF) the process exits. The main goroutine stays locked to the main
// thread, so the exiting goroutine never runs there (a locked main thread is
// parked, not ended).
func exitChildMain() int {
	runtime.LockOSThread()
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

// exitsOf collects, for wait, the process exit records of pid.
func exitsOf(records <-chan []any, pid uint32, wait time.Duration) []*types.ProcessExitEvent {
	var exits []*types.ProcessExitEvent
	deadline := time.After(wait)
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
		case <-deadline:
			return exits
		}
	}
}

// TestProcessExitGroupDead (task nf4): a traced process's thread that exits
// while its siblings live yields an exit record with group_dead 0 and leaves
// the process in target_map; the process's own exit yields a group_dead
// record and removes it. On a kernel whose sched_process_exit carries
// group_dead (upstream 6.16+) that is the field; on one without (RHEL 9's
// 5.14) it is ior_exit_group_dead's task->signal->live fallback (exec.c),
// which a wrong answer turns into an untraced live process or a target_map
// entry outliving its process.
func TestProcessExitGroupDead(t *testing.T) {
	requireRoot(t)
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
	exits := exitsOf(tr.Records(), pid, time.Second)
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
	exits = exitsOf(tr.Records(), pid, time.Second)
	dead := 0
	for _, ev := range exits {
		if ev.GroupDead == 1 {
			dead++
		}
	}
	if dead == 0 {
		t.Fatalf("process %d's exit: no group_dead record in %s", pid, describeExits(exits))
	}
	// More than one is the fallback's race (two threads both saw live 0),
	// which iormesh_forget_target's single successful delete absorbs.
	t.Logf("process %d's exit records (tid:group_dead): %s", pid, describeExits(exits))
	if traced, err := tr.Targeted(pid); err != nil || traced {
		t.Fatalf("exited pid %d still in target_map (%v, %v)", pid, traced, err)
	}
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
