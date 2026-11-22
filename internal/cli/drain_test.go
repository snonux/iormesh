package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"iormesh/internal/bpf"
	"iormesh/internal/segment"
	"iormesh/internal/target"
)

// overCapSession runs a session whose trace dir already holds 2 MiB of
// published segments against a 1 MiB -disk-max-bytes — a sidecar
// restarted after its previous session ended at the cap — on a fake
// tracer in the background. It returns the oversized segment (removing
// it is collect draining the dir), the stderr, the cancel (SIGTERM) and
// the channel runSession's result arrives on.
func overCapSession(t *testing.T) (string, *syncBuffer, context.CancelFunc, chan error) {
	t.Helper()
	tr := newFakeTracer()
	old := openTracer
	openTracer = func(bpf.Config) (tracer, error) { return tr, nil }
	t.Cleanup(func() { openTracer = old })
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(dir, "shop", segment.ReadyDir)
	if err := os.MkdirAll(ready, 0o755); err != nil {
		t.Fatal(err)
	}
	seg := filepath.Join(ready, "old.events.parquet")
	if err := os.WriteFile(seg, make([]byte, 2<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := runConfig{project: "shop", duration: 300 * time.Millisecond, traceDir: dir,
		diskMaxBytes: 1 << 20, spec: target.Spec{Pattern: "iormesh-drain-test-matches-nothing-*"}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stderr := &syncBuffer{}
	done := make(chan error, 1)
	go func() { done <- runSession(ctx, cfg, stderr) }()
	return seg, stderr, cancel, done
}

// fastDrainPoll makes the drain wait poll every few milliseconds.
func fastDrainPoll(t *testing.T) {
	t.Helper()
	old := drainPollEvery
	drainPollEvery = 20 * time.Millisecond
	t.Cleanup(func() { drainPollEvery = old })
}

// stillRunning fails the test if the session returns within a while.
func stillRunning(t *testing.T, done chan error, what string) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("%s: run returned while the trace dir is over its cap: %v", what, err)
	case <-time.After(500 * time.Millisecond):
	}
}

// returned waits for the session's result, failing after a bound.
func returned(t *testing.T, done chan error, what string) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: run did not return", what)
		return nil
	}
}

// TestRunPodOverCapWaitsForDrain: in a pod, a run that finds its trace
// dir over -disk-max-bytes does not exit — the kubelet would restart it
// into the same refusal, crash-looping the pod NotReady with collect
// unable to exec in (task ki4). It holds no BPF and waits until collect
// has drained the dir under the cap, then returns.
func TestRunPodOverCapWaitsForDrain(t *testing.T) {
	fakePodEnv(t, inPod)
	fastDrainPoll(t)
	seg, stderr, _, done := overCapSession(t)
	stillRunning(t, done, "pod")
	if !strings.Contains(stderr.String(), "waiting for collect to drain it") {
		t.Errorf("stderr: %s", stderr)
	}
	if err := os.Remove(seg); err != nil {
		t.Fatal(err)
	}
	if err := returned(t, done, "drained"); !errors.Is(err, segment.ErrDiskLimit) {
		t.Errorf("drained: run returned %v, want the preflight's disk limit", err)
	}
}

// TestRunPodOverCapSIGTERM: SIGTERM (the pod going) ends the wait.
func TestRunPodOverCapSIGTERM(t *testing.T) {
	fakePodEnv(t, inPod)
	fastDrainPoll(t)
	_, _, cancel, done := overCapSession(t)
	stillRunning(t, done, "pod")
	cancel()
	if err := returned(t, done, "SIGTERM"); !errors.Is(err, segment.ErrDiskLimit) {
		t.Errorf("SIGTERM: run returned %v, want the preflight's disk limit", err)
	}
}

// TestRunHostOverCapFails: outside a pod nothing restarts the run and
// nothing collects through an exec, so it still fails at once (PLAN.md:
// fails fast if the disk is already at the limit).
func TestRunHostOverCapFails(t *testing.T) {
	fakePodEnv(t, map[string]string{})
	_, _, _, done := overCapSession(t)
	if err := returned(t, done, "host"); !errors.Is(err, segment.ErrDiskLimit) {
		t.Errorf("host: run returned %v, want the disk limit", err)
	}
}
