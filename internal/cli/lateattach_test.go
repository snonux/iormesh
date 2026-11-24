package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"iormesh/internal/javaattach"
	"iormesh/internal/target"
	"iormesh/internal/types"
)

// seqLoader answers each pid's loads from a script: the i-th load of a
// pid gets script[pid][i], the last entry once the script ran out. Late
// attaches load from their own goroutines, so it locks.
type seqLoader struct {
	mu     sync.Mutex
	script map[uint32][]error
	loads  map[uint32]int
}

func newSeqLoader(script map[uint32][]error) *seqLoader {
	return &seqLoader{script: script, loads: map[uint32]int{}}
}

func (l *seqLoader) Load(_ context.Context, r javaattach.Request) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	i := l.loads[r.PID]
	l.loads[r.PID]++
	s := l.script[r.PID]
	if len(s) == 0 {
		return nil
	}
	return s[min(i, len(s)-1)]
}

func (l *seqLoader) count(pid uint32) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.loads[pid]
}

// pollUntil polls the session until its log holds want, failing after
// a few seconds.
func pollUntil(t *testing.T, s *session, log *bytes.Buffer, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(log.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("stderr lacks %q:\n%s", want, log.String())
		}
		if err := s.poll(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// startAgentSession starts s's Java join as `run -agent` does, logging
// into the returned buffer, and stops its late attaches at cleanup.
func startAgentSession(t *testing.T, s *session, agent, spool string) *bytes.Buffer {
	t.Helper()
	var log bytes.Buffer
	s.log = &log
	cfg := runConfig{agent: agent, spool: spool}
	if err := s.startJava(context.Background(), cfg, &log); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.stopLate() })
	return &log
}

