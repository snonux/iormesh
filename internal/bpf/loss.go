// Adapted from ior internal/ringbuf_drop_consumer.go @ b21afc50328c.
// Changes: the counter is folded into Losses beside the userspace decode
// losses; reading takes a module map directly; the attached programs'
// recursion misses are read beside it as possible loss (Missed,
// progmiss.go), outside Total.

package bpf

import (
	"encoding/binary"
	"fmt"
	"sync/atomic"
	"unsafe"
)

// ringbufDropMapName is the single-slot PERCPU_ARRAY in internal/c/maps.h.
// Every generated handler bumps its CPU's slot when bpf_ringbuf_reserve()
// fails, i.e. when a record is lost because event_map is full.
const ringbufDropMapName = "ringbuf_drop_map"

// perCPUStride is the per-CPU element stride libbpfgo returns for a __u64
// per-CPU value: the element size rounded up to 8 bytes.
const perCPUStride = 8

// Losses counts every record a trace wanted but did not deliver, and
// beside it the program runs the kernel skipped. All counters are
// cumulative since the tracer was opened, so the correlator can turn a
// delta into a drop event (PLAN.md, "Trace output contract": loss is
// recorded, never silent - and never inflated).
type Losses struct {
	// Kernel is the number of records the BPF programs could not reserve in
	// the ring buffer (userspace backpressure), summed over CPUs.
	Kernel uint64
	// Missed is how often the kernel skipped one of the attached programs
	// - the raw_syscalls dispatchers and the other tracepoint programs -
	// under its recursion guard (recursion misses, progmiss.go). It is
	// possible loss, not lost records: the skip comes before the
	// program's target filter, so it counts untraced processes' calls -
	// for a dispatcher, every syscall on the host, traced kind or not -
	// and only bounds the target records lost from above (task vd4). Hence it is not in Total; it reaches
	// the trace as its own skipped_runs drop.
	Missed uint64
	// Unknown, Malformed and Short are records that arrived but could not be
	// decoded: an event type without decoder, a decoder that rejected the
	// record's length, or a record too short to carry its type.
	Unknown, Malformed, Short uint64
}

// Total is every record known lost, kernel- and userspace-side: failed
// ring reservations and undecodable records, all of them the trace's own.
// Skipped program runs (Missed) are not in it.
func (l Losses) Total() uint64 {
	return l.Kernel + l.Unknown + l.Malformed + l.Short
}

// decodeLosses are the userspace-side counters; the poll goroutine bumps
// them while Losses reads them concurrently.
type decodeLosses struct {
	unknown, malformed, short atomic.Uint64
}

func (d *decodeLosses) count(e decodeError) {
	switch e {
	case decodeUnknown:
		d.unknown.Add(1)
	case decodeMalformed:
		d.malformed.Add(1)
	case decodeShort:
		d.short.Add(1)
	}
}

// valueReader is the one map call the drop counter needs, so the per-CPU
// sum is testable without a kernel.
type valueReader interface {
	GetValue(key unsafe.Pointer) ([]byte, error)
}

// readKernelDrops returns ringbuf_drop_map's slot 0 summed over all CPUs.
func readKernelDrops(m valueReader) (uint64, error) {
	key := uint32(0)
	raw, err := m.GetValue(unsafe.Pointer(&key))
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", ringbufDropMapName, err)
	}
	return sumPerCPUCounters(raw)
}

// sumPerCPUCounters adds up the little-endian __u64 counters libbpfgo returns
// for a per-CPU map value (one element per possible CPU, 8-byte stride).
func sumPerCPUCounters(raw []byte) (uint64, error) {
	if len(raw) == 0 || len(raw)%perCPUStride != 0 {
		return 0, fmt.Errorf("invalid per-cpu counter size %d (want a non-zero multiple of %d)",
			len(raw), perCPUStride)
	}
	var total uint64
	for off := 0; off < len(raw); off += perCPUStride {
		total += binary.LittleEndian.Uint64(raw[off : off+perCPUStride])
	}
	return total, nil
}
