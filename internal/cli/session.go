package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"iormesh/internal/correlate"
	"iormesh/internal/target"
	"iormesh/internal/types"
)

// cloneThread is CLONE_THREAD: a task_newtask record with it is a new
// thread, not a new process.
const cloneThread = 0x00010000

// session ties one run's tracer and correlator to its target spec
// (docs/implementation.md M2 steps 1–3). It judges a pattern session's
// execs, links every match to the session's app node, keeps the set of
// live targets that decides when a PID or port session is over, and
// follows a port session's port to a restarted owner (portfollow.go).
type session struct {
	tr   tracer
	c    *correlate.Correlator
	spec target.Spec
	// pattern is a pattern session's target pattern, "" otherwise: only
	// a pattern names an app (schema.AppNodeID) that outlives its
	// processes.
	pattern string
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
	// java is the -spool run's join, polled on the loop's ticker (nil
	// without -spool).
	java *javaOutput
	// seg is the -trace-dir run's segment writer, ticked on the loop's
	// ticker (nil without -trace-dir).
	seg *segOutput
	// follow is a -listen-port session's wait for a restarted owner
	// (decision #23, task mh4); nil for a PID or pattern session.
	follow *portFollow
	// late loads the agent into JVMs that appear while a -agent session
	// runs (lateattach.go, task lh4); nil without -agent.
	late *lateAttacher
	// log is where the session reports what it decides on its own (a
	// followed or refused port owner, a late attach): run's stderr,
	// io.Discard in tests.
	log io.Writer
}

