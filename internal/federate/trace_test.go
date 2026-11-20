package federate

import (
	"os"
	"path/filepath"
	"testing"

	"iormesh/internal/schema"
)

// trace builds one synthetic host session — events and the graph the
// correlator would export for them — and writes it as an NDJSON trace
// directory, so the merge tests read exactly what `iormesh run` writes.
type trace struct {
	host, project string
	seq           uint64
	events        []schema.Event
	nodes         map[string]schema.Node
	edges         []schema.Edge
}

func newTrace(host, project string) *trace {
	return &trace{host: host, project: project, nodes: map[string]schema.Node{}}
}

func ptr[T any](v T) *T { return &v }

// event appends one event of pid; fd -1 for none.
func (t *trace) event(pid uint32, kind string, fd int32, ret int64, wall uint64,
	conn string, args schema.Args) {
	t.seq++
	ev := schema.Event{SchemaVersion: schema.Version, Project: t.project, Seq: t.seq,
		BootNS: wall - wall0 + 1, WallNS: wall, HostID: t.host, PID: pid, PIDInc: 1,
		TID: pid, Comm: "app", Event: kind, FD: fd, Ret: ret, Args: args,
		ConnID: conn, Attribution: schema.AttributionNone}
	if ret < 0 {
		ev.Err = "EINPROGRESS"
	}
	t.events = append(t.events, ev)
}

// proc adds pid's process node and runs_on edge, returning its id.
func (t *trace) proc(pid uint32) string {
	id := schema.ProcessNodeID(t.host, pid, 1)
	host := schema.HostNodeID(t.host)
	t.nodes[host] = schema.Node{Type: schema.NodeHost, ID: host}
	if _, ok := t.nodes[id]; !ok {
		t.nodes[id] = schema.Node{Type: schema.NodeProcess, ID: id,
			Attrs: map[string]string{"comm": "app"}}
		t.edges = append(t.edges, schema.Edge{Type: schema.EdgeRunsOn, From: id, To: host})
	}
	return id
}

// socket adds the conn's socket node and pid's unresolved connected
// edge: a host alone never sees the far end of a cross-host connection.
func (t *trace) socket(pid uint32, conn string) {
	sock := schema.SocketNodeID(conn)
	t.nodes[sock] = schema.Node{Type: schema.NodeSocket, ID: sock}
	t.edges = append(t.edges, schema.Edge{Type: schema.EdgeConnected, From: t.proc(pid),
		To: sock, State: schema.StateUnresolved})
}

// tuple sets args' 4-tuple from "addr:port" strings.
func tuple(local, peer string) schema.Args {
	l, p := mustAddrPort(local), mustAddrPort(peer)
	return schema.Args{LocalAddr: ptr(l.Addr().String()), LocalPort: ptr(l.Port()),
		RemoteAddr: ptr(p.Addr().String()), RemotePort: ptr(p.Port())}
}

// withSeq adds the four tcp_sock fields to a tcp_state record's args.
func withSeq(a schema.Args, state string, f seqFields) schema.Args {
	a.SockState = ptr(state)
	a.SndUna, a.RcvNxt = ptr(f.SndUna), ptr(f.RcvNxt)
	a.BytesAcked, a.BytesReceived = ptr(f.BytesAcked), ptr(f.BytesReceived)
	return a
}

// client records a connecting end as the tracer does: SYN_SENT inside
// connect, ESTABLISHED, then CLOSE after a 20-byte request, a 300-byte
// response and both FINs — every keyed record rebuilding k.
func (t *trace) client(pid uint32, conn, local, peer string, k ISNPair, wall uint64) {
	t.socket(pid, conn)
	a := tuple(local, peer)
	syn := a
	syn.SockState = ptr("SYN_SENT")
	t.event(pid, schema.KindTCPState, 7, 0, wall, conn, syn)
	t.event(pid, schema.KindConnect, 7, -115, wall, conn, a)
	t.event(pid, schema.KindTCPState, 7, 0, wall+1000, conn, withSeq(a, "ESTABLISHED",
		seqFields{SndUna: k.Client + 1, RcvNxt: k.Server + 1, BytesAcked: 1}))
	t.event(pid, schema.KindTCPState, 7, 0, wall+9000, conn, withSeq(a, "CLOSE",
		seqFields{SndUna: k.Client + 22, RcvNxt: k.Server + 302, BytesAcked: 22,
			BytesReceived: 301}))
}

// server records an accepting end: the accept snapshot, taken with the
// request and the client's FIN already queued (S2's 50 ms accept delay),
// then the accept4 that returned its fd.
func (t *trace) server(pid uint32, conn, local, peer string, k ISNPair, wall uint64) {
	t.socket(pid, conn)
	a := tuple(local, peer)
	t.event(pid, schema.KindTCPState, 9, 0, wall+2000, conn, withSeq(a, "CLOSE_WAIT",
		seqFields{SndUna: k.Server + 1, RcvNxt: k.Client + 22, BytesReceived: 21}))
	t.event(pid, schema.KindAccept4, 9, 9, wall+2000, conn, a)
}

// write stores the trace as dir/events.ndjson and dir/graph.json through
// the schema's own writers, which validate every record.
func (t *trace) write(tb testing.TB, dir string) NDJSONDir {
	tb.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		tb.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, eventsFile))
	if err != nil {
		tb.Fatal(err)
	}
	sink := schema.NewNDJSONSink(f)
	for i := range t.events {
		if err := sink.Event(&t.events[i]); err != nil {
			tb.Fatal(err)
		}
	}
	if err := sink.Close(); err != nil {
		tb.Fatal(err)
	}
	g := schema.Graph{SchemaVersion: schema.Version, Edges: t.edges}
	for _, n := range t.nodes {
		g.Nodes = append(g.Nodes, n)
	}
	if err := schema.WriteGraphFile(filepath.Join(dir, graphFile), g); err != nil {
		tb.Fatal(err)
	}
	return NDJSONDir{Dir: dir}
}
