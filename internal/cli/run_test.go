package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"iormesh/internal/bpf"
	"iormesh/internal/correlate"
	"iormesh/internal/schema"
	"iormesh/internal/target"
	"iormesh/internal/types"
)

// fakeTracer stands in for bpf.Tracer: records come from a channel the
// test feeds, losses from a counter it sets.
type fakeTracer struct {
	records chan []any
	mu      sync.Mutex
	losses  bpf.Losses
	lossErr error
	closed  bool
	// The target_map calls a session made, and the gap AdoptExec reports.
	added, removed, adopted, ignored []uint32
	gap                              uint64
	exited                           map[uint32]bool
	// pending is what PendingAggregates hands finish, pendingErr its error.
	pending    []*types.RwAggregateEvent
	pendingErr error
	aggStopped bool
}

func newFakeTracer() *fakeTracer { return &fakeTracer{records: make(chan []any, 16)} }

func (f *fakeTracer) Records() <-chan []any { return f.records }

func (f *fakeTracer) Losses() (bpf.Losses, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.losses, f.lossErr
}

func (f *fakeTracer) AddTarget(pid uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.added = append(f.added, pid)
	return nil
}

func (f *fakeTracer) RemoveTarget(pid uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, pid)
	return nil
}

// AdoptExec adopts unless the test marked pid exited before its verdict.
func (f *fakeTracer) AdoptExec(pid uint32) (uint64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.exited[pid] {
		return 0, false, nil
	}
	f.adopted = append(f.adopted, pid)
	return f.gap, true, nil
}

func (f *fakeTracer) IgnoreExec(pid uint32, since uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ignored = append(f.ignored, pid)
	return nil
}

func (f *fakeTracer) StopAggregation() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.aggStopped = true
	return nil
}

func (f *fakeTracer) PendingAggregates() ([]*types.RwAggregateEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.aggStopped {
		return nil, errors.New("pending aggregates read before the aggregation was stopped")
	}
	return f.pending, f.pendingErr
}

// StackTrace has no stacks to give: the fake sends no stack records.
func (f *fakeTracer) StackTrace(uint32) ([]uint64, error) {
	return nil, errors.New("no stacks in the fake")
}

func (f *fakeTracer) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeTracer) setLosses(l bpf.Losses, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.losses, f.lossErr = l, err
}

// testCorrelator writes into dir/events.ndjson like run does, with a
// session header already queued.
func testCorrelator(t *testing.T, dir string) *correlate.Correlator {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, EventsFile))
	if err != nil {
		t.Fatal(err)
	}
	c := correlate.New(correlate.Config{Host: correlate.Host{ID: "h", Project: "p"},
		Sink:     schema.NewNDJSONSink(f),
		ProcInfo: func(uint32) (string, uint64) { return "x", 1 }})
	if err := c.Session(schema.Session{BootID: "b", FQDN: "h.example", Kernel: "6", Arch: "x86_64"}); err != nil {
		t.Fatal(err)
	}
	return c
}

// readEvents reads dir's events strictly, as normalize would.
func readEvents(t *testing.T, dir string) []schema.Event {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, EventsFile))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	events, err := schema.ReadNDJSON(f)
	if err != nil {
		t.Fatalf("events do not read back strictly: %v", err)
	}
	for i, e := range events {
		if e.Seq != uint64(i+1) {
			t.Fatalf("event %d has seq %d: want gap-free", i, e.Seq)
		}
	}
	return events
}

func kinds(events []schema.Event) string {
	var k []string
	for _, e := range events {
		k = append(k, e.Event)
	}
	return strings.Join(k, ",")
}

// pidSession is the session of a -pid run over pids, started.
func pidSession(t *testing.T, tr tracer, c *correlate.Correlator, pids ...uint32) (*session, runConfig) {
	t.Helper()
	cfg := runConfig{spec: target.Spec{PIDs: pids}, project: "p", duration: time.Hour}
	s := newSession(tr, c, cfg)
	if err := s.start(pids); err != nil {
		t.Fatal(err)
	}
	return s, cfg
}

