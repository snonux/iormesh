package federate

import (
	"cmp"
	"maps"
	"net/netip"
	"slices"

	"iormesh/internal/schema"
)

// Half is one end of a TCP connection as one host's trace recorded it:
// everything the host knew under one conn_id.
type Half struct {
	Host    string   // host_id of the trace
	Project string   // project of the session that traced it
	ConnID  string   // the host-local conn_id (schema Event.ConnID)
	Procs   []string // process node ids that held the socket, sorted
	Role    string   // RoleClient, RoleServer, or "" when no record said
	// Aliases are the other host-local conn ids of the same socket: one
	// per further session of the host that traced it (Fuse folds them,
	// foldSessions), sorted. The merge renames them along with ConnID.
	Aliases []string
	// Local and Peer are the 4-tuple this host saw. Across NAT they
	// differ between the two ends, so fusion never compares them; they
	// are what an unresolved half reports as its far end (M6 expands to
	// it).
	Local, Peer netip.AddrPort
	Key         ISNPair // valid only when HasKey
	HasKey      bool
	// KeyConflict marks a half whose records rebuilt different keys: a
	// producer bug or a misattributed record, and either way no key to
	// fuse on.
	KeyConflict bool
	// Established is the earliest wall-clock time (ns) of the half's keyed
	// records, or of all its records when none is keyed — the minimum, not
	// the first read, since two sessions on one host may report the same
	// socket in different orders. Wall clock, not
	// boot ns: two hosts' boot clocks share no epoch, while NTP keeps
	// wall clocks within far less than the fusion window.
	Established uint64
	// Before marks a half whose socket was already open when its session
	// started (an initial snapshot, args.sock_snapshot initial_client or
	// initial_server; decision #35): the trace saw no connect or accept,
	// so Established is only an upper bound of when the connection was
	// established, and the fusion window bounds it from one side only
	// (countCandidates).
	Before bool
}

// stateRec is one tcp_state record of a half, kept until the half's role
// is known: whether a record may rebuild the key depends on it.
type stateRec struct {
	state string
	wall  uint64
	boot  uint64 // the host's boot clock: one clock for every session on the host
	f     seqFields
	has   bool // the record carried the four tcp_sock fields
	// initial marks the tracer's initial snapshot of a socket open before
	// the session (decision #35): read unlocked like the accept snapshot,
	// so it keys a half only when no state change does (key).
	initial bool
}

// halfAcc accumulates one (host, conn_id) across all events and sources.
type halfAcc struct {
	project        string
	projects       map[string]bool // every session's project that reported the half
	procs          map[string]bool
	client, server bool // role evidence: connect / accept, SYN_SENT, an initial snapshot
	tcp            bool // a tcp_state record: only TCP ends are halves
	before         bool // an initial snapshot: open before the session (Half.Before)
	local, peer    netip.AddrPort
	first          uint64 // earliest wall ns of any record
	recs           []stateRec
	timing         timing // what the clock bounds read (clock.go)
}

// LocalConn names a conn_id on one host: conn ids are host-local, so the
// same string on two hosts is two connections.
type LocalConn struct{ Host, ConnID string }

// collector turns event streams into halves.
type collector struct {
	acc map[LocalConn]*halfAcc
	// drops holds every drop event, by the session (host and project)
	// whose trace reported it: records that session lost, so the byte
	// sums that guard the clock bounds' offset matching against 4 GiB
	// laps (clockproof.go) may undercount — for the connections a drop
	// could have touched (dropTaints).
	drops map[hostProject][]dropRec
	// ends holds every end event's reason, by the session that wrote
	// it: whether a final snapshot of that session followed the targets'
	// last calls (ackedCovered).
	ends map[hostProject][]string
}

// hostProject names one host's sessions of one project: a drop is the
// loss of the session that reported it, not of another project's
// session tracing the same host with its own BPF programs and ring.
type hostProject struct{ Host, Project string }

func newCollector() *collector {
	return &collector{acc: map[LocalConn]*halfAcc{}, drops: map[hostProject][]dropRec{},
		ends: map[hostProject][]string{}}
}

// Halves folds events — of any number of hosts, in any order — into the
// TCP halves the merge would fuse, exactly as Merge collects them from
// its sources. It lets a caller holding events rather than trace
// directories (the tracer's end-to-end test, which splits a loopback
// trace into two hosts to check both ends rebuild one key) run the same
// role and key rules.
func Halves(events []schema.Event) []Half {
	c := newCollector()
	for i := range events {
		c.add(&events[i])
	}
	return c.halves()
}

