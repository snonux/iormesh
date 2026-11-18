package normalize

import (
	"path/filepath"
	"testing"

	"iormesh/internal/schema"
)

// Open flags as the JDK issues them. oCloexec is the kind of extra bit
// a golden does not declare and the check must tolerate.
const (
	oWronlyCreatTrunc = 577 // O_WRONLY|O_CREAT|O_TRUNC
	oCloexec          = 0o2000000
)

// run holds the run-specific values of one synthetic trace: two runs with
// different values must normalize to the same output.
type run struct {
	host        string
	tmp         string
	server      proc // also the only process of file-basic
	client      proc
	port        uint16
	timeBase    uint64
	connID      string
	serverAccFD int32
}

// proc is one traced process and its scenario thread; noise runs on
// tid+1 (a JIT or GC thread doing its own I/O).
type proc struct {
	pid uint32
	inc uint64
	tid uint32
}

// traceBuilder appends raw events the way the correlator would emit them:
// per-host seq from 1, boot-clock times, attribution none.
type traceBuilder struct {
	r      run
	events []schema.Event
}

func runA() run {
	return run{host: "3f2a9c", tmp: "/tmp/iormesh-test-1234",
		server: proc{4711, 91700000000, 4712}, client: proc{4800, 91800000000, 4801},
		port: 41234, timeBase: 91822003417, connID: "a1b2c3d4e5f60718", serverAccFD: 9}
}

// runB differs from runA in every run-specific value.
func runB() run {
	return run{host: "77beef", tmp: "/var/tmp/other-run",
		server: proc{20001, 5000, 20007}, client: proc{20100, 6000, 20111},
		port: 53999, timeBase: 7, connID: "ffff000011112222", serverAccFD: 12}
}

func strp(s string) *string { return &s }
func u16p(v uint16) *uint16 { return &v }
func u32p(v uint32) *uint32 { return &v }

// add appends one event of p's thread tid (p.tid, or noise on p.tid+1).
func (b *traceBuilder) add(p proc, tid uint32, kind string, fd int32, gen uint32,
	set func(*schema.Event)) {
	n := uint64(len(b.events) + 1)
	e := schema.Event{SchemaVersion: schema.Version, Project: "demo", Seq: n,
		BootNS: b.r.timeBase + 1000*n, WallNS: 1794953012000000000 + 1000*n,
		HostID: b.r.host, PID: p.pid, PIDInc: p.inc, TID: tid, Comm: "java",
		Event: kind, FD: fd, FDGen: gen, LatencyNS: 17000 + n,
		Attribution: schema.AttributionNone}
	if fd >= 0 {
		e.Ret = int64(fd)
	}
	if set != nil {
		set(&e)
	}
	b.events = append(b.events, e)
}

func (b *traceBuilder) marker(p proc, scenario, edge string) {
	b.add(p, p.tid, schema.KindMarker, schema.NoFD, 0, func(e *schema.Event) {
		e.Ret, e.Err = -2, "ENOENT"
		e.Args.Marker = strp(scenario + "/" + edge)
	})
}

func (b *traceBuilder) open(p proc, tid uint32, path string, flags uint32, fd int32, gen uint32) {
	b.add(p, tid, schema.KindOpenat, fd, gen, func(e *schema.Event) {
		e.Args.Path, e.Args.Flags = strp(path), u32p(flags)
	})
}

func (b *traceBuilder) io(p proc, tid uint32, kind string, fd int32, gen uint32, n uint64) {
	b.add(p, tid, kind, fd, gen, func(e *schema.Event) { e.Bytes, e.Ret = n, int64(n) })
}

func (b *traceBuilder) close(p proc, fd int32, gen uint32) {
	b.add(p, p.tid, schema.KindClose, fd, gen, func(e *schema.Event) { e.Ret = 0 })
}

// fileWork is file-basic's I/O: write, close, reopen, read, close — both
// opens land on fd 7, as FileOutputStream then FileInputStream do, and a
// noise thread works on fd 4 in between.
func (b *traceBuilder) fileWork(p proc, name string) {
	path := filepath.Join(b.r.tmp, name)
	b.open(p, p.tid+1, "/opt/app/lib/app.jar", 0, 4, 1)
	b.open(p, p.tid, path, oWronlyCreatTrunc|oCloexec, 7, 1)
	b.io(p, p.tid, schema.KindWrite, 7, 1, 4096)
	b.io(p, p.tid+1, schema.KindRead, 4, 1, 512)
	b.close(p, 7, 1)
	b.open(p, p.tid, path, oCloexec, 7, 2)
	b.io(p, p.tid, schema.KindRead, 7, 2, 4096)
	b.close(p, 7, 2)
}

// jvmNoise is I/O outside any bracket: startup before, shutdown after.
func (b *traceBuilder) jvmNoise(p proc) {
	b.open(p, p.tid, "/usr/lib/jvm/lib/modules", 0, 3, 1)
	b.io(p, p.tid+1, schema.KindRead, 3, 1, 8192)
}

// fileBasic builds a raw file-basic trace and graph for run r.
func fileBasic(r run) Trace {
	b := &traceBuilder{r: r}
	p := r.server
	b.jvmNoise(p)
	b.marker(p, "file-basic", "begin")
	b.fileWork(p, "file-basic.dat")
	b.marker(p, "file-basic", "end")
	b.open(p, p.tid, filepath.Join(r.tmp, "file-basic.dat"), 0, 7, 3)
	return Trace{Events: b.events, Graph: r.graph(
		[]proc{p}, []string{"file-basic.dat"}, false)}
}

