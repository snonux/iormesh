package correlate

import (
	"net/netip"
	"time"

	"golang.org/x/sys/unix"

	"iormesh/internal/schema"
	"iormesh/internal/target"
)

// procState is one traced process incarnation.
type procState struct {
	pid uint32
	inc uint64
	// comm is the process's current name: read with inc (Identity) when
	// the session took the process on (Seed) or else when the correlator
	// first saw the pid (ProcInfo), then replaced on every successful
	// exec (execComm), because the kernel renames the process to the new
	// program there. Events stamp it when they are built, so those before
	// an exec keep the old name.
	comm string
	// commFromExe says the last exec record carried no comm, so the
	// following Exec derives it from the executable (exeComm).
	commFromExe bool
	// fds holds the live fd instances; lastGen remembers each number's
	// newest generation after close too, so a number reused by a syscall
	// M1 does not trace (socket, dup, pipe, …) still gets a fresh fd_gen.
	fds     map[int32]uint32
	lastGen map[int32]uint32
	// sockOf binds an fd instance to the socket it names; tcp_state
	// records carry that binding. It holds the socket itself, not its sk
	// address: the sk is dropped from the socket table at TCP_CLOSE (the
	// address may be reused), while the fd — after an RST, say — stays
	// open and its events still belong to that connection.
	sockOf map[int32]*sockState
	// paths holds the current path of each fd instance a traced open (or
	// a dup of one) named, rewritten by renames (paths.go). It names the
	// dirfd of a relative *at call and the file of an fd metadata event.
	paths map[int32]fdPath
	// cloexec is the close-on-exec state of the fd instances the trace
	// knows it of (fdtable.go): true set, false clear, absent unknown.
	cloexec map[int32]bool
	// flowsOf lists the UDP flows (by key) each fd started (ipc.go).
	flowsOf map[int32][][2]netip.AddrPort
	// born is when each live instance a traced call created was created
	// (the creating record's boot time), past each number's previous
	// instance: a folded read/write record can arrive after its fd was
	// closed and reused, and is matched to its instance by time
	// (aggregate.go). clock points at the correlator's current record
	// time, which stamps both.
	born  map[int32]uint64
	past  map[int32]pastInstance
	clock *uint64
	// dead: the process's last thread exited (exitTask); renames and
	// unlinks skip it. Its fd table goes a fuse window later (fdsDropped,
	// reap.go); inc and comm stay for the late records of its sockets.
	// dyingAt is when its latest fuse window began.
	dead, fdsDropped bool
	dyingAt          uint64
	// socks counts the TCP sockets whose records name this process and
	// that are still in the socket table (not yet TCP_CLOSE): the ones
	// the kernel may still report after the process died (reap.go).
	socks int
	// pod is the pod object its events carry in a pod (pod.go): resolved
	// on its first event (procPod) or inherited at fork (inheritPod); nil
	// until then, and always outside a pod.
	pod *schema.Pod
}

// Identity is a process incarnation as one /proc/<pid>/stat read saw it:
// its comm and its start time on the boot clock (pid_incarnation).
//
// Argv and Exe are the command line and executable of a process already
// running when the session took it on (task nh4): it has no exec event to
// carry them, so its attach event does (attach). They are raw — the
// caller read them from /proc/<pid>/cmdline and /proc/<pid>/exe, checked
// against StartNS — and the correlator redacts them before the event
// exists and keeps neither. Empty when unread: /proc could not be read,
// or an exec event follows (a pattern's exec adoption). ArgvCut says
// Argv is a bounded read's cut copy (target.MaxCmdline): its last
// element is a fragment the policy cannot judge, masked whole like a
// cut exec record's (ExecInfo.Cut).
type Identity struct {
	Comm    string
	StartNS uint64
	Argv    []string
	Exe     string
	ArgvCut bool
}

// Seed tells the correlator who pid is, read by the caller when the
// session took the process on: at session start for a -pid or port
// target (a pattern match goes through Incarnation). Call it before
// Handle sees the process's first record. Without it the correlator
// reads /proc only when that record is handled, and a correlator lagging
// past the target's exit and a reuse of its PID would give the target's
// events the new process's incarnation (and comm). Whatever state the
// correlator held under pid belongs to an earlier process and is
// replaced. The target gets an attach event (task ze4): its process node
// is in the graph even if it never makes a traced call, and a graph
// rebuilt from the events must have it too. The error is the sink's, or
// ErrEnded after End.
func (c *Correlator) Seed(pid uint32, id Identity) error {
	if c.ended {
		return ErrEnded
	}
	c.attach(pid, id, nil)
	return c.release(false)
}

// seed is Seed, returning the new state.
func (c *Correlator) seed(pid uint32, id Identity) *procState {
	delete(c.procs, pid)
	return c.newProc(pid, id)
}

// proc returns pid's state, registering a process seen for the first
// time with the identity /proc shows for it now (ProcInfo). That read is
// late by the correlator's lag, so the processes a session knows of
// before their first record are seeded instead (Seed, Incarnation, and
// newTask's check of the read against the fork record's time).
//
// The last process looked up is kept aside: an event's handling asks for
// its process two or three times, and a burst's events are mostly of one
// process (decision #25's budget, task ud4). newProc, the only place a
// pid's state is replaced, refreshes it; reap, which deletes a dead
// process's state, clears it.
func (c *Correlator) proc(pid uint32) *procState {
	if c.lastProc != nil && c.lastPID == pid {
		return c.lastProc
	}
	if p, ok := c.procs[pid]; ok {
		c.lastPID, c.lastProc = pid, p
		return p
	}
	comm, inc := c.cfg.ProcInfo(pid)
	return c.newProc(pid, Identity{Comm: comm, StartNS: inc})
}

