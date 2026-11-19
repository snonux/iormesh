package integrationtests

// Socket races end to end (PLAN.md M2 acceptance; docs/testing.md, "M2
// acceptance"): workload/edgepeer's sock-races role runs, in one traced
// process, a refused connect, an unreachable connect, a reset in the
// accept queue, a reset mid-stream and a shutdown racing a blocked
// accept. Its facts lines name each race's fds and ports; the trace must
// show each race's outcome on exactly those sockets, every socket
// instance under one conn_id at most, fused only where both ends really
// connected, and no conn_id or tuple bleeding into a later socket that
// reuses the fd number or the struct sock.

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"iormesh/internal/schema"
)

// sockKey is one socket instance: an fd number and its generation in
// the traced process (fd numbers are reused; the generation is not).
type sockKey struct {
	fd  int32
	gen uint32
}

// sockTrace is the sock-races process's events, indexed for the checks.
type sockTrace struct {
	pid    uint32
	events []schema.Event
	byKey  map[sockKey][]schema.Event
	facts  map[string]map[string]string
	edges  map[string]string // conn node id → state of its connected edge
}

// TestSocketRaces: each race leaves its outcome in the trace on the
// sockets its facts name, and the conn_ids and connected edges stay
// consistent across them all.
func TestSocketRaces(t *testing.T) {
	enableParallelIfRequested(t)
	h := newHarness(t)
	peer := absPath(t, edgePeerDefault)
	var w *Workload
	run := traceJudged(t, h, func(h *Harness) ([]*Workload, error) {
		var err error
		w, err = h.StartHelper(peer, "sock-races")
		return []*Workload{w}, err
	})
	st := newSockTrace(t, run, readFacts(w))
	t.Run("refused", st.checkRefused)
	t.Run("unreachable", st.checkUnreachable)
	t.Run("reset in the accept queue", st.checkResetQueued)
	t.Run("reset mid-stream", st.checkResetMid)
	t.Run("shutdown racing accept", st.checkAcceptRace)
	t.Run("conn ids", st.checkConnIDs)
}

// newSockTrace indexes the run's events of the one traced process by
// socket instance and its connected edges by conn node.
func newSockTrace(t *testing.T, run tracedRun, facts map[string]map[string]string) *sockTrace {
	t.Helper()
	for _, race := range []string{"refused", "unreachable", "reset-queued", "reset-mid", "accept-race"} {
		if facts[race] == nil {
			t.Fatalf("edgepeer printed no %q facts line (got %v)", race, facts)
		}
	}
	st := &sockTrace{pid: uint32(run.pids[0]), byKey: map[sockKey][]schema.Event{},
		facts: facts, edges: map[string]string{}}
	for _, e := range run.Trace.Events {
		if e.PID != st.pid {
			continue
		}
		st.events = append(st.events, e)
		if e.FD >= 0 {
			k := sockKey{e.FD, e.FDGen}
			st.byKey[k] = append(st.byKey[k], e)
		}
	}
	for _, e := range run.Trace.Graph.Edges {
		if e.Type == schema.EdgeConnected {
			st.edges[e.To] = e.State
		}
	}
	return st
}

// checkRefused: the connect to the bound, non-listening port fails with
// ECONNREFUSED after SYN_SENT, never ESTABLISHED; its socket is one conn
// of its own, an unresolved one (nothing answered).
func (st *sockTrace) checkRefused(t *testing.T) {
	f := st.facts["refused"]
	c := st.find(t, "the refused connect", func(e schema.Event) bool {
		return e.Event == schema.KindConnect && st.isFD(e, f["fd"]) && port(e.Args.RemotePort) == f["port"]
	})
	wantErr(t, c, "ECONNREFUSED")
	states := st.states(c)
	if !slices.Equal(states, []string{"SYN_SENT", "CLOSE"}) {
		t.Errorf("refused socket's tcp_states %v, want [SYN_SENT CLOSE]", states)
	}
	st.wantConn(t, c, schema.StateUnresolved)
}

// checkUnreachable: the connect to 255.255.255.255 fails with
// ENETUNREACH before any state transition: no tcp_state, no conn_id, no
// connected edge — the unregistered socket left nothing behind.
func (st *sockTrace) checkUnreachable(t *testing.T) {
	f := st.facts["unreachable"]
	c := st.find(t, "the unreachable connect", func(e schema.Event) bool {
		return e.Event == schema.KindConnect && st.isFD(e, f["fd"]) && e.Err == "ENETUNREACH"
	})
	for _, e := range st.byKey[sockKey{c.FD, c.FDGen}] {
		if e.Event == schema.KindTCPState || e.ConnID != "" {
			t.Errorf("unreachable socket: event %d (%s) with conn_id %q", e.Seq, e.Event, e.ConnID)
		}
	}
}

