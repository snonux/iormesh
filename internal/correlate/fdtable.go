package correlate

import (
	"maps"

	"golang.org/x/sys/unix"

	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// The fd lifecycle (M2 step 6, PLAN.md "Descriptor lifecycle"): copies
// (dup*, fcntl F_DUPFD*), inheritance across fork and the closes an exec
// makes. Each per-process fd table is the procState maps (fds, lastGen,
// sockOf, paths, cloexec); an fd instance is (pid, incarnation, fd,
// fd_gen), and everything here keeps that identity from aliasing: a copy
// is a new instance of its number, a closed number's next instance gets a
// new generation.
//
// The model follows ior's fdTracker (internal/eventloop_state.go: Dup on
// dup*/fcntl, inherit/copyTable on fork, dropOnExec/survivesExec on exec)
// with iormesh's generation numbers instead of ior's procfs fallback.

// enterDup3 is dup3's own record layout: the old fd and the flags, whose
// O_CLOEXEC the copy gets.
func (c *Correlator) enterDup3(r *types.Dup3Event) {
	c.advance(r.Time)
	c.enters[r.Tid] = enter{name: r.TraceId.Name(), shape: shapeFd, time: r.Time,
		pid: r.Pid, fd: r.Fd, flags: r.Flags}
}

// enterFcntl keeps fcntl's command and argument for its exit: what the
// call did to the fd table depends on both (fcntlDetail). The record's fd
// is the raw unsigned argument; as int32 an invalid one stays invalid.
func (c *Correlator) enterFcntl(r *types.FcntlEvent) {
	c.advance(r.Time)
	c.enters[r.Tid] = enter{name: r.TraceId.Name(), shape: shapeFd, time: r.Time,
		pid: r.Pid, fd: int32(r.Fd), flags: int32(r.Cmd), arg: r.Arg}
}

// dupDetail registers a copy's new descriptor (ret) as a new instance of
// its number naming what the old one names: its path and its socket, so
// the copy's reads and writes join the same file and connection. The copy
// shares the open file description, not the descriptor flags: its
// close-on-exec state is the one the call gives it (clear for dup, dup2
// and F_DUPFD; dup3's O_CLOEXEC; set for F_DUPFD_CLOEXEC). dup2 onto
// itself returns the same fd and creates nothing; a copy onto an open
// number closes that instance first, which create's new generation is.
func dupDetail(p *procState, oldfd int32, ret int64, cloexec bool) {
	if ret < 0 || int32(ret) == oldfd {
		return
	}
	newfd := int32(ret)
	fp, hasPath := p.paths[oldfd]
	sock := p.sockOf[oldfd]
	p.create(newfd, false)
	if hasPath {
		p.paths[newfd] = fp
	}
	if sock != nil {
		p.sockOf[newfd] = sock
	}
	p.cloexec[newfd] = cloexec
}

// fcntlDetail records fcntl's command and applies the commands that
// change or reveal the fd table: the F_DUPFD forms copy (ret is the new
// fd), F_SETFD sets the close-on-exec state and F_GETFD reports it. The
// JDK asks F_GETFD of the fds it redirects onto a child's stdio before it
// spawns one, so their state is known exactly where exec needs it; the
// pipes it hands its spawn helper stay unknown (only the helper queries
// them, after the exec already dropped them from the table).
// The other commands (F_GETFL, F_SETFL, locks, …) leave the table alone.
func fcntlDetail(ev *schema.Event, p *procState, en enter, ret int64) {
	cmd := uint32(en.flags)
	ev.Args.Cmd = &cmd
	if ret < 0 {
		return
	}
	switch cmd {
	case unix.F_DUPFD:
		dupDetail(p, en.fd, ret, false)
	case unix.F_DUPFD_CLOEXEC:
		dupDetail(p, en.fd, ret, true)
	case unix.F_SETFD:
		p.cloexec[en.fd] = en.arg&unix.FD_CLOEXEC != 0
	case unix.F_GETFD:
		p.cloexec[en.fd] = ret&unix.FD_CLOEXEC != 0
	}
}

// openCloexec records an opened or accepted fd's close-on-exec state:
// the call's flags say it exactly (O_CLOEXEC; SOCK_CLOEXEC has the same
// value).
func (p *procState) openCloexec(fd int32, flags int32) {
	p.cloexec[fd] = flags&unix.O_CLOEXEC != 0
}

// forked gives a traced process's new child (newTask) a copy of the
// parent's fd table and records the fork as a forked edge, which the
// normalizer's bracket cut follows. creator is the clone caller's tgid, 0
// when the record could not say. The copy keeps the parent's generations
// and lastGen: an inherited fd is the parent's instance, seen from a new
// process, and the child's next instance of a number continues after the
// parent's (one lineage, so no generation is reused for another file).
//
// A child sharing the table (CLONE_FILES without CLONE_THREAD, rare: the
// JDK's posix_spawn uses CLONE_VM|CLONE_VFORK) gets a copy as well; its
// later changes then do not reach the parent's view.
func (c *Correlator) forked(creator uint32, child *procState, pid uint32) {
	if creator == 0 || creator == pid {
		return
	}
	parent := c.proc(creator)
	child.inherit(parent)
	c.graph.forked(creator, parent.inc, pid, child.inc)
}

// inherit replaces p's (fresh) fd table with a copy of parent's. Sockets
// are shared, not copied: parent and child hold the same connection. The
// shared *sockState also shares its fdGen, so a socket first bound after
// the fork (rare) reports one process's generation for both.
func (p *procState) inherit(parent *procState) {
	p.fds = maps.Clone(parent.fds)
	p.lastGen = maps.Clone(parent.lastGen)
	p.sockOf = maps.Clone(parent.sockOf)
	p.paths = maps.Clone(parent.paths)
	p.cloexec = maps.Clone(parent.cloexec)
	p.born = maps.Clone(parent.born)
	p.past = maps.Clone(parent.past)
}

// execTask applies a successful exec (sched_process_exec, emitted for
// traced processes only) to the process's fd table: the kernel closed
// every close-on-exec fd (do_close_on_exec). Without this, the new
// program reusing such a number through an untraced syscall would inherit
// the old program's instance, path and socket.
func (c *Correlator) execTask(r *types.ProcessExecEvent) {
	c.advance(r.Time)
	if p, ok := c.procs[r.Pid]; ok {
		p.closeOnExec()
	}
}

// closeOnExec closes the fds an exec closed: those whose close-on-exec
// state is set or unknown (survivesExec). Deleting from a map while
// ranging over it is safe in Go.
func (p *procState) closeOnExec() {
	for fd := range p.fds {
		if !p.survivesExec(fd) {
			p.close(fd)
		}
	}
}

// survivesExec reports whether fd stays open across an exec. A known
// state decides. An unknown one (the fd predates the trace or came from an
// untraced syscall — socket, pipe, …) counts as closed, as in ior: keeping
// an fd the kernel closed would hand its path and socket to whatever the
// new program puts on the number, while dropping one that survived gives
// its next use a new generation and loses its path and socket binding — an
// inherited socket (fd > 2) not opened by a traced syscall loses its
// conn_id after the exec. Unknown stdio (0–2) is the
// exception: it is inherited by convention and almost never close-on-exec,
// and a new generation on every exec's stdout would split one instance.
func (p *procState) survivesExec(fd int32) bool {
	if set, known := p.cloexec[fd]; known {
		return !set
	}
	return fd >= 0 && fd < 3
}
