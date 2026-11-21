package schema

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleEvent(seq uint64) Event {
	path := "/tmp/a.dat"
	return Event{SchemaVersion: Version, Project: "p", Seq: seq, HostID: "h", PID: 1, TID: 1,
		Event: KindOpenat, FD: 3, FDGen: 1, Ret: 3, Attribution: AttributionNone,
		Args: Args{Path: &path}}
}

func TestNDJSONRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	s := NewNDJSONSink(&buf)
	for seq := uint64(1); seq <= 3; seq++ {
		if err := s.Event(new(sampleEvent(seq))); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(buf.String(), "\n"); n != 3 {
		t.Fatalf("%d lines, want 3:\n%s", n, buf.String())
	}
	got, err := ReadNDJSON(bytes.NewReader(buf.Bytes()))
	if err != nil || len(got) != 3 || got[2].Seq != 3 || *got[0].Args.Path != "/tmp/a.dat" {
		t.Fatalf("read back %d events, err %v", len(got), err)
	}
}

func TestNDJSONRejectsInvalidEvent(t *testing.T) {
	var buf bytes.Buffer
	s := NewNDJSONSink(&buf)
	bad := sampleEvent(1)
	bad.Seq = 0
	err := s.Event(&bad)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid event: err %v, want ErrInvalid", err)
	}
	// The rejected record is not written, so the error names it (task
	// mf4): its kind, process and fd are what debugging the producer needs.
	if want := bad.Brief(); !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not name the event (%s)", err, want)
	}
	// A rejected event is the producer's bug, not a broken sink: the
	// next valid event is still written.
	if err := s.Event(new(sampleEvent(1))); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil || strings.Count(buf.String(), "\n") != 1 {
		t.Fatalf("close %v, output %q; want exactly the valid event", err, buf.String())
	}
}

// failWriter fails every write after the first n bytes and records Close.
type failWriter struct {
	n      int
	closed bool
}

var errDiskFull = errors.New("disk full")

func (w *failWriter) Write(p []byte) (int, error) {
	if len(p) > w.n {
		return 0, errDiskFull
	}
	w.n -= len(p)
	return len(p), nil
}

func (w *failWriter) Close() error { w.closed = true; return nil }

// TestNDJSONWriteErrorsPropagate: the writer's error reaches the caller,
// at the latest on Close (writes are buffered), and sticks.
func TestNDJSONWriteErrorsPropagate(t *testing.T) {
	w := &failWriter{n: 10}
	s := NewNDJSONSink(w)
	var err error
	for seq := uint64(1); seq <= 2000 && err == nil; seq++ {
		err = s.Event(new(sampleEvent(seq)))
	}
	if !errors.Is(err, errDiskFull) {
		t.Fatalf("Event error %v, want disk full once the buffer flushes", err)
	}
	if err := s.Event(new(sampleEvent(1))); !errors.Is(err, errDiskFull) {
		t.Fatalf("after a failure: %v, want the sticky error", err)
	}
	if err := s.Close(); !errors.Is(err, errDiskFull) || !w.closed {
		t.Fatalf("Close %v closed %v; want the error and the writer closed", err, w.closed)
	}
}

func TestNDJSONFlushErrorOnClose(t *testing.T) {
	w := &failWriter{n: 0}
	s := NewNDJSONSink(w)
	if err := s.Event(new(sampleEvent(1))); err != nil {
		t.Fatalf("buffered write failed early: %v", err)
	}
	if err := s.Close(); !errors.Is(err, errDiskFull) {
		t.Fatalf("Close %v, want the flush error", err)
	}
}

