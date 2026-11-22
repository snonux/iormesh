package correlate

import (
	"testing"

	"iormesh/internal/agentspool"
	"iormesh/internal/schema"
)

// sockIO sends a socket I/O event (kind, on conn) of tid completing 100
// ns after boot time at.
func (jt *joinTest) sockIO(tid uint32, kind, conn string, at uint64) {
	jt.t.Helper()
	jt.seq++
	ev := schema.Event{SchemaVersion: schema.Version, Project: "p", Seq: jt.seq, BootNS: at,
		HostID: "h", PID: jvmPID, PIDInc: jvmStart, TID: tid, Comm: "java", Event: kind,
		FD: 5, ConnID: conn, LatencyNS: 100, Attribution: schema.AttributionNone}
	if err := jt.j.Event(&ev); err != nil {
		jt.t.Fatal(err)
	}
}

// forwardedCase is one scenario of TestJavaJoinHTTPForwarded: the
// request's span [at+1000, at+5000], what the server thread did on which
// connection and when (offsets from at), and the conn_id the event must
// get.
type forwardedCase struct {
	held    bool
	virtual bool
	io      []forwardedIO
	want    string
}

type forwardedIO struct {
	kind, conn string
	at         uint64
}

// TestJavaJoinHTTPForwarded (task 7j4): behind a proxy that names the
// client (RemoteIpValve, ForwardedHeaderFilter) the agent records a
// servlet request's local end only, which every accepted connection of
// the listener shares. A held request with a start is then joined by its
// thread's own socket I/O inside the span: the one connection on the
// request's ends it touched. Never a guess: not without a held span, not
// for a virtual thread (its carrier serves others inside the span), not
// when the thread touched two such connections, and never by I/O outside
// the span or on a connection whose ends are not the request's (the
// thread's own call to a database) or that this process opened toward
// the request's local end (a call to its own listener).
func TestJavaJoinHTTPForwarded(t *testing.T) {
	body := forwardedIO{"read", "c1", 2000}
	cases := map[string]forwardedCase{
		"own read in the span": {held: true, io: []forwardedIO{body}, want: "c1"},
		"database call too": {held: true, want: "c1",
			io: []forwardedIO{body, {"write", "db", 3000}}},
		"not held":       {held: false, io: []forwardedIO{body}},
		"virtual thread": {held: true, virtual: true, io: []forwardedIO{body}},
		"two inbound": {held: true,
			io: []forwardedIO{body, {"read", "c2", 3000}}},
		"before the span": {held: true, io: []forwardedIO{{"read", "c1", 500}}},
		"after the span":  {held: true, io: []forwardedIO{{"write", "c1", 6000}}},
		"database only":   {held: true, io: []forwardedIO{{"write", "db", 3000}}},
		// The servlet calls its own listener (review of 7j4): the loopback
		// connection's server end is on the request's local end, but this
		// thread is its client.
		"self call only": {held: true,
			io: []forwardedIO{{"write", "self", 2000}, {"read", "self", 3000}}},
		"own read and a self call": {held: true, want: "c1",
			io: []forwardedIO{body, {"write", "self", 2500}, {"read", "self", 3000}}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			ev := forwardedEvent(t, c)
			if ev.ConnID != c.want {
				t.Errorf("conn_id %q, want %q", ev.ConnID, c.want)
			}
			if wantSeq := uint32(min(len(c.want), 1)); ev.HTTP.ReqSeq != wantSeq {
				t.Errorf("req_seq %d, want %d", ev.HTTP.ReqSeq, wantSeq)
			}
		})
	}
}

// forwardedEvent runs one forwardedCase and returns its http event.
func forwardedEvent(t *testing.T, c forwardedCase) schema.Event {
	jt := newJoinTest(t)
	at := jt.now + 1000
	const carrier = 90
	r := jt.request(at+5000, "/f", "127.0.0.1:8080", "")
	r.Server, r.Held, r.StartMono = true, c.held, jt.mono(at+1000)
	recs := []agentspool.Record{}
	tid := uint32(mainTID)
	if c.virtual {
		r.ThreadID, r.KTID, tid = 3, carrier, carrier
		recs = append(recs, agentspool.Thread{ID: 3, KTID: carrier, Virtual: true})
	}
	jt.ring(append(recs, r, jt.watermark(at+20_000_000))...)
	// Two clients' connections through the proxy on the listener, the
	// thread's own connection to a database, and its call to its own
	// listener: one conn_id for both ends of the loopback connection.
	jt.sock(at, "c1", "127.0.0.1", 8080, "10.0.0.5", 50001, "ESTABLISHED")
	jt.sock(at, "c2", "127.0.0.1", 8080, "10.0.0.5", 50002, "ESTABLISHED")
	jt.sock(at, "db", "127.0.0.1", 40001, "10.0.0.9", 5432, "ESTABLISHED")
	jt.sock(at, "self", "127.0.0.1", 40002, "127.0.0.1", 8080, "ESTABLISHED")
	jt.sock(at, "self", "127.0.0.1", 8080, "127.0.0.1", 40002, "ESTABLISHED")
	for _, io := range c.io {
		jt.sockIO(tid, io.kind, io.conn, at+io.at)
	}
	jt.event(999, 1000, "write", at+10_000_000)
	jt.close()
	for _, ev := range jt.sink.events {
		if ev.Event == schema.KindHTTP {
			return ev
		}
	}
	t.Fatalf("no http event in %v", kinds(jt.sink.events))
	return schema.Event{}
}

// TestConnIndexThreadIOBounded: the index keeps a thread's newest TCP socket
// calls only, never a kernel transition (tcp_state), and forgets a thread
// idle for Retain.
func TestConnIndexThreadIOBounded(t *testing.T) {
	x := newConnIndex()
	x.observe(sockEvent(schema.KindTCPState, "c1", "ESTABLISHED", 0, "10.0.0.1", 8080,
		"10.0.0.2", 5000))
	x.observe(sockEvent(schema.KindSendto, "u1", "", 0, "10.0.0.1", 45678, "10.0.0.53", 53))
	x.noteIO(&schema.Event{PID: 1, TID: 4, Event: schema.KindSendto, ConnID: "u1", BootNS: 1})
	if _, ok := x.io[threadKey{1, 4}]; ok {
		t.Error("a UDP flow's call noted")
	}
	for i := range uint64(10 * threadIOKeep) {
		x.noteIO(&schema.Event{PID: 1, TID: 2, Event: schema.KindRead, ConnID: "c1", BootNS: i})
	}
	x.noteIO(&schema.Event{PID: 1, TID: 3, Event: schema.KindTCPState, ConnID: "c1", BootNS: 1})
	if n := len(x.io[threadKey{1, 2}]); n < threadIOKeep || n >= 2*threadIOKeep {
		t.Errorf("%d calls kept, want [%d, %d)", n, threadIOKeep, 2*threadIOKeep)
	}
	if _, ok := x.io[threadKey{1, 3}]; ok {
		t.Error("a tcp_state noted as the thread's call")
	}
	x.purge(10*threadIOKeep-1+defaultRetain, defaultRetain) // the last call ended at 10*keep-1
	if len(x.io) != 1 {
		t.Errorf("forgotten before Retain: %d threads", len(x.io))
	}
	x.purge(10*threadIOKeep+defaultRetain, defaultRetain)
	if len(x.io) != 0 {
		t.Errorf("still kept after Retain: %d threads", len(x.io))
	}
}
