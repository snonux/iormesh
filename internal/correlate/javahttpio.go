package correlate

import (
	"net/netip"
	"slices"

	"iormesh/internal/schema"
)

// The second way to a request's connection (task 7j4): by the socket I/O
// its own thread did inside the request's span.
//
// Behind a proxy that names the client (Tomcat's RemoteIpValve, Spring's
// ForwardedHeaderFilter) a servlet's getRemoteAddr is the X-Forwarded-For
// claim, not the socket's peer, so the agent records no remote end for a
// forwarded request; its local end is the listener's, which every
// accepted connection shares, and the lookup by ends answers nothing.
// A held request with a start (a synchronous library's: the recording
// thread worked on it alone from start to completion) is then joined by
// the connections its thread read or wrote inside the span: the one of
// them whose ends match every end the record names. The ends filter out
// the thread's own outbound calls (a database, another service), which
// do not share the listener's end; two inbound candidates answer
// nothing. A request whose body and reply moved outside the span (a GET
// whose reply the container still buffered at service's return) has no
// I/O inside it and stays without a connection.
//
// A virtual thread's records name its carrier, which runs other virtual
// threads inside the span, so its requests are never joined this way.

// threadIOKeep is how many socket I/O calls the index keeps per thread:
// the newest, enough for a request's own reads and writes among its
// outbound calls; a span with more I/O than that after the request's own
// finds only the newer calls and, their ends not matching, no connection.
const threadIOKeep = 128

// threadKey is a thread of a process.
type threadKey struct {
	pid, tid uint32
}

// threadIO is one socket call of a thread: its completion and connection.
type threadIO struct {
	end  uint64
	conn string
}

// noteIO remembers a socket event's call on its thread. Only calls a
// thread makes itself count: a tcp_state is the kernel's transition,
// whichever task the softirq ran in.
func (x *connIndex) noteIO(ev *schema.Event) {
	if ev.ConnID == "" || ev.TID == 0 || ev.Event == schema.KindTCPState {
		return
	}
	k := threadKey{ev.PID, ev.TID}
	calls := append(x.io[k], threadIO{end: ev.BootNS + ev.LatencyNS, conn: ev.ConnID})
	if len(calls) >= 2*threadIOKeep {
		// Trimmed in bulk, so the append stays amortised O(1).
		calls = slices.Delete(calls, 0, len(calls)-threadIOKeep)
	}
	x.io[k] = calls
}

// pruneIO forgets the threads whose last socket call completed more than
// retain before now: no held request still waiting reaches back so far.
func (x *connIndex) pruneIO(now, retain uint64) {
	for k, calls := range x.io {
		if calls[len(calls)-1].end+retain < now {
			delete(x.io, k)
		}
	}
}

// byThread returns the one connection of pid on the request's ends
// (local, remote: either may be empty, not both) that thread tid made a
// socket call on completing inside [from, to]; "" for none or several.
func (x *connIndex) byThread(pid, tid uint32, from, to uint64, local, remote string) string {
	l, r := parseEnd(local), parseEnd(remote)
	if !l.IsValid() && !r.IsValid() {
		return "" // no end to tell the request's connection from the thread's others
	}
	found := ""
	calls := x.io[threadKey{pid, tid}]
	for i := len(calls) - 1; i >= 0 && calls[i].end >= from; i-- {
		c := calls[i]
		if c.end > to || c.conn == found || !x.hasEnds(connKey{pid, c.conn}, l, r) {
			continue
		}
		if found != "" {
			return "" // two connections on the request's ends: never a guess
		}
		found = c.conn
	}
	return found
}

// hasEnds reports whether connection k was indexed under a pair with
// every valid end of l and r.
func (x *connIndex) hasEnds(k connKey, l, r netip.AddrPort) bool {
	for _, p := range x.keys[k] {
		if (!l.IsValid() || p.local == l) && (!r.IsValid() || p.remote == r) {
			return true
		}
	}
	return false
}
