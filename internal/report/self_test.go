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