func newSession(tr tracer, c *correlate.Correlator, cfg runConfig) *session {
	s := &session{tr: tr, c: c, spec: cfg.spec, live: map[uint32]uint64{}, log: io.Discard}
	if cfg.spec.Mode() == target.ModeListenPort {
		s.follow = newPortFollow(cfg)
	}
	if cfg.spec.Mode() == target.ModePattern {
		s.pattern = cfg.spec.Pattern
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
// process. The identity includes the command line and executable
// (attachIdentity): a target already running has no exec event to carry
// them. A pattern scan runs after the probes attached, so a process
// exec'ing meanwhile is seen by the scan, by its exec record, or by both
// — adopt takes it once. A port session also remembers its owners'
// keys, by which it recognises them after a restart (portFollow).
func (s *session) start(pids []uint32) error {
	if s.follow != nil {
		s.follow.remember(pids)
	}
	for _, pid := range pids {
		id := attachIdentity(pid)
		s.live[pid] = id.StartNS
		// A target already gone has nothing to seed: an empty identity
		// would only add a pid@0 process node with no events.
		if id.StartNS == 0 {
			continue
		}
		if err := s.c.Seed(pid, id); err != nil {
			return err
		}
	}
	if s.pattern == "" {
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

// adoptRunning traces a process the start scan found (addRunning). Its
// earlier syscalls precede the session, so they are no attach gap. Its
// exec precedes the session too, so its attach carries its command line
// (attachIdentity).
func (s *session) adoptRunning(pid uint32) error {
	id := attachIdentity(pid)
	if id.StartNS == 0 {
		return nil // gone already
	}
	added, err := s.addRunning(pid, id.StartNS)
	if err != nil || !added {
		return err
	}
	return s.adopt(pid, id, 0)
}

// addRunning puts a running process, started at start, into target_map
// and reports whether it is still there. The start time is read before
// the insert and the liveness checked after: a process that exited in
// between had its exit handled before the entry existed, so nothing
// would ever remove the entry, and a later process under the PID would
// be traced. Such an entry is removed again.
func (s *session) addRunning(pid uint32, start uint64) (bool, error) {
	if err := s.tr.AddTarget(pid); err != nil {
		return false, err
	}
	if !processAlive(pid, start) {
		return false, s.tr.RemoveTarget(pid)
	}
	return true, nil
}

// handleAll handles one batch of records in order; the first error stops
// it (the trace ends there, so the rest is never needed). The session is
// the records' last consumer — neither it nor the correlator keeps a
// record's pointer — so the batch then goes back to the tracer, which
// returns the records to their decoders' pools (bpf.Tracer.Release;
// decision #25's budget).
func (s *session) handleAll(batch []any) error {
	defer s.tr.Release(batch)
	for _, rec := range batch {
		if err := s.handle(rec); err != nil {
			return err
		}
	}
	return nil
}

// handle routes one record: exec reports to the pattern judge, process
// lifecycle records to the live set and then, like everything else, to
// the correlator. A traced process's exec (a target's, a child's) may
// start a JVM, which -agent's late attach then tries (task lh4).
func (s *session) handle(rec any) error {
	switch r := rec.(type) {
	case *types.ExecArgvEvent:
		if r.EventType == types.PROCESS_ARGV_EVENT {
			s.late.execBoot(r.Pid, r.Time)
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
			s.java.exited(r.Pid, r.Time)
		}
	case *types.JavaMarkEvent:
		// The Java join's, not the correlator's: it pairs the mark with
		// the event the correlator makes of the enter that follows.
		s.java.mark(r)
		return nil
	}
	return s.c.Handle(rec)
}

// judgeExec matches an untraced process's exec against the pattern
// (decision #23: argv and exe, never the comm) and adopts or dismisses
// it. An adopted exec is also recorded (redacted, by the correlator):
// it is how the app came to be traced, and its argv is the app's; and
// with -agent its JVM gets the agent once it is ready (task lh4).
func (s *session) judgeExec(ev *types.ExecArgvEvent) error {
	argv, cut := execArgv(ev)
	exe := types.StringValue(ev.Exe[:])
	if !s.match.Match(exe, argv) || s.isTracer(ev.Pid, exe) {
		return s.tr.IgnoreExec(ev.Pid, ev.Time, execAlive(ev))
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
	s.late.execBoot(ev.Pid, ev.Time)
	return s.c.Exec(execInfo(ev, argv, cut))
}

// execAlive returns IgnoreExec's liveness check: whether the process that
// made the exec ev reports still runs. It started no later than the exec
// (a later start is a reused PID's) and is alive with that start time,
// not a zombie (processAlive). IgnoreExec asks only when it had to put
// back the entry of a later exec it took out, so the /proc reads cost
// nothing on the common path.
func execAlive(ev *types.ExecArgvEvent) func() bool {
	return func() bool {
		start := processStart(ev.Pid)
		return start != 0 && start <= ev.Time && processAlive(ev.Pid, start)
	}
}

// procCmdline reads a process's command line, bounded
// (target.ProcFS.CmdlineBounded: cut says it was longer); a variable so
// tests need no /proc.
var procCmdline = func(pid uint32) ([]string, bool, error) {
	return target.HostProcFS().CmdlineBounded(pid)
}

// execArgv returns an exec record's argv and whether it is a cut copy. A
// cut block (a Java main class sits behind the class path) is completed
// from /proc/<pid>/cmdline — but only when that still starts with the
// bytes the record copied: by the time userspace reads it, the process
// may have exec'd again or its PID been reused, and another program's
// command line must not be recorded as this exec's. Otherwise the cut
// argv is all there is, and cut is true (the correlator masks its
// partial last element). The completion is bounded too
// (target.MaxCmdline), so an event line stays far below the NDJSON
// readers' 1 MiB; a completion cut there is cut as well. The argv is a
// secret's carrier: the record and this result are never logged.
func execArgv(ev *types.ExecArgvEvent) (argv []string, cut bool) {
	block := ev.Argv[:min(int(ev.ArgvLen), len(ev.Argv))]
	argv = target.SplitArgv(block)
	if ev.ArgvLen >= ev.ArgvTotal {
		return argv, false
	}
	full, fullCut, err := procCmdline(ev.Pid)
	if err == nil && len(block) > 0 && sameArgvStart(full, block) {
		return full, fullCut
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
	return s.c.Incarnation(s.pattern, pid, id, gap)
}

// childStart is a followed child's start time, read when the session
// handles its task_newtask record — possibly after the child exited and
// its PID was reused. The child started before its record (the kernel
// sets the start time first, /proc rounds it down), so a later start is
// another process's, and the child counts as unknown (0): over() takes it
// for gone, which it is. The correlator reads /proc for the child on its
// own (correlate's childIdentity, same check), a second read that may
// disagree with this one only if the PID is reused in between.
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
// id or an unknown start time does not). A pattern session is never
// over: it waits for the app to re-appear (decision #23) until its
// duration or a stop. A port session that is over may still go on: it
// waits for a restarted owner (ended, followPort).
func (s *session) over() bool {
	if s.pattern != "" {
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
