package correlate

import (
	"path"
	"strings"

	"iormesh/internal/redact"
	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// cloneThread is CLONE_THREAD: a task_newtask record with it set is a new
// thread of an existing process, not a new process.
const cloneThread = 0x00010000

// maxCommLen is the kernel's TASK_COMM_LEN - 1: the bytes of a comm
// before its NUL. An exec names the process after the executable's
// basename cut to that length (__set_task_comm).
const maxCommLen = 15

// newTask registers a traced process's new child (M2 step 1: the kernel
// follows it into target_map). A new pid is a new process by definition,
// so any state the correlator still holds under that pid belongs to a
// predecessor that exited (or whose records were lost), and is replaced —
// by a copy of its parent's fd table (M2 step 6, forked). New threads
// need nothing: they share their process's state.
//
// The fork is also an event (task ze4): the forked edge and the child's
// inherited fds are graph state no other event shows, and the controller
// rebuilds the graph from the events alone (decision #3). It is numbered
// here, after every event of the parent the child's copy reflects.
func (c *Correlator) newTask(r *types.TaskNewtaskEvent) {
	c.advance(r.Time)
	if r.CloneFlags&cloneThread != 0 || r.ScopeFlags != 0 {
		return
	}
	child := c.seed(r.Pid, c.childIdentity(r))
	ev := c.processEvent(schema.KindFork, r.Pid, child, r.Time)
	if parent, ok := c.forked(r.CreatorPid, child, r.Pid); ok {
		// Copies: the record is recycled once Handle returns, and the
		// queued event must not alias the parent's live state.
		ppid, pinc := r.CreatorPid, parent.inc
		ev.Args.ParentPID, ev.Args.ParentInc = &ppid, &pinc
	}
	c.enqueue(ev, nil)
}

// processEvent builds a synthetic event of one process (fork, attach,
// drop attach_gap): no fd, no syscall, at bootNS.
func (c *Correlator) processEvent(kind string, pid uint32, p *procState, bootNS uint64) schema.Event {
	return schema.Event{
		SchemaVersion: schema.Version, Project: c.cfg.Host.Project, HostID: c.cfg.Host.ID,
		BootNS: bootNS, WallNS: c.wall(bootNS), PID: pid, PIDInc: p.inc, TID: pid,
		Comm: p.comm, Event: kind, FD: schema.NoFD, Attribution: schema.AttributionNone,
	}
}

// childIdentity reads a new child's incarnation from /proc. The record
// does not carry it (a classic task_newtask tracepoint has no task
// pointer), and the read happens when the correlator handles the record,
// so a lagging correlator may find the child gone and its PID reused.
// The kernel sets the child's start time before the tracepoint fires and
// /proc rounds it down to a clock tick, so the child's start is never
// after the record; one that is belongs to a later process. Such a child
// keeps an unknown incarnation (0) and the name it inherited, which the
// record carries — not the stranger's. So does a child simply gone (an
// unreadable /proc entry). A reuse within the fork's own clock tick
// rounds down to a start at or before the record and is not detected.
func (c *Correlator) childIdentity(r *types.TaskNewtaskEvent) Identity {
	comm, start := c.cfg.ProcInfo(r.Pid)
	if start == 0 || start > r.Time {
		return Identity{Comm: types.StringValue(r.Comm[:])}
	}
	return Identity{Comm: comm, StartNS: start}
}

// exitTask forgets a dead thread's pending enter and parked stack: its
// syscall never returns, and the tid may be reused. A dead process is
// also dropped from the native stack table (M2 step 9), which would
// otherwise grow with every process a long session sees. The process
// state stays, even once the whole group is dead: the kernel still
// reports its sockets' last transitions after the exit (tcp_state, owned
// by the dead pid), and they belong to that incarnation. A later process
// under the same PID is announced by its own task_newtask record or
// attach (newTask, Incarnation), which is where the old state is
// replaced.
func (c *Correlator) exitTask(r *types.ProcessExitEvent) {
	c.advance(r.Time)
	delete(c.enters, r.Tid)
	delete(c.stacks, r.Tid)
	if r.GroupDead != 0 && c.cfg.Stacks != nil {
		c.cfg.Stacks.Forget(r.Pid) // its stacks are written; its maps are gone
	}
}

// Incarnation records that the session's pattern matched pid, a process
// it now traces, and links it to the app the pattern names
// (schema.AppNodeID of the project and pattern) in the graph: every
// incarnation of a restarting app hangs off the same app node (M2 step
// 3). The attach event says so in the trace (task ze4), with the
// pattern, so a graph rebuilt from the events has the app too. id is
// the process as the caller read it at adoption (Seed: the correlator
// may handle the process's records only after it is gone and its PID
// reused). gap is the number of records the process produced between
// its exec and the attach (bpf.Tracer.AdoptExec); a non-zero gap is
// queued as an attach_gap drop naming the process right after the
// attach, ahead of all of its traced events, so the loss is visible
// exactly where it happened — before the process's first syscall. pid
// was untraced until now, so whatever state the correlator held under
// it belongs to an earlier process.
func (c *Correlator) Incarnation(pattern string, pid uint32, id Identity, gap uint64) error {
	if c.ended {
		return ErrEnded
	}
	p := c.attach(pid, id, &pattern)
	c.graph.incarnation(schema.AppNodeID(c.cfg.Host.Project, pattern), pid, p.inc)
	if gap > 0 {
		ev := c.hostEvent(schema.KindDrop)
		ev.PID, ev.PIDInc, ev.TID, ev.Comm = pid, p.inc, pid, p.comm
		reason := schema.DropAttachGap
		ev.Args.Lost, ev.Args.DropReason = &gap, &reason
		c.enqueue(ev, nil)
	}
	return c.release(false)
}

// attach seeds pid as id and queues its attach event, stamped now (the
// session took the process on now; its records come later). pattern is
// the pattern session's, nil for a -pid or port target.
func (c *Correlator) attach(pid uint32, id Identity, pattern *string) *procState {
	p := c.seed(pid, id)
	ev := c.processEvent(schema.KindAttach, pid, p, c.cfg.Clock())
	ev.Args.Pattern = pattern
	c.enqueue(ev, nil)
	return p
}

// ExecInfo is one exec of a traced process as the caller read it: the
// exec record's time and executable, and the argv, completed from
// /proc/<pid>/cmdline when the record's copy was cut short and /proc
// still showed the same command line.
type ExecInfo struct {
	PID, TID uint32
	BootNS   uint64 // the exec's time on the boot clock
	Exe      string // the path as exec'd; "" when unreadable
	Argv     []string
	// Cut says Argv is the record's cut copy: its last element is a
	// fragment ("https://u:secr", "--passw") the policy cannot judge, so
	// it is masked whole.
	Cut bool
}

// Exec queues the exec event of a traced process (M2 step 8): its
// executable and its argv, redacted by the correlator's policy before the
// event exists, so no sink, queue or later stage ever holds the raw
// secret (decision #9; the environment is never captured). A pattern
// session calls it after Incarnation, so the attach gap's drop comes
// first, as it must precede every traced event of the process. The event
// carries the comm after the exec: by the time the program's argv exists,
// the kernel has renamed the process (execComm).
func (c *Correlator) Exec(x ExecInfo) error {
	if c.ended {
		return ErrEnded
	}
	c.advance(x.BootNS)
	p := c.proc(x.PID)
	exe := ""
	if x.Exe != "" {
		// A path, but one that may embed a URL like any argument.
		exe = c.redact.Text(x.Exe)
	}
	c.exeFallbackComm(x.PID, p, exe)
	ev := schema.Event{
		SchemaVersion: schema.Version, Project: c.cfg.Host.Project, HostID: c.cfg.Host.ID,
		BootNS: x.BootNS, WallNS: c.wall(x.BootNS), PID: x.PID, PIDInc: p.inc, TID: x.TID,
		Comm: p.comm, Event: schema.KindExec, FD: schema.NoFD,
		Attribution: schema.AttributionNone,
	}
	if exe != "" {
		ev.Args.Path = &exe
	}
	if len(x.Argv) > 0 {
		ev.Args.Argv = c.redact.Argv(x.Argv)
		if x.Cut {
			ev.Args.Argv[len(ev.Args.Argv)-1] = redact.Mask
		}
	}
	c.enqueue(ev, nil)
	return c.release(false)
}

// execComm applies the comm a successful exec gave pid (the kernel's,
// read by the exec record after the rename). Only a successful exec
// reaches here — sched_process_exec does not fire for a failed one — so
// a failed exec leaves the comm alone, as the kernel does. An empty comm
// (an unreadable record) defers to the exec's executable, which the
// following Exec carries (exeComm); until then the old name stays, the
// best one known.
func (c *Correlator) execComm(pid uint32, p *procState, comm string) {
	if comm == "" {
		p.commFromExe = true
		return
	}
	p.commFromExe = false
	c.setComm(pid, p, comm)
}

// renameTask applies a task_rename record (exec.c, task 0g4): the kernel
// changed a task's comm — prctl(PR_SET_NAME), pthread_setname_np, a
// /proc/<tid>/comm write, or an exec's own rename, which arrives just
// before the exec record and names the process the same. Only the thread
// group leader's (tid == pid) rename renames the process: an event's comm
// is the process's name (docs/schema.md), the one /proc/<pid>/comm and ps
// show, and that is the leader's comm. A worker thread naming itself (a
// JVM, tokio or Go runtime pool) leaves the process's name alone; Java
// thread names reach the trace through the agent (java.thread). A process
// the correlator has not met yet needs nothing: its first record reads
// its name from /proc (proc), which already holds the new one. An empty
// comm (the handler drops records it cannot read, so never in practice)
// renames nothing.
func (c *Correlator) renameTask(r *types.TaskRenameEvent) {
	c.advance(r.Time)
	if r.Tid != r.Pid {
		return
	}
	p, ok := c.procs[r.Pid]
	if !ok {
		return
	}
	if comm := types.StringValue(r.Comm[:]); comm != "" {
		c.setComm(r.Pid, p, comm)
	}
}

// exeFallbackComm names the process after its executable (exeComm) when
// its exec record carried no comm (execComm). exe is the redacted path:
// what the trace may show of it.
func (c *Correlator) exeFallbackComm(pid uint32, p *procState, exe string) {
	if !p.commFromExe {
		return
	}
	p.commFromExe = false
	if comm := exeComm(exe); comm != "" {
		c.setComm(pid, p, comm)
	}
}

// setComm renames the process for every later event and in the graph.
func (c *Correlator) setComm(pid uint32, p *procState, comm string) {
	if comm == p.comm {
		return
	}
	p.comm = comm
	c.graph.renamed(pid, p.inc, comm)
}

// exeComm is the comm the kernel gives a process exec'ing exe: its
// basename, cut to maxCommLen bytes. The kernel cuts bytes and may split
// a multi-byte character; the cut here drops such a fragment, so the
// comm stays valid UTF-8 in the JSON. "" when exe has no basename.
func exeComm(exe string) string {
	if exe == "" {
		return ""
	}
	base := path.Base(exe)
	if base == "/" || base == "." {
		return ""
	}
	if len(base) <= maxCommLen {
		return base
	}
	return strings.ToValidUTF8(base[:maxCommLen], "")
}
