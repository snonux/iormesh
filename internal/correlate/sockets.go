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
	tcpSynSent     = 2
	tcpSynRecv     = 3
	tcpClose       = 7
	tcpListen      = 10
	tcpNewSynRecv  = 12
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
	// table resolves the socket alone once its fuse window passed; nil is
	// the TCP table. UDP flows and unix connections pair in tables of
	// their own (ipc.go), which must also forget the waiting entry.
	table resolver
}

// resolver gives a socket whose fuse window passed unfused its own conn
// id and forgets it as a pairing candidate.
type resolver interface {
	resolveAlone(s *sockState)
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

// observe applies one tcp_state record to its socket and returns it,
// whether this was the first record of the socket — the one that binds
// the owner's fd (procState.sockGen) — and whether the record went out
// under a stale owner, so that it must name no process or fd.
//
// A known address can start a new socket: the old one's TCP_CLOSE record
// was lost (a skipped run, sock.c) and the kernel reused the address. Two
// proofs, mirroring the tracer's owner check (iormesh_sock_owner_stale):
//   - a transition out of CLOSE or LISTEN (newLife) begins every life —
//     a connect's CLOSE -> SYN_SENT is its socket's first record — so it
//     starts a new socket and binds the fd, as on an unknown address;
//   - a record whose tuple contradicts the socket's (otherLife) is a new
//     life too. An accept snapshot binds its own fd anyway; any other such
//     record came before the accept and went out under the stale owner's
//     pid and fd, which belong to the old life: it binds nothing and is
//     reported stale.
func (t *sockTable) observe(r *types.TcpStateEvent) (s *sockState, first, stale bool) {
	local, remote := tuple(r)
	s, ok := t.bySk[r.Sk]
	reborn := false
	switch {
	case !ok || newLife(r):
		first = true
	case s.otherLife(local, remote):
		reborn = true
		stale = r.Oldstate != types.TCP_STATE_SNAPSHOT
	}
	if first || reborn {
		s = &sockState{firstSeen: r.Time}
		t.bySk[r.Sk] = s
	}
	if local.IsValid() {
		s.local, s.remote = local, remote
	}
	if establishes(r) && s.established == 0 {
		s.established = r.Time
		t.fuse(s)
	}
	if r.Newstate == tcpClose {
		// The address may be reused for a new socket once this one is
		// freed; events already queued keep their pointer.
		delete(t.bySk, r.Sk)
	}
	return s, first, stale
}

// newLife reports whether r is a transition that begins a socket's life:
// out of CLOSE (connect's CLOSE -> SYN_SENT, listen's CLOSE -> LISTEN) or
// out of LISTEN (an accepted child is born LISTEN -> SYN_RECV). An accept
// snapshot is no transition; its old state is a marker.
func newLife(r *types.TcpStateEvent) bool {
	return r.Oldstate == tcpClose || r.Oldstate == tcpListen
}

// otherLife reports whether a record with this tuple belongs to another
// socket than s, one at the same struct sock address: the tracer's
// userspace backstop (sock.c, "Accepted residual risk") for a lost
// TCP_CLOSE run. Losing that record keeps s in bySk, and the next socket
// the kernel allocates at the address — an accepted child, typically —
// would otherwise report under s and its conn id: one conn mixing two
// connections, which the merge can only call a key conflict (task qf4,
// M4-9 on Rocky 9.9).
//
// A socket's tuple never changes once it is complete: both addresses
// specified and both ports set. Before that it may still change within
// one life: CLOSE -> SYN_SENT carries no local port unless the socket was
// bound, a dual-stack AF_INET6 socket bound to [::]:P that connects to an
// IPv4 peer reports [::]:P at SYN_SENT (tcp_v6_connect runs
// tcp_v4_connect before it maps sk_v6_rcv_saddr) and the real address
// from ESTABLISHED on, and a listener's records have no remote port. So
// only two complete tuples that differ are two lives. The same tuple
// reused right away stays undetected here; the ISN pair then differs, and
// the merge still refuses to fuse the mixed conn.
func (s *sockState) otherLife(local, remote netip.AddrPort) bool {
	return complete(s.local) && complete(s.remote) && complete(local) && complete(remote) &&
		(local != s.local || remote != s.remote)
}

// complete reports whether an endpoint is fully known: a specified address
// and a port.
func complete(a netip.AddrPort) bool {
	return a.IsValid() && !a.Addr().IsUnspecified() && a.Port() != 0
}

// establishes reports whether r shows the connection established: the
// SYN_SENT -> ESTABLISHED transition, or an accept snapshot taken in any
// state the connection only reaches after establishing (CLOSE_WAIT when
// the peer already sent its FIN, say) — the snapshot is the only record
// such a server end gets.
func establishes(r *types.TcpStateEvent) bool {
	if r.Oldstate != types.TCP_STATE_SNAPSHOT {
		return r.Newstate == tcpEstablished
	}
	return r.Newstate != tcpClose && r.Newstate != tcpSynSent && r.Newstate != tcpSynRecv
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
	k := [2]netip.AddrPort{s.local, s.remote}
	if t.waiting[k] == s { // a later socket may reuse the tuple
		delete(t.waiting, k)
	}
	// A never-established socket (a refused connect) hashes its first
	// record's time, so two refused connects on one tuple stay apart.
	at := s.established
	if at == 0 {
		at = s.firstSeen
	}
	s.connID = hashID(fmt.Sprintf("%s|%s|%d", s.local, s.remote, at))
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
		// A dual-stack socket (the JVM's default) reports IPv4 peers as
		// ::ffff:a.b.c.d; unmapping lets it fuse with a plain AF_INET end
		// and gives args the address the peer itself would report.
		la, ra = netip.AddrFrom16(r.Saddr).Unmap(), netip.AddrFrom16(r.Daddr).Unmap()
	default:
		return netip.AddrPort{}, netip.AddrPort{}
	}
	return netip.AddrPortFrom(la, uint16(r.Sport)), netip.AddrPortFrom(ra, uint16(r.Dport))
}