// TestTraceLoopReasons: every stop condition maps to its schema end
// reason, and the failing ones also return an error.
func TestTraceLoopReasons(t *testing.T) {
	fakeProcs(t, 10)
	cases := map[string]struct {
		setup   func(*fakeTracer, context.CancelFunc)
		dur     time.Duration
		want    string
		wantErr bool
	}{
		"duration":      {func(*fakeTracer, context.CancelFunc) {}, 20 * time.Millisecond, schema.EndDuration, false},
		"stop":          {func(_ *fakeTracer, cancel context.CancelFunc) { cancel() }, time.Hour, schema.EndStop, false},
		"stream closed": {func(f *fakeTracer, _ context.CancelFunc) { close(f.records) }, time.Hour, schema.EndError, true},
		"loss read fails": {func(f *fakeTracer, _ context.CancelFunc) {
			f.setLosses(bpf.Losses{}, errors.New("map gone"))
		}, time.Hour, schema.EndError, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tr := newFakeTracer()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tc.setup(tr, cancel)
			c := testCorrelator(t, t.TempDir())
			s, cfg := pidSession(t, tr, c, 10)
			cfg.duration = tc.dur
			reason, err := traceLoop(ctx, s, cfg)
			if reason != tc.want || (err != nil) != tc.wantErr {
				t.Fatalf("traceLoop = %q, %v; want %q, error %v", reason, err, tc.want, tc.wantErr)
			}
			_ = c.Close()
		})
	}
}

// liveUntilExit makes every pid look alive until the returned function
// is called, from any goroutine.
func liveUntilExit(t *testing.T) func() {
	t.Helper()
	var mu sync.Mutex
	live := true
	origAlive, origIdentity := processAlive, processIdentity
	processAlive = func(uint32, uint64) bool { mu.Lock(); defer mu.Unlock(); return live }
	processIdentity = func(uint32) correlate.Identity { return correlate.Identity{Comm: "x", StartNS: 1} }
	t.Cleanup(func() { processAlive, processIdentity = origAlive, origIdentity })
	return func() { mu.Lock(); defer mu.Unlock(); live = false }
}

// TestRunWritesSessionDropsEnd drives the loop and finish with a fake
// tracer: records, loss growth seen on a tick, the targets exiting and
// loss that only the final read after the loop sees. The file must read
// back strictly as session, events, drops, end — in gap-free seq order.
func TestRunWritesSessionDropsEnd(t *testing.T) {
	exitAll := liveUntilExit(t)
	dir := t.TempDir()
	tr := newFakeTracer()
	c := testCorrelator(t, dir)
	tr.records <- []any{&types.FdEvent{EventType: types.ENTER_FD_EVENT, TraceId: types.SYS_ENTER_READ, Time: 1, Pid: 10, Tid: 10, Fd: 0}}
	tr.records <- []any{&types.RetEvent{EventType: types.EXIT_RET_EVENT, TraceId: types.SYS_EXIT_READ, Time: 2, Pid: 10, Tid: 10, Ret: 1}}
	tr.setLosses(bpf.Losses{Kernel: 3}, nil)
	go func() {
		time.Sleep(liveCheckEvery + liveCheckEvery/2) // one tick sees the 3 losses
		// A record after that tick: the drop must precede it, which only
		// the loop's own poll achieves (the drain polls only later).
		tr.records <- []any{&types.FdEvent{EventType: types.ENTER_FD_EVENT, TraceId: types.SYS_ENTER_READ, Time: 3, Pid: 10, Tid: 10}}
		tr.records <- []any{&types.RetEvent{EventType: types.EXIT_RET_EVENT, TraceId: types.SYS_EXIT_READ, Time: 4, Pid: 10, Tid: 10, Ret: 1}}
		exitAll()
	}()
	s, cfg := pidSession(t, tr, c, 10)
	cfg.out, cfg.duration = dir, time.Minute
	reason, err := traceLoop(context.Background(), s, cfg)
	if err != nil || reason != schema.EndTargetsExited {
		t.Fatalf("traceLoop = %q, %v", reason, err)
	}
	// The aggregation is frozen before the final drain, not only in finish.
	tr.mu.Lock()
	stopped := tr.aggStopped
	tr.mu.Unlock()
	if !stopped {
		t.Fatal("traceLoop drained the last records with the aggregation still running")
	}
	tr.setLosses(bpf.Losses{Kernel: 3, Short: 1}, nil) // only finish's poll sees this one
	var stderr bytes.Buffer
	if err := finish(cfg, tr, c, nil, reason, &stderr); err != nil {
		t.Fatal(err)
	}
	events := readEvents(t, dir)
	if got := kinds(events); got != "session,read,drop,read,drop,end" {
		t.Fatalf("event kinds %s: want session,read,drop,read,drop,end", got)
	}
	// 3 from the in-loop poll, then 1 from finish's: one drop of 4, or a
	// drop behind the second read, would mean the loop never polled.
	if a, b := *events[2].Args.Lost, *events[4].Args.Lost; a != 3 || b != 1 {
		t.Fatalf("drops lost %d then %d, want 3 then 1", a, b)
	}
	end := events[len(events)-1]
	if *end.Args.EndReason != schema.EndTargetsExited || !tr.closed {
		t.Fatalf("end %q, tracer closed %v", *end.Args.EndReason, tr.closed)
	}
	if _, err := os.Stat(filepath.Join(dir, GraphFile)); err != nil {
		t.Fatalf("graph not written: %v", err)
	}
	if !strings.Contains(stderr.String(), "stopped (targets_exited); lost records 4") {
		t.Fatalf("summary %q", stderr.String())
	}
}

