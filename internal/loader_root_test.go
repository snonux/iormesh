package internal_test

// Root tests of the M1 loader (internal/bpf). They live in ./internal, not
// in internal/bpf, so the one root test binary `mage bpfRootTest` builds
// runs them beside the socket tests (gatecmd.RootBPFTests); an external
// test package avoids the import cycle internal/bpf → internal.

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	ibpf "iormesh/internal/bpf"
	"iormesh/internal/tracepoints"
	"iormesh/internal/types"
)

// target is a bash process that opens, reads and closes probePath with
// shell builtins - so the syscalls run in bash's own PID, which M1 traces
// without process-tree following - each time a line arrives on its stdin.
type target struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	acks  *bufio.Scanner
}

const probePath = "/etc/hostname"

func startTarget(t *testing.T) *target {
	t.Helper()
	script := fmt.Sprintf(`while read -r _; do exec 3< %s; read -r -u 3 _; exec 3<&-; echo ok; done`, probePath)
	return startScript(t, "bash", "-c", script)
}

// startScript starts name with args as a target: each line written to its
// stdin is a poke it acknowledges with one line on stdout.
func startScript(t *testing.T, name string, args ...string) *target {
	t.Helper()
	cmd := exec.Command(name, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Wait() })
	return &target{cmd: cmd, stdin: stdin, acks: bufio.NewScanner(stdout)}
}

func (tg *target) pid() uint32 { return uint32(tg.cmd.Process.Pid) }

// poke makes the target open, read and close probePath once, and waits
// until it has.
func (tg *target) poke(t *testing.T) {
	t.Helper()
	if _, err := io.WriteString(tg.stdin, "go\n"); err != nil {
		t.Fatal(err)
	}
	if !tg.acks.Scan() {
		t.Fatalf("target %d did not acknowledge: %v", tg.pid(), tg.acks.Err())
	}
}

func openTracer(t *testing.T, pids ...uint32) *ibpf.Tracer {
	t.Helper()
	return openTracerWith(t, ibpf.Config{Pids: pids})
}

func requireRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root to load BPF programs (run `mage bpfRootTest`)")
	}
}