// checkResetQueued: the client connected (ESTABLISHED, then CLOSE on its
// RST); accept(2) took the already reset child — a CLOSE snapshot with
// the client's port as remote — and its read saw the reset (an error or
// EOF, whichever the helper reported). The two ends are two unresolved
// conns, never fused: the child never reached ESTABLISHED in the trace,
// and a join is never guessed. When the child lands on the client's
// freed fd number (the usual case), it is a new generation of it.
func (st *sockTrace) checkResetQueued(t *testing.T) {
	f := st.facts["reset-queued"]
	client := st.find(t, "the queued client's connect", func(e schema.Event) bool {
		return e.Event == schema.KindConnect && st.isFD(e, f["client_fd"]) && port(e.Args.RemotePort) == f["port"]
	})
	wantErr(t, client, "")
	if got := port(client.Args.LocalPort); got != f["client_port"] {
		t.Errorf("queued client's connect local_port %s, want %s", got, f["client_port"])
	}
	if s := st.states(client); !slices.Equal(s, []string{"SYN_SENT", "ESTABLISHED", "CLOSE"}) {
		t.Errorf("queued client's tcp_states %v, want [SYN_SENT ESTABLISHED CLOSE]", s)
	}
	server := st.find(t, "the accept(2) of the reset child", func(e schema.Event) bool {
		return e.Event == schema.KindAccept && st.isFD(e, f["server_fd"]) && port(e.Args.RemotePort) == f["client_port"]
	})
	st.checkResetChild(t, server, f)
	if server.FD == client.FD && server.FDGen == client.FDGen {
		t.Errorf("the reset child reuses the client's fd %d under the same generation %d", server.FD, server.FDGen)
	}
	cc := st.wantConn(t, client, schema.StateUnresolved)
	sc := st.wantConn(t, server, schema.StateUnresolved)
	if cc == sc {
		t.Errorf("the queued client and the reset child share conn %s", cc)
	}
}

// checkResetChild: the reset child's snapshot is CLOSE, its local port
// is the listener's or 0 (the kernel clears it when the reset unhashes
// the child), never another socket's, and its read reports the reset.
func (st *sockTrace) checkResetChild(t *testing.T, accept schema.Event, f map[string]string) {
	t.Helper()
	if s := st.states(accept); !slices.Equal(s, []string{"CLOSE"}) {
		t.Errorf("reset child's tcp_states %v, want the [CLOSE] snapshot only", s)
	}
	if lp := port(accept.Args.LocalPort); lp != f["port"] && lp != "0" && lp != "" {
		t.Errorf("reset child's local_port %s, want %s (or 0)", lp, f["port"])
	}
	want := f["read_err"]
	if want == "none" {
		want = ""
	}
	reads := st.of(accept, schema.KindRead)
	if len(reads) != 1 || reads[0].Err != want {
		t.Errorf("reset child's reads %v, want one with err %q", summarize(reads), want)
	}
}

// checkResetMid: client and server end are one fused conn; the data
// moved over it and the server's read after the client's reset fails
// with ECONNRESET on that same conn; both ends end in CLOSE.
func (st *sockTrace) checkResetMid(t *testing.T) {
	f := st.facts["reset-mid"]
	client := st.find(t, "the mid-stream client's connect", func(e schema.Event) bool {
		return e.Event == schema.KindConnect && st.isFD(e, f["client_fd"]) && port(e.Args.RemotePort) == f["port"]
	})
	server := st.find(t, "the mid-stream accept4", func(e schema.Event) bool {
		return e.Event == schema.KindAccept4 && st.isFD(e, f["server_fd"]) && port(e.Args.RemotePort) == f["client_port"]
	})
	if lp := port(server.Args.LocalPort); lp != f["port"] {
		t.Errorf("mid-stream accept4 local_port %s, want %s", lp, f["port"])
	}
	cc := st.wantConn(t, client, schema.StateFused)
	sc := st.wantConn(t, server, schema.StateFused)
	if cc != sc {
		t.Fatalf("mid-stream ends on conns %s and %s, want one fused conn", cc, sc)
	}
	reads := st.of(server, schema.KindRead)
	if len(reads) != 2 || reads[0].Err != "" || reads[0].Bytes != 5 || reads[1].Err != "ECONNRESET" {
		t.Errorf("mid-stream server reads %v, want 5 bytes then ECONNRESET", summarize(reads))
	}
	for _, end := range []schema.Event{client, server} {
		if s := st.states(end); len(s) == 0 || s[len(s)-1] != "CLOSE" {
			t.Errorf("mid-stream end fd %d: tcp_states %v, want a final CLOSE", end.FD, s)
		}
	}
}

// checkAcceptRace: the accept4 blocked on the racing thread (not the
// main one) fails with EINVAL once the listener is shut down, and
// returns no socket: no fd, no conn_id.
func (st *sockTrace) checkAcceptRace(t *testing.T) {
	f := st.facts["accept-race"]
	a := st.find(t, "the accept4 racing the shutdown", func(e schema.Event) bool {
		return e.Event == schema.KindAccept4 && strconv.Itoa(int(e.TID)) == f["tid"]
	})
	wantErr(t, a, "EINVAL")
	if a.TID == st.pid || a.FD >= 0 || a.ConnID != "" {
		t.Errorf("racing accept4: tid %d (pid %d), fd %d, conn_id %q; want another thread, no fd, no conn",
			a.TID, st.pid, a.FD, a.ConnID)
	}
}