// graph builds the correlator's per-host graph: host, processes, the
// files they opened, and optionally the fused connection between the two.
func (r run) graph(procs []proc, files []string, conn bool) schema.Graph {
	g := schema.Graph{SchemaVersion: schema.Version}
	host := schema.HostNodeID(r.host)
	g.Nodes = append(g.Nodes, schema.Node{Type: schema.NodeHost, ID: host})
	for i, p := range procs {
		id := schema.ProcessNodeID(r.host, p.pid, p.inc)
		file := schema.FileNodeID(r.host, filepath.Join(r.tmp, files[i]))
		g.Nodes = append(g.Nodes,
			schema.Node{Type: schema.NodeProcess, ID: id, Attrs: map[string]string{"comm": "java"}},
			schema.Node{Type: schema.NodeFile, ID: file})
		g.Edges = append(g.Edges,
			schema.Edge{Type: schema.EdgeRunsOn, From: id, To: host},
			schema.Edge{Type: schema.EdgeOpened, From: id, To: file})
		if conn {
			g.Edges = append(g.Edges, schema.Edge{Type: schema.EdgeConnected,
				From: id, To: schema.SocketNodeID(r.connID), State: schema.StateFused})
		}
	}
	if conn {
		g.Nodes = append(g.Nodes, schema.Node{Type: schema.NodeSocket,
			ID: schema.SocketNodeID(r.connID)})
	}
	g.Sort()
	return g
}

// tcpLoopback builds a raw two-process tcp-loopback trace: the server
// starts first, the client's bracket overlaps the server's, the server
// waits for the client's EOF before it closes, and the
// dual-stack JVM reports loopback as ::ffff:127.0.0.1.
func tcpLoopback(r run) Trace {
	b := &traceBuilder{r: r}
	s, c := r.server, r.client
	b.jvmNoise(s)
	b.marker(s, "tcp-loopback", "begin")
	b.fileWork(s, "tcp-loopback-server.dat")
	b.jvmNoise(c)
	b.marker(c, "tcp-loopback", "begin")
	b.fileWork(c, "tcp-loopback-client.dat")
	b.add(c, c.tid, schema.KindConnect, 6, 2, b.sockArgs(false))
	b.add(s, s.tid, schema.KindAccept, r.serverAccFD, 3, b.sockArgs(true))
	b.io(c, c.tid, schema.KindWrite, 6, 2, 5)
	b.io(s, s.tid, schema.KindRead, r.serverAccFD, 3, 5)
	b.io(s, s.tid, schema.KindWrite, r.serverAccFD, 3, 5)
	b.io(c, c.tid, schema.KindRead, 6, 2, 5)
	b.close(c, 6, 2)
	b.marker(c, "tcp-loopback", "end")
	// The server closes only after reading the client's EOF, so the
	// client always closes first (the workload's deterministic teardown).
	b.io(s, s.tid, schema.KindRead, r.serverAccFD, 3, 0)
	b.close(s, r.serverAccFD, 3)
	b.marker(s, "tcp-loopback", "end")
	return Trace{Events: b.events, Graph: r.graph([]proc{s, c},
		[]string{"tcp-loopback-server.dat", "tcp-loopback-client.dat"}, true)}
}

// sockArgs sets the connection's addresses: the server's local port is
// the client's remote port; the other ends are ephemeral.
func (b *traceBuilder) sockArgs(server bool) func(*schema.Event) {
	return func(e *schema.Event) {
		e.ConnID = b.r.connID
		e.Args.LocalAddr = strp("::ffff:127.0.0.1")
		e.Args.RemoteAddr = strp("::ffff:127.0.0.1")
		if server {
			e.Args.LocalPort, e.Args.RemotePort = u16p(b.r.port), u16p(b.r.port+7)
		} else {
			e.Args.LocalPort, e.Args.RemotePort = u16p(b.r.port+7), u16p(b.r.port)
		}
	}
}

// noop builds a raw noop trace: only a noise thread inside the bracket.
func noop(r run) Trace {
	b := &traceBuilder{r: r}
	p := r.server
	b.jvmNoise(p)
	b.marker(p, "noop", "begin")
	b.io(p, p.tid+1, schema.KindRead, 3, 1, 64)
	b.marker(p, "noop", "end")
	return Trace{Events: b.events, Graph: r.graph([]proc{p}, []string{"unused"}, false)}
}

// golden loads a shipped M0 expectation.
func golden(t *testing.T, scenario string) Expectation {
	t.Helper()
	x, err := LoadExpectation(filepath.Join("..", "..", "workload", "scenarios",
		scenario, "expect.json"))
	if err != nil {
		t.Fatal(err)
	}
	return x
}

// normalized cuts and normalizes a raw trace of run r for scenario.
func normalized(t *testing.T, raw Trace, r run, scenario string) Trace {
	t.Helper()
	got, err := Normalize(raw, Options{Bracket: scenario, TmpPrefix: r.tmp})
	if err != nil {
		t.Fatal(err)
	}
	return got
}
