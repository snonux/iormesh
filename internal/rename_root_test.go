package internal_test

// Root test of task 0g4 on the real BPF object and the correlator: a
// process that renames itself with prctl(PR_SET_NAME), and one of its
// threads that does the same. exec.c's task_rename program reports both;
// the correlator renames the process for the leader's rename only.
// Listed in gatecmd.RootBPFTests; `mage bpfRootTest` runs it.

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"iormesh/internal/correlate"
	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// renameChildEnv makes the test binary the child of TestRenameReflected
// (TestMain dispatches it).
const renameChildEnv = "IORMESH_RENAME_CHILD"

// The names the child gives its main thread and its worker thread: 15
// bytes at most, the kernel's comm limit.
const (
	renameLeaderComm = "renamed-0g4"
	renameWorkerComm = "worker-0g4"
)

// init locks the child's main goroutine to the main thread, whose tid is
// the pid: only an init function runs there for certain (exit_root_test.go
// says why), and prctl(PR_SET_NAME) renames the calling thread.
func init() {
	if os.Getenv(renameChildEnv) != "" {
		runtime.LockOSThread()
	}
}

// renameChildMain waits for a line on stdin, fstat(-1)s, names a fresh
// worker thread renameWorkerComm, names the main thread renameLeaderComm,
// fstat(-1)s again and prints the worker's tid.
func renameChildMain() int {
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		return 1
	}
	if unix.Gettid() != unix.Getpid() {
		fmt.Fprintln(os.Stderr, "rename child: main goroutine not on the main thread")
		return 1
	}
	var st unix.Stat_t
	_ = unix.Fstat(-1, &st)
	worker := make(chan int)
	go func() {
		// Returning still locked ends the thread; its rename is done.
		runtime.LockOSThread()
		if err := setThreadName(renameWorkerComm); err != nil {
			worker <- -1
			return
		}
		worker <- unix.Gettid()
	}()
	tid := <-worker
	if tid < 0 {
		return 1
	}
	if err := setThreadName(renameLeaderComm); err != nil {
		fmt.Fprintln(os.Stderr, "rename child: prctl:", err)
		return 1
	}
	_ = unix.Fstat(-1, &st)
	fmt.Println(tid)
	return 0
}

// setThreadName names the calling thread with prctl(PR_SET_NAME), the call
// tokio, JVM and Bun worker pools make (pthread_setname_np on the own
// thread is the same call).
func setThreadName(name string) error {
	p, err := unix.BytePtrFromString(name)
	if err != nil {
		return err
	}
	err = unix.Prctl(unix.PR_SET_NAME, uintptr(unsafe.Pointer(p)), 0, 0, 0)
	runtime.KeepAlive(p)
	return err
}

// TestRenameReflected: the task_rename program reports both renames, the
// worker's under its own tid; the child's fstat(-1) before the renames
// carries its old comm and the one after the leader's new name, never the
// worker's - an event's comm is the process's name.
func TestRenameReflected(t *testing.T) {
	requireRoot(t)
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), renameChildEnv+"=1")
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
	log := collectRecords(tr)
	// The child's name before any rename, read as a session reads it when
	// it takes the process on (correlate.Seed): a lazy /proc read at the
	// first record would already see the new name.
	comm, start := correlate.ReadProcInfo(pid)
	if _, err := stdin.Write([]byte("go\n")); err != nil {
		t.Fatal(err)
	}
	var worker uint32
	if _, err := fmt.Fscan(stdout, &worker); err != nil {
		t.Fatalf("child %d named no worker: %v", pid, err)
	}
	var stats []schema.Event
	for _, ev := range seededEvents(t, log, pid, correlate.Identity{Comm: comm, StartNS: start}) {
		if ev.Event == schema.KindNewfstat && ev.Err == "EBADF" {
			stats = append(stats, ev)
		}
	}
	checkRenameRecords(t, log.recs, pid, worker)
	if len(stats) < 2 {
		t.Fatalf("child %d: %d fstat(-1) events, want 2", pid, len(stats))
	}
	sort.Slice(stats, func(a, b int) bool { return stats[a].BootNS < stats[b].BootNS })
	before, after := stats[0], stats[len(stats)-1]
	if before.Comm == renameLeaderComm || before.Comm == renameWorkerComm {
		t.Errorf("fstat before the renames has comm %q, want the old name", before.Comm)
	}
	if after.Comm != renameLeaderComm {
		t.Errorf("fstat after the renames has comm %q, want %q", after.Comm, renameLeaderComm)
	}
}

// seededEvents lets the last records arrive, stops collecting and returns
// the correlator's events of pid, every event validated, with pid seeded
// as id before its first record.
func seededEvents(t *testing.T, log *recordLog, pid uint32, id correlate.Identity) []schema.Event {
	t.Helper()
	time.Sleep(300 * time.Millisecond)
	close(log.stop)
	<-log.done
	sink := &seqSink{}
	c := correlate.New(correlate.Config{Host: correlate.Host{ID: "h", Project: "p"}, Sink: sink})
	if err := c.Seed(pid, id); err != nil {
		t.Fatal(err)
	}
	for _, r := range log.recs {
		if err := c.Handle(r); err != nil {
			t.Fatalf("correlate %+v: %v", r, err)
		}
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close correlator: %v", err)
	}
	var out []schema.Event
	for _, e := range sink.events {
		if err := e.Validate(); err != nil {
			t.Errorf("%s event invalid: %v", e.Event, err)
		}
		if e.PID == pid {
			out = append(out, e)
		}
	}
	return out
}

// checkRenameRecords wants a task_rename record of the leader (tid = pid)
// and one of the worker thread, each with its new name.
func checkRenameRecords(t *testing.T, recs []any, pid, worker uint32) {
	t.Helper()
	want := map[uint32]string{pid: renameLeaderComm, worker: renameWorkerComm}
	for _, rec := range recs {
		r, ok := rec.(*types.TaskRenameEvent)
		if !ok || r.Pid != pid {
			continue
		}
		if name, ok := want[r.Tid]; ok && types.StringValue(r.Comm[:]) == name {
			delete(want, r.Tid)
		}
	}
	for tid, name := range want {
		t.Errorf("no task_rename record of pid %d tid %d naming %q", pid, tid, name)
	}
}
