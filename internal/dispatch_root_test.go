package internal_test

import (
	"io"
	"os/exec"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// TestSharedCPUNotSkipped is the regression test of task 8e4: a traced and
// an untraced read/write loop pinned to one CPU. On Linux 7.2 a
// per-syscall tracepoint program ran preemptibly behind a per-program
// recursion guard, so whenever one loop was preempted inside the read
// handler the kernel skipped the other's reads for a whole time slice -
// ~900 k skipped runs in a few seconds, the target's reads among them.
// Behind the raw_syscalls dispatchers no run can be skipped by another run
// of itself, so this load skips none. (What can still skip them is a task
// preempted inside a bpf() map syscall on that CPU: rare, and absent from
// this test's load.) Each loop runs for 3 s.
func TestSharedCPUNotSkipped(t *testing.T) {
	requireRoot(t)
	if _, err := exec.LookPath("taskset"); err != nil {
		t.Skip("needs taskset to pin both loops to one CPU")
	}
	cpu := strconv.Itoa(runtime.NumCPU() - 1)
	loop := "exec timeout 3 taskset -c " + cpu + " dd if=/dev/zero of=/dev/null bs=16 status=none"
	// The traced loop waits for a line on stdin, so it starts only after
	// the probes are attached; timeout's dd child is followed (M2).
	traced := exec.Command("bash", "-c", "read -r _; "+loop)
	stdin, err := traced.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := traced.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = traced.Wait() })
	tr := openTracer(t, uint32(traced.Process.Pid))
	untraced := exec.Command("bash", "-c", loop)
	if err := untraced.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(stdin, "go\n"); err != nil {
		t.Fatal(err)
	}
	drainUntil(tr.Records(), 5*time.Second, func() { _ = traced.Wait(); _ = untraced.Wait() })
	l, err := tr.Losses()
	if err != nil {
		t.Fatalf("losses: %v", err)
	}
	if l.Missed != 0 {
		t.Fatalf("%d program runs skipped with two read loops sharing CPU %s (want 0): %+v", l.Missed, cpu, l)
	}
}

// drainUntil consumes records while wait runs, so the ring buffer never
// fills, and returns once wait has returned or after limit.
func drainUntil(records <-chan []any, limit time.Duration, wait func()) {
	done := make(chan struct{})
	go func() { wait(); close(done) }()
	deadline := time.After(limit)
	for {
		select {
		case <-records:
		case <-done:
			return
		case <-deadline:
			return
		}
	}
}