// checkConnIDs: every socket instance carries one conn_id at most; a
// conn_id is shared by two instances exactly when its connected edge is
// fused, and by one when unresolved; every conn of the events has its
// edge and every edge its events (no stale edge). The races made four
// conns: the refused connect, both ends of the queued reset, and the
// mid-stream connection.
func (st *sockTrace) checkConnIDs(t *testing.T) {
	holders := map[string][]sockKey{}
	for k, evs := range st.byKey {
		ids := map[string]bool{}
		for _, e := range evs {
			if e.ConnID != "" {
				ids[e.ConnID] = true
			}
		}
		if len(ids) > 1 {
			t.Errorf("fd %d gen %d carries conn_ids %v, want one", k.fd, k.gen, ids)
		}
		for id := range ids {
			holders[id] = append(holders[id], k)
		}
	}
	for id, keys := range holders {
		want := map[int]string{1: schema.StateUnresolved, 2: schema.StateFused}[len(keys)]
		if got, ok := st.edges[schema.SocketNodeID(id)]; !ok || got != want {
			t.Errorf("conn %s on %d sockets %v: edge %q (present %v), want %q", id, len(keys), keys, got, ok, want)
		}
	}
	if len(holders) != len(st.edges) || len(holders) != 4 {
		t.Errorf("%d conns on events, %d connected edges; want 4 each", len(holders), len(st.edges))
	}
}

// find returns the process's first event that match accepts.
func (st *sockTrace) find(t *testing.T, what string, match func(schema.Event) bool) schema.Event {
	t.Helper()
	for _, e := range st.events {
		if match(e) {
			return e
		}
	}
	t.Fatalf("%s is not in the trace", what)
	return schema.Event{}
}

// of returns the events of kind on e's socket instance, in order.
func (st *sockTrace) of(e schema.Event, kind string) []schema.Event {
	var out []schema.Event
	for _, x := range st.byKey[sockKey{e.FD, e.FDGen}] {
		if x.Event == kind {
			out = append(out, x)
		}
	}
	return out
}

// states lists the TCP states e's socket instance went through.
func (st *sockTrace) states(e schema.Event) []string {
	var out []string
	for _, x := range st.of(e, schema.KindTCPState) {
		out = append(out, *x.Args.SockState)
	}
	return out
}

// wantConn checks that e's socket instance carries a conn_id, the same on
// all of its events, whose connected edge has state; it returns the id.
func (st *sockTrace) wantConn(t *testing.T, e schema.Event, state string) string {
	t.Helper()
	id := ""
	for _, x := range st.byKey[sockKey{e.FD, e.FDGen}] {
		if x.ConnID == "" || x.ConnID == id {
			continue
		}
		if id != "" {
			t.Fatalf("fd %d gen %d: conn_ids %s and %s", e.FD, e.FDGen, id, x.ConnID)
		}
		id = x.ConnID
	}
	if id == "" {
		t.Fatalf("fd %d gen %d (%s) has no conn_id", e.FD, e.FDGen, e.Event)
	}
	if got := st.edges[schema.SocketNodeID(id)]; got != state {
		t.Errorf("conn %s of fd %d gen %d: edge state %q, want %q", id, e.FD, e.FDGen, got, state)
	}
	return id
}

// isFD reports whether e's fd is the one a facts line printed.
func (st *sockTrace) isFD(e schema.Event, fd string) bool {
	return strconv.Itoa(int(e.FD)) == fd
}

// port renders an optional port the way the facts print it; "" unset.
func port(p *uint16) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(int(*p))
}

// wantErr checks e's errno name ("" for success).
func wantErr(t *testing.T, e schema.Event, want string) {
	t.Helper()
	if e.Err != want {
		t.Errorf("event %d (%s fd %d): err %q, want %q", e.Seq, e.Event, e.FD, e.Err, want)
	}
}

// summarize renders events for a failure message.
func summarize(evs []schema.Event) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, fmt.Sprintf("%s(bytes=%d err=%q)", e.Event, e.Bytes, e.Err))
	}
	return out
}

// readFacts parses the facts lines a helper printed after its PID
// ("name k=v …", workload/edgepeer) into name → key → value. The
// workload's stdout channel holds them once it exited, and is closed at
// its EOF.
func readFacts(w *Workload) map[string]map[string]string {
	facts := map[string]map[string]string{}
	for line := range w.proc.stdout {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		kv := map[string]string{}
		for _, f := range fields[1:] {
			k, v, _ := strings.Cut(f, "=")
			kv[k] = v
		}
		facts[fields[0]] = kv
	}
	return facts
}