// failSink rejects every event, like an NDJSON sink whose disk is full.
type failSink struct{}

func (failSink) Event(schema.Event) error { return errors.New("disk full") }
func (failSink) Close() error             { return nil }

// TestTraceLoopWriteError: a sink error stops the loop at once with end
// reason error and returns the error.
func TestTraceLoopWriteError(t *testing.T) {
	fakeProcs(t, 10)
	tr := newFakeTracer()
	c := correlate.New(correlate.Config{Host: correlate.Host{ID: "h", Project: "p"}, Sink: failSink{},
		ProcInfo: func(uint32) (string, uint64) { return "x", 1 }})
	tr.records <- []any{&types.FdEvent{EventType: types.ENTER_FD_EVENT, TraceId: types.SYS_ENTER_READ, Time: 1, Pid: 10, Tid: 10}}
	tr.records <- []any{&types.RetEvent{EventType: types.EXIT_RET_EVENT, TraceId: types.SYS_EXIT_READ, Time: 2, Pid: 10, Tid: 10, Ret: 1}}
	s, cfg := pidSession(t, tr, c, 10)
	reason, err := traceLoop(context.Background(), s, cfg)
	if reason != schema.EndError || err == nil {
		t.Fatalf("traceLoop = %q, %v; want error and a non-nil error", reason, err)
	}
}

