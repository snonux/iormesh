package correlate

import (
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// markerRoot is the reserved, never-existing directory the workload opens
// to bracket a scenario (workload/src/iormesh/workload/Marker.java).
const markerRoot = "/iormesh-marker/"

// shape is the record layout an enter came in, which decides how its exit
// becomes an event: the layout says which arguments the enter carries.
type shape uint8

const (
	shapeFd   shape = iota // fd_event and its widened forms: one fd argument
	shapeOpen              // open_event: dirfd, path, flags; returns an fd
	shapePath              // path_event: dirfd, path, flags (files.go)
	shapeName              // name_event: two dirfd/path pairs (files.go)
	// shapeIPC: the M2 step 7 records with their own layout — pipe and
	// socketpair, kill's null_event, epoll_ctl, the poll waits, epoll
	// create (ipc.go). bind, sendto, recvfrom and the zero-copy calls are
	// plain fd records and stay shapeFd.
	shapeIPC
)

// enter is a syscall enter waiting for its exit on the same thread. A
// thread runs one syscall at a time, so the tid alone keys it. fd is the
// fd argument, or the dirfd of the path shapes; newDirfd and newPath are
// the second name of a name_event. flags holds the syscall's flags (open,
// accept4, dup3) or fcntl's command, arg fcntl's third argument. fd2 is a
// second descriptor (a pipe's write end, epoll_ctl's target); addr and sig
// are the side records of M2 step 7 that arrived while the call ran
// (sock_addr_event, signal_event), nil when none did.
type enter struct {
	name     string // syscall name, e.g. "openat"
	shape    shape
	time     uint64
	pid      uint32
	fd       int32
	newDirfd int32
	path     string
	newPath  string
	// pathKnown is false when the path read faulted (and no fixup came
	// yet): an empty path then is unknown, not AT_EMPTY_PATH's "the fd
	// itself".
	pathKnown bool
	flags     int32
	arg       uint64
	fd2       int32
	addr      *sockAddr
	sig       *sigInfo
}

func (c *Correlator) enterOpen(r *types.OpenEvent) {
	c.advance(r.Time)
	p := c.proc(r.Pid)
	if comm := types.StringValue(r.Comm[:]); comm != "" && p.comm == "" {
		p.comm = comm
	}
	c.enters[r.Tid] = enter{name: r.TraceId.Name(), shape: shapeOpen, time: r.Time,
		pid: r.Pid, fd: r.Dirfd, path: types.StringValue(r.Filename[:]),
		pathKnown: r.FilenameStatus != types.PATH_READ_FAILED, flags: r.Flags}
}

// fixupName applies the path the BPF side re-read at sys_exit after the
// enter-time read faulted. It is submitted before the exit record, so the
// enter is still pending (filter.c, ior_emit_name_fixup). The second slot
// is a name_event's new name.
func (c *Correlator) fixupName(r *types.OpenNameFixupEvent) {
	en, ok := c.enters[r.Tid]
	if !ok || en.name != r.TraceId.Name() {
		return
	}
	switch r.Slot {
	case types.OPEN_NAME_FIXUP_SLOT_FIRST:
		en.path, en.pathKnown = types.StringValue(r.Filename[:]), true
	case types.OPEN_NAME_FIXUP_SLOT_SECOND:
		en.newPath = types.StringValue(r.Filename[:])
	default:
		return
	}
	c.enters[r.Tid] = en
}

func (c *Correlator) enterFd(r *types.FdEvent) {
	c.advance(r.Time)
	c.enters[r.Tid] = enter{name: r.TraceId.Name(), shape: shapeFd, time: r.Time,
		pid: r.Pid, fd: r.Fd}
}

// accept handles both halves: the enter carries nothing the exit needs
// but its start time; the exit's ret is the new fd.
func (c *Correlator) accept(r *types.AcceptEvent) {
	c.advance(r.Time)
	if r.EventType == types.ENTER_ACCEPT_EVENT {
		c.enters[r.Tid] = enter{name: r.TraceId.Name(), time: r.Time, pid: r.Pid,
			fd: schema.NoFD, flags: r.Flags}
		return
	}
	c.exit(r.Tid, r.TraceId.Name(), r.Ret, r.Time)
}

func (c *Correlator) exitRet(r *types.RetEvent) {
	c.advance(r.Time)
	c.exit(r.Tid, r.TraceId.Name(), r.Ret, r.Time)
}

// exit pairs an exit with its thread's pending enter and queues the event.
// An exit without a matching enter is counted, not guessed at.
func (c *Correlator) exit(tid uint32, name string, ret int64, at uint64) {
	en, ok := c.enters[tid]
	delete(c.enters, tid)
	if !ok || en.name != name {
		c.unpaired++
		return
	}
	ev := c.base(en, tid, ret, at)
	sock, ok := c.syscallDetail(&ev, en, ret)
	if !ok {
		return
	}
	c.enqueue(ev, sock)
}

// base fills the envelope fields every syscall event shares.
func (c *Correlator) base(en enter, tid uint32, ret int64, at uint64) schema.Event {
	p := c.proc(en.pid)
	ev := schema.Event{
		SchemaVersion: schema.Version, Project: c.cfg.Host.Project, HostID: c.cfg.Host.ID,
		BootNS: en.time, WallNS: c.wall(en.time), PID: en.pid, PIDInc: p.inc, TID: tid,
		Comm: p.comm, Event: en.name, FD: schema.NoFD, Ret: ret,
		Attribution: schema.AttributionNone,
	}
	if at > en.time {
		ev.LatencyNS = at - en.time
	}
	if ret < 0 {
		ev.Err = unix.ErrnoName(syscall.Errno(-ret))
	}
	// Native stacks are opt-in: without them the map is always empty and
	// the length check is all an event pays (decision #25).
	if len(c.stacks) != 0 {
		ev.StackID = c.takeStack(tid, en.time)
	}
	return ev
}

func (c *Correlator) wall(bootNS uint64) uint64 {
	return uint64(int64(bootNS) + c.cfg.WallOffsetNS)
}

// syscallDetail sets the kind-specific fields and updates fd state. It
// returns the socket the event acts on, if any, so the queue can hold the
// event until that socket's conn id is known, and false when the event
// must not be emitted. The shape switch comes first and is an integer
// compare: read and write, the hot kinds, pay for nothing they don't use
// (decision #25).
func (c *Correlator) syscallDetail(ev *schema.Event, en enter, ret int64) (*sockState, bool) {
	p := c.proc(en.pid)
	switch en.shape {
	case shapeOpen:
		return nil, c.openDetail(ev, p, en, ret)
	case shapePath:
		return nil, c.pathDetail(ev, p, en, ret)
	case shapeName:
		c.nameDetail(ev, p, en, ret)
		return nil, true
	case shapeIPC:
		return c.ipcDetail(ev, p, en, ret)
	}
	switch en.name {
	case schema.KindAccept, schema.KindAccept4:
		return c.acceptDetail(ev, p, en, ret), true
	}
	ev.FD, ev.FDGen = en.fd, p.use(en.fd)
	sock := p.sockOf[en.fd]
	switch en.name {
	case schema.KindRead, schema.KindWrite:
		if ret > 0 {
			ev.Bytes = uint64(ret)
		}
	case schema.KindClose:
		// Linux releases the fd even when close fails (EINTR, EIO); only
		// EBADF means there was nothing to release.
		if ret != -int64(syscall.EBADF) {
			p.close(en.fd)
			c.dropFlows(p, en.fd)
		}
	case schema.KindConnect:
		sock = c.connectDetail(ev, p, en, sock)
	case schema.KindBind, schema.KindSendto, schema.KindRecvfrom,
		schema.KindSendfile64, schema.KindSplice, schema.KindTee, schema.KindCopyFileRange:
		sock = c.socketFdDetail(ev, p, en, ret, sock)
	default:
		fdFileDetail(ev, p, en, ret)
	}
	return sock, true
}

// openDetail turns an open into a file event, or a marker event when it
// targets the reserved marker root. A dirfd-relative path is resolved
// against the dirfd's path when the trace knows it. An open whose path is
// unknown (the read faulted and was not recovered) still creates its fd,
// but is not emitted: schema requires args.path, and a file node "" would
// be a lie.
func (c *Correlator) openDetail(ev *schema.Event, p *procState, en enter, ret int64) bool {
	if name, ok := strings.CutPrefix(en.path, markerRoot); ok && name != "" {
		ev.Event, ev.Args.Marker = schema.KindMarker, &name
		return true
	}
	if ret >= 0 {
		ev.FD, ev.FDGen = int32(ret), p.create(int32(ret), false)
	}
	if en.path == "" {
		c.noPath++
		return false
	}
	path, flags := p.resolve(en.fd, en.path, true), uint32(en.flags)
	ev.Args.Path, ev.Args.Flags = &path, &flags
	if ret >= 0 {
		p.setPath(int32(ret), path)
		p.openCloexec(int32(ret), en.flags)
		c.graph.opened(ev.PID, ev.PIDInc, path)
	}
	return true
}

// acceptDetail registers the accepted fd. A TCP socket was bound by the
// snapshot record the BPF side emits right before the accept exit; an
// AF_UNIX one by its sock_addr record (en.addr), which joins it with the
// connecting client's end (ipc.go). Its close-on-exec state is accept4's
// SOCK_CLOEXEC (accept's enter flags are 0).
func (c *Correlator) acceptDetail(ev *schema.Event, p *procState, en enter, ret int64) *sockState {
	if ret < 0 {
		return nil
	}
	fd := int32(ret)
	ev.FD, ev.FDGen = fd, p.create(fd, true)
	p.openCloexec(fd, en.flags)
	if en.addr != nil {
		return c.unixEnd(p, fd, en)
	}
	sock := p.sockOf[fd]
	sock.fillArgs(&ev.Args)
	return sock
}
