package correlate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"

	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// Kernel TCP states (include/net/tcp_states.h) the correlator acts on.
const (
	tcpEstablished = 1
	tcpClose       = 7
	afInet         = 2
	afInet6        = 10
)

// tcpStateNames names the kernel's TCP states for args.sock_state.
var tcpStateNames = map[int32]string{
	1: "ESTABLISHED", 2: "SYN_SENT", 3: "SYN_RECV", 4: "FIN_WAIT1", 5: "FIN_WAIT2",
	6: "TIME_WAIT", 7: "CLOSE", 8: "CLOSE_WAIT", 9: "LAST_ACK", 10: "LISTEN",
	11: "CLOSING", 12: "NEW_SYN_RECV",
}

// sockState is one TCP socket, i.e. one end of a connection.
type sockState struct {
	local, remote netip.AddrPort
	firstSeen     uint64 // boot ns of its first record
	established   uint64 // boot ns it reached ESTABLISHED; 0 = not yet
	// connID is empty until the socket is fused with its other end or its
	// fuse window passed; fused tells which of the two happened.
	connID string
	fused  bool
	// fdGen is the generation of the owner's fd bound at the socket's
	// first record; every later transition reports that instance.
	fdGen uint32
}

// fillArgs copies the known 4-tuple into args. A nil socket (an fd the
// trace never bound) leaves args alone.
func (s *sockState) fillArgs(a *schema.Args) {
	if s == nil {
		return
	}
	if s.local.IsValid() {
		addr, port := s.local.Addr().String(), s.local.Port()
		a.LocalAddr, a.LocalPort = &addr, &port
	}
	if s.remote.IsValid() && s.remote.Port() != 0 {
		addr, port := s.remote.Addr().String(), s.remote.Port()
		a.RemoteAddr, a.RemotePort = &addr, &port
	}
}

// deadline is when an unfused socket stops waiting for its other end.
func (s *sockState) deadline(window uint64) uint64 {
	if s.established != 0 {
		return s.established + window
	}
	return s.firstSeen + window
}

// sockTable keeps live sockets by struct sock address, and established,
// unfused ones by their (local, remote) tuple for the loopback join.
type sockTable struct {
	window  uint64
	bySk    map[uint64]*sockState
	waiting map[[2]netip.AddrPort]*sockState
}

func newSockTable(window uint64) *sockTable {
	return &sockTable{window: window, bySk: map[uint64]*sockState{},
		waiting: map[[2]netip.AddrPort]*sockState{}}
}

func (t *sockTable) get(sk uint64) *sockState {
	if sk == 0 {
		return nil
	}
	return t.bySk[sk]
}

// observe applies one tcp_state record to its socket and returns it, and
// whether this was the socket's first record.
func (t *sockTable) observe(r *types.TcpStateEvent) (*sockState, bool) {
	s, ok := t.bySk[r.Sk]
	if !ok {
		s = &sockState{firstSeen: r.Time}
		t.bySk[r.Sk] = s
	}
	local, remote := tuple(r)
	if local.IsValid() {
		s.local, s.remote = local, remote
	}
	if r.Newstate == tcpEstablished && s.established == 0 {
		s.established = r.Time
		t.fuse(s)
	}
	if r.Newstate == tcpClose {
		// The address may be reused for a new socket once this one is
		// freed; events already queued keep their pointer.
		delete(t.bySk, r.Sk)
	}
	return s, !ok
}

// fuse joins s with an established, unfused socket whose tuple mirrors
// s's within the window: the two ends of one loopback connection.
func (t *sockTable) fuse(s *sockState) {
	peer, ok := t.waiting[[2]netip.AddrPort{s.remote, s.local}]
	if ok && peer.connID == "" && within(peer.established, s.established, t.window) {
		delete(t.waiting, [2]netip.AddrPort{s.remote, s.local})
		id := connID(s, peer)
		s.connID, peer.connID = id, id
		s.fused, peer.fused = true, true
		return
	}
	t.waiting[[2]netip.AddrPort{s.local, s.remote}] = s
}

// resolveAlone gives a socket whose window passed unfused its own conn id.
func (t *sockTable) resolveAlone(s *sockState) {
	if s.connID != "" {
		return
	}
	delete(t.waiting, [2]netip.AddrPort{s.local, s.remote})
	s.connID = hashID(fmt.Sprintf("%s|%s|%d", s.local, s.remote, s.established))
}

func within(a, b, window uint64) bool {
	if a > b {
		a, b = b, a
	}
	return b-a <= window
}

// connID is the hex SHA-256 (first 16 chars) of the sorted address pair
// plus the earlier establish time, so both ends compute the same id and a
// later connection reusing the same ports gets another.
func connID(a, b *sockState) string {
	x, y := a.local.String(), a.remote.String()
	if y < x {
		x, y = y, x
	}
	return hashID(fmt.Sprintf("%s|%s|%d", x, y, min(a.established, b.established)))
}

func hashID(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:16]
}

// tuple decodes a record's local and remote endpoint. Unknown families
// yield invalid AddrPorts, which leave the socket's tuple unchanged.
func tuple(r *types.TcpStateEvent) (local, remote netip.AddrPort) {
	var la, ra netip.Addr
	switch r.Family {
	case afInet:
		la = netip.AddrFrom4([4]byte(r.Saddr[:4]))
		ra = netip.AddrFrom4([4]byte(r.Daddr[:4]))
	case afInet6:
		la, ra = netip.AddrFrom16(r.Saddr), netip.AddrFrom16(r.Daddr)
	default:
		return netip.AddrPort{}, netip.AddrPort{}
	}
	return netip.AddrPortFrom(la, uint16(r.Sport)), netip.AddrPortFrom(ra, uint16(r.Dport))
}

// tcpState records the socket transition as an event and binds the
// owner's fd to the socket.
func (c *Correlator) tcpState(r *types.TcpStateEvent) {
	c.advance(r.Time)
	s, first := c.socks.observe(r)
	if r.Pid == 0 {
		return
	}
	p := c.proc(r.Pid)
	gen := p.sockGen(r.Fd, r.Sk, s, first, r.Oldstate == types.TCP_STATE_SNAPSHOT)
	state := tcpStateNames[r.Newstate]
	if state == "" {
		state = fmt.Sprintf("STATE_%d", r.Newstate)
	}
	ev := schema.Event{SchemaVersion: schema.Version, Project: c.cfg.Host.Project,
		HostID: c.cfg.Host.ID, BootNS: r.Time, WallNS: c.wall(r.Time), PID: r.Pid,
		PIDInc: p.inc, Comm: p.comm, Event: schema.KindTCPState, FD: r.Fd, FDGen: gen,
		Attribution: schema.AttributionNone}
	ev.Args.SockState = &state
	s.fillArgs(&ev.Args)
	c.enqueue(ev, s)
}
