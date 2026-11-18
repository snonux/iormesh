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

// enter is a syscall enter waiting for its exit on the same thread. A
// thread runs one syscall at a time, so the tid alone keys it.
type enter struct {
	name  string // syscall name, e.g. "openat"
	time  uint64
	pid   uint32
	fd    int32
	path  string
	flags int32
}

func (c *Correlator) enterOpen(r *types.OpenEvent) {
	c.advance(r.Time)
	p := c.proc(r.Pid)
	if comm := types.StringValue(r.Comm[:]); comm != "" && p.comm == "" {
		p.comm = comm
	}
	c.enters[r.Tid] = enter{name: r.TraceId.Name(), time: r.Time, pid: r.Pid, fd: schema.NoFD,
		path: types.StringValue(r.Filename[:]), flags: r.Flags}
}

// fixupName applies the path the BPF side re-read at sys_exit after the
// enter-time read faulted. It is submitted before the exit record, so the
// enter is still pending (filter.c, ior_emit_name_fixup). Only the first
// path slot exists for the open family.
func (c *Correlator) fixupName(r *types.OpenNameFixupEvent) {
	en, ok := c.enters[r.Tid]
	if !ok || r.Slot != types.OPEN_NAME_FIXUP_SLOT_FIRST || en.name != r.TraceId.Name() {
		return
	}
	en.path = types.StringValue(r.Filename[:])
	c.enters[r.Tid] = en
}

func (c *Correlator) enterFd(r *types.FdEvent) {
	c.advance(r.Time)
	c.enters[r.Tid] = enter{name: r.TraceId.Name(), time: r.Time, pid: r.Pid, fd: r.Fd}
}

// accept handles both halves: the enter carries nothing the exit needs
// but its start time; the exit's ret is the new fd.
func (c *Correlator) accept(r *types.AcceptEvent) {
	c.advance(r.Time)
	if r.EventType == types.ENTER_ACCEPT_EVENT {
		c.enters[r.Tid] = enter{name: r.TraceId.Name(), time: r.Time, pid: r.Pid, fd: schema.NoFD}
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
	return ev
}

func (c *Correlator) wall(bootNS uint64) uint64 {
	return uint64(int64(bootNS) + c.cfg.WallOffsetNS)
}

// syscallDetail sets the kind-specific fields and updates fd state. It
// returns the socket the event acts on, if any, so the queue can hold the
// event until that socket's conn id is known, and false when the event
// must not be emitted.
func (c *Correlator) syscallDetail(ev *schema.Event, en enter, ret int64) (*sockState, bool) {
	p := c.proc(en.pid)
	switch en.name {
	case schema.KindOpen, schema.KindOpenat, schema.KindOpenat2:
		return nil, c.openDetail(ev, p, en, ret)
	case schema.KindAccept, schema.KindAccept4:
		return c.acceptDetail(ev, p, ret), true
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
		}
	case schema.KindConnect:
		sock.fillArgs(&ev.Args)
	}
	return sock, true
}

// openDetail turns an open into a file event, or a marker event when it
// targets the reserved marker root. An open whose path is unknown (the
// read faulted and was not recovered) still creates its fd, but is not
// emitted: schema requires args.path, and a file node "" would be a lie.
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
	path, flags := en.path, uint32(en.flags)
	ev.Args.Path, ev.Args.Flags = &path, &flags
	if ret >= 0 {
		c.graph.opened(ev.PID, ev.PIDInc, path)
	}
	return true
}

// acceptDetail registers the accepted fd. Its socket was bound by the
// snapshot record the BPF side emits right before the accept exit.
func (c *Correlator) acceptDetail(ev *schema.Event, p *procState, ret int64) *sockState {
	if ret < 0 {
		return nil
	}
	fd := int32(ret)
	ev.FD, ev.FDGen = fd, p.create(fd, true)
	sock := p.sockOf[fd]
	sock.fillArgs(&ev.Args)
	return sock
}
