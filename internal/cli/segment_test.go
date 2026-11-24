package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	parquetgo "github.com/parquet-go/parquet-go"

	"iormesh/internal/correlate"
	"iormesh/internal/schema"
	"iormesh/internal/segment"
)

// TestDiskLimitEndsTraceCleanly is the watchdog path (docs/milestones.md
// M4 "Disk watchdog") through the
// run loop: once the (fake) partition fills, the loop ends the trace as
// disk_limit without an error (exit 0), and finish publishes a last
// segment that holds the session header through the end event.
func TestDiskLimitEndsTraceCleanly(t *testing.T) {
	fakeProcs(t, 10)
	var full atomic.Bool
	statfs := func(string) (segment.Usage, error) {
		if full.Load() {
			return segment.Usage{Used: 99, Avail: 1}, nil
		}
		return segment.Usage{Used: 10, Avail: 90}, nil
	}
	dir := filepath.Join(t.TempDir(), "p")
	w, err := segment.New(segment.Config{Dir: dir, FQDN: "h.example", Session: "s1", Project: "p",
		CheckEvery: time.Millisecond, Watchdog: &segment.Watchdog{MaxUsedPercent: 90, Statfs: statfs}})
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	seg := &segOutput{w: w, stderr: &stderr}
	c := correlate.New(correlate.Config{Host: correlate.Host{ID: "h", Project: "p"},
		Sink: seg.sink(nil), ProcInfo: func(uint32) (string, uint64) { return "x", 1 }})
	if err := c.Session(schema.Session{BootID: "b", FQDN: "h.example", Kernel: "6", Arch: "x86_64"}); err != nil {
		t.Fatal(err)
	}
	tr := newFakeTracer()
	s, cfg := pidSession(t, tr, c, 10)
	s.seg = seg
	full.Store(true)
	reason, err := traceLoop(context.Background(), s, cfg)
	if err != nil || reason != schema.EndDiskLimit {
		t.Fatalf("traceLoop = %q, %v; want disk_limit, no error", reason, err)
	}
	if err := finish(cfg, tr, c, outputs{seg: seg}, reason, &stderr); err != nil {
		t.Fatal(err)
	}
	names, err := filepath.Glob(filepath.Join(dir, segment.ReadyDir, "*"+segment.Ext))
	if err != nil || len(names) != 1 {
		t.Fatalf("ready/ = %v, %v; want one segment", names, err)
	}
	events, err := parquetgo.ReadFile[schema.Event](names[0])
	if err != nil {
		t.Fatal(err)
	}
	// The target's attach (task ze4) is the session's only process event.
	if got := kinds(events); got != "session,attach,end" || *events[2].Args.EndReason != schema.EndDiskLimit {
		t.Fatalf("segment holds %s, want session,attach,end with disk_limit", got)
	}
	if !strings.Contains(stderr.String(), "disk limit reached") {
		t.Fatalf("stderr %q: want the trip reported", stderr.String())
	}
}

// TestCheckTraceDisk is the startup check against the real partition: a
// limit no disk can be under fails before anything loads, and creates the
// trace dir it needs for statfs; no -trace-dir checks nothing.
func TestCheckTraceDisk(t *testing.T) {
	traceDir := filepath.Join(t.TempDir(), "traces")
	for name, tc := range map[string]struct {
		cfg  runConfig
		trip bool
	}{
		"no trace dir":  {runConfig{diskLimit: 1e-9}, false},
		"limits off":    {runConfig{traceDir: traceDir}, false},
		"percent trips": {runConfig{traceDir: traceDir, diskLimit: 1e-9}, true},
		"floor trips":   {runConfig{traceDir: traceDir, diskMinFree: 1 << 62}, true},
	} {
		t.Run(name, func(t *testing.T) {
			err := checkTraceDisk(tc.cfg)
			if errors.Is(err, segment.ErrDiskLimit) != tc.trip || (!tc.trip && err != nil) {
				t.Fatalf("checkTraceDisk = %v, want trip %v", err, tc.trip)
			}
		})
	}
	if _, err := os.Stat(traceDir); err != nil {
		t.Fatalf("trace dir not created: %v", err)
	}
}

// TestTeeSink: with -out and -trace-dir both, every event reaches both,
// and closing the events stream leaves the segment writer open for the
// tables' last rows.
func TestTeeSink(t *testing.T) {
	dir := t.TempDir()
	w, err := segment.New(segment.Config{Dir: filepath.Join(dir, "p"), FQDN: "h", Session: "s1",
		Project: "p"})
	if err != nil {
		t.Fatal(err)
	}
	seg := &segOutput{w: w}
	f, err := os.Create(filepath.Join(dir, EventsFile))
	if err != nil {
		t.Fatal(err)
	}
	sink := seg.sink(schema.NewNDJSONSink(f))
	ev := schema.Event{SchemaVersion: schema.Version, Project: "p", Seq: 1, HostID: "h",
		Event: schema.KindEnd, FD: schema.NoFD, Attribution: schema.AttributionNone,
		Args: schema.Args{EndReason: new(string)}}
	*ev.Args.EndReason = schema.EndStop
	if err := sink.Event(&ev); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if got := readEvents(t, dir); len(got) != 1 {
		t.Fatalf("NDJSON holds %d events", len(got))
	}
	ev.Seq = 2
	if err := w.Event(&ev); err != nil {
		t.Fatalf("segment writer closed with the event stream: %v", err)
	}
	if err := seg.close(); err != nil {
		t.Fatal(err)
	}
}
