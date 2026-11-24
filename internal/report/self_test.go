package report

import (
	"testing"

	"iormesh/internal/schema"
)

// selfTrace is one process (pid 50) that calls its own server over
// loopback — both ends of s1, fds 5 (client, thread 51) and 6 (server,
// thread 52) — and talks to itself through a pipe (p1: read end 7, write
// end 8).
func selfTrace() *trace {
	tr := &trace{host: "h1"}
	tr.add(50, 51, schema.KindConnect, 5, 0, "s1", schema.Args{RemoteAddr: ptr("127.0.0.1"),
		RemotePort: ptr(uint16(9000))}, nil)
	tr.add(50, 52, schema.KindAccept4, 6, 6, "s1", schema.Args{LocalAddr: ptr("127.0.0.1"),
		LocalPort: ptr(uint16(9000))}, nil)
	tr.add(50, 51, schema.KindWrite, 5, 10, "s1", schema.Args{}, nil)
	tr.add(50, 52, schema.KindRead, 6, 10, "s1", schema.Args{}, nil)
	tr.add(50, 52, schema.KindWrite, 6, 4, "s1", schema.Args{}, nil)
	ev := tr.add(50, 52, schema.KindHTTP, schema.NoFD, 0, "s1", schema.Args{}, nil)
	ev.HTTP = &schema.HTTP{Method: "GET", Scheme: "http", Path: "/self", Status: 200}
	tr.add(50, 51, schema.KindRead, 5, 4, "s1", schema.Args{}, nil)
	ev = tr.add(50, 51, schema.KindHTTP, schema.NoFD, 0, "s1", schema.Args{}, nil)
	ev.HTTP = &schema.HTTP{Method: "GET", Scheme: "http", Host: "127.0.0.1", Path: "/self", Status: 200}
	tr.add(50, 51, schema.KindPipe2, 7, 0, "p1", schema.Args{FD2: ptr(int32(8)), FD2Gen: ptr(uint32(1))}, nil)
	tr.add(50, 51, schema.KindWrite, 8, 3, "p1", schema.Args{}, nil)
	tr.add(50, 51, schema.KindRead, 7, 3, "p1", schema.Args{}, nil)
	return tr
}

// TestPeekReceivedOnce (task ok4): a recvfrom that peeked (args.peek)
// before the read that consumes the same bytes adds nothing to the
// connection's received bytes: they count once, with the read.
func TestPeekReceivedOnce(t *testing.T) {
	tr := &trace{host: "h1"}
	tr.add(50, 51, schema.KindConnect, 5, 0, "s1", schema.Args{RemoteAddr: ptr("127.0.0.1"),
		RemotePort: ptr(uint16(9000))}, nil)
	tr.add(50, 51, schema.KindRecvfrom, 5, 4, "s1", schema.Args{Peek: ptr(true)}, nil)
	tr.add(50, 51, schema.KindRead, 5, 4, "s1", schema.Args{}, nil)
	r := Build(collect(tr, 0), Input{})
	if c := r.Processes[0].Conns; len(c) != 1 || c[0].RecvBytes != 4 {
		t.Fatalf("connection entries %+v, want one that received 4 bytes", c)
	}
}

// TestUntrackedCountsEventsOnce: an event that misses its thread, its
// method, its connection and its file under the cap is one untracked
// event, never four.
func TestUntrackedCountsEventsOnce(t *testing.T) {
	tr := meshTrace()
	for limit := 1; limit < 12; limit++ {
		if r := Build(collect(tr, limit), meshInput()); r.Untracked == 0 || r.Untracked > uint64(len(tr.events)) {
			t.Errorf("limit %d: %d untracked of %d events", limit, r.Untracked, len(tr.events))
		}
	}
}

// TestClosedFDsReturnTheirBudget (task 2i4): an open fd holds one
// tracked entry only while it is open — a process opening, reading and
// closing one file a hundred times under a cap of 20 keeps every byte,
// and a process seen after it is still in the rollup. A second open of
// an fd instance whose close was lost takes no second entry.
func TestClosedFDsReturnTheirBudget(t *testing.T) {
	tr := &trace{host: "h1"}
	for range 100 {
		tr.add(60, 60, schema.KindOpenat, 3, 3, "", schema.Args{Path: ptr("/d/f"), Flags: ptr(uint32(0))}, nil)
		tr.add(60, 60, schema.KindRead, 3, 10, "", schema.Args{}, nil)
		tr.add(60, 60, schema.KindClose, 3, 0, "", schema.Args{}, nil)
	}
	for range 30 { // no close seen: the fd instance is reopened in place
		tr.add(60, 60, schema.KindOpenat, 4, 4, "", schema.Args{Path: ptr("/d/g"), Flags: ptr(uint32(0))}, nil)
	}
	tr.add(61, 61, schema.KindOpenat, 3, 3, "", schema.Args{Path: ptr("/d/f"), Flags: ptr(uint32(0))}, nil)
	c := collect(tr, 20)
	r := Build(c, Input{})
	if r.Untracked != 0 || len(r.Processes) != 2 {
		t.Fatalf("%d untracked, %d processes (tracked %d); want 0 and 2", r.Untracked, len(r.Processes),
			c.tracked)
	}
	if f := r.Processes[0].Files[0]; f.ReadBytes != 1000 {
		t.Errorf("read %d bytes of /d/f, want 1000", f.ReadBytes)
	}
	// Live: two processes, two threads, three files, the open fds 4 of
	// pid 60 and 3 of pid 61.
	if c.tracked != 9 {
		t.Errorf("tracked %d entries, want 9", c.tracked)
	}
}

// TestReportSelfConnection:a process holding both ends of a connection
// or a pipe gets one entry per end — each with its own role, bytes and
// requests, each the other's peer — and the client thread's call is
// out, the server thread's in.
func TestReportSelfConnection(t *testing.T) {
	r := Build(collect(selfTrace(), 0), Input{})
	js, text := render(t, r)
	golden(t, "self.json.golden", js)
	golden(t, "self.txt.golden", text)
	p := r.Processes[0]
	if len(p.Conns) != 4 {
		t.Fatalf("%d connection entries, want 4 (two ends of s1, two of p1): %+v", len(p.Conns), p.Conns)
	}
	cli, srv := p.Conns[0], p.Conns[1]
	if cli.Role != RoleClient || srv.Role != RoleServer || cli.SentBytes != 10 || cli.RecvBytes != 4 ||
		srv.SentBytes != 4 || srv.RecvBytes != 10 || cli.PeerState != PeerTraced || srv.PeerState != PeerTraced {
		t.Errorf("s1 ends %+v / %+v", cli, srv)
	}
	if cli.Remote != "127.0.0.1:9000" || len(cli.Requests) != 1 || len(srv.Requests) != 1 {
		t.Errorf("s1 client %+v, server %+v", cli, srv)
	}
	for _, th := range p.Threads {
		want := map[uint32]string{51: DirOut, 52: DirIn}[th.TID]
		if len(th.Calls) != 1 || th.Calls[0].Dir != want {
			t.Errorf("thread %d calls %+v, want one %q", th.TID, th.Calls, want)
		}
	}
	if w, rd := p.Conns[2], p.Conns[3]; rd.PeerState != PeerTraced || w.PeerState != PeerTraced ||
		rd.RecvBytes != 3 || w.SentBytes != 3 || rd.SentBytes != 0 || w.RecvBytes != 0 || w.FD != 8 || rd.FD != 7 {
		t.Errorf("pipe ends %+v / %+v", rd, w)
	}
}