// newProc registers pid as the incarnation id, in the state and the graph.
func (c *Correlator) newProc(pid uint32, id Identity) *procState {
	p := &procState{pid: pid, inc: id.StartNS, comm: id.Comm, clock: &c.recTime}
	p.resetFds()
	c.procs[pid] = p
	c.lastPID, c.lastProc = pid, p
	c.graph.process(pid, id.StartNS, id.Comm)
	return p
}

// resetFds gives p an empty fd table: fresh maps, so a dropped table
// (reapOne) also returns the buckets the old maps grew — Go maps never
// shrink, and clearing them would keep a busy parent's table per child.
func (p *procState) resetFds() {
	p.fds, p.lastGen = map[int32]uint32{}, map[int32]uint32{}
	p.sockOf, p.paths = map[int32]*sockState{}, map[int32]fdPath{}
	p.cloexec, p.flowsOf = map[int32]bool{}, map[int32][][2]netip.AddrPort{}
	p.born, p.past = map[int32]uint64{}, map[int32]pastInstance{}
}

// create registers a new fd instance returned by a traced syscall and
// returns its generation. A non-socket creation (open, dup) drops any
// stale socket binding of the number; accept keeps the one its snapshot
// record set just before. The new instance has no path and no known
// close-on-exec state until its creator sets them: the number's old ones
// belonged to the instance it replaces, which is retired as the number's
// past instance (aggregate.go).
func (p *procState) create(fd int32, socket bool) uint32 {
	p.retire(fd)
	gen := p.lastGen[fd] + 1
	p.lastGen[fd] = gen
	p.fds[fd] = gen
	p.born[fd] = *p.clock
	if !socket {
		delete(p.sockOf, fd)
	}
	delete(p.paths, fd)
	delete(p.cloexec, fd)
	return gen
}

// use returns the generation of an fd a syscall acts on. An fd never seen
// predates the trace (gen 0); one seen and closed before was recreated by
// an untraced syscall, so it is a new instance (gen + 1).
func (p *procState) use(fd int32) uint32 {
	if gen, ok := p.fds[fd]; ok {
		return gen
	}
	gen := uint32(0)
	if last, ok := p.lastGen[fd]; ok {
		gen = last + 1
	}
	p.lastGen[fd] = gen
	p.fds[fd] = gen
	return gen
}

// nextGen is the generation the next creation of fd will get, for the
// accept snapshot that arrives just before its accept exit.
func (p *procState) nextGen(fd int32) uint32 {
	return p.lastGen[fd] + 1
}

// sockGen binds fd to socket sk on the socket's first record and returns
// the fd generation a tcp_state event reports:
//   - the accept snapshot precedes the accept exit that creates the fd, so
//     it reports the generation that creation will get (use() would
//     register, and maybe bump, the number before it exists);
//   - the first record of a connecting socket (CLOSE -> SYN_SENT, inside
//     connect) binds the fd the connect acts on;
//   - later transitions report the instance bound then (kept on the
//     socket), even once the fd was closed (TCP_CLOSE often follows
//     close(2)) — never re-binding a number another open may reuse.
//
// If the accept exit after a snapshot is lost, the fd is never created:
// the snapshot reported last+1 while later uses of the number report the
// use() rule's generation. The two then disagree; a drop event already
// marks that gap.
func (p *procState) sockGen(fd int32, s *sockState, first, snapshot bool) uint32 {
	switch {
	case snapshot:
		p.sockOf[fd] = s
		s.fdGen = p.nextGen(fd)
	case first:
		p.sockOf[fd] = s
		s.fdGen = p.use(fd)
	}
	return s.fdGen
}

// close forgets a released fd instance. lastGen keeps the number's
// generation, so whatever reuses the number is a new instance; past keeps
// the instance itself for a folded record that arrives late (retire).
func (p *procState) close(fd int32) {
	p.retire(fd)
	delete(p.born, fd)
	delete(p.fds, fd)
	delete(p.sockOf, fd)
	delete(p.paths, fd)
	delete(p.cloexec, fd)
}

// ReadProcInfo reads comm and start time from one read of
// /proc/<pid>/stat, so the two always describe the same incarnation. The
// start time has clock-tick resolution, which is enough to tell PID reuses
// apart. A process already gone yields ("", 0): its events still carry
// its pid.
func ReadProcInfo(pid uint32) (string, uint64) {
	st, err := target.HostProcFS().Stat(pid)
	if err != nil {
		return "", 0
	}
	return st.Comm, st.StartNS
}

// BootToWallOffset measures wall-clock minus boot-clock time now, the
// Config.WallOffsetNS that converts BPF timestamps (bpf_ktime_get_boot_ns)
// to wall time. Measured once per session: the offset drifts only when the
// wall clock is stepped, and a stepped clock must not reorder events.
func BootToWallOffset() (int64, error) {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts); err != nil {
		return 0, err
	}
	return time.Now().UnixNano() - ts.Nano(), nil
}
