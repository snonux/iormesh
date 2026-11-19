package correlate

import (
	"reflect"
	"testing"

	"iormesh/internal/agentspool"
	"iormesh/internal/schema"
)

// lossyRing is a fakeRing whose agent reports the given losses.
type lossyRing struct {
	*fakeRing
	losses agentspool.Losses
}

func (r lossyRing) Losses() agentspool.Losses { return r.losses }

// sock sends a socket event of the JVM's main thread carrying conn's ends,
// as the correlator emits a tcp_state (state) once the conn id is known.
func (jt *joinTest) sock(at uint64, conn, local string, lport uint16, remote string,
	rport uint16, state string) {
	jt.t.Helper()
	jt.seq++
	ev := schema.Event{SchemaVersion: schema.Version, Project: "p", Seq: jt.seq, BootNS: at,
		WallNS: at + 7, HostID: "h", PID: jvmPID, PIDInc: jvmStart, TID: mainTID, Comm: "java",
		Event: schema.KindTCPState, FD: 5, ConnID: conn, Attribution: schema.AttributionNone}
	ev.Args.LocalAddr, ev.Args.LocalPort = &local, &lport
	ev.Args.RemoteAddr, ev.Args.RemotePort = &remote, &rport
	ev.Args.SockState = &state
	if err := jt.j.Event(ev); err != nil {
		jt.t.Fatal(err)
	}
}

// request is an HTTP record of the main thread at boot time at.
func (jt *joinTest) request(at uint64, path, local, remote string) agentspool.HTTP {
	return agentspool.HTTP{ExitMono: jt.mono(at), StackID: 10, ThreadID: 1, KTID: mainTID,
		Status: 200, Method: "GET", Scheme: "http", Host: "127.0.0.1", Path: path,
		Version: "HTTP/1.1", Local: local, Remote: remote,
		ReqHeaders:  []string{"authorization: Bearer leaked"},
		RespHeaders: []string{"location: /next?token=leaked", "x-a: b"},
		ReqBytes:    -1, RespBytes: -1}
}

// kinds lists the sink's event kinds in order.
func kinds(evs []schema.Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Event
	}
	return out
}

