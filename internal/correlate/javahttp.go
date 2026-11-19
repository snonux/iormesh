package correlate

import (
	"net/netip"
	"slices"
	"strings"

	"iormesh/internal/agentspool"
	"iormesh/internal/schema"
)

// The http events of the Java join (M3 step 7, decision #22). The agent's
// HTTP hooks write one HTTP record per finished request; the joiner turns
// each into an http event and merges it into the event stream by time.
//
// Order: an http event is stamped when its request completed, so it must
// follow the syscalls that completed before it — on its own thread above
// all (the golden checks per-thread order). Events reach the joiner in
// completion order (the correlator numbers them so), so an http event
// waits until an event that completed Grace after it has arrived, or
// Timeout has passed; it then goes out ahead of the first held event that
// completed after it.
//
// Connection: the record names the request's socket ends, as far as the
// library still held them. The joiner keeps the ends of every socket
// event that passed with a conn_id (tcp_state, connect, accept carry
// them) and looks the request's up: both ends, else the remote, else the
// local one (a server's local end reads as unknown once the exchange
// closed the connection; a client's remote is known only for a literal
// IP host). A request whose ends match nothing keeps no conn_id.
//
// Numbering: req_seq counts a connection's requests per process and end
// (client or server: a same-JVM loopback call has both) in completion
// order, which is request order over HTTP/1.1 (one at a time per
// connection, pipelined or not) — up to the clock: two requests on one
// connection completing within the agent's stamp resolution of each
// other on different threads could swap (documented in
// docs/schema.md). It is left out — the hops then pair by time only —
// for HTTP/2, which multiplexes, for a request without a connection,
// for every request of a JVM that may have lost an HTTP record (a lost
// request would shift the count silently), and from the first request
// that took several exchanges on a connection on (a client library's
// own redirect or authentication retry: the server saw each exchange
// as a request).

// pendingHTTP is an HTTP record waiting for its turn in the stream.
type pendingHTTP struct {
	boot uint64 // ExitMono on the boot clock
	v    *jvm
	rec  agentspool.HTTP
}

// connEnd and connPair key the socket index; addresses are unmapped
// (the JVM's dual-stack sockets show IPv4 peers as ::ffff:a.b.c.d to the
// kernel, as a.b.c.d to Java).
type connEnd struct {
	pid  uint32
	addr netip.AddrPort
}

type connPair struct {
	pid           uint32
	local, remote netip.AddrPort
}

// connSeq keys a request counter: a connection's requests as one end of
// it saw them. The side matters when both ends are one process (a
// same-JVM loopback call): they share the conn_id, and one counter would
// number the client's requests 1, 3, 5 and the server's 2, 4, 6.
type connSeq struct {
	pid    uint32
	conn   string
	server bool
}

// seqStopped marks a counter that numbers no more: one of its requests
// took several exchanges (a redirect or authentication retry the client
// library made by itself), so the server counted more requests than the
// client and every later ordinal would pair wrongly.
const seqStopped = ^uint32(0)

// closedConn is a closed socket's index entry, forgotten Retain after the
// close: an http event of its last request may be emitted after the
// close's event passed.
type closedConn struct {
	pair connPair
	conn string
	at   uint64
}

// connSet is the connections seen on one key (an end or both): a lookup
// answers only when exactly one is known, never a guess between several
// — a client's two connections to one server share the remote end, a
// server's accepted ones its local end.
type connSet map[string]struct{}

// connIndex finds a request's connection by its socket ends.
type connIndex struct {
	byPair   map[connPair]connSet
	byLocal  map[connEnd]connSet
	byRemote map[connEnd]connSet
	seq      map[connSeq]uint32
	closed   []closedConn
}

func newConnIndex() *connIndex {
	return &connIndex{byPair: map[connPair]connSet{}, byLocal: map[connEnd]connSet{},
		byRemote: map[connEnd]connSet{}, seq: map[connSeq]uint32{}}
}

// observe indexes a socket event's ends under its conn_id; a closed
// socket's entries stay until purge.
func (x *connIndex) observe(ev *schema.Event) {
	a := ev.Args
	if ev.ConnID == "" || a.LocalAddr == nil || a.LocalPort == nil {
		return
	}
	local := eventEnd(*a.LocalAddr, *a.LocalPort)
	remote := netip.AddrPort{}
	if a.RemoteAddr != nil && a.RemotePort != nil {
		remote = eventEnd(*a.RemoteAddr, *a.RemotePort)
	}
	p := connPair{ev.PID, local, remote}
	x.add(p, ev.ConnID)
	if ev.Event == schema.KindTCPState && a.SockState != nil && *a.SockState == "CLOSE" {
		x.closed = append(x.closed, closedConn{pair: p, conn: ev.ConnID, at: ev.BootNS})
	}
}

