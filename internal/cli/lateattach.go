package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"iormesh/internal/javaattach"
)

// A `run -agent` session also loads the agent into JVMs that appear
// while it runs (PLAN.md M3 "Attach", task lh4): a pattern match exec'd
// later, a traced process that execs a JVM, a restarted port owner the
// session adopts. Such a JVM is still starting at its exec, so it is
// tried again until it accepts the agent or the deadline passes.
const (
	// lateAttachEvery is the pause between two tries of one process.
	lateAttachEvery = 250 * time.Millisecond
	// lateAttachFor bounds the tries, from the process's (last) exec:
	// a JVM installs its SIGQUIT handler early in its start, well
	// within it, however busy the host.
	lateAttachFor = 30 * time.Second
	// lateNotJVMFor is how long after its exec a process may still map
	// no libjvm.so: the java launcher loads it in its first steps, so a
	// process without it after that is no JVM and dropped silently.
	lateNotJVMFor = 5 * time.Second
	// lateAttachParallel bounds the handshakes in flight: each can
	// take up to javaattach.DefaultTimeout, and the tracer has a trace
	// to run.
	lateAttachParallel = 2
	// lateAttachPending bounds the processes waiting for their attach,
	// so an exec storm cannot pile up goroutines.
	lateAttachPending = 64
)

// lateAttacher runs the late attaches of one `run -agent` session. The
// trace loop (the main goroutine) hands it processes (exec) and, at its
// tick, collects what the attaches did (collect): the lines to print and
// the stages made inside JVMs' roots, which the Java join then scans and
// removes at the session's end. Each process is tried on a goroutine of
// its own, so no attach ever blocks the loop. A nil *lateAttacher is a
// session without -agent; its methods do nothing.
type lateAttacher struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	l      agentLoader
	spool  string
	// sem holds one token per handshake in flight (lateAttachParallel).
	sem chan struct{}
	// every, deadline and notJVM are lateAttachEvery, lateAttachFor and
	// lateNotJVMFor; fields so tests need not wait that long.
	every, deadline, notJVM time.Duration

	// stMu guards st: the stages are made by the attach goroutines.
	stMu sync.Mutex
	st   *agentStages

	// mu guards everything below, shared by the loop and the attaches.
	mu sync.Mutex
	// pending maps each process being tried to its job.
	pending map[uint32]*lateJob
	// lines are the attaches' reports, not yet printed; handed are the
	// stages already given to the Java join.
	lines  []string
	handed map[agentStage]bool
	// full says the pending limit was reported already (named once).
	full bool
}

// lateJob is one process being tried. execAt is its last exec (or
// adoption): a process that execs again while it is tried — a wrapper
// script exec'ing java — restarts its windows.
type lateJob struct {
	pid    uint32
	execAt time.Time
}

// newLateAttacher returns the late attacher of a session loading agent
// (the trusted, resolved jar) with spool, or nil without -agent.
func newLateAttacher(ctx context.Context, agent, spool string) *lateAttacher {
	if agent == "" {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	return &lateAttacher{ctx: ctx, cancel: cancel, l: newAgentLoader(), spool: spool,
		sem: make(chan struct{}, lateAttachParallel), every: lateAttachEvery,
		deadline: lateAttachFor, notJVM: lateNotJVMFor, st: newAgentStages(agent),
		pending: map[uint32]*lateJob{}, handed: map[agentStage]bool{}}
}

// share makes the stages the start-time attach made available to the
// late ones, which reuse a stage when javaattach says a JVM may share it.
// They reached the Java join already.
func (a *lateAttacher) share(stages []agentStage) {
	if a == nil {
		return
	}
	a.stMu.Lock()
	a.st.all = append(a.st.all, stages...)
	a.stMu.Unlock()
	a.mu.Lock()
	for _, s := range stages {
		a.handed[s] = true
	}
	a.mu.Unlock()
}

// exec schedules pid's attach: it just exec'd (or was adopted) and may
// be a JVM. A pid tried already only has its windows restarted.
func (a *lateAttacher) exec(pid uint32) {
	if a == nil || a.ctx.Err() != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if j, ok := a.pending[pid]; ok {
		j.execAt = time.Now()
		return
	}
	if len(a.pending) >= lateAttachPending {
		if !a.full {
			a.full = true
			a.lines = append(a.lines, fmt.Sprintf("%d processes wait for the agent already; pid %d "+
				"and later ones get none (start them with -javaagent)", lateAttachPending, pid))
		}
		return
	}
	j := &lateJob{pid: pid, execAt: time.Now()}
	a.pending[pid] = j
	a.wg.Add(1)
	go a.run(j)
}

// collect prints the attaches' lines on w and hands the stages made
// since the last call to the Java join (javaOutput.addStages). The trace
// loop calls it before it polls the join, so a new ring is scanned then.
func (a *lateAttacher) collect(java *javaOutput, w io.Writer) {
	if a == nil {
		return
	}
	a.stMu.Lock()
	all := append([]agentStage(nil), a.st.all...)
	a.stMu.Unlock()
	a.mu.Lock()
	lines := a.lines
	a.lines = nil
	var fresh []agentStage
	for _, s := range all {
		if !a.handed[s] {
			a.handed[s] = true
			fresh = append(fresh, s)
		}
	}
	a.mu.Unlock()
	for _, l := range lines {
		_, _ = fmt.Fprintf(w, "iormesh: %s\n", l)
	}
	java.addStages(fresh)
}

// stop cancels the attaches still running, waits for them (a cancelled
// handshake returns at once) and collects what they did, so every stage
// reaches the Java join, which removes it with the session.
func (a *lateAttacher) stop(java *javaOutput, w io.Writer) {
	if a == nil {
		return
	}
	a.cancel()
	a.wg.Wait()
	a.collect(java, w)
}

// busy reports whether a process is still being tried.
func (a *lateAttacher) busy() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.pending) > 0
}