// TestLateAttachExecMatched (task lh4): a JVM a pattern session adopts
// at its exec, after the session started, gets the agent once it is
// ready — tried again while it maps no libjvm.so yet and while it has no
// SIGQUIT handler yet — and is loaded once, the load reported.
func TestLateAttachExecMatched(t *testing.T) {
	fakeProcs(t, 71)
	agent, spool := agentFiles(t)
	if err := os.WriteFile(filepath.Join(spool, "71.ring"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	l := newSeqLoader(map[uint32][]error{
		71: {javaattach.ErrNotJVM, javaattach.ErrNoQuitHandler, nil}})
	useLoader(t, l)
	tr := newFakeTracer()
	c := testCorrelator(t, t.TempDir())
	defer func() { _ = c.Close() }()
	s := patternSession(t, tr, c)
	log := startAgentSession(t, s, agent, spool)
	err := s.handle(execRecord(71, 30, "/jdk/bin/java", "/jdk/bin/java", "-cp", "w.jar",
		"iormesh.test.NoSuchRestartApp", "/d"))
	if err != nil {
		t.Fatal(err)
	}
	pollUntil(t, s, log, "agent loaded into pid 71;")
	s.stopLate()
	if n := l.count(71); n != 3 {
		t.Errorf("%d loads of pid 71, want 3 (not a JVM yet, starting, loaded)", n)
	}
}

// TestLateAttachTracedExec (task lh4): in a -pid session, a traced
// process that execs a JVM (a target's child here) gets the agent, as
// does a restarted port owner a -listen-port session adopts.
func TestLateAttachTracedExec(t *testing.T) {
	t.Run("child exec", func(t *testing.T) {
		fakeProcs(t, 10, 11)
		agent, spool := agentFiles(t)
		l := newSeqLoader(map[uint32][]error{11: {javaattach.ErrNoQuitHandler, nil}})
		useLoader(t, l)
		tr := newFakeTracer()
		c := testCorrelator(t, t.TempDir())
		defer func() { _ = c.Close() }()
		s, _ := pidSession(t, tr, c, 10)
		log := startAgentSession(t, s, agent, spool)
		rec := execRecord(11, 40, "/jdk/bin/java", "/jdk/bin/java", "-jar", "w.jar")
		rec.EventType = types.PROCESS_ARGV_EVENT
		if err := s.handle(rec); err != nil {
			t.Fatal(err)
		}
		pollUntil(t, s, log, "agent loaded into pid 11, but it wrote no ring")
		s.stopLate()
		if n := l.count(11); n != 2 {
			t.Errorf("%d loads of pid 11, want 2", n)
		}
	})
	t.Run("port owner", func(t *testing.T) {
		f := newFakePort(t)
		app := target.Owner{Exe: "/usr/bin/java", UID: 1000}
		f.live[10], f.owners[10], f.holders = true, app, []uint32{10}
		agent, spool := agentFiles(t)
		l := newSeqLoader(nil)
		useLoader(t, l)
		tr := newFakeTracer()
		c := testCorrelator(t, t.TempDir())
		defer func() { _ = c.Close() }()
		s, _ := portSession(t, f, c, tr, time.Minute, 10)
		log := startAgentSession(t, s, agent, spool)
		f.live[10], f.holders = false, nil
		mustEnded(t, s, false, "owner gone")
		f.now = f.now.Add(2 * time.Second)
		f.live[22], f.owners[22], f.holders = true, app, []uint32{22}
		mustEnded(t, s, false, "restarted owner")
		pollUntil(t, s, log, "agent loaded into pid 22, but it wrote no ring")
	})
}

// syncProcs makes processAlive answer from a map the test may change
// while late attaches read it.
type syncProcs struct {
	mu   sync.Mutex
	live map[uint32]bool
}

func useSyncProcs(t *testing.T, pids ...uint32) *syncProcs {
	t.Helper()
	fakeProcs(t)
	p := &syncProcs{live: map[uint32]bool{}}
	for _, pid := range pids {
		p.live[pid] = true
	}
	processAlive = func(pid uint32, _ uint64) bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.live[pid]
	}
	return p
}

func (p *syncProcs) exit(pid uint32) {
	p.mu.Lock()
	p.live[pid] = false
	p.mu.Unlock()
}

// TestLateAttachEndsNamedOnce (task lh4): a late attach ends for good —
// a refusal (-XX:+DisableAttachMechanism) after one try, a JVM still
// starting at the deadline, one that exits while starting — and each is
// named exactly once; a process that never maps libjvm.so is dropped
// silently once its window passed.
func TestLateAttachEndsNamedOnce(t *testing.T) {
	procs := useSyncProcs(t, 72, 73, 74, 75)
	agent, spool := agentFiles(t)
	l := newSeqLoader(map[uint32][]error{
		72: {javaattach.ErrAttachDisabled},
		73: {javaattach.ErrNotJVM},
		74: {javaattach.ErrNoQuitHandler},
		75: {javaattach.ErrNoQuitHandler},
	})
	useLoader(t, l)
	tr := newFakeTracer()
	c := testCorrelator(t, t.TempDir())
	defer func() { _ = c.Close() }()
	s := patternSession(t, tr, c)
	log := startAgentSession(t, s, agent, spool)
	s.late.every, s.late.notJVM, s.late.deadline = 5*time.Millisecond, 50*time.Millisecond, 100*time.Millisecond
	for _, pid := range []uint32{72, 73, 74, 75} {
		s.late.exec(pid)
	}
	pollUntil(t, s, log, "pid 72: the JVM runs with")
	for l.count(75) < 2 {
		time.Sleep(time.Millisecond)
	}
	procs.exit(75)
	pollUntil(t, s, log, "pid 75: it exited before its JVM accepted the agent")
	pollUntil(t, s, log, "pid 74: its JVM did not accept the agent within 100ms")
	for deadline := time.Now().Add(5 * time.Second); s.late.busy() && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	s.stopLate()
	for _, pid := range []string{"pid 72", "pid 74", "pid 75"} {
		if n := strings.Count(log.String(), pid+":"); n != 1 {
			t.Errorf("%s named %d times, want once:\n%s", pid, n, log.String())
		}
	}
	if strings.Contains(log.String(), "pid 73") {
		t.Errorf("a process that is no JVM was named:\n%s", log.String())
	}
	if n := l.count(72); n != 1 {
		t.Errorf("%d loads of the refusing JVM, want 1", n)
	}
	if n := l.count(73); n < 2 {
		t.Errorf("%d loads of the process without libjvm.so, want tries within its window", n)
	}
}

// ctxLoader answers ErrNoQuitHandler until the session ends, as a JVM
// that never finishes starting would.
type ctxLoader struct{}

func (ctxLoader) Load(context.Context, javaattach.Request) error { return javaattach.ErrNoQuitHandler }

// TestLateAttachStopRemovesStages (task lh4): a JVM in another mount
// namespace gets a stage, reused across its tries; when the session ends
// while it is still tried, the stage reaches the Java join and is
// removed, once.
func TestLateAttachStopRemovesStages(t *testing.T) {
	fakeProcs(t, 20)
	agent, spool := agentFiles(t)
	useLoader(t, ctxLoader{})
	stages := useStages(t, agent, map[uint32]string{20: "a"})
	tr := newFakeTracer()
	c := testCorrelator(t, t.TempDir())
	defer func() { _ = c.Close() }()
	s := patternSession(t, tr, c)
	startAgentSession(t, s, agent, spool)
	s.late.every = time.Millisecond
	s.late.exec(20)
	time.Sleep(50 * time.Millisecond)
	s.stopLate()
	if len(*stages) != 1 {
		t.Fatalf("%d stages made, want 1 reused across the tries", len(*stages))
	}
	if r := (*stages)[0].removed; r != 1 {
		t.Errorf("stage removed %d times, want once", r)
	}
	s.late.exec(21) // after the stop: nothing is tried any more
	if s.late.busy() {
		t.Error("a process was scheduled after the stop")
	}
}

// gateLoader blocks every load until the current gate opens, then
// refuses it, so the test decides when the pending attaches end.
type gateLoader struct {
	mu   sync.Mutex
	gate chan struct{}
}

func (g *gateLoader) Load(ctx context.Context, _ javaattach.Request) error {
	g.mu.Lock()
	gate := g.gate
	g.mu.Unlock()
	select {
	case <-gate:
	case <-ctx.Done():
	}
	return javaattach.ErrAttachDisabled
}

// arm makes the loads from now on wait for open.
func (g *gateLoader) arm() {
	g.mu.Lock()
	g.gate = make(chan struct{})
	g.mu.Unlock()
}

// open ends the loads waiting, and lets later ones through until arm.
func (g *gateLoader) open() {
	g.mu.Lock()
	close(g.gate)
	g.mu.Unlock()
}

// TestLateAttachOverflowNamedEachTime (task lh4 review): with the
// pending queue full, an exec gets no agent and is named; once the
// queue drained and fills again, the next dropped exec is named too —
// not dropped silently because an earlier overflow was reported.
func TestLateAttachOverflowNamedEachTime(t *testing.T) {
	var pids []uint32
	for i := range uint32(lateAttachPending + 1) {
		pids = append(pids, 1000+i, 2000+i)
	}
	fakeProcs(t, pids...)
	agent, spool := agentFiles(t)
	g := &gateLoader{}
	useLoader(t, g)
	tr := newFakeTracer()
	c := testCorrelator(t, t.TempDir())
	defer func() { _ = c.Close() }()
	s := patternSession(t, tr, c)
	log := startAgentSession(t, s, agent, spool)
	for _, base := range []uint32{1000, 2000} {
		g.arm()
		for i := range uint32(lateAttachPending + 1) {
			s.late.exec(base + i)
		}
		pollUntil(t, s, log, fmt.Sprintf("pid %d gets no agent", base+lateAttachPending))
		g.open()
		for deadline := time.Now().Add(5 * time.Second); s.late.busy() && time.Now().Before(deadline); {
			time.Sleep(time.Millisecond)
		}
		if s.late.busy() {
			t.Fatal("the pending attaches did not end")
		}
	}
	s.stopLate()
	if n := strings.Count(log.String(), "gets no agent"); n != 2 {
		t.Errorf("%d overflow lines, want one per overflow:\n%s", n, log.String())
	}
}

// raceLoader counts each pid's loads and the most in flight at once.
// The first load of first — the start-time attach's — hands the record
// loop an exec record of first and answers startErr once a second load
// of first began or a grace period passed, so a late attach that
// overlaps it is seen.
type raceLoader struct {
	tr       *fakeTracer
	first    uint32
	startErr error

	mu       sync.Mutex
	loads    map[uint32]int
	inFlight map[uint32]int
	maxIn    map[uint32]int
}

func (l *raceLoader) Load(_ context.Context, r javaattach.Request) error {
	l.mu.Lock()
	l.loads[r.PID]++
	n := l.loads[r.PID]
	l.inFlight[r.PID]++
	l.maxIn[r.PID] = max(l.maxIn[r.PID], l.inFlight[r.PID])
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		l.inFlight[r.PID]--
		l.mu.Unlock()
	}()
	if r.PID != l.first || n != 1 {
		return nil
	}
	rec := execRecord(l.first, 40, "/jdk/bin/java", "/jdk/bin/java", "-jar", "w.jar")
	rec.EventType = types.PROCESS_ARGV_EVENT
	l.tr.records <- []any{rec}
	for deadline := time.Now().Add(300 * time.Millisecond); time.Now().Before(deadline); {
		l.mu.Lock()
		second := l.loads[r.PID] > 1
		l.mu.Unlock()
		if second {
			break
		}
		time.Sleep(time.Millisecond)
	}
	return l.startErr
}