func TestReadNDJSONRejects(t *testing.T) {
	var buf bytes.Buffer
	s := NewNDJSONSink(&buf)
	_ = s.Event(new(sampleEvent(1)))
	_ = s.Close()
	good := buf.String()
	// Each must fail for its own reason, not an unrelated one.
	for name, c := range map[string]struct {
		in   string
		want error
	}{
		"blank line":    {good + "\n", ErrInvalid},
		"unknown key":   {strings.Replace(good, `"seq"`, `"Seq"`, 1), ErrInvalid},
		"not json":      {good + "{\n", ErrInvalid},
		"line too long": {good + strings.Repeat("x", maxLine+1) + "\n", bufio.ErrTooLong},
	} {
		if _, err := ReadNDJSON(strings.NewReader(c.in)); !errors.Is(err, c.want) {
			t.Errorf("%s: err %v, want %v", name, err, c.want)
		}
	}
	if _, err := ReadNDJSON(strings.NewReader(good + "\n")); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("error %v does not name line 2", err)
	}
}

func sampleGraph() Graph {
	host, proc := HostNodeID("h"), ProcessNodeID("h", 1, 7)
	file := FileNodeID("h", "/tmp/a.dat")
	return Graph{SchemaVersion: Version,
		Nodes: []Node{{Type: NodeFile, ID: file}, {Type: NodeProcess, ID: proc}, {Type: NodeHost, ID: host}},
		Edges: []Edge{{Type: EdgeRunsOn, From: proc, To: host}, {Type: EdgeOpened, From: proc, To: file}}}
}

func TestWriteGraphSortsAndValidates(t *testing.T) {
	var a, b bytes.Buffer
	g := sampleGraph()
	if err := WriteGraph(&a, g); err != nil {
		t.Fatal(err)
	}
	g.Nodes[0], g.Nodes[2] = g.Nodes[2], g.Nodes[0] // input order must not matter
	if err := WriteGraph(&b, g); err != nil || a.String() != b.String() {
		t.Fatalf("two writes differ (err %v):\n%s\n%s", err, a.String(), b.String())
	}
	if _, err := DecodeGraph(a.Bytes()); err != nil {
		t.Fatalf("written graph does not read back: %v", err)
	}
	bad := sampleGraph()
	bad.Edges = append(bad.Edges, Edge{Type: EdgeOpened, From: ProcessNodeID("h", 9, 9), To: HostNodeID("h")})
	if err := WriteGraph(&a, bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("dangling edge: %v, want ErrInvalid", err)
	}
}

func TestWriteGraphFileIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "graph.json")
	if err := WriteGraphFile(path, sampleGraph()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	bad := sampleGraph()
	bad.SchemaVersion = 2
	if err := WriteGraphFile(path, bad); err == nil {
		t.Fatal("invalid graph written")
	}
	after, _ := os.ReadFile(path)
	entries, _ := os.ReadDir(dir)
	if !bytes.Equal(before, after) || len(entries) != 1 {
		t.Fatalf("failed write changed the file or left %d entries", len(entries))
	}
	if err := WriteGraphFile(filepath.Join(dir, "missing", "g.json"), sampleGraph()); err == nil {
		t.Fatal("write into a missing directory succeeded")
	}
}

func TestNDJSONClosedSink(t *testing.T) {
	w := &failWriter{n: 1 << 20}
	s := NewNDJSONSink(w)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Event(new(sampleEvent(1))); !errors.Is(err, errSinkClosed) {
		t.Fatalf("Event after Close: %v, want errSinkClosed", err)
	}
	w.closed = false
	if err := s.Close(); err != nil || w.closed {
		t.Fatalf("second Close: err %v, closed the writer again %v", err, w.closed)
	}
}

func TestWriteGraphFileModeAndRenameCleanup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "graph.json")
	if err := WriteGraphFile(path, sampleGraph()); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v err %v, want 0644", fi.Mode().Perm(), err)
	}
	// A directory in the way makes the rename fail after the temp file
	// was fully written: it must not be left behind.
	blocked := filepath.Join(dir, "sub")
	if err := os.MkdirAll(filepath.Join(blocked, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteGraphFile(blocked, sampleGraph()); err == nil {
		t.Fatal("rename onto a non-empty directory succeeded")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("%d entries after a failed rename, want graph.json and sub only", len(entries))
	}
}
