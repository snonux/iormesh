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
		"ended disk_limit":  "already ended (disk_limit)",
		"ended":             "already ended (no reason given)",
		"dead":              "died without its end",
		"running":           "no session id",
		"running 1a2b3c4d!": `answered "running 1a2b3c4d!": no session id`,
		"ended ../x":        `answered "ended ../x", not an attach state`,
		"hello":             `answered "hello"`,
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

// TestParseAttach: state.json is written in the pod, so an answer is
// one iormesh writes or an error (task 5g4) — the session id deploy
// prints is 8 lowercase hex, an end reason snake_case, pending and dead
// take no words — and an odd answer is quoted, never echoed raw.
func TestParseAttach(t *testing.T) {
	session, last, err := parseAttach("running 0f1e2d3c", "pod", false)
	if session != "0f1e2d3c" || last != "" || err != nil {
		t.Errorf("running: %q, %q, %v", session, last, err)
	}
	if session, last, err = parseAttach("pending", "pod", false); session != "" || last == "" || err != nil {
		t.Errorf("pending: %q, %q, %v", session, last, err)
	}
	for _, answer := range []string{
		"running 0F1E2D3C", "running 0f1e2d3", "running 0f1e2d3c4", "running 0f1e2d3c x", "running  0f1e2d3c",
		"running 0f1e2d3c\x1b[2J", "running \x1b]0;pwned\x07", "running g0000000", "running ",
		"ended Disk_limit", "ended disk limit", "ended disk-limit", "ended \x1b[2J", "ended " + strings.Repeat("a", 33),
		"pending x", "dead x", "", "RUNNING 0f1e2d3c",
	} {
		session, _, err := parseAttach(answer, "pod", false)
		if session != "" || !errors.Is(err, ErrNotAttached) {
			t.Errorf("%q: session %q, err %v; want a refusal", answer, session, err)
			continue
		}
		// The refusal is the odd-answer one, not a reading of the
		// answer as an ended or dead session.
		if !strings.Contains(err.Error(), "not an attach state") && !strings.Contains(err.Error(), "no session id") {
			t.Errorf("%q: err %v, want it refused as no attach state", answer, err)
		}
		if strings.ContainsAny(err.Error(), "\x1b\x07") {
			t.Errorf("%q: raw control bytes in %q", answer, err)
		}
	}
	for _, reason := range []string{"disk_limit", "signal", "duration", strings.Repeat("a", 32)} {
		if _, _, err := parseAttach("ended "+reason, "pod", false); !strings.Contains(err.Error(), "already ended ("+reason+")") {
			t.Errorf("ended %s: %v", reason, err)
		}
	}
}

// TestWaitAttachedProbeFails: `iormesh ready -state` failing in the
// running tracer container (a remote exit status) fails the wait at
// once — no wait changes it — with no pod get (task 5g4).
func TestWaitAttachedProbeFails(t *testing.T) {
	fastPoll(t)
	f := newFake(t)
	f.states(t, "pending")
	t.Setenv("FAKE_KUBECTL_EXIT", "2")
	t.Setenv("FAKE_KUBECTL_REMOTE", "2")
	_, err := newTestKubectl(t, testTarget()).WaitAttached(context.Background(), "shop", time.Minute)
	if !errors.Is(err, ErrNotAttached) || !strings.Contains(err.Error(), "failed in the tracer container") {
		t.Fatalf("err = %v", err)
	}
	if calls := f.calls(t); len(calls) != 1 {
		t.Errorf("calls = %q, want the one exec", calls)
	}
}

// TestWaitAttachedInterrupted: a ctx cancelled during the wait (Ctrl-C)
// ends it with an error saying so and wrapping context.Canceled, not
// the timeout's message (task 5g4).
func TestWaitAttachedInterrupted(t *testing.T) {
	fastPoll(t)
	f := newFake(t)
	f.states(t, "pending")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	_, err := newTestKubectl(t, testTarget()).WaitAttached(ctx, "shop", time.Minute)
	if !errors.Is(err, ErrNotAttached) || !errors.Is(err, context.Canceled) ||
		!strings.Contains(err.Error(), "interrupted") || !strings.Contains(err.Error(), "probes not attached yet") ||
		strings.Contains(err.Error(), "not attached after") {
		t.Fatalf("err = %v", err)
	}
	if n := len(f.calls(t)); n < 1 {
		t.Errorf("%d probes", n)
	}
}

// TestWaitAttachedSlowProbe: a probe in flight when the wait's timeout
// passes still gets its answer — it runs under its own bound — so a
// slow exec that answers running attaches (task 5g4); a probe over its
// own bound is cut off and named in the timeout message.
func TestWaitAttachedSlowProbe(t *testing.T) {
	fastPoll(t)
	f := newFake(t)
	f.pod(t, withTracer(false, true, testImage))
	f.states(t, "running 1a2b3c4d")
	t.Setenv("FAKE_KUBECTL_EXEC_SLEEP", "0.3")
	session, err := newTestKubectl(t, testTarget()).WaitAttached(context.Background(), "shop", 50*time.Millisecond)
	if err != nil || session != "1a2b3c4d" {
		t.Fatalf("slow probe: %q, %v", session, err)
	}
	orig := attachProbeTimeout
	attachProbeTimeout = 50 * time.Millisecond
	t.Cleanup(func() { attachProbeTimeout = orig })
	t.Setenv("FAKE_KUBECTL_EXEC_SLEEP", "5")
	start := time.Now()
	_, err = newTestKubectl(t, testTarget()).WaitAttached(context.Background(), "shop", 10*time.Millisecond)
	if !errors.Is(err, ErrNotAttached) || !strings.Contains(err.Error(), "no answer within 50ms") ||
		!strings.Contains(err.Error(), "not attached after 10ms") {
		t.Fatalf("probe over its bound: err = %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("probe over its bound took %s", d)
	}
}
