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
	// match is the pattern compiled once; every exec on the host asks it.
	match target.Matcher
	// selfExe is the tracer's executable: another iormesh session's
	// command line names its pattern, but it is never a target.
	selfExe string
	// live maps each traced process to its start time (its incarnation),
	// so a PID the kernel reuses is not taken for the target that exited.
	// 0 means unknown: /proc could not be read, and over() does not take
	// whatever runs under that PID now for the target.
	live map[uint32]uint64
}

func newSession(tr tracer, c *correlate.Correlator, cfg runConfig) *session {
	s := &session{tr: tr, c: c, spec: cfg.spec, live: map[uint32]uint64{}}
	if cfg.spec.Mode() == target.ModePattern {
		s.app = schema.AppNodeID(cfg.project, cfg.spec.Pattern)
		// The spec was validated, so the pattern compiles.
		s.match, _ = cfg.spec.Matcher()
		s.selfExe = target.HostProcFS().Exe(uint32(os.Getpid()))
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
		if err := s.adoptRunning(pid); err != nil {
			return err
		}
	}
	return nil
}

// adoptRunning traces a process the start scan found. Its start time is
// read before the insert and its liveness checked after: a process that
// exited in between had its exit handled before the entry existed, so
// nothing would ever remove the entry, and a later process under the PID
// would be traced. Such an entry is removed again. Its earlier syscalls
// precede the session, so they are no attach gap.
func (s *session) adoptRunning(pid uint32) error {
	start := processStart(pid)
	if start == 0 {
		return nil // gone already
	}
	if err := s.tr.AddTarget(pid); err != nil {
		return err
	}
	if !processAlive(pid, start) {
		return s.tr.RemoveTarget(pid)
	}
	return s.adopt(pid, 0)
}

// handle routes one record: exec reports to the pattern judge, process
// lifecycle records to the live set and then, like everything else, to
// the correlator.
func (s *session) handle(rec any) error {
	switch r := rec.(type) {
	case *types.ExecArgvEvent:
		defer r.Recycle()
		if r.EventType == types.PROCESS_ARGV_EVENT {
			return s.c.Exec(execInfo(r, execArgv(r)))
		}
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
// it. An adopted exec is also recorded (redacted, by the correlator):
// it is how the app came to be traced, and its argv is the app's.
func (s *session) judgeExec(ev *types.ExecArgvEvent) error {
	argv := execArgv(ev)
	exe := types.StringValue(ev.Exe[:])
	if !s.match.Match(exe, argv) || s.isTracer(ev.Pid, exe) {
		return s.tr.IgnoreExec(ev.Pid, ev.Time)
	}
	gap, adopted, err := s.tr.AdoptExec(ev.Pid)
	if err != nil || !adopted {
		// Not adopted: the process exited before the verdict, and the
		// kernel already dropped its entry. It is no incarnation.
		return err
	}
	if err := s.adopt(ev.Pid, gap); err != nil {
		return err
	}
	return s.c.Exec(execInfo(ev, argv))
}

// execArgv returns an exec record's argv. A record whose argv block was
// cut short is completed from /proc/<pid>/cmdline, since a Java main
// class sits behind the class path; if that read fails (the process is
// gone), the cut argv is all there is.
func execArgv(ev *types.ExecArgvEvent) []string {
	argv := target.SplitArgv(ev.Argv[:min(int(ev.ArgvLen), len(ev.Argv))])
	if ev.ArgvLen < ev.ArgvTotal {
		if full, err := target.HostProcFS().Cmdline(ev.Pid); err == nil {
			argv = full
		}
	}
	return argv
}

// execInfo is the correlator's view of an exec record with its argv.
func execInfo(ev *types.ExecArgvEvent, argv []string) correlate.ExecInfo {
	return correlate.ExecInfo{PID: ev.Pid, TID: ev.Tid, BootNS: ev.Time,
		Exe: types.StringValue(ev.Exe[:]), Argv: argv}
}

// isTracer reports whether a matching exec is another iormesh session —
// "iormesh run -pattern X" matches X by its own argv. The record's exe is
// the path as exec'd; /proc/<pid>/exe resolves it.
func (s *session) isTracer(pid uint32, exe string) bool {
	return target.IsTracer(exe, s.selfExe) ||
		target.IsTracer(target.HostProcFS().Exe(pid), s.selfExe)
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
// whether that incarnation still lives (a reused PID, a zombie, a thread
// id or an unknown start time does not). A pattern session is never over: it waits for the
// app to re-appear (decision #23) until its duration or a stop.
func (s *session) over() bool {
	if s.app != "" {
		return false
	}
	for pid, start := range s.live {
		// An unknown start time could not tell the target from a
		// process that reused its PID: the read failed because the
		// target was gone, so it counts as gone.
		if start == 0 || !processAlive(pid, start) {
			delete(s.live, pid)
		}
	}
	return len(s.live) == 0
}
