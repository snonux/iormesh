package k8s

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Tests for the attach wait's started= times (task eh4): after a debug
// tracer replacement the answer's session StartedAt, compared with the
// new tracer container's running.startedAt, tells the predecessor's
// state.json (waited past) from the new run's own (acted on at once).

// The new tracer container's start in these tests (stRunning's), and
// started= words before and after it.
const (
	predStarted = " started=2026-11-20T23:00:00.5Z"      // the predecessor's run, an hour earlier
	newStarted  = " started=2026-11-21T00:00:03.25Z"     // the new run, after its container started
	inSkew      = " started=2026-11-20T23:59:58Z"        // exactly attachSkew before the start: still new
	pastSkew    = " started=2026-11-20T23:59:57.999999Z" // just past attachSkew before it
)

// replaced deploys iormesh-2 in place of an ended iormesh and serves a
// pod whose status has iormesh-2 running since stRunning's start.
func replaced(t *testing.T) (*fake, *Kubectl) {
	t.Helper()
	f := newFake(t)
	f.pod(t, podWith(t, tracerSpec{"iormesh", testImage, stKilled}))
	k := newTestKubectl(t, testTarget())
	if out, err := k.Deploy(context.Background(), "iormesh", "run", "-project", "shop"); err != nil || out != Replaced {
		t.Fatalf("Deploy = %v, %v", out, err)
	}
	f.pod(t, podWith(t, tracerSpec{"iormesh", testImage, stKilled}, tracerSpec{"iormesh-2", testImage, stRunning}))
	return f, k
}

// verbs is the kubectl verb of each call after the deploy's get and
// debug: "get" or "exec".
func verbs(t *testing.T, f *fake) []string {
	t.Helper()
	var out []string
	for _, c := range f.calls(t)[2:] {
		v := "?"
		for _, a := range c {
			if a == "get" || a == "exec" {
				v = a
				break
			}
		}
		out = append(out, v)
	}
	return out
}

// TestWaitAttachedOlderState: a state whose session started before the
// new container — dead, ended, or running under a reused pid — is the
// predecessor's and is waited past until the new run says running; the
// container's start costs one pod read for the whole wait, and the
// timeout's last answer names both instants.
func TestWaitAttachedOlderState(t *testing.T) {
	fastPoll(t)
	for _, old := range []string{"dead", "ended duration", "running 0a0b0c0d"} {
		f, k := replaced(t)
		f.states(t, old+predStarted, old+pastSkew, "pending", "running 1a2b3c4d"+newStarted)
		session, err := k.WaitAttached(context.Background(), "shop", time.Minute)
		if err != nil || session != "1a2b3c4d" {
			t.Fatalf("%s: WaitAttached = %q, %v", old, session, err)
		}
		if got, want := strings.Join(verbs(t, f), " "), "exec get exec exec exec"; got != want {
			t.Errorf("%s: calls %q, want %q", old, got, want)
		}
	}
	f, k := replaced(t)
	f.states(t, "dead"+predStarted)
	_, err := k.WaitAttached(context.Background(), "shop", 50*time.Millisecond)
	for _, want := range []string{"not attached after", "still the ended tracer's state (dead, started " +
		"2026-11-20T23:00:00Z, before this container started at 2026-11-21T00:00:00Z)",
		"failed before attaching", "kubectl logs checkout-7d9f --container=iormesh-2"} {
		if !errors.Is(err, ErrNotAttached) || !strings.Contains(err.Error(), want) {
			t.Errorf("timeout: err = %v, want %q", err, want)
		}
	}
}

// TestWaitAttachedNewState: a state whose session started after the new
// container, or within attachSkew before it, is the new run's own:
// running attaches, ended or dead fails at the first probe — dead too,
// which the state-change fallback alone would have waited past as the
// predecessor's first answer.
func TestWaitAttachedNewState(t *testing.T) {
	fastPoll(t)
	for _, c := range []struct{ answer, want string }{
		{"ended duration" + newStarted, "already ended (duration)"},
		{"ended" + newStarted, "already ended (no reason given)"},
		{"dead" + newStarted, "died without its end"},
		{"ended disk_limit" + inSkew, "already ended (disk_limit)"},
		{"dead" + inSkew, "died without its end"},
	} {
		f, k := replaced(t)
		f.states(t, c.answer, "running 1a2b3c4d"+newStarted)
		_, err := k.WaitAttached(context.Background(), "shop", time.Minute)
		if !errors.Is(err, ErrNotAttached) || !strings.Contains(err.Error(), c.want) ||
			!strings.Contains(err.Error(), "see kubectl logs checkout-7d9f --container=iormesh-2") {
			t.Errorf("%q: err = %v, want %q and the logs", c.answer, err, c.want)
		}
		if got := strings.Join(verbs(t, f), " "); got != "exec get" {
			t.Errorf("%q: calls %q, want one probe and its pod read", c.answer, got)
		}
	}
	// After the predecessor's state, the new run's dead fails at once
	// too, though the word did not change.
	f, k := replaced(t)
	f.states(t, "dead"+predStarted, "dead"+newStarted, "running 1a2b3c4d"+newStarted)
	if _, err := k.WaitAttached(context.Background(), "shop", time.Minute); !errors.Is(err, ErrNotAttached) ||
		!strings.Contains(err.Error(), "died without its end") {
		t.Errorf("dead after dead: err = %v", err)
	}
	for _, skew := range []string{inSkew, newStarted} {
		f, k = replaced(t)
		f.states(t, "running 1a2b3c4d"+skew)
		if session, err := k.WaitAttached(context.Background(), "shop", time.Minute); err != nil || session != "1a2b3c4d" {
			t.Errorf("running%s: WaitAttached = %q, %v", skew, session, err)
		}
	}
}

