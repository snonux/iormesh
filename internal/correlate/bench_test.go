package correlate

import (
	"encoding/binary"
	"io"
	"testing"

	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// burstOps is how many write+read pairs one benchmark stream holds: big
// enough that the per-process state is warm and the sink's buffer cycles
// many times, small enough to build once per benchmark.
const burstOps = 4096

// burstStream synthesizes the raw ring records of syscall-burst (the S3
// load, decision #25): per op a 1-byte write and a 1-byte read on one
// file fd, each an fd_event enter and a ret_event exit, exactly as
// generated_tracepoints.c emits them for sys_enter/exit_write and _read.
// Four records make two events.
func burstStream(tb testing.TB) [][]byte {
	tb.Helper()
	var recs [][]byte
	now := uint64(1_000_000)
	add := func(ev interface{ Bytes() ([]byte, error) }) {
		raw, err := ev.Bytes()
		if err != nil {
			tb.Fatal(err)
		}
		recs = append(recs, raw)
	}
	call := func(enter, exit types.TraceId) {
		now += 700
		add(&types.FdEvent{EventType: types.ENTER_FD_EVENT, TraceId: enter, Time: now,
			Pid: 4242, Tid: 4243, Fd: 7})
		now += 700
		add(&types.RetEvent{EventType: types.EXIT_RET_EVENT, TraceId: exit, Time: now,
			Pid: 4242, Tid: 4243, Ret: 1})
	}
	for range burstOps {
		call(types.SYS_ENTER_WRITE, types.SYS_EXIT_WRITE)
		call(types.SYS_ENTER_READ, types.SYS_EXIT_READ)
	}
	return recs
}

// decodeBurst is the slice of internal/bpf's decoder table the burst
// uses (that package needs libbpfgo and cannot be imported by a plain
// go test): the same pooled fast decoders the tracer runs.
func decodeBurst(raw []byte) any {
	switch types.EventType(binary.LittleEndian.Uint32(raw)) {
	case types.ENTER_FD_EVENT:
		return types.NewFdEventFast(raw)
	case types.EXIT_RET_EVENT:
		return types.NewRetEventFast(raw)
	}
	return nil
}

// BenchmarkSyscallBurst measures the tracer's userspace cost per emitted
// event on the syscall-burst shape: decode, correlate, encode and buffer
// the NDJSON line (into io.Discard, so disk speed is not measured). It is
// the number decision #25's "≤ 2 µs per emitted event" budget is held
// against on the consumer side; ns/event is reported as a metric so
// before/after comparisons need no arithmetic. Profile it with
//
//	go test ./internal/correlate -run '^$' -bench SyscallBurst -cpuprofile cpu.out
func BenchmarkSyscallBurst(b *testing.B) {
	recs := burstStream(b)
	c := New(Config{Host: Host{ID: "0123456789abcdef0123456789abcdef", Project: "bench"},
		Sink: schema.NewNDJSONSink(io.Discard), WallOffsetNS: 1_700_000_000_000_000_000,
		ProcInfo: func(uint32) (string, uint64) { return "java", 123_456_789 }})
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		rec := decodeBurst(recs[i%len(recs)])
		if err := c.Handle(rec); err != nil {
			b.Fatal(err)
		}
		// The tracer's session recycles every record after Handle.
		types.Recycle(rec)
	}
	b.StopTimer()
	// Four records per two events: one event per two records.
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/(float64(b.N)/2), "ns/event")
	if err := c.Close(); err != nil {
		b.Fatal(err)
	}
}
