package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"iormesh/internal/bpf"
	"iormesh/internal/segment"
	"iormesh/internal/target"
)

func (f *fakeTracer) Skipped() []bpf.Probe { return nil }

func (f *fakeTracer) PidNS() uint32 { return 0 }

// isClosed reports whether the session closed the tracer: in bpf.Tracer,
// Close detaches every program and frees every map and link.
func (f *fakeTracer) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// syncBuffer is a bytes.Buffer safe to read while the session writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// lingerSession runs a whole session — a pattern matching nothing, a
// short -duration, segments into a temp trace dir — on a fake tracer in
// the background, and returns the tracer, the session's ready/, its
// stderr, its cancel (SIGTERM) and the channel its result arrives on.
func lingerSession(t *testing.T, linger bool) (*fakeTracer, string, *syncBuffer, context.CancelFunc, chan error) {
	t.Helper()
	tr := newFakeTracer()
	old := openTracer
	openTracer = func(bpf.Config) (tracer, error) { return tr, nil }
	t.Cleanup(func() { openTracer = old })
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := runConfig{project: "shop", duration: 300 * time.Millisecond, traceDir: dir, linger: linger,
		spec: target.Spec{Pattern: "iormesh-linger-test-matches-nothing-*"}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stderr := &syncBuffer{}
	done := make(chan error, 1)
	go func() { done <- runSession(ctx, cfg, stderr) }()
	return tr, filepath.Join(dir, "shop", segment.ReadyDir), stderr, cancel, done
}

// waitEnded waits (polling, bounded) until the session closed its tracer
// and published a segment into ready/.
func waitEnded(t *testing.T, tr *fakeTracer, ready string, done chan error) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if tr.isClosed() && published(ready) {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("session returned before it ended: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatalf("session did not end: closed %v, ready %v", tr.isClosed(), published(ready))
}

// published reports whether ready/ holds a parquet segment.
func published(ready string) bool {
	m, _ := filepath.Glob(filepath.Join(ready, "*.parquet"))
	return len(m) > 0
}

// TestRunLinger: with -linger, once the session's duration ends the
// tracer is closed (no BPF held) and the final segment is in ready/, yet
// the run has not returned — a debug container stays enterable for
// `iormesh ready`; SIGTERM (ctx) then ends it, with the session's result.
func TestRunLinger(t *testing.T) {
	tr, ready, stderr, cancel, done := lingerSession(t, true)
	waitEnded(t, tr, ready, done)
	select {
	case err := <-done:
		t.Fatalf("-linger: run returned when the session ended: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	if !strings.Contains(stderr.String(), "BPF released, lingering for collect until SIGTERM") {
		t.Errorf("stderr: %s", stderr)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run after SIGTERM: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("-linger: SIGTERM did not end the run")
	}
}

// TestRunNoLinger: without -linger the run returns once the session
// ended, as before.
func TestRunNoLinger(t *testing.T) {
	tr, ready, _, _, done := lingerSession(t, false)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("run without -linger did not return")
	}
	if !tr.isClosed() || !published(ready) {
		t.Errorf("closed %v, published %v", tr.isClosed(), published(ready))
	}
}

// TestLingerFlag: -linger is run's, and start refuses it (a unit must end
// with its session).
func TestLingerFlag(t *testing.T) {
	cfg, err := parseRunFlags([]string{"-pattern", "x*", "-project", "shop", "-linger"}, &bytes.Buffer{})
	if err != nil || !cfg.linger {
		t.Fatalf("run -linger: %+v, %v", cfg.linger, err)
	}
	if cfg, err := parseRunFlags([]string{"-pattern", "x*", "-project", "shop"}, &bytes.Buffer{}); err != nil ||
		cfg.linger {
		t.Fatalf("run without -linger: %v, %v", cfg.linger, err)
	}
}
