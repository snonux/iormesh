package cli

import (
	"fmt"
	"os"
	"strings"

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
// Each target's identity is read now and seeded into the correlator,
// which handles the target's first record only later: by then the target
// may have exited and its PID been reused, and /proc would name the new
// process. A pattern scan runs after the probes attached, so a process
// exec'ing meanwhile is seen by the scan, by its exec record, or by both
// — adopt takes it once.
func (s *session) start(pids []uint32) error {
	for _, pid := range pids {
		id := processIdentity(pid)
		s.live[pid] = id.StartNS
		s.c.Seed(pid, id)
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
	id := processIdentity(pid)
	if id.StartNS == 0 {
		return nil // gone already
	}
	if err := s.tr.AddTarget(pid); err != nil {
		return err
	}
	if !processAlive(pid, id.StartNS) {
		return s.tr.RemoveTarget(pid)
	}
	return s.adopt(pid, id, 0)
}

// handleAll handles one batch of records in order; the first error stops
// it (the trace ends there, so the rest is never needed).
func (s *session) handleAll(batch []any) error {
	for _, rec := range batch {
		if err := s.handle(rec); err != nil {
			return err
		}
	}
	return nil
}

// handle routes one record: exec reports to the pattern judge, process
// lifecycle records to the live set and then, like everything else, to
// the correlator. The session is the record's last consumer — neither it
// nor the correlator keeps the pointer — so it returns the record to its
// decoder's pool afterwards (types.Recycle; decision #25's budget).
func (s *session) handle(rec any) error {
	defer types.Recycle(rec)
	switch r := rec.(type) {
	case *types.ExecArgvEvent:
		if r.EventType == types.PROCESS_ARGV_EVENT {
			argv, cut := execArgv(r)
			return s.c.Exec(execInfo(r, argv, cut))
		}
		return s.judgeExec(r)
	case *types.TaskNewtaskEvent:
		if r.CloneFlags&cloneThread == 0 && r.ScopeFlags == 0 {
			s.live[r.Pid] = childStart(r)
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
	argv, cut := execArgv(ev)
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
	if err := s.adopt(ev.Pid, processIdentity(ev.Pid), gap); err != nil {
		return err
	}
	return s.c.Exec(execInfo(ev, argv, cut))
}

// procCmdline reads a process's command line; a variable so tests need
// no /proc.
var procCmdline = func(pid uint32) ([]string, error) {
	return target.HostProcFS().Cmdline(pid)
}

// execArgv returns an exec record's argv and whether it is the record's
// cut copy. A cut block (a Java main class sits behind the class path) is
// completed from /proc/<pid>/cmdline — but only when that still starts
// with the bytes the record copied: by the time userspace reads it, the
// process may have exec'd again or its PID been reused, and another
// program's command line must not be recorded as this exec's. Otherwise
// the cut argv is all there is, and cut is true (the correlator masks its
// partial last element). The argv is a secret's carrier: the record and
// this result are never logged.
func execArgv(ev *types.ExecArgvEvent) (argv []string, cut bool) {
	block := ev.Argv[:min(int(ev.ArgvLen), len(ev.Argv))]
	argv = target.SplitArgv(block)
	if ev.ArgvLen >= ev.ArgvTotal {
		return argv, false
	}
	full, err := procCmdline(ev.Pid)
	if err == nil && len(block) > 0 && sameArgvStart(full, block) {
		return full, false
	}
	return argv, true
}

// sameArgvStart reports whether argv, NUL-joined as in the argument block,
// starts with block.
func sameArgvStart(argv []string, block []byte) bool {
	joined := strings.Join(argv, "\x00") + "\x00"
	return strings.HasPrefix(joined, string(block))
}

// execInfo is the correlator's view of an exec record with its argv.
func execInfo(ev *types.ExecArgvEvent, argv []string, cut bool) correlate.ExecInfo {
	return correlate.ExecInfo{PID: ev.Pid, TID: ev.Tid, BootNS: ev.Time,
		Exe: types.StringValue(ev.Exe[:]), Argv: argv, Cut: cut}
}

// isTracer reports whether a matching exec is another iormesh session —
// "iormesh run -pattern X" matches X by its own argv. The record's exe is
// the path as exec'd; /proc/<pid>/exe resolves it.
func (s *session) isTracer(pid uint32, exe string) bool {
	return target.IsTracer(exe, s.selfExe) ||
		target.IsTracer(target.HostProcFS().Exe(pid), s.selfExe)
}

// adopt makes pid, read as id at adoption, an incarnation of the
// session's app, once per process incarnation: the start scan and the
// exec record may both report it. id seeds the correlator
// (correlate.Seed), which may meet the process's records only after it
// is gone.
func (s *session) adopt(pid uint32, id correlate.Identity, gap uint64) error {
	if seen, ok := s.live[pid]; ok && seen == id.StartNS {
		return nil
	}
	s.live[pid] = id.StartNS
	return s.c.Incarnation(s.app, pid, id, gap)
}

// childStart is a followed child's start time, read when the session
// handles its task_newtask record — possibly after the child exited and
// its PID was reused. The child started before its record (the kernel
// sets the start time first, /proc rounds it down), so a later start is
// another process's, and the child counts as unknown (0): over() takes it
// for gone, which it is.
func childStart(r *types.TaskNewtaskEvent) uint64 {
	start := processStart(r.Pid)
	if start > r.Time {
		return 0
	}
	return start
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