func openTracerWith(t *testing.T, cfg ibpf.Config) *ibpf.Tracer {
	t.Helper()
	requireRoot(t)
	tr, err := ibpf.Open(cfg)
	if err != nil {
		t.Fatalf("open tracer: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

// opensOf collects, for wait, the paths of open records of pid.
func opensOf(tr *ibpf.Tracer, pid uint32, wait time.Duration) []string {
	var paths []string
	deadline := time.After(wait)
	for {
		select {
		case rec, ok := <-tr.Records():
			if !ok {
				return paths
			}
			if ev, isOpen := rec.(*types.OpenEvent); isOpen && ev.Pid == pid {
				paths = append(paths, types.StringValue(ev.Filename[:]))
			}
		case <-deadline:
			return paths
		}
	}
}

// TestLoaderDeliversRecords: the default probe set, attached by Open, turns
// a target's open of probePath into a decoded *types.OpenEvent, and the loss
// counters read without error and show no loss for this light load.
func TestLoaderDeliversRecords(t *testing.T) {
	tg := startTarget(t)
	tr := openTracer(t, tg.pid())
	tg.poke(t)
	if paths := opensOf(tr, tg.pid(), 2*time.Second); !contains(paths, probePath) {
		t.Fatalf("no open of %s from pid %d; opens seen: %v", probePath, tg.pid(), paths)
	}
	l, err := tr.Losses()
	if err != nil {
		t.Fatalf("losses: %v", err)
	}
	if l.Total() != 0 {
		t.Fatalf("losses on a two-record load: %+v", l)
	}
	if s := tr.Skipped(); len(s) != 0 {
		t.Logf("tracepoints missing on this kernel: %v", s)
	}
}

// TestLoaderAttachesEveryFamily: every event family (M2 step 7) loads and
// attaches on this kernel - the opt-in ones' programs pass the verifier
// too - and the signal family's raw signal_generate reports a signal sent
// to a target from outside the trace (the test process), attributed to the
// target.
func TestLoaderAttachesEveryFamily(t *testing.T) {
	tg := startTarget(t)
	tr := openTracerWith(t, ibpf.Config{Pids: []uint32{tg.pid()},
		Probes: ibpf.ProbesFor(tracepoints.AllFamilies())})
	if s := tr.Skipped(); len(s) != 0 {
		t.Fatalf("probes skipped with every family: %v", s)
	}
	// SIGWINCH: bash ignores it, so the target lives on; the record is
	// emitted whatever the result.
	if err := syscall.Kill(int(tg.pid()), syscall.SIGWINCH); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case rec, ok := <-tr.Records():
			if !ok {
				t.Fatal("records closed before the signal record")
			}
			ev, isSig := rec.(*types.SignalEvent)
			if isSig && ev.TargetPid == tg.pid() && ev.Sig == int32(syscall.SIGWINCH) &&
				ev.Scope&types.SIGNAL_SCOPE_TARGET != 0 && ev.Pid == uint32(os.Getpid()) {
				return
			}
		case <-deadline:
			t.Fatalf("no signal_generate record for SIGWINCH to pid %d", tg.pid())
		}
	}
}

// TestLoaderTargetsOnlyTheMap: a process outside target_map yields nothing;
// after AddTarget it does; after RemoveTarget it stops; after a second
// AddTarget it is traced again. Each negative phase is followed by a
// positive one, so a dead pipeline cannot pass it.
func TestLoaderTargetsOnlyTheMap(t *testing.T) {
	tg := startTarget(t)
	tr := openTracer(t)
	tg.poke(t)
	if paths := opensOf(tr, tg.pid(), 500*time.Millisecond); len(paths) != 0 {
		t.Fatalf("untargeted pid %d traced: %v", tg.pid(), paths)
	}
	if err := tr.AddTarget(tg.pid()); err != nil {
		t.Fatal(err)
	}
	tg.poke(t)
	if paths := opensOf(tr, tg.pid(), 2*time.Second); !contains(paths, probePath) {
		t.Fatalf("after AddTarget: no open of %s; got %v", probePath, paths)
	}
	if err := tr.RemoveTarget(tg.pid()); err != nil {
		t.Fatal(err)
	}
	tg.poke(t)
	if paths := opensOf(tr, tg.pid(), 500*time.Millisecond); len(paths) != 0 {
		t.Fatalf("after RemoveTarget pid %d still traced: %v", tg.pid(), paths)
	}
	if err := tr.AddTarget(tg.pid()); err != nil {
		t.Fatal(err)
	}
	tg.poke(t)
	if paths := opensOf(tr, tg.pid(), 2*time.Second); !contains(paths, probePath) {
		t.Fatalf("after re-adding: no open of %s; got %v", probePath, paths)
	}
}

// TestLoaderCloseEndsRecords: Close closes Records even with nobody
// reading and both channels full (Buffer 1, many records unread), a second
// Close is harmless, and the map methods refuse to touch the freed module.
func TestLoaderCloseEndsRecords(t *testing.T) {
	tg := startTarget(t)
	tr := openTracerWith(t, ibpf.Config{Pids: []uint32{tg.pid()}, Buffer: 1})
	for range 20 {
		tg.poke(t)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, err := tr.Losses(); err == nil {
		t.Fatal("Losses after Close read a freed module")
	}
	if err := tr.AddTarget(tg.pid()); err == nil {
		t.Fatal("AddTarget after Close touched a freed module")
	}
	timeout := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-tr.Records():
			if !ok {
				return
			}
		case <-timeout:
			t.Fatal("Records not closed after Close")
		}
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if strings.HasSuffix(x, want) {
			return true
		}
	}
	return false
}

// TestLoaderRefusesEmptyAttach: when every probe's tracepoint is missing
// (what an unmounted tracefs looks like too), Open fails instead of running
// a session that traces nothing.
func TestLoaderRefusesEmptyAttach(t *testing.T) {
	requireRoot(t)
	tr, err := ibpf.Open(ibpf.Config{Probes: []ibpf.Probe{{
		Program: "handle_sys_enter_openat", Category: "syscalls", Event: "sys_enter_no_such_call",
	}}})
	if err == nil {
		_ = tr.Close()
		t.Fatal("Open with only missing tracepoints succeeded")
	}
	if tr != nil {
		t.Fatal("Open returned a tracer together with its error")
	}
}
