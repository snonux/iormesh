package correlate

import (
	"testing"

	"iormesh/internal/normalize"
	"iormesh/internal/types"
)

// The golden tests feed the correlator record streams shaped like the real
// M1 scenarios (JVM noise outside the bracket included) and run the result
// through the normalizer's golden check against the shipped expect.json.
// They pin the contract between correlator and normalizer — conn ids on
// socket events, file node ids from the event path, marker events — that
// the M1 integration test relies on.

const tmp = "/tmp/run1"

func checkGolden(t *testing.T, c *Correlator, sink *memSink, scenario string) {
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

func TestGoldenFileBasic(t *testing.T) {
	c, sink := newTest(t)
	now := uint64(0)
	f := feeder{t, c, 10, 12, &now}
	f.open("/usr/lib/jvm/lib/modules", 0, 4) // JVM startup, outside the bracket
	f.open("/iormesh-marker/file-basic/begin", 0, -2)
	fileWork(f, "file-basic.dat", 5)
	f.open("/iormesh-marker/file-basic/end", 0, -2)
	checkGolden(t, c, sink, "file-basic")
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
	client.handle(tcp(client.tick(), 0xA, 100, 6, tcpClose, synSent, "127.0.0.1:0", srv))
	client.handle(tcp(client.tick(), 0xA, 100, 6, synSent, tcpEstablished, cli, srv))
	client.exit(types.SYS_EXIT_CONNECT, 0)
	snow = cnow
	server.handle(&types.AcceptEvent{EventType: types.ENTER_ACCEPT_EVENT, TraceId: types.SYS_ENTER_ACCEPT,
		Time: server.tick(), Pid: 200, Tid: 201, Fd: 6})
	server.handle(tcp(server.tick(), 0xB, 200, 7, types.TCP_STATE_SNAPSHOT, tcpEstablished, srv, cli))
	server.handle(&types.AcceptEvent{EventType: types.EXIT_ACCEPT_EVENT, TraceId: types.SYS_EXIT_ACCEPT,
		Time: server.tick(), Pid: 200, Tid: 201, Fd: -1, Ret: 7})

	client.write(6, 5)
	server.read(7, 5)
	server.write(7, 5)
	client.read(6, 5)
	client.close(6)
	server.close(7)
	client.open("/iormesh-marker/tcp-loopback/end", 0, -2)
	server.open("/iormesh-marker/tcp-loopback/end", 0, -2)
	checkGolden(t, c, sink, "tcp-loopback")
}
