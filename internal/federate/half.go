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
}

// stateRec is one tcp_state record of a half, kept until the half's role
// is known: whether a record may rebuild the key depends on it.
type stateRec struct {
	state string
	wall  uint64
	f     seqFields
	has   bool // the record carried the four tcp_sock fields
}

// halfAcc accumulates one (host, conn_id) across all events and sources.
type halfAcc struct {
	project        string
	procs          map[string]bool
	client, server bool // role evidence: connect / accept, SYN_SENT
	tcp            bool // a tcp_state record: only TCP ends are halves
	local, peer    netip.AddrPort
	first          uint64 // earliest wall ns of any record
	recs           []stateRec
	timing         timing // what the clock bounds read (clock.go)
}

// LocalConn names a conn_id on one host: conn ids are host-local, so the
// same string on two hosts is two connections.
type LocalConn struct{ Host, ConnID string }

// collector turns event streams into halves.
type collector struct{ acc map[LocalConn]*halfAcc }

func newCollector() *collector { return &collector{acc: map[LocalConn]*halfAcc{}} }

// add folds one event into its half. Events without a conn_id name no
// connection and are ignored, as are kinds that say nothing about role,
// key, owner or the instants the clock bounds read (timing.add).
func (c *collector) add(ev *schema.Event) {
	if ev.ConnID == "" {
		return
	}
	k := LocalConn{ev.HostID, ev.ConnID}
	a := c.acc[k]
	if a == nil {
		a = &halfAcc{project: ev.Project, procs: map[string]bool{}, first: ev.WallNS}
		c.acc[k] = a
	}
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
		a.addState(ev)
	}
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
// connecting side passes through SYN_SENT) and its tcp_sock fields.
func (a *halfAcc) addState(ev *schema.Event) {
	a.tcp = true
	args := &ev.Args
	r := stateRec{state: *args.SockState, wall: ev.WallNS}
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
// whose accept the trace never saw (accepted before the session
// attached, or by an untraced thread of a traced process) has no role
// evidence — the accept snapshot is no proof on its own — and stays
// unresolved as no_role.
func (a *halfAcc) half(k LocalConn) Half {
	h := Half{Host: k.Host, Project: a.project, ConnID: k.ConnID,
		Procs: slices.Sorted(maps.Keys(a.procs)), Local: a.local, Peer: a.peer,
		Established: a.first}
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

// key rebuilds h's key from every eligible record; all must agree. CLOSE
// counts on an accepted socket (a child reset in the accept queue gets
// its only record there, and S2 showed the rebuild holds) or after the
// half was keyed already, never on a connect that CLOSE ended unanswered.
func (a *halfAcc) key(h *Half) {
	for _, r := range a.recs {
		ok := keyState(r.state) ||
			(r.state == "CLOSE" && (h.Role == RoleServer || h.HasKey))
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