// add folds one event into its half. Events without a conn_id name no
// connection and are ignored, as are kinds that say nothing about role,
// key, owner or the instants the clock bounds read (timing.add); a drop
// is kept for the session that reported it (dropRec).
func (c *collector) add(ev *schema.Event) {
	if ev.Event == schema.KindDrop {
		k := hostProject{ev.HostID, ev.Project}
		c.drops[k] = append(c.drops[k], newDropRec(ev))
	}
	if ev.Event == schema.KindEnd && ev.Args.EndReason != nil {
		k := hostProject{ev.HostID, ev.Project}
		c.ends[k] = append(c.ends[k], *ev.Args.EndReason)
	}
	if ev.ConnID == "" {
		return
	}
	k := LocalConn{ev.HostID, ev.ConnID}
	a := c.acc[k]
	if a == nil {
		a = &halfAcc{project: ev.Project, projects: map[string]bool{}, procs: map[string]bool{},
			first: ev.WallNS}
		c.acc[k] = a
	}
	a.projects[ev.Project] = true
	a.first = min(a.first, ev.WallNS)
	if ev.PID != 0 {
		a.procs[schema.ProcessNodeID(ev.HostID, ev.PID, ev.PIDInc)] = true
	}
	a.timing.add(ev)
	switch ev.Event {
	case schema.KindConnect:
		a.client = true
		a.tuple(&ev.Args)
	case schema.KindAccept, schema.KindAccept4:
		a.server = true
		a.tuple(&ev.Args)
	case schema.KindTCPState:
		a.tuple(&ev.Args)
		if !isFinal(ev) {
			a.addState(ev)
		}
	}
}

// initialRole folds an initial snapshot's role (schema.Event.InitialRole)
// into the half's role evidence: the socket was open before the session,
// so no connect or accept will ever say it.
func (a *halfAcc) initialRole(ev *schema.Event) {
	switch ev.InitialRole() {
	case RoleClient:
		a.client, a.before = true, true
	case RoleServer:
		a.server, a.before = true, true
	}
}

// isFinal reports whether ev is the tracer's final snapshot of a socket
// still open at the end of its session (args.sock_snapshot; task sf4). It
// is there for the counter proof alone (timing.state): no transition, so
// no role evidence, and read from an unlocked socket like the accept
// snapshot, so a torn read could rebuild a wrong key and turn a fusable
// half into a key conflict. It never keys a half: the half's earlier
// records do, as before.
func isFinal(ev *schema.Event) bool {
	return ev.Args.SockSnapshot != nil && *ev.Args.SockSnapshot == schema.SockSnapshotFinal
}

// tuple keeps the newest 4-tuple the records name. Each side is taken
// only when present: a connect before bind knows no local address, and
// a wildcard-bound socket reports its port without one.
func (a *halfAcc) tuple(args *schema.Args) {
	if args.LocalAddr != nil && args.LocalPort != nil {
		if addr, err := netip.ParseAddr(*args.LocalAddr); err == nil {
			a.local = netip.AddrPortFrom(addr, *args.LocalPort)
		}
	}
	if args.RemoteAddr != nil && args.RemotePort != nil {
		if addr, err := netip.ParseAddr(*args.RemoteAddr); err == nil {
			a.peer = netip.AddrPortFrom(addr, *args.RemotePort)
		}
	}
}

// addState records a tcp_state event: its role evidence (only the
// connecting side passes through SYN_SENT; an initial snapshot names its
// role) and its tcp_sock fields.
func (a *halfAcc) addState(ev *schema.Event) {
	a.tcp = true
	args := &ev.Args
	r := stateRec{state: *args.SockState, wall: ev.WallNS, boot: ev.BootNS,
		initial: ev.InitialRole() != ""}
	a.initialRole(ev)
	if args.SndUna != nil {
		// Validate guarantees all four together on a tcp_state record.
		r.f = seqFields{SndUna: *args.SndUna, RcvNxt: *args.RcvNxt,
			BytesAcked: *args.BytesAcked, BytesReceived: *args.BytesReceived}
		r.has = true
	}
	if r.state == "SYN_SENT" {
		a.client = true
	}
	a.recs = append(a.recs, r)
}

// halves returns every TCP half, sorted by (host, conn_id). A conn_id
// with both a connecting and an accepting end on one host is a loopback
// pair the host's correlator already fused; it is no half.
func (c *collector) halves() []Half {
	var out []Half
	for k, a := range c.acc {
		if !a.tcp || (a.client && a.server) {
			continue
		}
		out = append(out, a.half(k))
	}
	slices.SortFunc(out, func(x, y Half) int {
		return cmp.Or(cmp.Compare(x.Host, y.Host), cmp.Compare(x.ConnID, y.ConnID))
	})
	return out
}

// half finishes one accumulated half: its role and its key. A server end
// whose accept the trace never saw (accepted by an untraced thread of a
// traced process) has no role evidence — the accept snapshot is no proof
// on its own — and stays unresolved as no_role. One accepted before the
// session has its initial snapshot's role (decision #35), unless the
// tracer could not seed it (an old tracer, a socket it missed).
func (a *halfAcc) half(k LocalConn) Half {
	h := Half{Host: k.Host, Project: a.project, ConnID: k.ConnID,
		Procs: slices.Sorted(maps.Keys(a.procs)), Local: a.local, Peer: a.peer,
		Established: a.first, Before: a.before}
	switch {
	case a.client:
		h.Role = RoleClient
	case a.server:
		h.Role = RoleServer
	default:
		return h // no role, so no SYN correction: the key cannot be rebuilt
	}
	a.key(&h)
	return h
}

