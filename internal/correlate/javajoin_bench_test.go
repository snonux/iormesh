package correlate

import (
	"testing"

	"iormesh/internal/agentspool"
	"iormesh/internal/schema"
)

// discard is a sink that keeps nothing.
type discard struct{}

func (discard) Event(*schema.Event) error { return nil }
func (discard) Close() error              { return nil }

// BenchmarkJavaJoinPassThrough is the joiner's per-event cost for an
// event no ring claims (another process): queued, decided at the next
// scan, numbered and handed on. Task 3b4 measured the http events'
// bookkeeping on this path (docs/spikes/s3-overhead.md).
func BenchmarkJavaJoinPassThrough(b *testing.B) {
	now := uint64(1_000_000_000)
	j := NewJavaJoin(JavaJoinConfig{Next: discard{}, Tables: nil,
		Scan:       func() ([]AgentRing, error) { return nil, nil },
		ProcInfo:   func(uint32) (string, uint64) { return "", 0 },
		Clock:      func() uint64 { return now },
		MonoOffset: func() (int64, error) { return 0, nil }})
	ev := schema.Event{SchemaVersion: schema.Version, Project: "p", HostID: "h", PID: 9,
		TID: 9, Comm: "c", Event: "write", FD: 3, LatencyNS: 100,
		Attribution: schema.AttributionNone}
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		ev.BootNS = now
		now += 1000
		if err := j.Event(&ev); err != nil {
			b.Fatal(err)
		}
	}
}

// nopTables drops the method and thread tables.
type nopTables struct{}

func (nopTables) Method(schema.Method) error { return nil }
func (nopTables) Thread(schema.Thread) error { return nil }

// BenchmarkJavaJoinJoined is the joiner's per-event cost on the S3 burst
// under the agent (task ud4): every event is a marked one-byte write
// inside its own bracket, so each is held until its JVM's watermark
// passes, paired with its mark, bracketed and given attribution java.
// The ring delivers a bracket and a watermark per event; a scan runs
// every 50 ms of the fake clock, as in the tracer. The real tracer reads
// a real clock per event (BenchmarkBootClock).
func BenchmarkJavaJoinJoined(b *testing.B) {
	now := uint64(1_000_000_000)
	ring := burstRing(now)
	spool := []AgentRing{ring}
	j := NewJavaJoin(JavaJoinConfig{Next: discard{}, Tables: nopTables{},
		Scan:        func() ([]AgentRing, error) { r := spool; spool = nil; return r, nil },
		ProcInfo:    func(uint32) (string, uint64) { return "java", jvmStart },
		Clock:       func() uint64 { return now },
		MonoOffset:  func() (int64, error) { return testOffset, nil },
		Retain:      100_000_000, // keeps the bracket lists short, as a long run's would be
		AttachMarks: func(uint32, []MarkedNative) ([]string, error) { return nil, nil }})
	cookie := j.cookie(symWriteBytes)
	ev := schema.Event{SchemaVersion: schema.Version, Project: "p", HostID: "h", PID: jvmPID,
		PIDInc: jvmStart, TID: mainTID, Comm: "java", Event: "write", FD: 3, Ret: 1,
		LatencyNS: 100, Bytes: 1, Attribution: schema.AttributionNone}
	b.ReportAllocs()
	for b.Loop() {
		mono := now - testOffset
		j.Mark(NativeMark{PID: jvmPID, TID: mainTID, EnterBoot: now, Cookie: cookie,
			EntryMono: mono - markLead})
		ring.queue = append(ring.queue,
			agentspool.Bracket{Hook: 1, KTID: mainTID, EnterMono: mono - 2*markLead,
				ExitMono: mono + 200, StackID: 10, ThreadID: 1},
			agentspool.Watermark{Mono: mono + 300})
		ev.BootNS = now
		if err := j.Event(&ev); err != nil {
			b.Fatal(err)
		}
		now += 1000
	}
	if err := j.Close(); err != nil {
		b.Fatal(err)
	}
	if st := j.Stats(); st.Attributed == 0 {
		b.Fatalf("nothing attributed: %+v", st)
	}
}

// burstRing is a JVM's ring holding the hook table, the main thread and
// the stack of the burst's writes.
func burstRing(now uint64) *fakeRing {
	return &fakeRing{hdr: agentspool.Header{Version: agentspool.Version, PID: jvmPID,
		CreatedMono: now - testOffset - 1}, queue: []agentspool.Record{
		agentspool.Hook{ID: 1, Kind: "write", Class: "java/io/FileOutputStream", Method: "write",
			Native: "writeBytes", Syscalls: []string{"write"}},
		agentspool.Thread{ID: 1, KTID: mainTID, Name: "main"},
		agentspool.Stack{ID: 10, Frames: []string{
			"java.io.FileOutputStream#writeBytes(FileOutputStream.java:-2)",
			"app.Main#save(Main.java:12)"}},
	}}
}

// BenchmarkBootClock compares JavaJoin.Event's clock read before task ud4
// (BootNow, a clock_gettime syscall) with the bootClock that replaced it.
func BenchmarkBootClock(b *testing.B) {
	b.Run("BootNow", func(b *testing.B) {
		for b.Loop() {
			_ = BootNow()
		}
	})
	b.Run("bootClock", func(b *testing.B) {
		c := newBootClock(BootNow)
		for b.Loop() {
			_ = c.now()
		}
	})
}
