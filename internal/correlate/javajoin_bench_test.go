package correlate

import (
	"testing"

	"iormesh/internal/schema"
)

// discard is a sink that keeps nothing.
type discard struct{}

func (discard) Event(schema.Event) error { return nil }
func (discard) Close() error             { return nil }

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
		if err := j.Event(ev); err != nil {
			b.Fatal(err)
		}
	}
}
