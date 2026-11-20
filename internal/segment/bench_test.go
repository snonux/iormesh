package segment

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// BenchmarkWriterEvent is the segment writer's whole per-event cost on
// the tracer's hottest shape (a 1-byte read): validation, the batch copy,
// column encoding and zstd pages written to a real file, rotation off.
// Compare with schema's BenchmarkNDJSONSink (decision #25, task ee4).
func BenchmarkWriterEvent(b *testing.B) {
	dir := filepath.Join(b.TempDir(), "shop")
	w, err := New(Config{Dir: dir, FQDN: "h", Session: "s1", Project: "shop",
		Interval: time.Hour, MaxBytes: 1 << 40})
	if err != nil {
		b.Fatal(err)
	}
	ev := burstEvent(1)
	b.ReportAllocs()
	for i := range b.N {
		ev.Seq, ev.BootNS = uint64(i)+1, ev.BootNS+700
		if err := w.Event(&ev); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	if err := w.Close(); err != nil {
		b.Fatal(err)
	}
	// The file's bytes per event: decision #25's volume item, though a
	// burst of identical reads compresses far better than a real trace.
	st, err := os.Stat(filepath.Join(dir, ReadyDir, Name("h", "s1", w.start, 0)))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(float64(st.Size())/float64(b.N), "file-B/event")
}