func (x *connIndex) add(p connPair, conn string) {
	if p.local.IsValid() {
		addTo(x.byLocal, connEnd{p.pid, p.local}, conn)
	}
	if p.remote.IsValid() {
		addTo(x.byRemote, connEnd{p.pid, p.remote}, conn)
		if p.local.IsValid() {
			addTo(x.byPair, p, conn)
		}
	}
}

func addTo[K comparable](m map[K]connSet, k K, conn string) {
	set := m[k]
	if set == nil {
		set = connSet{}
		m[k] = set
	}
	set[conn] = struct{}{}
}

// lookup returns the conn_id of a request's ends: by both ends, else the
// remote, else the local one; "" when unknown or when several
// connections are known on that key.
func (x *connIndex) lookup(pid uint32, local, remote string) string {
	l, r := parseEnd(local), parseEnd(remote)
	switch {
	case l.IsValid() && r.IsValid():
		return only(x.byPair[connPair{pid, l, r}])
	case r.IsValid():
		return only(x.byRemote[connEnd{pid, r}])
	case l.IsValid():
		return only(x.byLocal[connEnd{pid, l}])
	}
	return ""
}

// only is the set's one connection, "" for none or several.
func only(set connSet) string {
	if len(set) != 1 {
		return ""
	}
	for c := range set {
		return c
	}
	return ""
}

// purge forgets the sockets closed more than retain before now, and
// their request counters.
func (x *connIndex) purge(now, retain uint64) {
	n := 0
	for ; n < len(x.closed) && x.closed[n].at+retain < now; n++ {
		c := x.closed[n]
		removeFrom(x.byPair, c.pair, c.conn)
		removeFrom(x.byLocal, connEnd{c.pair.pid, c.pair.local}, c.conn)
		removeFrom(x.byRemote, connEnd{c.pair.pid, c.pair.remote}, c.conn)
		delete(x.seq, connSeq{c.pair.pid, c.conn, false})
		delete(x.seq, connSeq{c.pair.pid, c.conn, true})
	}
	x.closed = slices.Delete(x.closed, 0, n)
}

func removeFrom[K comparable](m map[K]connSet, k K, conn string) {
	if set := m[k]; set != nil {
		delete(set, conn)
		if len(set) == 0 {
			delete(m, k)
		}
	}
}

// eventEnd parses a socket event's address, unmapped; invalid if it does
// not parse.
func eventEnd(addr string, port uint16) netip.AddrPort {
	a, err := netip.ParseAddr(addr)
	if err != nil {
		return netip.AddrPort{}
	}
	return netip.AddrPortFrom(a.Unmap(), port)
}

// parseEnd parses the agent's "ip:port" ("[ip]:port"); invalid when empty
// or malformed.
func parseEnd(s string) netip.AddrPort {
	ap, err := netip.ParseAddrPort(s)
	if err != nil {
		return netip.AddrPort{}
	}
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
}

// addHTTP queues an HTTP record by its time; records arrive in nearly
// time order, so the insertion point is searched from the back.
func (j *JavaJoin) addHTTP(v *jvm, r agentspool.HTTP) {
	p := pendingHTTP{boot: j.boot(r.ExitMono), v: v, rec: r}
	i := len(j.https)
	for i > 0 && j.https[i-1].boot > p.boot {
		i--
	}
	j.https = slices.Insert(j.https, i, p)
}

// httpDue reports whether the next HTTP record goes out before held
// event ev (nil: none held): it completed earlier, or ev is the trace's
// end, which stays last.
func (j *JavaJoin) httpDue(ev *schema.Event) bool {
	if len(j.https) == 0 {
		return false
	}
	return ev == nil || ev.Event == schema.KindEnd || j.https[0].boot < ev.BootNS+ev.LatencyNS
}

// httpReady reports whether the next HTTP record may go out now: every
// event that completed before it has arrived (one that completed Grace
// after it did), or Timeout passed.
func (j *JavaJoin) httpReady() bool {
	h := j.https[0]
	return j.maxEnd >= h.boot+j.cfg.Grace || j.scanned >= h.boot+j.cfg.Timeout
}

// emitHTTP sends the next HTTP record as an http event.
func (j *JavaJoin) emitHTTP() error {
	h := j.https[0]
	j.https = slices.Delete(j.https, 0, 1)
	ev, err := j.httpEvent(h)
	if err != nil {
		return err
	}
	return j.emit(ev)
}