func (l *raceLoader) counts(pid uint32) (loads, maxIn int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.loads[pid], l.maxIn[pid]
}

// TestLateAttachSkipsStartTimeJVM (task 7l4): a JVM the session starts
// with that execs (or whose exec record arrives) while its start-time
// load is in flight is not tried by the late attacher at the same time
// — two attaches could each send a SIGQUIT, and the second prints a
// thread dump on the app's stdout. Loaded at start, it is not loaded
// again; one the start-time attach found no JVM yet (a wrapper that
// exec'd java) is handed to the late attacher once that load ended.
func TestLateAttachSkipsStartTimeJVM(t *testing.T) {
	cases := []struct {
		name      string
		startErr  error
		wantLoads int
	}{
		{"loaded at start", nil, 1},
		{"no JVM yet at start", javaattach.ErrNotJVM, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeProcs(t, 10)
			agent, spool := agentFiles(t)
			tr := newFakeTracer()
			l := &raceLoader{tr: tr, first: 10, startErr: tc.startErr, loads: map[uint32]int{},
				inFlight: map[uint32]int{}, maxIn: map[uint32]int{}}
			useLoader(t, l)
			c := testCorrelator(t, t.TempDir())
			defer func() { _ = c.Close() }()
			s, _ := pidSession(t, tr, c, 10)
			log := startAgentSession(t, s, agent, spool)
			for deadline := time.Now().Add(5 * time.Second); s.late.busy() && time.Now().Before(deadline); {
				time.Sleep(time.Millisecond)
			}
			s.stopLate()
			loads, maxIn := l.counts(10)
			if maxIn != 1 {
				t.Errorf("%d loads of pid 10 in flight at once, want 1", maxIn)
			}
			if loads != tc.wantLoads {
				t.Errorf("%d loads of pid 10, want %d:\n%s", loads, tc.wantLoads, log.String())
			}
		})
	}
}