// TestWaitAttachedNoContainerStart: without the container's start — no
// status for it yet in the pod read — the started= answers fall back to
// the state-change check: the first one is waited past, a different one
// (another session's time) fails, and the pod is read again each probe
// until it gives a start.
func TestWaitAttachedNoContainerStart(t *testing.T) {
	fastPoll(t)
	// No status for iormesh-2 yet, or one that is not running (the exec
	// raced the status): either way no start to compare with.
	for _, status := range []string{"", stWaiting} {
		f := newFake(t)
		f.pod(t, podWith(t, tracerSpec{"iormesh", testImage, stKilled}))
		k := newTestKubectl(t, testTarget())
		if _, err := k.Deploy(context.Background(), "iormesh", "run"); err != nil {
			t.Fatal(err)
		}
		f.pod(t, podWith(t, tracerSpec{"iormesh", testImage, stKilled}, tracerSpec{"iormesh-2", testImage, status}))
		f.states(t, "dead"+predStarted, "dead"+predStarted, "running 1a2b3c4d"+newStarted)
		if session, err := k.WaitAttached(context.Background(), "shop", time.Minute); err != nil || session != "1a2b3c4d" {
			t.Fatalf("status %q: WaitAttached = %q, %v", status, session, err)
		}
		if got, want := strings.Join(verbs(t, f), " "), "exec get exec get exec get"; got != want {
			t.Errorf("status %q: calls %q, want %q", status, got, want)
		}
		f.states(t, "dead"+newStarted)
		if _, err := k.WaitAttached(context.Background(), "shop", time.Minute); !errors.Is(err, ErrNotAttached) ||
			!strings.Contains(err.Error(), "state changed from") {
			t.Errorf("status %q: changed time: err = %v", status, err)
		}
	}
}

// TestWaitAttachedTimelessAfterOlder: with the container's start known,
// an answer without started= (nothing to compare) falls back to the
// state-change check against the predecessor's first answer — it is
// not taken as the predecessor's for lacking a time.
func TestWaitAttachedTimelessAfterOlder(t *testing.T) {
	fastPoll(t)
	f, k := replaced(t)
	f.states(t, "dead"+predStarted, "dead", "running 1a2b3c4d"+newStarted)
	if _, err := k.WaitAttached(context.Background(), "shop", time.Minute); !errors.Is(err, ErrNotAttached) ||
		!strings.Contains(err.Error(), "state changed from") {
		t.Errorf("err = %v", err)
	}
}

// TestWaitAttachedMalformedStarted: a started= word that is no bounded
// time fails the wait at once, even after a replacement, quoted.
func TestWaitAttachedMalformedStarted(t *testing.T) {
	fastPoll(t)
	f, k := replaced(t)
	f.states(t, "dead started=yesterday", "running 1a2b3c4d"+newStarted)
	_, err := k.WaitAttached(context.Background(), "shop", time.Minute)
	if !errors.Is(err, ErrNotAttached) || !strings.Contains(err.Error(), `answered "dead started=yesterday"`) {
		t.Errorf("err = %v", err)
	}
}

// TestParseStarted: the started= word is RFC3339 (fractional seconds
// optional, any zone) or unix nanoseconds, bounded in length and to
// [2020, 2200); anything else is refused and quoted, never echoed raw.
func TestParseStarted(t *testing.T) {
	want := time.Date(2026, 11, 21, 22, 4, 5, 123456789, time.UTC)
	for _, v := range []string{"2026-11-21T22:04:05.123456789Z", "2026-11-21T23:04:05.123456789+01:00",
		"1795298645123456789"} {
		_, got, ok := splitStarted("running 0f1e2d3c started=" + v)
		if !ok || !got.Equal(want) {
			t.Errorf("%s: %v, %v; want %v", v, got, ok, want)
		}
	}
	if _, got, ok := splitStarted("dead started=2026-11-21T22:04:05Z"); !ok || !got.Equal(want.Truncate(time.Second)) {
		t.Errorf("whole seconds: %v, %v", got, ok)
	}
	for _, v := range []string{
		"", "yesterday", "2026-11-21", "2026-11-21 22:04:05Z", "2026-11-21T22:04:05", "2026-13-21T22:04:05Z",
		"2019-12-31T23:59:59.999999999Z", "2200-01-01T00:00:00Z", "9999-01-01T00:00:00Z",
		"1577836799999999999", "0", "-1795298645123456789", "99999999999999999999", "1795298645123456789x",
		"2026-11-21T22:04:05.1234567891234567891Z", "2026-11-21T22:04:05Z\x1b[2J", "\x1b]0;pwned\x07",
		"2026-11-21T22:04:05Z started=2026-11-21T22:04:05Z",
	} {
		answer := "dead started=" + v
		session, _, err := parseAttach(answer, "pod", &predecessor{logs: "logs", start: want})
		if session != "" || !errors.Is(err, ErrNotAttached) || !strings.Contains(err.Error(), "not an attach state") &&
			!strings.Contains(err.Error(), "not an RFC3339 or unix-nanosecond time") {
			t.Errorf("%q: session %q, err %v; want a refusal", answer, session, err)
			continue
		}
		if strings.ContainsAny(err.Error(), "\x1b\x07") {
			t.Errorf("%q: raw control bytes in %q", answer, err)
		}
	}
	// Only running, ended and dead carry a time.
	if _, _, err := parseAttach("pending started=2026-11-21T22:04:05Z", "pod", nil); err == nil ||
		!strings.Contains(err.Error(), "not an attach state") {
		t.Errorf("pending with a time: %v", err)
	}
	if _, _, err := parseAttach("started=2026-11-21T22:04:05Z", "pod", nil); err == nil {
		t.Error("a time alone accepted")
	}
}