// httpEvent builds the http event of one record: envelope from the JVM
// and the stream, the request redacted again by the tracer's own policy
// (the agent's is a copy; this one is the reference, decision #9), the
// connection and its ordinal, and the Java side from the record's stack
// with the OTEL span the request was recorded in.
func (j *JavaJoin) httpEvent(h pendingHTTP) (schema.Event, error) {
	v, r := h.v, h.rec
	ev := schema.Event{SchemaVersion: schema.Version, Project: j.project, HostID: j.hostID,
		BootNS: h.boot, WallNS: uint64(int64(h.boot) + j.wallOff), PID: v.pid,
		PIDInc: v.incarnation(), TID: uint32(r.KTID), Comm: v.comm, Event: schema.KindHTTP,
		FD: schema.NoFD, Attribution: schema.AttributionNone}
	ev.HTTP = &schema.HTTP{Method: r.Method, Scheme: r.Scheme, Host: r.Host,
		Path: j.policy.URL(r.Path), Status: uint16(min(r.Status, 999)), Version: r.Version,
		ReqHeaders: j.headers(r.ReqHeaders), RespHeaders: j.headers(r.RespHeaders)}
	if ev.HTTP.Method == "" {
		ev.HTTP.Method = "?" // a library that lost its method still made a request
	}
	ev.ConnID = j.conns.lookup(v.pid, r.Local, r.Remote)
	ev.HTTP.ReqSeq = j.reqSeq(v, ev.ConnID, r)
	j.stats.HTTP++
	if ev.ConnID == "" {
		j.stats.HTTPNoConn++
	}
	stack, err := j.stack(v, r.StackID)
	if err != nil || stack == nil {
		return ev, err
	}
	java := &schema.Java{Stack: stack}
	if th, ok := v.threads[r.ThreadID]; ok && !th.Virtual {
		java.Thread = th.Name
	}
	ev.Attribution, ev.Java = schema.AttributionJava, java
	ev.Otel = otelOf(r.Otel) // with java only: the schema ties otel to it
	return ev, nil
}

// reqSeq numbers a request on its connection, as its end (client or
// server) saw it; 0 (absent) when it cannot be numbered soundly: no
// connection, HTTP/2, a JVM that lost an HTTP record (dropped, abandoned
// or undecodable — any of them, the reader cannot tell the type of the
// last two), or a connection on which a request took several exchanges
// (that request and every later one: the ends' counts no longer agree).
func (j *JavaJoin) reqSeq(v *jvm, conn string, r agentspool.HTTP) uint32 {
	if conn == "" || strings.HasPrefix(r.Version, "HTTP/2") || httpLost(v.ring.Losses()) {
		return 0
	}
	k := connSeq{v.pid, conn, r.Server}
	if j.conns.seq[k] == seqStopped || r.Exchanges > 1 {
		j.conns.seq[k] = seqStopped
		return 0
	}
	j.conns.seq[k]++
	return j.conns.seq[k]
}

// httpLost reports whether a ring may have lost an HTTP record.
func httpLost(l agentspool.Losses) bool {
	return l.DroppedHTTP+l.Abandoned+l.Malformed > 0
}

// headers re-applies the tracer's header policy to "name: value" lines.
func (j *JavaJoin) headers(hs []string) []string {
	if len(hs) == 0 {
		return nil
	}
	out := make([]string, len(hs))
	for i, h := range hs {
		name, value, ok := strings.Cut(h, ": ")
		if !ok {
			out[i] = j.policy.Text(h)
			continue
		}
		out[i] = name + ": " + j.policy.Header(name, value)
		if urlHeaders[strings.ToLower(name)] {
			// A relative reference ("/next?token=x") has no "://" for
			// the policy's text scan to find.
			out[i] = name + ": " + j.policy.URL(j.policy.Header(name, value))
		}
	}
	return out
}

// urlHeaders hold a URI reference, often relative: masked as a URL whole
// (the agent's Redaction.URL_HEADERS).
var urlHeaders = map[string]bool{"location": true, "content-location": true,
	"referer": true, "origin": true}

// seen takes what the http events borrow from the stream as ev passes:
// the envelope's project, host and clock offset, the latest completion
// time, and the socket ends of a socket event.
func (j *JavaJoin) seen(ev *schema.Event) {
	j.project, j.hostID = ev.Project, ev.HostID
	if ev.WallNS != 0 {
		j.wallOff = int64(ev.WallNS) - int64(ev.BootNS)
	}
	j.maxEnd = max(j.maxEnd, ev.BootNS+ev.LatencyNS)
	j.conns.observe(ev)
}
