package correlate

import (
	"iormesh/internal/redact"
	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// cloneThread is CLONE_THREAD: a task_newtask record with it set is a new
// thread of an existing process, not a new process.
const cloneThread = 0x00010000

// newTask registers a traced process's new child (M2 step 1: the kernel
// follows it into target_map) while it surely still exists, so its pid
// incarnation is read from /proc before a short-lived child can vanish.
// A new pid is a new process by definition, so any state the correlator
// still holds under that pid belongs to a predecessor that exited (or
// whose records were lost), and is replaced — by a copy of its parent's
// fd table (M2 step 6, forked). New threads need nothing: they share
// their process's state.
func (c *Correlator) newTask(r *types.TaskNewtaskEvent) {
	c.advance(r.Time)
	if r.CloneFlags&cloneThread != 0 || r.ScopeFlags != 0 {
		return
	}
	delete(c.procs, r.Pid)
	c.forked(r.CreatorPid, c.proc(r.Pid), r.Pid)
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
// it now traces, and links it to app (schema.AppNodeID) in the graph:
// every incarnation of a restarting app hangs off the same app node (M2
// step 3). gap is the number of records the process produced between its
// exec and the attach (bpf.Tracer.AdoptExec); a non-zero gap is queued as
// an attach_gap drop naming the process, ahead of all of its traced
// events, so the loss is visible exactly where it happened — before the
// process's first event. pid was untraced until now, so whatever state
// the correlator held under it belongs to an earlier process.
func (c *Correlator) Incarnation(app string, pid uint32, gap uint64) error {
	if c.ended {
		return ErrEnded
	}
	delete(c.procs, pid)
	p := c.proc(pid)
	c.graph.incarnation(app, pid, p.inc)
	if gap > 0 {
		ev := c.hostEvent(schema.KindDrop)
		ev.PID, ev.PIDInc, ev.TID, ev.Comm = pid, p.inc, pid, p.comm
		reason := schema.DropAttachGap
		ev.Args.Lost, ev.Args.DropReason = &gap, &reason
		c.enqueue(ev, nil)
	}
	return c.release(false)
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
// first, as it must precede every traced event of the process.
func (c *Correlator) Exec(x ExecInfo) error {
	if c.ended {
		return ErrEnded
	}
	c.advance(x.BootNS)
	p := c.proc(x.PID)
	ev := schema.Event{
		SchemaVersion: schema.Version, Project: c.cfg.Host.Project, HostID: c.cfg.Host.ID,
		BootNS: x.BootNS, WallNS: c.wall(x.BootNS), PID: x.PID, PIDInc: p.inc, TID: x.TID,
		Comm: p.comm, Event: schema.KindExec, FD: schema.NoFD,
		Attribution: schema.AttributionNone,
	}
	if x.Exe != "" {
		// A path, but one that may embed a URL like any argument.
		exe := c.redact.Text(x.Exe)
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
