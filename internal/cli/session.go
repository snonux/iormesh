package cli

import (
	"fmt"
	"os"

	"iormesh/internal/correlate"
	"iormesh/internal/schema"
	"iormesh/internal/target"
	"iormesh/internal/types"
)

// cloneThread is CLONE_THREAD: a task_newtask record with it is a new
// thread, not a new process.
const cloneThread = 0x00010000

// session ties one run's tracer and correlator to its target spec
// (docs/implementation.md M2 steps 1–3). It judges a pattern session's
// execs, links every match to the session's app node, and keeps the set
// of live targets that decides when a PID or port session is over.
type session struct {
	tr   tracer
	c    *correlate.Correlator
	spec target.Spec
	// app is the app node id of a pattern session, "" otherwise: only a
	// pattern names an app that outlives its processes.
	app string
	// live maps each traced process to its start time (its incarnation),
	// so a PID the kernel reuses is not taken for the target that exited.
	live map[uint32]uint64
}

func newSession(tr tracer, c *correlate.Correlator, cfg runConfig) *session {
	s := &session{tr: tr, c: c, spec: cfg.spec, live: map[uint32]uint64{}}
	if cfg.spec.Mode() == target.ModePattern {
		s.app = schema.AppNodeID(cfg.project, cfg.spec.Pattern)
	}
	return s
}

// start registers the targets the tracer was opened with (pids, already
// in target_map) and, for a pattern, every process that matches already.
// A pattern scan runs after the probes attached, so a process exec'ing
// meanwhile is seen by the scan, by its exec record, or by both — adopt
// takes it once.
func (s *session) start(pids []uint32) error {
	for _, pid := range pids {
		s.live[pid] = processStart(pid)
	}
	if s.app == "" {
		return nil
	}
	found, err := s.spec.Resolve(target.HostProcFS(), uint32(os.Getpid()))
	if err != nil {
		return fmt.Errorf("scan for %q: %w", s.spec.Pattern, err)
	}
	for _, pid := range found {
		if err := s.tr.AddTarget(pid); err != nil {
			return err
		}
		// Already running: its earlier syscalls precede the session, they
		// are no gap of it.
		if err := s.adopt(pid, 0); err != nil {
			return err
		}
	}
	return nil
}

// handle routes one record: exec reports to the pattern judge, process
// lifecycle records to the live set and then, like everything else, to
// the correlator.
func (s *session) handle(rec any) error {
	switch r := rec.(type) {
	case *types.ExecArgvEvent:
		defer r.Recycle()
		return s.judgeExec(r)
	case *types.TaskNewtaskEvent:
		if r.CloneFlags&cloneThread == 0 && r.ScopeFlags == 0 {
			s.live[r.Pid] = processStart(r.Pid)
		}
	case *types.ProcessExitEvent:
		if r.GroupDead != 0 {
			delete(s.live, r.Pid)
		}
	}
	return s.c.Handle(rec)
}

// judgeExec matches an untraced process's exec against the pattern
// (decision #23: argv and exe, never the comm) and adopts or dismisses
// it. A record whose argv block was cut short is completed from
// /proc/<pid>/cmdline, since a Java main class sits behind the class path.
func (s *session) judgeExec(ev *types.ExecArgvEvent) error {
	argv := target.SplitArgv(ev.Argv[:min(int(ev.ArgvLen), len(ev.Argv))])
	if ev.ArgvLen < ev.ArgvTotal {
		if full, err := target.HostProcFS().Cmdline(ev.Pid); err == nil {
			argv = full
		}
	}
	if !s.spec.Matches(types.StringValue(ev.Exe[:]), argv) {
		return s.tr.IgnoreExec(ev.Pid, ev.Time)
	}
	gap, err := s.tr.AdoptExec(ev.Pid, ev.Time)
	if err != nil {
		return err
	}
	return s.adopt(ev.Pid, gap)
}

// adopt makes pid an incarnation of the session's app, once per process
// incarnation: the start scan and the exec record may both report it.
func (s *session) adopt(pid uint32, gap uint64) error {
	start := processStart(pid)
	if seen, ok := s.live[pid]; ok && seen == start {
		return nil
	}
	s.live[pid] = start
	return s.c.Incarnation(s.app, pid, gap)
}

// over reports whether a PID or port session has nothing left to trace:
// every target and followed child exited. Exit records remove most of
// them; this also drops those whose exit record was lost, by asking /proc
// whether that incarnation still lives (a reused PID, a zombie or a
// thread id does not). A pattern session is never over: it waits for the
// app to re-appear (decision #23) until its duration or a stop.
func (s *session) over() bool {
	if s.app != "" {
		return false
	}
	for pid, start := range s.live {
		if !processAlive(pid, start) {
			delete(s.live, pid)
		}
	}
	return len(s.live) == 0
}
