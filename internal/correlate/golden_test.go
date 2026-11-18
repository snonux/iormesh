package correlate

import (
	"testing"

	"iormesh/internal/normalize"
	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// The golden tests feed the correlator record streams shaped like the real
// M1 scenarios (JVM noise outside the bracket included) and run the result
// through the normalizer's golden check against the shipped expect.json.
// They pin the contract between correlator and normalizer — conn ids on
// socket events, file node ids from the event path, marker events — that
// the M1 integration test relies on.

const tmp = "/tmp/run1"

// checkGolden closes c, normalizes what it emitted to scenario's bracket
// and checks it against the shipped expectation; it returns the
// normalized trace for further assertions.
func checkGolden(t *testing.T, c *Correlator, sink *memSink, scenario string) normalize.Trace {
	t.Helper()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := normalize.Normalize(normalize.Trace{Events: sink.events, Graph: c.Graph()},
		normalize.Options{Bracket: scenario, TmpPrefix: tmp})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	want, err := normalize.LoadExpectation("../../workload/scenarios/" + scenario + "/expect.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range normalize.Check(got, want) {
		t.Error(v)
	}
	return got
}

// fileWork is file-basic's I/O: write a file, reopen it, read it back.
func fileWork(f feeder, name string, fd int32) {
	path := tmp + "/" + name
	f.open(path, 577, int64(fd))
	f.write(fd, 4096)
	f.close(fd)
	f.open(path, 0, int64(fd))
	f.read(fd, 4096)
	f.close(fd)
}

// TestGoldenFileBasic also carries the host-level events a real run
// writes — session header, a drop inside the bracket, end — to pin that
// they do not disturb the bracket cut or the subset check.
func TestGoldenFileBasic(t *testing.T) {
	c, sink := newTest(t)
	if err := c.Session(testSession); err != nil {
		t.Fatal(err)
	}
	now := uint64(0)
	f := feeder{t, c, 10, 12, &now}
	f.open("/usr/lib/jvm/lib/modules", 0, 4) // JVM startup, outside the bracket
	f.open("/iormesh-marker/file-basic/begin", 0, -2)
	fileWork(f, "file-basic.dat", 5)
	if err := c.RecordLosses(2); err != nil {
		t.Fatal(err)
	}
	f.open("/iormesh-marker/file-basic/end", 0, -2)
	if err := c.End(schema.EndTargetsExited); err != nil {
		t.Fatal(err)
	}
	got := checkGolden(t, c, sink, "file-basic")
	if n := len(byKind(got.Events, schema.KindDrop)); n != 1 {
		t.Fatalf("%d drop events in the bracket, want the one inside it", n)
	}
	if n := len(byKind(got.Events, schema.KindSession)) + len(byKind(got.Events, schema.KindEnd)); n != 0 {
		t.Fatalf("session/end inside the bracket: %d, want 0 (they lie outside it)", n)
	}
}

func TestGoldenTCPLoopback(t *testing.T) {
	c, sink := newTest(t)
	snow, cnow := uint64(0), uint64(50)
	server := feeder{t, c, 200, 201, &snow}
	client := feeder{t, c, 100, 101, &cnow}
	server.open("/iormesh-marker/tcp-loopback/begin", 0, -2)
	fileWork(server, "tcp-loopback-server.dat", 5)
	client.open("/iormesh-marker/tcp-loopback/begin", 0, -2)
	fileWork(client, "tcp-loopback-client.dat", 5)

	client.handle(&types.FdEvent{EventType: types.ENTER_FD_EVENT, TraceId: types.SYS_ENTER_CONNECT,
		Time: client.tick(), Pid: 100, Tid: 101, Fd: 6})
	// The JVM's sockets are dual-stack: AF_INET6 with ::ffff: addresses.
	client.handle(tcp6(client.tick(), 0xA, 100, 6, tcpClose, synSent, "127.0.0.1:0", srv))
	client.handle(tcp6(client.tick(), 0xA, 100, 6, synSent, tcpEstablished, cli, srv))
	client.exit(types.SYS_EXIT_CONNECT, 0)
	snow = cnow
	server.handle(&types.AcceptEvent{EventType: types.ENTER_ACCEPT_EVENT, TraceId: types.SYS_ENTER_ACCEPT,
		Time: server.tick(), Pid: 200, Tid: 201, Fd: 6})
	server.handle(tcp6(server.tick(), 0xB, 200, 7, types.TCP_STATE_SNAPSHOT, tcpEstablished, srv, cli))
	server.handle(&types.AcceptEvent{EventType: types.EXIT_ACCEPT_EVENT, TraceId: types.SYS_EXIT_ACCEPT,
		Time: server.tick(), Pid: 200, Tid: 201, Fd: -1, Ret: 7})

	client.write(6, 5)
	server.read(7, 5)
	server.write(7, 5)
	client.read(6, 5)
	client.close(6)
	server.read(7, 0) // the server reads to the client's EOF before closing
	server.close(7)
	client.open("/iormesh-marker/tcp-loopback/end", 0, -2)
	server.open("/iormesh-marker/tcp-loopback/end", 0, -2)
	checkGolden(t, c, sink, "tcp-loopback")
}
