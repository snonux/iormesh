package correlate

import (
	"fmt"
	"net/netip"

	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// M2 step 7 (PLAN.md "What we trace": local IPC, UDP, poll/epoll): the
// records of the ipc and poll families and the socket addresses of the
// socket family. Connections other than TCP become sockStates too, so their
// events wait for a conn id and draw connected edges exactly like TCP's:
//   - a pipe or socketpair: both ends are made by one call, so it is one
//     connection, fused at once; a child that inherits an end (fork, dup2
//     onto its stdio) joins the same node;
//   - a unix stream connection: the connecting socket and the accepted
//     one point at each other (unix_sk->peer), and that pair is the key
//     (unixTable);
//   - a UDP flow: UDP has no connection, so the unit is one local/remote
//     address pair, seen from either side; the two sides of a loopback flow
//     mirror each other and fuse within the window like TCP's ends.

// Socket types and AF_UNIX. The close-on-exec flag of pipe2's flags,
// socketpair's type and epoll_create1's flags is O_CLOEXEC's one value
// (openCloexec).
const (
	sockStream = 1
	sockDgram  = 2
	afUnix     = 1
)

// sockAddr is what a sock_addr_event said about one call (types.h): the
// socket's own addresses, its address argument and, for AF_UNIX, its path
// and the kernel's pairing.
type sockAddr struct {
	family, sockType uint32
	local, remote    netip.AddrPort // the socket's, from the kernel
	arg              netip.AddrPort // the address argument; invalid if none
	path             string         // AF_UNIX argument; "@name" if abstract
	sk, peer         uint64
}

// handleIPC dispatches the M2 step 7 records and reports whether rec was
// one of them.
func (c *Correlator) handleIPC(rec any) bool {
	switch r := rec.(type) {
	case *types.PipeEvent:
		c.pairRecord(r.EventType == types.ENTER_PIPE_EVENT, r.TraceId, r.Time, r.Pid, r.Tid,
			r.Flags, r.Fd0, r.Fd1, r.Ret)
	case *types.SocketpairEvent:
		c.pairRecord(r.EventType == types.ENTER_SOCKETPAIR_EVENT, r.TraceId, r.Time, r.Pid, r.Tid,
			r.Type, r.Sv0, r.Sv1, r.Ret)
	case *types.NullEvent:
		if r.EventType == types.ENTER_NULL_EVENT {
			c.enterIPC(r.TraceId, r.Time, r.Pid, r.Tid, schema.NoFD, schema.NoFD, 0)
		}
	case *types.EpollCtlEvent:
		c.enterIPC(r.TraceId, r.Time, r.Pid, r.Tid, r.Epfd, r.Fd, r.Op)
	case *types.PollEvent:
		c.enterIPC(r.TraceId, r.Time, r.Pid, r.Tid, r.Fd, schema.NoFD, 0)
	case *types.EventfdEvent:
		c.eventfdRecord(r)
	case *types.SockAddrEvent:
		c.sockAddrRecord(r)
	case *types.SignalEvent:
		c.signalRecord(r)
	default:
		return false
	}
	return true
}

// enterIPC parks one shapeIPC enter: fd is the call's descriptor (NoFD for
// kill and poll), fd2 a second one, flags the call's flags or op.
func (c *Correlator) enterIPC(id types.TraceId, at uint64, pid, tid uint32, fd, fd2, flags int32) {
	c.advance(at)
	c.enters[tid] = enter{name: id.Name(), shape: shapeIPC, time: at, pid: pid,
		fd: fd, fd2: fd2, flags: flags}
}

// pairRecord handles both halves of pipe, pipe2 and socketpair: the exit
// carries the two new descriptors, which the pending enter takes before
// the exit is paired. flags is pipe2's flags or socketpair's type, whose
// O_CLOEXEC/SOCK_CLOEXEC (one value) the ends get.
func (c *Correlator) pairRecord(isEnter bool, id types.TraceId, at uint64, pid, tid uint32,
	flags, fd0, fd1 int32, ret int64) {
	if isEnter {
		c.enterIPC(id, at, pid, tid, schema.NoFD, schema.NoFD, flags)
		return
	}
	c.advance(at)
	if en, ok := c.enters[tid]; ok && en.name == id.Name() {
		en.fd, en.fd2 = fd0, fd1
		c.enters[tid] = en
	}
	c.exit(tid, id.Name(), ret, at)
}

// eventfdRecord handles epoll_create and epoll_create1, which use the
// eventfd record layout on both sides; the exit carries the new fd as ret.
func (c *Correlator) eventfdRecord(r *types.EventfdEvent) {
	if r.EventType == types.ENTER_EVENTFD_EVENT {
		c.enterIPC(r.TraceId, r.Time, r.Pid, r.Tid, schema.NoFD, schema.NoFD, r.Flags)
		return
	}
	c.advance(r.Time)
	c.exit(r.Tid, r.TraceId.Name(), r.Ret, r.Time)
}

// sockAddrRecord attaches a socket call's addresses to its pending enter;
// the record comes right before the exit (sockaddr.c). One whose enter is
// gone (lost, or another call's) is dropped: nothing could carry it.
func (c *Correlator) sockAddrRecord(r *types.SockAddrEvent) {
	c.advance(r.Time)
	en, ok := c.enters[r.Tid]
	if !ok || en.name != r.TraceId.Name() {
		return
	}
	en.addr = decodeSockAddr(r)
	c.enters[r.Tid] = en
}

// decodeSockAddr converts the record's fixed-size fields.
func decodeSockAddr(r *types.SockAddrEvent) *sockAddr {
	a := &sockAddr{family: r.Family, sockType: r.SockType, sk: r.Sk, peer: r.Peer}
	a.local = addrPort(r.Family, r.LocalAddr, r.LocalPort)
	a.remote = addrPort(r.Family, r.RemoteAddr, r.RemotePort)
	a.arg = addrPort(r.ArgFamily, r.ArgAddr, r.ArgPort)
	switch r.PathKind {
	case types.SOCK_ADDR_PATH_NAMED:
		a.path = types.StringValue(r.Path[:])
	case types.SOCK_ADDR_PATH_ABSTRACT:
		a.path = "@" + types.StringValue(r.Path[:])
	}
	return a
}

// addrPort decodes one inet address of the record; IPv4-mapped IPv6 is
// unmapped, as for TCP. Other families yield an invalid AddrPort.
func addrPort(family uint32, addr [types.TCP_ADDR_LEN]byte, port uint32) netip.AddrPort {
	switch family {
	case afInet:
		return netip.AddrPortFrom(netip.AddrFrom4([4]byte(addr[:4])), uint16(port))
	case afInet6:
		return netip.AddrPortFrom(netip.AddrFrom16(addr).Unmap(), uint16(port))
	}
	return netip.AddrPort{}
}

// ipcDetail fills the shapeIPC kinds; it returns the connection the event
// belongs to (a pipe's), if any.
func (c *Correlator) ipcDetail(ev *schema.Event, p *procState, en enter, ret int64) (*sockState, bool) {
	switch en.name {
	case schema.KindPipe, schema.KindPipe2, schema.KindSocketpair:
		return c.pairDetail(ev, p, en, ret), true
	case schema.KindKill, schema.KindTgkill, schema.KindTkill:
		en.sig.fillKill(&ev.Args)
	case schema.KindEpollCreate, schema.KindEpollCreate1:
		if ret >= 0 {
			ev.FD, ev.FDGen = int32(ret), p.create(int32(ret), false)
			if en.name == schema.KindEpollCreate1 { // epoll_create's arg is a size
				p.openCloexec(int32(ret), en.flags)
			}
		}
	case schema.KindEpollCtl:
		if en.fd >= 0 {
			ev.FD, ev.FDGen = en.fd, p.use(en.fd)
		}
		op := uint32(en.flags)
		ev.Args.Cmd = &op
		if en.fd2 >= 0 { // a negative one names no instance (EBADF)
			fd2, gen2 := en.fd2, p.use(en.fd2)
			ev.Args.FD2, ev.Args.FD2Gen = &fd2, &gen2
		}
	default: // the waits: epoll_* on an epoll fd, poll and ppoll on none
		if en.fd >= 0 {
			ev.FD, ev.FDGen = en.fd, p.use(en.fd)
		}
	}
	return nil, true
}

// pairDetail creates both ends of a pipe or socketpair as new fd instances
// naming one connection, fused at once: the creator holds both ends, and
// whoever inherits one (a child's stdio) joins the same node. The event's
// fd is the first end (a pipe's read end), args.fd2 the second.
func (c *Correlator) pairDetail(ev *schema.Event, p *procState, en enter, ret int64) *sockState {
	if ret < 0 || en.fd < 0 || en.fd2 < 0 {
		return nil
	}
	s := &sockState{firstSeen: en.time, established: en.time, fused: true,
		connID: hashID(fmt.Sprintf("pair|%d|%d|%d|%d|%d", en.pid, p.inc, en.fd, en.fd2, en.time))}
	gen1 := p.create(en.fd, false)
	gen2 := p.create(en.fd2, false)
	for _, fd := range []int32{en.fd, en.fd2} {
		p.sockOf[fd] = s
		p.openCloexec(fd, en.flags)
	}
	fd2 := en.fd2
	ev.FD, ev.FDGen = en.fd, gen1
	ev.Args.FD2, ev.Args.FD2Gen = &fd2, &gen2
	return s
}

// connectDetail fills a connect: a TCP one from its socket's 4-tuple
// (tcp_state records), an AF_UNIX one with its path and, for a stream
// socket, the join with the accepted end; an inet datagram connect with
// the address it connected to.
func (c *Correlator) connectDetail(ev *schema.Event, p *procState, en enter, sock *sockState) *sockState {
	a := en.addr
	if a == nil {
		sock.fillArgs(&ev.Args)
		return sock
	}
	if a.family == afUnix {
		setPath(&ev.Args, a.path)
		if a.sockType == sockStream && a.peer != 0 {
			return c.unixEnd(p, en.fd, en)
		}
		return sock
	}
	setLocal(&ev.Args, a.local)
	setRemote(&ev.Args, a.arg)
	return sock
}

// socketFdDetail fills bind, the datagram calls and the zero-copy calls:
// bytes for what moved, and the addresses of their sock_addr record.
func (c *Correlator) socketFdDetail(ev *schema.Event, p *procState, en enter, ret int64,
	sock *sockState) *sockState {
	if ret > 0 && en.name != schema.KindBind {
		ev.Bytes = uint64(ret)
	}
	a := en.addr
	switch {
	case a == nil:
		return sock
	case en.name == schema.KindBind:
		bindArgs(&ev.Args, a)
		return sock
	case sock != nil: // a connected TCP or unix socket: its conn says it all
		sock.fillArgs(&ev.Args)
		return sock
	case a.family == afUnix:
		setPath(&ev.Args, a.path)
		return nil
	}
	return c.datagramDetail(ev, p, en, a, ret)
}

// bindArgs: an AF_UNIX bind names its socket file; an inet one its local
// address — the socket's after the call, which has the port the kernel
// picked for port 0, else the argument (a failed bind).
func bindArgs(args *schema.Args, a *sockAddr) {
	if a.family == afUnix {
		setPath(args, a.path)
		return
	}
	if a.local.IsValid() && a.local.Port() != 0 {
		setLocal(args, a.local)
		return
	}
	setLocal(args, a.arg)
}

func setPath(args *schema.Args, path string) {
	if path != "" {
		args.Path = &path
	}
}

// setLocal sets the local address and port; a wildcard address (an
// unbound or INADDR_ANY socket) is no address, so only the port is set.
func setLocal(args *schema.Args, ap netip.AddrPort) {
	if !ap.IsValid() {
		return
	}
	port := ap.Port()
	args.LocalPort = &port
	if !ap.Addr().IsUnspecified() {
		addr := ap.Addr().String()
		args.LocalAddr = &addr
	}
}

func setRemote(args *schema.Args, ap netip.AddrPort) {
	if !ap.IsValid() || ap.Port() == 0 {
		return
	}
	addr, port := ap.Addr().String(), ap.Port()
	args.RemoteAddr, args.RemotePort = &addr, &port
}

// unixEnd binds fd to its end of a unix stream connection: the accepted
// socket and the connecting one share a key, so whichever comes second
// fuses with the first (unixTable.join).
func (c *Correlator) unixEnd(p *procState, fd int32, en enter) *sockState {
	a := en.addr
	if a.family != afUnix || a.sockType != sockStream || a.sk == 0 || a.peer == 0 {
		return p.sockOf[fd]
	}
	s := c.unix.join(a.sk, a.peer, en.time)
	p.sockOf[fd] = s
	return s
}

// datagramDetail fills one UDP datagram's addresses — the socket's local
// one and the peer it went to or came from (the argument, or the socket's
// remote end when it is connected) — and returns its flow.
func (c *Correlator) datagramDetail(ev *schema.Event, p *procState, en enter, a *sockAddr,
	ret int64) *sockState {
	remote := a.arg
	if !remote.IsValid() {
		remote = a.remote
	}
	setLocal(&ev.Args, a.local)
	setRemote(&ev.Args, remote)
	if a.sockType != sockDgram || ret < 0 || !a.local.IsValid() || !remote.IsValid() {
		return nil
	}
	return c.flow(p, en.fd, flowLocal(a.local, remote), remote, en.time)
}

// flowLocal is the local end a flow is keyed by. A socket bound to the
// wildcard address sends from an address the route picks; towards a
// loopback peer that is the peer's own address (127.0.0.1, ::1), so the
// two sides of a loopback flow key alike. Towards anything else the
// wildcard stays and the flow cannot fuse — it is unresolved, not guessed.
func flowLocal(local, remote netip.AddrPort) netip.AddrPort {
	if local.Addr().IsUnspecified() && remote.Addr().IsLoopback() {
		return netip.AddrPortFrom(remote.Addr(), local.Port())
	}
	return local
}

// flow returns the UDP flow of (local, remote), creating it on its first
// datagram, owned by the fd that carried it (dropFlows ends it with that
// fd). A new flow waits in the datagram table for its mirror image — the
// other side of a loopback exchange — like a TCP end does.
func (c *Correlator) flow(p *procState, fd int32, local, remote netip.AddrPort, at uint64) *sockState {
	k := [2]netip.AddrPort{local, remote}
	if s, ok := c.flows[k]; ok {
		return s
	}
	s := &sockState{local: local, remote: remote, firstSeen: at, established: at,
		table: c.datagram}
	c.flows[k] = s
	p.flowsOf[fd] = append(p.flowsOf[fd], k)
	c.datagram.fuse(s)
	return s
}

// dropFlows ends the UDP flows fd started when it is closed, so the flow
// table does not grow with every peer a long-lived process ever had, and a
// later socket reusing the tuple is a flow of its own. A flow outlives an
// exec or the process's exit (no close record) until the session ends —
// bounded by the sockets the trace saw.
func (c *Correlator) dropFlows(p *procState, fd int32) {
	for _, k := range p.flowsOf[fd] {
		delete(c.flows, k)
	}
	delete(p.flowsOf, fd)
}

// unixTable pairs the two ends of unix stream connections. Each end is
// keyed by its socket and its peer, sorted, so both ends compute one key.
type unixTable struct {
	waiting map[[2]uint64]*sockState
	keys    map[*sockState][2]uint64
}

func newUnixTable() *unixTable {
	return &unixTable{waiting: map[[2]uint64]*sockState{}, keys: map[*sockState][2]uint64{}}
}

// join returns the end for (sk, peer): fused with the waiting other end
// when there is one, else a new end that waits.
func (t *unixTable) join(sk, peer, at uint64) *sockState {
	k := [2]uint64{min(sk, peer), max(sk, peer)}
	if other, ok := t.waiting[k]; ok && other.connID == "" {
		delete(t.waiting, k)
		delete(t.keys, other)
		id := hashID(fmt.Sprintf("unix|%x|%x|%d", k[0], k[1], other.established))
		other.connID, other.fused = id, true
		return &sockState{firstSeen: at, established: at, connID: id, fused: true, table: t}
	}
	s := &sockState{firstSeen: at, established: at, table: t}
	t.waiting[k] = s
	t.keys[s] = k
	return s
}

// resolveAlone gives an end whose other end never came its own id.
func (t *unixTable) resolveAlone(s *sockState) {
	if s.connID != "" {
		return
	}
	k := t.keys[s]
	if t.waiting[k] == s {
		delete(t.waiting, k)
	}
	delete(t.keys, s)
	s.connID = hashID(fmt.Sprintf("unix|%x|%x|%d", k[0], k[1], s.established))
}
