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

// capSession is a session against a 1 MiB -disk-max-bytes on a fake
// tracer, run in the background.
type capSession struct {
	tr     *fakeTracer
	seg    string // a 2 MiB segment in ready/; removing it is collect draining the dir
	stderr *syncBuffer
	cancel context.CancelFunc // SIGTERM
	done   chan error         // runSession's result
}

// startCapSession starts a capSession lasting duration. With preload,
// seg is written before the start — a sidecar restarted after its
// previous session ended at the cap; without, the dir starts empty and
// the test writes seg itself (fillSegment).
func startCapSession(t *testing.T, duration time.Duration, preload bool) capSession {
	t.Helper()
	s := capSession{tr: newFakeTracer(), stderr: &syncBuffer{}, done: make(chan error, 1)}
	old := openTracer
	openTracer = func(bpf.Config) (tracer, error) { return s.tr, nil }
	t.Cleanup(func() { openTracer = old })
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s.seg = filepath.Join(dir, "shop", segment.ReadyDir, "old.events.parquet")
	if preload {
		fillSegment(t, s.seg)
	}
	cfg := runConfig{project: "shop", duration: duration, traceDir: dir,
		diskMaxBytes: 1 << 20, spec: target.Spec{Pattern: "iormesh-drain-test-matches-nothing-*"}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s.cancel = cancel
	go func() { s.done <- runSession(ctx, cfg, s.stderr) }()
	return s
}

// fillSegment writes 2 MiB, twice the cap, as a published segment.
func fillSegment(t *testing.T, seg string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(seg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(seg, make([]byte, 2<<20), 0o644); err != nil {
		t.Fatal(err)
	}
}

// overCapSession is a short session whose trace dir is over the cap
// before it starts, returned as the earlier tests take it.
func overCapSession(t *testing.T) (string, *syncBuffer, context.CancelFunc, chan error) {
	t.Helper()
	s := startCapSession(t, 300*time.Millisecond, true)
	return s.seg, s.stderr, s.cancel, s.done
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
// nothing collects through an exec, so it still fails at once
// (docs/milestones.md M4 "Disk watchdog": fails fast if the disk is already at
// the limit).
func TestRunHostOverCapFails(t *testing.T) {
	fakePodEnv(t, map[string]string{})
	_, _, _, done := overCapSession(t)
	if err := returned(t, done, "host"); !errors.Is(err, segment.ErrDiskLimit) {
		t.Errorf("host: run returned %v, want the disk limit", err)
	}
}

// waitStderr waits (polling, bounded) until the session's stderr holds
// want, failing if the run returns first.
func waitStderr(t *testing.T, s capSession, want string, bound time.Duration) {
	t.Helper()
	deadline := time.Now().Add(bound)
	for !strings.Contains(s.stderr.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("no %q on stderr within %v: %s", want, bound, s.stderr)
		}
		select {
		case err := <-s.done:
			t.Fatalf("run returned before %q: %v; stderr: %s", want, err, s.stderr)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// TestRunPodCapTripsMidSession: a pod session under its cap with a long
// -duration; segments outgrow the cap while it traces, so the writer's
// watchdog tick (segment.DefaultCheckEvery, 5 s) ends the session with
// disk_limit. The run then waits with the tracer closed — BPF released
// — and returns nil (a clean disk_limit end) once collect drained the
// dir (task ki4).
func TestRunPodCapTripsMidSession(t *testing.T) {
	fakePodEnv(t, inPod)
	fastDrainPoll(t)
	s := startCapSession(t, time.Hour, false)
	waitStderr(t, s, "iormesh: attached", 10*time.Second)
	fillSegment(t, s.seg)
	waitStderr(t, s, "waiting for collect to drain it", 20*time.Second)
	if !s.tr.isClosed() {
		t.Error("waiting at the cap with the tracer still open")
	}
	stillRunning(t, s.done, "mid-session cap")
	if err := os.Remove(s.seg); err != nil {
		t.Fatal(err)
	}
	if err := returned(t, s.done, "drained"); err != nil {
		t.Errorf("drained after a disk_limit end: run returned %v, want nil", err)
	}
}
