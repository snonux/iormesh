package k8s

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// shortBounds shortens callTimeout and streamIdle for one test.
func shortBounds(t *testing.T, call, idle time.Duration) {
	t.Helper()
	oc, oi := callTimeout, streamIdle
	callTimeout, streamIdle = call, idle
	t.Cleanup(func() { callTimeout, streamIdle = oc, oi })
}

// readyTar serves plantedReady's two segments as the fake's stream.
func readyTar(t *testing.T, f *fake) {
	t.Helper()
	var buf bytes.Buffer
	if err := WriteReady(&buf, io.Discard, plantedReady(t)); err != nil {
		t.Fatal(err)
	}
	f.tar(t, buf.Bytes())
}

// within runs do and fails the test when it takes longer than limit:
// the fake hangs for 30 s, so an unbounded call shows as slow.
func within(t *testing.T, limit time.Duration, do func() error) error {
	t.Helper()
	start := time.Now()
	err := do()
	if took := time.Since(start); took > limit {
		t.Fatalf("call took %s, want under %s (err %v)", took.Round(time.Millisecond), limit, err)
	}
	return err
}

const readyDir = "/opt/iormesh/data/traces/shop/ready"

// TestFetchStalledStream: an exec stream that sends part of a tar header
// and then goes silent (kubectl alive, no bytes) ends within the idle timeout as
// ErrStalled, not hung on the fetch lock; nothing half-written is left,
// and the lock is free for the next fetch (task pi4).
func TestFetchStalledStream(t *testing.T) {
	shortBounds(t, 5*time.Second, 300*time.Millisecond)
	f := newFake(t)
	readyTar(t, f)
	t.Setenv("FAKE_KUBECTL_HANG", "stall")
	local := t.TempDir()
	k := newTestKubectl(t, testTarget())
	err := within(t, 4*time.Second, func() error { return k.Fetch(context.Background(), readyDir, local) })
	if !errors.Is(err, ErrStalled) || !strings.Contains(err.Error(), "no data for 300ms") {
		t.Fatalf("err = %v, want ErrStalled naming the idle bound", err)
	}
	assertDir(t, local, map[string]string{})
	t.Setenv("FAKE_KUBECTL_HANG", "")
	if err := k.Fetch(context.Background(), readyDir, local); err != nil {
		t.Fatalf("fetch after the stall: %v", err)
	}
}

// TestFetchSlowStreamNotCut: a stream slower in total than the idle
// timeout but never silent that long is fetched whole — the bound is on
// silence, not on the fetch's length.
func TestFetchSlowStreamNotCut(t *testing.T) {
	shortBounds(t, 5*time.Second, time.Second)
	f := newFake(t)
	readyTar(t, f)
	t.Setenv("FAKE_KUBECTL_HANG", "trickle")
	local := t.TempDir()
	start := time.Now()
	if err := newTestKubectl(t, testTarget()).Fetch(context.Background(), readyDir, local); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took < time.Second {
		t.Fatalf("stream took %s: not slower than the idle bound, the test proves nothing", took)
	}
	assertDir(t, local, map[string]string{"h-s1-1-0.parquet": "seg0", "h-s1-1-0.methods.parquet": "methods0"})
}

// TestFetchRemoveBounded: a remove exec that hangs after a good stream
// is given up on within callTimeout; the segments that arrived stay.
func TestFetchRemoveBounded(t *testing.T) {
	shortBounds(t, 300*time.Millisecond, 5*time.Second)
	f := newFake(t)
	readyTar(t, f)
	t.Setenv("FAKE_KUBECTL_HANG", "remove")
	local := t.TempDir()
	err := within(t, 4*time.Second, func() error {
		return newTestKubectl(t, testTarget()).Fetch(context.Background(), readyDir, local)
	})
	if !errors.Is(err, ErrStalled) || !strings.Contains(err.Error(), "no answer within 300ms") {
		t.Fatalf("err = %v, want ErrStalled naming the call bound", err)
	}
	assertDir(t, local, map[string]string{"h-s1-1-0.parquet": "seg0", "h-s1-1-0.methods.parquet": "methods0"})
}

// TestGetPodBounded: Check's `kubectl get pod` that hangs is given up
// on within callTimeout.
func TestGetPodBounded(t *testing.T) {
	shortBounds(t, 300*time.Millisecond, 5*time.Second)
	f := newFake(t)
	f.pod(t, preparedPod)
	t.Setenv("FAKE_KUBECTL_HANG", "get")
	err := within(t, 4*time.Second, func() error { return newTestKubectl(t, testTarget()).Check(context.Background()) })
	if !errors.Is(err, ErrStalled) {
		t.Fatalf("err = %v, want ErrStalled", err)
	}
}

// TestDebugBounded: a `kubectl debug` that hangs is given up on within
// callTimeout, and its --custom temporary is removed all the same.
func TestDebugBounded(t *testing.T) {
	shortBounds(t, 300*time.Millisecond, 5*time.Second)
	f := newFake(t)
	f.pod(t, preparedPod)
	t.Setenv("FAKE_KUBECTL_HANG", "debug")
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	err := within(t, 4*time.Second, func() error {
		return newTestKubectl(t, testTarget()).Debug(context.Background(), "iormesh", "run", "-project", "shop")
	})
	if !errors.Is(err, ErrStalled) {
		t.Fatalf("err = %v, want ErrStalled", err)
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "iormesh-debug-*.json")); len(left) != 0 {
		t.Errorf("custom file left behind: %v", left)
	}
}

// TestCallCancelledNotStalled: a call the caller cancelled (Ctrl-C) is
// not reported as a stall: the bound did not cut it off.
func TestCallCancelledNotStalled(t *testing.T) {
	shortBounds(t, 5*time.Second, 5*time.Second)
	f := newFake(t)
	f.pod(t, preparedPod)
	t.Setenv("FAKE_KUBECTL_HANG", "get")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	err := within(t, 4*time.Second, func() error { return newTestKubectl(t, testTarget()).Check(ctx) })
	if err == nil || errors.Is(err, ErrStalled) {
		t.Fatalf("err = %v, want a failure that is not ErrStalled", err)
	}
	if _, serr := os.Stat(f.log); serr != nil {
		t.Errorf("kubectl never ran: %v", serr)
	}
}
