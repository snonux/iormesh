package k8s

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// fastPoll shortens the pause between probes for the test.
func fastPoll(t *testing.T) {
	t.Helper()
	orig := attachPoll
	attachPoll = 10 * time.Millisecond
	t.Cleanup(func() { attachPoll = orig })
}

// TestWaitAttached: the wait probes `iormesh ready -project P -state` in
// the tracer container until it answers running, and returns the session
// id: two pending answers, then running, are three execs and no get.
func TestWaitAttached(t *testing.T) {
	fastPoll(t)
	f := newFake(t)
	f.pod(t, withTracer(false, true, testImage))
	f.states(t, "pending", "pending", "running 1a2b3c4d")
	session, err := newTestKubectl(t, testTarget()).WaitAttached(context.Background(), "shop", time.Minute)
	if err != nil || session != "1a2b3c4d" {
		t.Fatalf("WaitAttached = %q, %v", session, err)
	}
	calls := f.calls(t)
	want := []string{"--context=kind-iormesh", "--namespace=shop", "exec", "checkout-7d9f", "--container=iormesh",
		"--", "iormesh", "ready", "-project", "shop", "-state"}
	if len(calls) != 3 || !slices.Equal(calls[0], want) || !slices.Equal(calls[2], want) {
		t.Errorf("calls = %q, want three of %q", calls, want)
	}
}

// TestWaitAttachedTimeout: a tracer that never attaches fails the wait
// with ErrNotAttached once the timeout passed, naming the last answer
// and where to look; it does not fail early.
func TestWaitAttachedTimeout(t *testing.T) {
	fastPoll(t)
	f := newFake(t)
	f.pod(t, withTracer(false, true, testImage))
	f.states(t, "pending")
	start := time.Now()
	_, err := newTestKubectl(t, testTarget()).WaitAttached(context.Background(), "shop", 150*time.Millisecond)
	if !errors.Is(err, ErrNotAttached) || !strings.Contains(err.Error(), "not attached after 150ms") ||
		!strings.Contains(err.Error(), "probes not attached yet") || !strings.Contains(err.Error(), "kubectl logs") {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d < 150*time.Millisecond {
		t.Errorf("gave up after %s, before the timeout", d)
	}
	if n := len(f.calls(t)); n < 2 {
		t.Errorf("%d probes, want it to retry", n)
	}
}

// TestWaitAttachedContainerDown: an exec kubectl itself fails is read
// against the pod: a tracer container still coming up (waiting) is
// retried until the timeout, one that exited fails at once.
func TestWaitAttachedContainerDown(t *testing.T) {
	fastPoll(t)
	f := newFake(t)
	t.Setenv("FAKE_KUBECTL_EXEC_DOWN", "1")
	f.pod(t, strings.Replace(withTracer(false, true, testImage), `{"running":{"startedAt":"2026-11-21T00:00:00Z"}}`,
		`{"waiting":{"reason":"ContainerCreating"}}`, 1))
	_, err := newTestKubectl(t, testTarget()).WaitAttached(context.Background(), "shop", 100*time.Millisecond)
	if !errors.Is(err, ErrNotAttached) || !strings.Contains(err.Error(), "container not found") {
		t.Fatalf("still creating: err = %v", err)
	}
	f = newFake(t)
	t.Setenv("FAKE_KUBECTL_EXEC_DOWN", "1")
	f.pod(t, strings.Replace(withTracer(false, false, testImage), `"exitCode":0`, `"exitCode":1,"reason":"Error"`, 1))
	_, err = newTestKubectl(t, testTarget()).WaitAttached(context.Background(), "shop", time.Minute)
	if !errors.Is(err, ErrNotAttached) || !strings.Contains(err.Error(), "terminated (Error, exit code 1)") {
		t.Fatalf("exited: err = %v", err)
	}
	if calls := f.calls(t); len(calls) != 2 || calls[1][2] != "get" {
		t.Errorf("exited: calls = %q, want one exec and one get", calls)
	}
}

// TestWaitAttachedEnded: a session that ended or died before the wait
// saw it running fails at once, as does an answer that is no state; a
// bad project never reaches kubectl.
func TestWaitAttachedEnded(t *testing.T) {
	fastPoll(t)
	for answer, want := range map[string]string{
		"ended disk_limit": "already ended (disk_limit)",
		"dead":             "died without its end",
		"running":          "no session id",
		"hello":            `answered "hello"`,
	} {
		f := newFake(t)
		f.states(t, answer)
		_, err := newTestKubectl(t, testTarget()).WaitAttached(context.Background(), "shop", time.Minute)
		if !errors.Is(err, ErrNotAttached) || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", answer, err, want)
		}
		if n := len(f.calls(t)); n != 1 {
			t.Errorf("%q: %d calls, want one probe", answer, n)
		}
	}
	f := newFake(t)
	if _, err := newTestKubectl(t, testTarget()).WaitAttached(context.Background(), "-x", time.Minute); !errors.Is(err, ErrName) {
		t.Errorf("bad project: %v", err)
	}
	if calls := f.calls(t); len(calls) != 0 {
		t.Errorf("bad project ran kubectl: %q", calls)
	}
}