// TestJavaJoinHTTPEvents: each request becomes an http event after the
// syscalls that completed before it, on its connection (found by its two
// ends, the kernel's mapped address unmapped), numbered on it, joined to
// its stack and redacted again by the tracer's policy.
func TestJavaJoinHTTPEvents(t *testing.T) {
	jt := newJoinTest(t)
	at := jt.now + 1000
	jt.ring(jt.request(at+1000, "/a?token=leaked", "127.0.0.1:40000", "127.0.0.1:8080"),
		jt.request(at+3000, "/b", "127.0.0.1:40000", "127.0.0.1:8080"),
		jt.watermark(at+20_000_000))
	jt.sock(at, "c1", "::ffff:127.0.0.1", 40000, "::ffff:127.0.0.1", 8080, "ESTABLISHED")
	jt.event(jvmPID, mainTID, "write", at+500)
	jt.event(jvmPID, mainTID, "read", at+2000)
	jt.event(999, 1000, "write", at+10_000_000)
	jt.close()
	want := []string{"tcp_state", "write", "http", "read", "http", "write"}
	if got := kinds(jt.sink.events); !reflect.DeepEqual(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	h1, h2 := jt.sink.events[2], jt.sink.events[4]
	wantHTTP := &schema.HTTP{Method: "GET", Scheme: "http", Host: "127.0.0.1",
		Path: "/a?token=***", Status: 200, ReqSeq: 1, Version: "HTTP/1.1",
		ReqHeaders:  []string{"authorization: ***"},
		RespHeaders: []string{"location: /next?token=***", "x-a: b"}}
	if !reflect.DeepEqual(h1.HTTP, wantHTTP) || h1.ConnID != "c1" || h2.HTTP.ReqSeq != 2 {
		t.Errorf("http events %+v %+v, want %+v on c1, then req_seq 2", h1.HTTP, h2, wantHTTP)
	}
	if h1.Attribution != schema.AttributionJava || h1.Java.Thread != "main" ||
		h1.PID != jvmPID || h1.PIDInc != jvmStart || h1.TID != mainTID || h1.FD != schema.NoFD ||
		h1.WallNS != h1.BootNS+7 || h1.Project != "p" || h1.HostID != "h" {
		t.Errorf("http envelope %+v", h1)
	}
	if s := jt.j.Stats(); s.HTTP != 2 || s.HTTPNoConn != 0 {
		t.Errorf("stats %+v", s)
	}
}

// TestJavaJoinHTTPLatencyAndSizes: a record with a start becomes an event
// spanning the request (boot_ns the start, boot_ns + latency_ns its
// completion), ordered by that completion; known body sizes are kept, an
// unknown one (-1) is absent, and a record without a start keeps its
// completion as boot_ns with no latency.
func TestJavaJoinHTTPLatencyAndSizes(t *testing.T) {
	jt := newJoinTest(t)
	at := jt.now + 1000
	timed := jt.request(at+3000, "/t", "127.0.0.1:40000", "127.0.0.1:8080")
	timed.StartMono, timed.ReqBytes, timed.RespBytes = jt.mono(at+1000), 0, -1
	untimed := jt.request(at+5000, "/u", "127.0.0.1:40000", "127.0.0.1:8080")
	untimed.ReqBytes, untimed.RespBytes = -1, 12
	jt.ring(timed, untimed, jt.watermark(at+20_000_000))
	jt.event(jvmPID, mainTID, "read", at+2000)
	jt.event(999, 1000, "write", at+10_000_000)
	jt.close()
	want := []string{"read", "http", "http", "write"}
	if got := kinds(jt.sink.events); !reflect.DeepEqual(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	h1, h2 := jt.sink.events[1], jt.sink.events[2]
	// The wall clock moves with the boot clock: the same offset on both.
	if h1.BootNS != at+1000 || h1.LatencyNS != 2000 || h1.WallNS-h1.BootNS != h2.WallNS-h2.BootNS {
		t.Errorf("timed: boot %d latency %d wall %d, want %d, 2000", h1.BootNS, h1.LatencyNS,
			h1.WallNS, at+1000)
	}
	if h1.HTTP.ReqBytes == nil || *h1.HTTP.ReqBytes != 0 || h1.HTTP.RespBytes != nil {
		t.Errorf("timed sizes %v %v, want 0 and absent", h1.HTTP.ReqBytes, h1.HTTP.RespBytes)
	}
	if h2.BootNS != at+5000 || h2.LatencyNS != 0 || h2.HTTP.ReqBytes != nil ||
		h2.HTTP.RespBytes == nil || *h2.HTTP.RespBytes != 12 {
		t.Errorf("untimed %+v %+v", h2, h2.HTTP)
	}
}

// TestJavaJoinHTTPSidesNumberedApart: a same-JVM loopback call has both
// ends' events on one conn_id; each end counts its own requests, so the
// client's and the server's of one request carry the same req_seq.
func TestJavaJoinHTTPSidesNumberedApart(t *testing.T) {
	jt := newJoinTest(t)
	at := jt.now + 1000
	var recs []agentspool.Record
	for i := range uint64(2) {
		srv := jt.request(at+1000+i*2000, "/", "", "127.0.0.1:40000")
		srv.Server = true
		cli := jt.request(at+2000+i*2000, "/", "127.0.0.1:40000", "127.0.0.1:8080")
		recs = append(recs, srv, cli)
	}
	jt.ring(append(recs, jt.watermark(at+20_000_000))...)
	jt.sock(at, "c1", "127.0.0.1", 40000, "127.0.0.1", 8080, "ESTABLISHED")
	jt.sock(at, "c1", "127.0.0.1", 8080, "127.0.0.1", 40000, "ESTABLISHED")
	jt.close()
	var got []uint32
	for _, e := range jt.sink.events {
		if e.Event == schema.KindHTTP {
			got = append(got, e.HTTP.ReqSeq)
		}
	}
	if !reflect.DeepEqual(got, []uint32{1, 1, 2, 2}) {
		t.Errorf("req_seq server, client, server, client = %v, want 1 1 2 2", got)
	}
}

// TestJavaJoinHTTPStopsAfterRetries: a request that took several
// exchanges (a redirect followed by the client library) and every later
// one on its connection stay unnumbered; the other end still counts.
func TestJavaJoinHTTPStopsAfterRetries(t *testing.T) {
	jt := newJoinTest(t)
	at := jt.now + 1000
	first := jt.request(at+1000, "/", "127.0.0.1:40000", "127.0.0.1:8080")
	retried := jt.request(at+2000, "/", "127.0.0.1:40000", "127.0.0.1:8080")
	retried.Exchanges = 2
	later := jt.request(at+3000, "/", "127.0.0.1:40000", "127.0.0.1:8080")
	jt.ring(first, retried, later, jt.watermark(at+20_000_000))
	jt.sock(at, "c1", "127.0.0.1", 40000, "127.0.0.1", 8080, "ESTABLISHED")
	jt.close()
	var got []uint32
	for _, e := range jt.sink.events {
		if e.Event == schema.KindHTTP {
			got = append(got, e.HTTP.ReqSeq)
		}
	}
	if !reflect.DeepEqual(got, []uint32{1, 0, 0}) {
		t.Errorf("req_seq %v, want 1 then none", got)
	}
}

// TestJavaJoinHTTPWaits: an http event waits until an event that
// completed Grace after it arrived — or Timeout — so a syscall of its
// thread that completed before it is never emitted after it.
func TestJavaJoinHTTPWaits(t *testing.T) {
	jt := newJoinTest(t)
	at := jt.now + 1000
	jt.ring(jt.request(at+1000, "/", "", ""), jt.watermark(at+20_000_000))
	jt.event(jvmPID, mainTID, "write", at+500)
	jt.now += defaultScanEvery
	if err := jt.j.Poll(); err != nil {
		t.Fatal(err)
	}
	if got := kinds(jt.sink.events); !reflect.DeepEqual(got, []string{"write"}) {
		t.Fatalf("before Grace: %v, want only the write", got)
	}
	jt.now = at + 1000 + defaultTimeout
	if err := jt.j.Poll(); err != nil {
		t.Fatal(err)
	}
	if got := kinds(jt.sink.events); !reflect.DeepEqual(got, []string{"write", "http"}) {
		t.Fatalf("after Timeout: %v", got)
	}
	jt.close()
}

// TestJavaJoinHTTPUnnumbered: no req_seq without a connection, over
// HTTP/2, or once the JVM's ring dropped an HTTP record.
func TestJavaJoinHTTPUnnumbered(t *testing.T) {
	cases := map[string]struct {
		version string
		remote  string
		losses  agentspool.Losses
		conn    string
	}{
		"no connection": {"HTTP/1.1", "127.0.0.1:9", agentspool.Losses{}, ""},
		"http/2":        {"HTTP/2", "127.0.0.1:8080", agentspool.Losses{}, "c1"},
		"dropped http":  {"HTTP/1.1", "127.0.0.1:8080", agentspool.Losses{DroppedHTTP: 1}, "c1"},
		"abandoned":     {"HTTP/1.1", "127.0.0.1:8080", agentspool.Losses{Abandoned: 1}, "c1"},
		"malformed":     {"HTTP/1.1", "127.0.0.1:8080", agentspool.Losses{Malformed: 1}, "c1"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			jt := newJoinTest(t)
			at := jt.now + 1000
			r := jt.request(at+1000, "/", "", c.remote)
			r.Version = c.version
			fr := jt.ring(r, jt.watermark(at+20_000_000))
			jt.spool = []AgentRing{lossyRing{fr, c.losses}}
			jt.sock(at, "c1", "127.0.0.1", 40000, "127.0.0.1", 8080, "ESTABLISHED")
			jt.close()
			h := jt.sink.events[len(jt.sink.events)-1]
			if h.Event != schema.KindHTTP || h.ConnID != c.conn || h.HTTP.ReqSeq != 0 {
				t.Errorf("event %+v %+v, want http on %q without req_seq", h, h.HTTP, c.conn)
			}
		})
	}
}

// TestJavaJoinHTTPBeforeEnd: the trace's end event stays last.
func TestJavaJoinHTTPBeforeEnd(t *testing.T) {
	jt := newJoinTest(t)
	at := jt.now + 1000
	jt.ring(jt.request(at+5000, "/", "", ""), jt.watermark(at+20_000_000))
	jt.seq++
	reason := schema.EndTargetsExited
	end := schema.Event{SchemaVersion: schema.Version, Project: "p", Seq: jt.seq, BootNS: at,
		HostID: "h", Event: schema.KindEnd, FD: schema.NoFD, Attribution: schema.AttributionNone}
	end.Args.EndReason = &reason
	if err := jt.j.Event(end); err != nil {
		t.Fatal(err)
	}
	jt.close()
	if got := kinds(jt.sink.events); !reflect.DeepEqual(got, []string{"http", "end"}) {
		t.Fatalf("order %v, want the end last", got)
	}
}

// TestConnIndex: a request's connection by both ends, by the remote end
// alone (a server after the close), by the local end alone (a client of
// a named host); an end several connections share answers nothing, and
// a closed one is forgotten after Retain.
func TestConnIndex(t *testing.T) {
	x := newConnIndex()
	ev := func(conn, state string, at uint64, local string, lport uint16, remote string,
		rport uint16) *schema.Event {
		e := &schema.Event{PID: 1, Event: schema.KindTCPState, ConnID: conn, BootNS: at}
		e.Args.LocalAddr, e.Args.LocalPort = &local, &lport
		e.Args.RemoteAddr, e.Args.RemotePort = &remote, &rport
		e.Args.SockState = &state
		return e
	}
	x.observe(ev("c1", "ESTABLISHED", 10, "::ffff:10.0.0.1", 8080, "::ffff:10.0.0.2", 5000))
	x.observe(ev("c2", "ESTABLISHED", 20, "::1", 6000, "::1", 443))
	for _, c := range []struct{ local, remote, want string }{
		{"10.0.0.1:8080", "10.0.0.2:5000", "c1"},
		{"", "10.0.0.2:5000", "c1"},
		{"[::1]:6000", "", "c2"},
		{"10.0.0.1:8080", "10.0.0.9:1", ""},
		{"", "", ""},
		{"garbage", "", ""},
	} {
		if got := x.lookup(1, c.local, c.remote); got != c.want {
			t.Errorf("lookup(%q, %q) = %q, want %q", c.local, c.remote, got, c.want)
		}
	}
	x.observe(ev("c3", "ESTABLISHED", 25, "::1", 6001, "::1", 443))
	if got := x.lookup(1, "", "[::1]:443"); got != "" {
		t.Errorf("remote end of two connections gave %q, want none", got)
	}
	if got := x.lookup(1, "[::1]:6001", "[::1]:443"); got != "c3" {
		t.Errorf("both ends of c3 gave %q", got)
	}
	x.observe(ev("c1", "CLOSE", 30, "::ffff:10.0.0.1", 8080, "::ffff:10.0.0.2", 5000))
	x.purge(30+defaultRetain, defaultRetain)
	if got := x.lookup(1, "", "10.0.0.2:5000"); got != "c1" {
		t.Errorf("forgotten before Retain passed: %q", got)
	}
	x.purge(31+defaultRetain, defaultRetain)
	if got := x.lookup(1, "", "10.0.0.2:5000"); got != "" || len(x.closed) != 0 {
		t.Errorf("still known after Retain: %q", got)
	}
}

// TestJavaJoinHTTPUnorderedStops: an unordered record (a sendAsync
// completion) gets no req_seq, and its connection's client end numbers no
// more; the server end, counted apart, goes on.
func TestJavaJoinHTTPUnorderedStops(t *testing.T) {
	jt := newJoinTest(t)
	at := jt.now + 1000
	first := jt.request(at+1000, "/1", "127.0.0.1:40000", "127.0.0.1:8080")
	async := jt.request(at+2000, "/2", "127.0.0.1:40000", "127.0.0.1:8080")
	async.Unordered = true
	after := jt.request(at+3000, "/3", "127.0.0.1:40000", "127.0.0.1:8080")
	jt.ring(first, async, after, jt.watermark(at+20_000_000))
	jt.sock(at, "c1", "127.0.0.1", 40000, "127.0.0.1", 8080, "ESTABLISHED")
	jt.event(999, 1000, "write", at+10_000_000)
	jt.close()
	var seqs []uint32
	for _, e := range jt.sink.events {
		if e.Event == schema.KindHTTP {
			seqs = append(seqs, e.HTTP.ReqSeq)
		}
	}
	if !reflect.DeepEqual(seqs, []uint32{1, 0, 0}) {
		t.Errorf("req_seq %v, want 1, then none from the unordered record on", seqs)
	}
}

// TestJavaJoinDNSEvents: a DNS record becomes a dns event after the
// syscalls that completed before it, with its mapping, the lookup's
// thread and stack, and no connection.
func TestJavaJoinDNSEvents(t *testing.T) {
	jt := newJoinTest(t)
	at := jt.now + 1000
	jt.ring(agentspool.DNS{ExitMono: jt.mono(at + 1000), StackID: 10, ThreadID: 1, KTID: mainTID,
		Host: "localhost", Addrs: []string{"127.0.0.1"}}, jt.watermark(at+20_000_000))
	jt.event(jvmPID, mainTID, "read", at+500)
	jt.event(jvmPID, mainTID, "write", at+2000)
	jt.event(999, 1000, "write", at+10_000_000)
	jt.close()
	want := []string{"read", "dns", "write", "write"}
	if got := kinds(jt.sink.events); !reflect.DeepEqual(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	d := jt.sink.events[1]
	if d.DNS == nil || d.DNS.Host != "localhost" || !reflect.DeepEqual(d.DNS.Addrs, []string{"127.0.0.1"}) ||
		d.Attribution != schema.AttributionJava || d.Java.Thread != "main" || d.ConnID != "" ||
		d.FD != schema.NoFD || d.Validate() != nil {
		t.Errorf("dns event %+v (%+v)", d, d.DNS)
	}
	if s := jt.j.Stats(); s.DNS != 1 || s.HTTP != 0 {
		t.Errorf("stats %+v", s)
	}
}

// netHTTPSeqs runs records on c1 (127.0.0.1:40000 → :8080) through a join
// and returns the http events' req_seq in stream order.
func netHTTPSeqs(t *testing.T, recs func(jt *joinTest, at uint64) []agentspool.Record) []uint32 {
	t.Helper()
	jt := newJoinTest(t)
	at := jt.now + 1000
	jt.ring(recs(jt, at)...)
	jt.sock(at, "c1", "127.0.0.1", 40000, "127.0.0.1", 8080, "ESTABLISHED")
	jt.event(999, 1000, "write", at+10_000_000)
	jt.close()
	var seqs []uint32
	for _, e := range jt.sink.events {
		if e.Event == schema.KindHTTP {
			seqs = append(seqs, e.HTTP.ReqSeq)
		}
	}
	return seqs
}

// netHTTP is a java.net.http client request of the main thread on c1.
func (jt *joinTest) netHTTP(at uint64, path string) agentspool.HTTP {
	r := jt.request(at, path, "127.0.0.1:40000", "127.0.0.1:8080")
	r.NetHTTP = true
	return r
}

// asyncMark is the marker HttpClient.sendAsync's entry writes.
func (jt *joinTest) asyncMark(at uint64) agentspool.HTTP {
	return agentspool.HTTP{ExitMono: jt.mono(at), Exchanges: 1, AsyncMark: true,
		ReqBytes: -1, RespBytes: -1}
}

// TestJavaJoinHTTPAsyncMarkerStopsNetHTTP: the review's interleaving.
// Sync P completes before any sendAsync and keeps its ordinal; async A is
// issued (its marker), its connection goes back to the pool while its
// record waits, and sync B reuses it — B's record arrives first. Server
// side A is 2 and B 3; numbering B 2 would pair it with A's server event,
// so B gets none, nor does A (unordered), nor anything after.
func TestJavaJoinHTTPAsyncMarkerStopsNetHTTP(t *testing.T) {
	seqs := netHTTPSeqs(t, func(jt *joinTest, at uint64) []agentspool.Record {
		a := jt.netHTTP(at+4000, "/a")
		a.Unordered = true
		return []agentspool.Record{jt.netHTTP(at+1000, "/p"), jt.asyncMark(at + 2000),
			jt.netHTTP(at+3000, "/b"), a, jt.netHTTP(at+5000, "/c"),
			jt.watermark(at + 20_000_000)}
	})
	if !reflect.DeepEqual(seqs, []uint32{1, 0, 0, 0}) {
		t.Errorf("req_seq %v, want P 1, then none from the sendAsync marker on", seqs)
	}
}

// TestJavaJoinHTTPSyncNetHTTPNumbered: a JVM that never calls sendAsync
// numbers its java.net.http requests, once the watermark passed them.
func TestJavaJoinHTTPSyncNetHTTPNumbered(t *testing.T) {
	seqs := netHTTPSeqs(t, func(jt *joinTest, at uint64) []agentspool.Record {
		return []agentspool.Record{jt.netHTTP(at+1000, "/1"), jt.netHTTP(at+2000, "/2"),
			jt.watermark(at + 20_000_000)}
	})
	if !reflect.DeepEqual(seqs, []uint32{1, 2}) {
		t.Errorf("req_seq %v, want 1, 2", seqs)
	}
}

// TestJavaJoinHTTPNetHTTPNeedsWatermark: without a watermark past its
// stamp a java.net.http request is released by the timeout or the close
// and not numbered: an unread sendAsync marker could precede it.
func TestJavaJoinHTTPNetHTTPNeedsWatermark(t *testing.T) {
	seqs := netHTTPSeqs(t, func(jt *joinTest, at uint64) []agentspool.Record {
		return []agentspool.Record{jt.netHTTP(at+1000, "/1")}
	})
	if !reflect.DeepEqual(seqs, []uint32{0}) {
		t.Errorf("req_seq %v, want none without a watermark", seqs)
	}
}

// TestJavaJoinHTTPAsyncMarkerSparesOthers: a sendAsync marker leaves
// HttpURLConnection requests (not java.net.http) and server ends numbered.
func TestJavaJoinHTTPAsyncMarkerSparesOthers(t *testing.T) {
	seqs := netHTTPSeqs(t, func(jt *joinTest, at uint64) []agentspool.Record {
		// The server end, counted apart on the same connection index.
		srv := jt.request(at+3000, "/s", "127.0.0.1:40000", "127.0.0.1:8080")
		srv.Server = true
		return []agentspool.Record{jt.asyncMark(at + 1000),
			jt.request(at+2000, "/u1", "127.0.0.1:40000", "127.0.0.1:8080"), srv,
			jt.request(at+4000, "/u2", "127.0.0.1:40000", "127.0.0.1:8080"),
			jt.watermark(at + 20_000_000)}
	})
	if !reflect.DeepEqual(seqs, []uint32{1, 1, 2}) {
		t.Errorf("req_seq %v, want HttpURLConnection 1, server 1, HttpURLConnection 2", seqs)
	}
}
