package schema

import (
	"encoding/json"
	"io"
	"testing"
)

// burstEvent is a syscall-burst event as the tracer writes it: a 1-byte
// read on a file fd, no args (task wc4, decision #25).
func burstEvent() Event {
	return Event{SchemaVersion: Version, Project: "bench", Seq: 123_456,
		BootNS: 9_876_543_210_123, WallNS: 1_763_500_000_123_456_789,
		HostID: "0123456789abcdef0123456789abcdef", PID: 4242, PIDInc: 123_456_789, TID: 4243,
		Comm: "java", Event: KindRead, FD: 7, FDGen: 1, Ret: 1, LatencyNS: 700, Bytes: 1,
		Attribution: AttributionNone}
}

// BenchmarkNDJSONSink is the sink's whole per-event cost: validation,
// encoding and buffering (into io.Discard).
func BenchmarkNDJSONSink(b *testing.B) {
	s := NewNDJSONSink(io.Discard)
	ev := burstEvent()
	b.ReportAllocs()
	for i := range b.N {
		ev.Seq = uint64(i) + 1
		if err := s.Event(ev); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkValidate, BenchmarkAppendEvent and BenchmarkJSONMarshal split
// the sink's cost: the checks it keeps on the hot path, the encoder it
// uses and the encoding/json call that encoder replaced.
func BenchmarkValidate(b *testing.B) {
	ev := burstEvent()
	for range b.N {
		if err := ev.Validate(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAppendEvent(b *testing.B) {
	ev := burstEvent()
	buf := make([]byte, 0, 512)
	b.ReportAllocs()
	for range b.N {
		buf = AppendEvent(buf[:0], &ev)
	}
}

func BenchmarkJSONMarshal(b *testing.B) {
	ev := burstEvent()
	b.ReportAllocs()
	for range b.N {
		if _, err := json.Marshal(ev); err != nil {
			b.Fatal(err)
		}
	}
}