// tcpState records the socket transition as an event and binds the
// owner's fd to the socket.
func (c *Correlator) tcpState(r *types.TcpStateEvent) {
	c.advance(r.Time)
	if r.Oldstate == types.TCP_STATE_FINAL {
		c.finalState(r)
		return
	}
	s, first, stale := c.socks.observe(r)
	if r.Pid == 0 || stale {
		// A stale record's pid and fd are the old life's owner: it
		// updates the socket but, like a record without an owner, is
		// not emitted, so the old process never holds the new
		// connection. The accept snapshot that follows carries the key.
		return
	}
	p := c.proc(r.Pid)
	gen := p.sockGen(r.Fd, s, first, r.Oldstate == types.TCP_STATE_SNAPSHOT)
	c.emitState(r, s, p, gen)
}

// finalState records the tracer's final snapshot of a socket still open
// at the end of the session (bpf.Tracer.FinalSockets; task sf4), marked
// args.sock_snapshot "final". It is there for its byte counters — the
// merge's counter proof reads them, internal/federate/clockproof.go — and
// changes nothing else: the socket keeps its tuple, its established time
// and its fd binding. So a snapshot that cannot add a reading is dropped:
// one of a socket this correlator never saw a record of (nothing of it
// reached the trace, so there is no connection to vouch for), one whose
// tuple contradicts the socket's (another life at the address, as
// otherLife finds for a state change), one without an owner, and one
// whose fields carriesSeq refuses — a connect still in SYN_SENT, or reads
// that never agreed.
func (c *Correlator) finalState(r *types.TcpStateEvent) {
	s, ok := c.socks.bySk[r.Sk]
	local, remote := tuple(r)
	if !ok || r.Pid == 0 || s.otherLife(local, remote) || !carriesSeq(r) {
		return
	}
	c.emitState(r, s, c.proc(r.Pid), s.fdGen)
}

// emitState queues r as a tcp_state event of socket s, owned by process p
// with fd generation gen.
func (c *Correlator) emitState(r *types.TcpStateEvent, s *sockState, p *procState, gen uint32) {
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
	if carriesSeq(r) {
		fillSeq(&ev.Args, r)
	}
	if r.Oldstate == types.TCP_STATE_FINAL {
		final := schema.SockSnapshotFinal
		ev.Args.SockSnapshot = &final
	}
	c.enqueue(ev, s)
}

// carriesSeq reports whether r's tcp_sock fields can rebuild the ISN pair
// (S2, experiment 1): only from ESTABLISHED on. The opening transitions
// (CLOSE -> SYN_SENT, SYN_RECV) read snd_una/rcv_nxt still 0 or the
// child uninitialised, and a CLOSE straight from SYN_SENT (a refused or
// timed-out connect) or from LISTEN never learned a peer ISN. Every
// other CLOSE ends an established connection (FIN or RST), which S2
// showed the rebuild holds through; an accept snapshot at CLOSE is a
// child reset in the accept queue, established before it was queued.
// An accept snapshot whose reads never agreed (TCP_SEQ_TORN: softirq
// was updating the unlocked child meanwhile, types.h) carries none either.
// Records that do not qualify carry none of the four, so the schema's
// all-four-or-none holds and the merge never reads a key that is no key.
func carriesSeq(r *types.TcpStateEvent) bool {
	if r.SeqStatus != types.TCP_SEQ_OK {
		return false
	}
	switch r.Newstate {
	case tcpSynSent, tcpSynRecv, tcpListen, tcpNewSynRecv:
		return false
	case tcpClose:
		return r.Oldstate != tcpSynSent && r.Oldstate != tcpSynRecv &&
			r.Oldstate != tcpListen && r.Oldstate != tcpClose
	}
	return true
}

// fillSeq copies the record's four tcp_sock fields into args, all four
// together (schema.Args, "all four or none").
func fillSeq(a *schema.Args, r *types.TcpStateEvent) {
	sndUna, rcvNxt := r.SndUna, r.RcvNxt
	acked, received := r.BytesAcked, r.BytesReceived
	a.SndUna, a.RcvNxt = &sndUna, &rcvNxt
	a.BytesAcked, a.BytesReceived = &acked, &received
}
