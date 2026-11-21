package internal_test

import (
	"io"
	"os/exec"
	"runtime"
	"strconv"
	"testing"
	"time"

	ibpf "iormesh/internal/bpf"
	"iormesh/internal/types"
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

// TestDispatchSeesSeveralKinds is the root half of the el9 regression test
// (task of4; TestEl9RecordLayout is the half that runs on any kernel). With
// the dispatchers reading the syscall number at the wrong offset, Rocky
// 9.9 sent nearly every call to the read handler (task lb4), so a trace of
// an open/read/close loop held one syscall kind. Here the same loop must
// yield more than one, and specifically all three enters.
func TestDispatchSeesSeveralKinds(t *testing.T) {
	tg := startTarget(t)
	tr := openTracerWith(t, ibpf.Config{Pids: []uint32{tg.pid()}, NoAggregation: true})
	for range 3 {
		tg.poke(t)
	}
	enters := []types.TraceId{types.SYS_ENTER_OPENAT, types.SYS_ENTER_READ, types.SYS_ENTER_CLOSE}
	kinds := traceKindsOf(tr.Records(), tg.pid(), func(k map[types.TraceId]int) bool {
		return k[enters[0]] > 0 && k[enters[1]] > 0 && k[enters[2]] > 0
	})
	if len(kinds) < 2 {
		t.Fatalf("pid %d's open/read/close loop traced as %d syscall kind(s): %v", tg.pid(), len(kinds), kinds)
	}
	for _, id := range enters {
		if kinds[id] == 0 {
			t.Errorf("no %v enter from pid %d; trace ids seen: %v", id, tg.pid(), kinds)
		}
	}
}

// traceKindsOf counts the records of pid by trace id until done accepts the
// counts, or for 5 s.
func traceKindsOf(records <-chan []any, pid uint32, done func(map[types.TraceId]int) bool) map[types.TraceId]int {
	type traced interface {
		GetPid() uint32
		GetTraceId() types.TraceId
	}
	kinds := map[types.TraceId]int{}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case batch, ok := <-records:
			if !ok {
				return kinds
			}
			for _, rec := range batch {
				if ev, isTraced := rec.(traced); isTraced && ev.GetPid() == pid {
					kinds[ev.GetTraceId()]++
				}
			}
			if done(kinds) {
				return kinds
			}
		case <-deadline:
			return kinds
		}
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