// key rebuilds h's key from every eligible record (keyRecords); all must
// agree. The records are taken in the order of the host's boot clock, so
// the earliest eligible one sets the key whatever order the sessions
// reported them in: one host's sessions share that clock, while each
// derives its wall clock from it with an offset of its own.
//
// A server half's accept snapshot is read from a socket nobody locks
// (the tracer's sock.c): softirq may be updating the four fields as it
// reads them, and although the tracer re-reads until two reads agree, a
// torn pair can remain. The locked state-change records therefore decide
// the key; the snapshot keys a half only when nothing else does (a child
// reset in the accept queue, a connection still open when the trace
// ended), and a snapshot disagreeing with them is no conflict. The
// snapshot is the half's earliest record: the tracer timestamps it before
// it registers the socket, so every state change of it is later. A lost
// snapshot makes the first state change the fallback instead, which only
// costs that record's say in the conflict check. An initial snapshot (a
// socket open before the session, decision #35) is read the same
// unlocked way, on either role, and is marked as such: it is the
// snapshot, and a server half that has one had no accept snapshot.
func (a *halfAcc) key(h *Half) {
	recs := slices.Clone(a.recs)
	slices.SortStableFunc(recs, func(x, y stateRec) int { return cmp.Compare(x.boot, y.boot) })
	var locked, snap []stateRec
	for _, r := range recs {
		if r.initial {
			snap = append(snap, r)
		} else {
			locked = append(locked, r)
		}
	}
	if h.Role == RoleServer && len(snap) == 0 && len(locked) > 0 {
		snap, locked = locked[:1], locked[1:]
	}
	keyRecords(h, locked)
	if !h.HasKey && !h.KeyConflict {
		keyRecords(h, snap)
	}
	if h.HasKey && len(snap) == 1 && snap[0].has {
		// Torn or not, the snapshot's time is when the half was
		// established at the latest.
		h.Established = min(h.Established, snap[0].wall)
	}
	if h.HasKey && h.Role == RoleClient {
		h.Established = clientEstablished(recs, h.Established)
	}
}

// clientEstablished returns when a keyed client half was established,
// keyed being the wall time of its earliest keyed record. That is the
// ESTABLISHED record when the trace has it. When a skipped run lost it
// (task qf4) and a later record keyed the half — a CLOSE perhaps minutes
// on — the SYN_SENT record before that is the better time: the handshake
// completes one round trip after it, while the keyed record may lie
// outside the fusion window around the server's accept.
func clientEstablished(recs []stateRec, keyed uint64) uint64 {
	syn := keyed
	for _, r := range recs {
		switch {
		case r.state == "ESTABLISHED" && r.has:
			return keyed
		case r.state == "SYN_SENT" && r.wall <= keyed:
			syn = r.wall
		}
	}
	return syn
}

// keyRecords keys h from recs: the first eligible record sets the key,
// any later one rebuilding another marks a conflict. A record rebuilds
// the key from ESTABLISHED on: snd_una − bytes_acked and rcv_nxt −
// bytes_received stay constant from there (tcp_snd_una_update and
// tcp_rcv_nxt_update move each pair together, a FIN included) up to and
// including the CLOSE that ends the connection, by FIN or by RST (S2;
// the root tests TestSockSeqFields and TestSockSeqAbortClose).
//
// CLOSE also ends a connect nobody answered, whose fields hold no peer
// ISN. The correlator, which sees the old state, leaves the fields out
// there; the merge sees only the new state and checks for itself. A
// CLOSE counts on an accepted socket (a child reset in the accept queue
// gets its only record there, and S2 showed the rebuild holds), after
// the half was keyed already (one disagreeing is a conflict), and on a
// client whose CLOSE counts its own SYN as acknowledged: the SYN-ACK's
// tcp_ack makes bytes_acked ≥ 1, and the same locked softirq sets
// rcv_nxt to the peer's ISN + 1 before ESTABLISHED, while an unanswered
// connect's CLOSE has bytes_acked 0. The last case is a client whose
// ESTABLISHED record a skipped inet_sock_set_state run lost (task qf4).
func keyRecords(h *Half, recs []stateRec) {
	for _, r := range recs {
		ok := keyState(r.state) || (r.state == "CLOSE" &&
			(h.Role == RoleServer || h.HasKey || r.f.BytesAcked >= 1))
		if !r.has || !ok {
			continue
		}
		k := keyFrom(h.Role, r.f)
		switch {
		case !h.HasKey:
			h.Key, h.HasKey, h.Established = k, true, r.wall
		case k != h.Key:
			h.KeyConflict = true
		}
		h.Established = min(h.Established, r.wall)
	}
}
