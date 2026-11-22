package ctl

import (
	"context"
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// runPodBounded is the pod fixture's run with a watchdog: a command
// still running after limit fails the test instead of hanging it.
func (f *podFixture) runPodBounded(t *testing.T, limit time.Duration, args ...string) int {
	t.Helper()
	done := make(chan int, 1)
	go func() { done <- f.run(args...) }()
	select {
	case code := <-done:
		return code
	case <-time.After(limit):
		t.Fatalf("%v did not return within %s", args, limit)
		return 0
	}
}

// TestPodDeployHung: a pod entry whose kubectl hangs is given up on
// after the per-host bound, as a server is (task pi4).
func TestPodDeployHung(t *testing.T) {
	f := newPodFixture(t, "")
	f.pod(t, podObject("checkout-0", uid(1), false))
	f.file(t, "hang", nil)
	f.e.hostTimeout = 200 * time.Millisecond
	if code := f.runPodBounded(t, 10*time.Second, "deploy", "kind/shop/checkout-0"); code != exitError {
		t.Fatalf("exit %d: %s", code, f.stderr)
	}
	if !strings.Contains(f.stderr.String(), "gave up on the host after 200ms") ||
		!strings.Contains(f.stderr.String(), "deploy failed on 1 pod targets") {
		t.Errorf("stderr: %s", f.stderr)
	}
}

// TestPodRmHung: rm of a pod entry whose last collect hangs returns
// after the per-host bound and leaves the registry alone.
func TestPodRmHung(t *testing.T) {
	f := newPodFixture(t, "")
	f.pod(t, podObject("checkout-0", uid(1), true))
	f.file(t, "hang", nil)
	f.e.hostTimeout = 200 * time.Millisecond
	if code := f.runPodBounded(t, 10*time.Second, "rm", "kind/shop/checkout-0"); code != exitError {
		t.Fatalf("exit %d: %s", code, f.stderr)
	}
	if !strings.Contains(f.stderr.String(), "gave up on the host after 200ms") ||
		!strings.Contains(f.stderr.String(), "registry unchanged") {
		t.Errorf("stderr: %s", f.stderr)
	}
	assertPodEntries(t, f.registry, 2)
}

// TestCollectInterrupted: a root context cancelled by a signal (Main's,
// interruptContext) ends a hung collect at once, and the command says
// it was interrupted, naming the signal — not a host failure's message.
func TestCollectInterrupted(t *testing.T) {
	f := newPodFixture(t, "")
	f.pod(t, podObject("checkout-0", uid(1), true))
	f.file(t, "hang", nil)
	ctx, cancel := context.WithCancelCause(context.Background())
	f.e.ctx = ctx
	time.AfterFunc(200*time.Millisecond, func() { cancel(errors.New("interrupt signal received")) })
	if code := f.runPodBounded(t, 10*time.Second, "collect", "kind/shop/checkout-0"); code != exitError {
		t.Fatalf("exit %d: %s", code, f.stderr)
	}
	if !strings.Contains(f.stderr.String(), "iormeshctl: interrupted (interrupt signal received)") ||
		strings.Contains(f.stderr.String(), "gave up on the host") {
		t.Errorf("stderr: %s", f.stderr)
	}
}

// TestInterruptContext: SIGINT and SIGTERM cancel Main's root context
// with the signal as its cause, instead of killing the process.
func TestInterruptContext(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		ctx, stop := interruptContext()
		if err := syscall.Kill(os.Getpid(), sig); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
			t.Fatalf("%v did not cancel the root context", sig)
		}
		if cause := context.Cause(ctx); cause == nil || !strings.Contains(cause.Error(), sig.String()) {
			t.Errorf("%v: cause %v", sig, cause)
		}
		stop()
	}
}