// TestDrainQuiet: the drain ends targets_exited once quiet, still polls
// losses while it waits, and reports a signal or the duration that cut it
// short as the reason instead.
func TestDrainQuiet(t *testing.T) {
	dir := t.TempDir()
	tr := newFakeTracer()
	tr.setLosses(bpf.Losses{Kernel: 2}, nil)
	c := testCorrelator(t, dir)
	s, _ := pidSession(t, tr, c, 10)
	reason, err := drainQuiet(context.Background(), s)
	if reason != schema.EndTargetsExited || err != nil {
		t.Fatalf("drainQuiet = %q, %v; want targets_exited", reason, err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	events := readEvents(t, dir)
	if got := kinds(events); got != "session,drop" || *events[1].Args.Lost != 2 {
		t.Fatalf("kinds %s: want the drain's poll to write drop lost 2", got)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	stopped, _ := pidSession(t, newFakeTracer(), testCorrelator(t, t.TempDir()), 10)
	if reason, _ := drainQuiet(cancelled, stopped); reason != schema.EndStop {
		t.Fatalf("signal during drain: %q, want stop", reason)
	}
	expired, cancel2 := context.WithTimeout(context.Background(), 0)
	defer cancel2()
	timedOut, _ := pidSession(t, newFakeTracer(), testCorrelator(t, t.TempDir()), 10)
	if reason, _ := drainQuiet(expired, timedOut); reason != schema.EndDuration {
		t.Fatalf("duration during drain: %q, want duration", reason)
	}
}

// TestFinishLossErrorEndsWithError: a loss counter that cannot be read at
// the end makes the end reason error — the trace might hide a gap — and
// the error is returned, while the end event and graph are still written.
func TestFinishLossErrorEndsWithError(t *testing.T) {
	dir := t.TempDir()
	tr := newFakeTracer()
	tr.setLosses(bpf.Losses{}, errors.New("map gone"))
	c := testCorrelator(t, dir)
	err := finish(runConfig{out: dir}, tr, c, nil, schema.EndDuration, &bytes.Buffer{})
	if err == nil {
		t.Fatal("finish hid the loss read error")
	}
	events := readEvents(t, dir)
	if got := kinds(events); got != "session,end" {
		t.Fatalf("kinds %s, want session,end", got)
	}
	if r := *events[1].Args.EndReason; r != schema.EndError {
		t.Fatalf("end reason %q, want error", r)
	}
}

// TestFinishNoLossNoDrop: a run without loss writes no drop event at all.
func TestFinishNoLossNoDrop(t *testing.T) {
	dir := t.TempDir()
	c := testCorrelator(t, dir)
	if err := finish(runConfig{out: dir}, newFakeTracer(), c, nil, schema.EndStop, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := kinds(readEvents(t, dir)); got != "session,end" {
		t.Fatalf("kinds %s, want session,end", got)
	}
}

// TestFinishDrainsPendingAggregates: the folded calls the kernel still
// holds at the end reach the trace ahead of the end event, a single call
// as a plain event and a run of calls with args.count; an unreadable map
// ends the trace as error, after the records it did return.
func TestFinishDrainsPendingAggregates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		reason string
	}{{"read", nil, schema.EndStop}, {"unreadable", errors.New("map gone"), schema.EndError}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tr := newFakeTracer()
			tr.pending = []*types.RwAggregateEvent{
				{EventType: types.RW_AGGREGATE_EVENT, TraceId: types.SYS_ENTER_WRITE, Time: 5, EndTime: 9,
					TotalBytes: 40, LatencyNs: 3, Pid: 10, Tid: 11, Fd: 4, Count: 8},
				{EventType: types.RW_AGGREGATE_EVENT, TraceId: types.SYS_ENTER_READ, Time: 6, EndTime: 7,
					TotalBytes: 2, LatencyNs: 1, Pid: 10, Tid: 12, Fd: 5, Count: 1},
			}
			tr.pendingErr = tc.err
			c := testCorrelator(t, dir)
			err := finish(runConfig{out: dir}, tr, c, nil, schema.EndStop, &bytes.Buffer{})
			if (err != nil) != (tc.err != nil) {
				t.Fatalf("finish = %v, want error %v", err, tc.err)
			}
			events := readEvents(t, dir)
			if got := kinds(events); got != "session,write,read,end" {
				t.Fatalf("kinds %s, want session,write,read,end", got)
			}
			w, r := events[1], events[2]
			if w.Args.Count == nil || *w.Args.Count != 8 || w.Bytes != 40 || r.Args.Count != nil || r.Bytes != 2 {
				t.Fatalf("write count %v bytes %d, read count %v bytes %d", w.Args.Count, w.Bytes, r.Args.Count, r.Bytes)
			}
			if got := *events[3].Args.EndReason; got != tc.reason {
				t.Fatalf("end reason %q, want %q", got, tc.reason)
			}
		})
	}
}
