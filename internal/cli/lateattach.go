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

	// stMu guards st: the stages are made by the attach goroutines, and
	// held while one is made (file I/O inside the JVM's root). The trace
	// loop never takes it once the session runs (share is start-time
	// only): it collects the new stages from made, under mu.
	stMu sync.Mutex
	st   *agentStages

	// mu guards everything below, shared by the loop and the attaches.
	mu sync.Mutex
	// pending maps each process being tried to its job.
	pending map[uint32]*lateJob
	// held are the JVMs the start-time attach loads (hold, loaded):
	// that attach claims each until its own load is done, so the late
	// attacher never works on one at the same time — each attach may
	// send a SIGQUIT, and a second one prints a thread dump on the app's
	// stdout (task 7l4). The value is the process's last exec meanwhile,
	// zero if it did not exec.
	held map[uint32]time.Time
	// lines are the attaches' reports and made the stages they made, both
	// not yet collected by the loop.
	lines []string
	made  []agentStage
	// full says the current overflow of the pending limit was named; it
	// is cleared when an attach ends and frees a slot, so the next
	// overflow is named again.
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
		pending: map[uint32]*lateJob{}, held: map[uint32]time.Time{}}
}

// hold claims pids for the start-time attach, before its loads begin:
// until loaded releases one, an exec of it is only noted.
func (a *lateAttacher) hold(pids []uint32) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, pid := range pids {
		a.held[pid] = time.Time{}
	}
}

// loaded is told by the start-time attach that pid's own load ended
// (loadAgents, a loadedFunc): it shares the stages that load made, then
// releases pid's claim — at once, not after the other pids' loads, so a
// JVM loaded early that then execs a new JVM gets the late attach in its
// new image like any later exec (task 9l4).
func (a *lateAttacher) loaded(pid uint32, retry bool, fresh []agentStage) {
	a.share(fresh)
	a.release(pid, retry)
}

// release ends the start-time attach's claim on pid. If it exec'd
// meanwhile it is scheduled with its windows from that exec, as exec
// would have — but only on retry, a start-time load that failed before
// any SIGQUIT could be sent (no libjvm.so or no handler yet: a wrapper
// still exec'ing java, a JVM still starting). One the start-time attach
// loaded, or that refused after the handshake began, is not tried again
// in that image, so a JVM gets at most one SIGQUIT from the session; an
// exec after the release is a new image, scheduled by exec as usual.
func (a *lateAttacher) release(pid uint32, retry bool) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	execAt, ok := a.held[pid]
	delete(a.held, pid)
	if ok && retry && !execAt.IsZero() && a.ctx.Err() == nil {
		a.schedule(pid, execAt)
	}
}

// releaseAll ends every claim still held without scheduling any: the
// start-time attach was given up, and the session is ending.
func (a *lateAttacher) releaseAll() {
	if a == nil {
		return
	}
	a.mu.Lock()
	clear(a.held)
	a.mu.Unlock()
}

// share makes the stages the start-time attach made available to the
// late ones, which reuse a stage when javaattach says a JVM may share it.
// They reach the Java join with the start-time attach's result, so they
// are not in made. It runs on the start-time loads' goroutine, before
// the pid whose load made them is released, and may wait for a late
// stage being made.
func (a *lateAttacher) share(stages []agentStage) {
	if a == nil || len(stages) == 0 {
		return
	}
	a.stMu.Lock()
	a.st.all = append(a.st.all, stages...)
	a.stMu.Unlock()
}

// exec schedules pid's attach: it just exec'd (or was adopted) and may
// be a JVM. A pid the start-time attach holds is only noted (release
// decides), so the two never work on one JVM at once.
func (a *lateAttacher) exec(pid uint32) {
	if a == nil || a.ctx.Err() != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.held[pid]; ok {
		a.held[pid] = time.Now()
		return
	}
	a.schedule(pid, time.Now())
}

// schedule starts pid's job, its windows from execAt, under mu. A pid
// tried already only has its windows restarted. With lateAttachPending
// processes pending, pid gets nothing; the first pid dropped in each
// overflow is named (full), the rest until a slot frees are covered by
// that line.
func (a *lateAttacher) schedule(pid uint32, execAt time.Time) {
	if j, ok := a.pending[pid]; ok {
		j.execAt = execAt
		return
	}
	if len(a.pending) >= lateAttachPending {
		if !a.full {
			a.full = true
			a.lines = append(a.lines, fmt.Sprintf("%d processes wait for the agent already; pid %d "+
				"gets no agent, nor does any other process exec'd before one of them is done "+
				"(start them with -javaagent)", lateAttachPending, pid))
		}
		return
	}
	j := &lateJob{pid: pid, execAt: execAt}
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
	a.mu.Lock()
	lines, fresh := a.lines, a.made
	a.lines, a.made = nil, nil
	a.mu.Unlock()
	for _, l := range lines {
		_, _ = fmt.Fprintf(w, "iormesh: %s\n", l)
	}
	java.addStages(fresh)
}

// stop cancels the attaches still running, waits for them and collects
// what they did, so every stage reaches the Java join, which removes it
// with the session. A cancelled attach ends at once while it waits for
// its next try, a token or the attach socket; one already waiting for
// the JVM's reply ends at that read's deadline, javaattach.DefaultTimeout
// at most (the load request is out, and the JVM still completes it).
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
		a.full = false // a slot is free: the next overflow is named again
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
	t, err := a.target(pid)
	if err != nil {
		return "", err
	}
	actx, cancel := context.WithTimeout(a.ctx, javaattach.DefaultTimeout)
	defer cancel()
	return loadAgent(actx, a.l, pid, t)
}

// target returns pid's agentTarget (agentStages.target) under stMu, and
// queues a stage it made in made for the loop to collect — under mu, so
// the loop never waits for a stage being made.
func (a *lateAttacher) target(pid uint32) (agentTarget, error) {
	a.stMu.Lock()
	n := len(a.st.all)
	t, err := a.st.target(pid, a.spool)
	fresh := append([]agentStage(nil), a.st.all[n:]...)
	a.stMu.Unlock()
	if len(fresh) > 0 {
		a.mu.Lock()
		a.made = append(a.made, fresh...)
		a.mu.Unlock()
	}
	return t, err
}
