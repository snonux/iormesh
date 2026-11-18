package correlate

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// update rewrites the export goldens: go test ./internal/correlate -update
var update = flag.Bool("update", false, "rewrite testdata goldens")

// TestExportGolden runs a fixed record stream (session header, file work,
// a drop, a fused loopback connection, end) through the correlator into the real NDJSON sink
// and graph writer, and compares both outputs byte for byte with the
// committed goldens. It pins the export format end to end: field order,
// conn ids, fd generations, graph order.
func TestExportGolden(t *testing.T) {
	var events bytes.Buffer
	c := New(Config{Host: Host{ID: "h1", Project: "p"}, Sink: schema.NewNDJSONSink(&events),
		WallOffsetNS: 1_700_000_000_000_000_000,
		ProcInfo:     func(pid uint32) (string, uint64) { return "java", 1000 + uint64(pid) },
		Clock:        func() uint64 { return 5 }})
	now := uint64(0)
	f := feeder{t, c, 100, 101, &now}
	if err := c.Session(testSession); err != nil {
		t.Fatal(err)
	}
	f.open("/iormesh-marker/demo/begin", 0, -2)
	fileWork(f, "demo.dat", 5)
	if err := c.RecordLosses(4); err != nil {
		t.Fatal(err)
	}
	loopback(t, c, 1000)
	f.open("/iormesh-marker/demo/end", 0, -2)
	if err := c.End(schema.EndDuration); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	var graph bytes.Buffer
	if err := schema.WriteGraph(&graph, c.Graph()); err != nil {
		t.Fatal(err)
	}
	compareGolden(t, "export.ndjson", events.Bytes())
	compareGolden(t, "export.graph.json", graph.Bytes())
	if _, err := schema.ReadNDJSON(&events); err != nil {
		t.Fatalf("exported stream does not read back strictly: %v", err)
	}
}

func compareGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs from the golden; got:\n%s", name, got)
	}
}

// TestSinkErrorStopsCorrelator: a failing NDJSON sink's error reaches the
// correlator's caller.
func TestSinkErrorStopsCorrelator(t *testing.T) {
	c := New(Config{Host: Host{ID: "h1", Project: "p"}, Sink: schema.NewNDJSONSink(errWriter{}),
		ProcInfo: func(uint32) (string, uint64) { return "", 0 }})
	now := uint64(0)
	f := feeder{t, c, 1, 1, &now}
	f.handle(&types.FdEvent{EventType: types.ENTER_FD_EVENT, TraceId: types.SYS_ENTER_READ,
		Time: f.tick(), Pid: 1, Tid: 1, Fd: 3})
	f.exit(types.SYS_EXIT_READ, 1) // buffered: no error yet
	if err := c.Close(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("Close: %v, want the sink's write error", err)
	}
}

// TestSinkErrorMidStream: once the sink's buffer overflows into a failing
// writer, Handle itself returns the error — the caller stops at once
// instead of tracing into the void until Close.
func TestSinkErrorMidStream(t *testing.T) {
	c := New(Config{Host: Host{ID: "h1", Project: "p"}, Sink: schema.NewNDJSONSink(errWriter{}),
		ProcInfo: func(uint32) (string, uint64) { return "", 0 }})
	now := uint64(0)
	f := feeder{t, c, 1, 1, &now}
	for range 1000 {
		_ = c.Handle(&types.FdEvent{EventType: types.ENTER_FD_EVENT, TraceId: types.SYS_ENTER_READ,
			Time: f.tick(), Pid: 1, Tid: 1, Fd: 3})
		err := c.Handle(&types.RetEvent{EventType: types.EXIT_RET_EVENT, TraceId: types.SYS_EXIT_READ,
			Time: f.tick(), Pid: 1, Tid: 1, Ret: 1})
		if errors.Is(err, os.ErrClosed) {
			return
		}
	}
	t.Fatal("1000 events into a failing sink and Handle never reported it")
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, os.ErrClosed }