// report queues one line for the loop to print.
func (a *lateAttacher) report(format string, args ...any) {
	a.mu.Lock()
	a.lines = append(a.lines, fmt.Sprintf(format, args...))
	a.mu.Unlock()
}

// since returns how long ago j's last exec was.
func (a *lateAttacher) since(j *lateJob) time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	return time.Since(j.execAt)
}

// run tries j's process until the agent is loaded, the process turns
// out to be no JVM, refuses, exits or runs out of the deadline (lateTry
// decides), pausing lateAttachEvery between tries. Whatever ends it is
// reported once — but a process that never was a JVM says nothing: most
// execs of a traced app are no JVM.
func (a *lateAttacher) run(j *lateJob) {
	defer a.wg.Done()
	defer func() {
		a.mu.Lock()
		delete(a.pending, j.pid)
		a.mu.Unlock()
	}()
	start := processStart(j.pid)
	starting := false // seen as a JVM still starting
	for {
		if start == 0 || !processAlive(j.pid, start) {
			if starting {
				a.report("no Java attribution for pid %d: it exited before its JVM accepted the agent", j.pid)
			}
			return
		}
		done, isJVM := a.try(j)
		if done {
			return
		}
		starting = starting || isJVM
		select {
		case <-a.ctx.Done():
			return
		case <-time.After(a.every):
		}
	}
}

// try makes one attempt at j's process and reports whether the job is
// over, and whether the process showed itself a JVM still starting.
func (a *lateAttacher) try(j *lateJob) (done, starting bool) {
	note, err := a.attempt(j.pid)
	switch {
	case err == nil:
		a.report("%s", note)
		return true, false
	case a.ctx.Err() != nil:
		return true, false // the session ends; nothing to report
	case errors.Is(err, javaattach.ErrNotJVM):
		return a.since(j) >= a.notJVM, false
	case !errors.Is(err, javaattach.ErrNoQuitHandler):
		a.report("%s", lateRefusal(j.pid, err))
		return true, false
	case a.since(j) >= a.deadline:
		a.report("no Java attribution for pid %d: its JVM did not accept the agent within %v (%v)",
			j.pid, a.deadline, err)
		return true, true
	}
	return false, true
}

// lateRefusal is the line naming a JVM that refused the agent, worded
// as the start-time attach words it (loadAgents).
func lateRefusal(pid uint32, err error) string {
	if stagingFailed(err) {
		return fmt.Sprintf("no Java attribution for pid %d: %v (start it with -javaagent instead)", pid, err)
	}
	return fmt.Sprintf("no Java attribution for pid %d: %v", pid, err)
}

// attempt stages (if pid needs it) and loads the agent once, as the
// start-time attach does (loadOne: the same checks, jar and stages),
// holding one of the lateAttachParallel tokens while it does.
func (a *lateAttacher) attempt(pid uint32) (string, error) {
	select {
	case a.sem <- struct{}{}:
	case <-a.ctx.Done():
		return "", a.ctx.Err()
	}
	defer func() { <-a.sem }()
	a.stMu.Lock()
	t, err := a.st.target(pid, a.spool)
	a.stMu.Unlock()
	if err != nil {
		return "", err
	}
	actx, cancel := context.WithTimeout(a.ctx, javaattach.DefaultTimeout)
	defer cancel()
	return loadAgent(actx, a.l, pid, t)
}
