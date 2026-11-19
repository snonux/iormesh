package bpf

import (
	"cmp"
	"encoding/binary"
	"fmt"
	"slices"
	"unsafe"

	"iormesh/internal/types"
)

// The read/write aggregation's map (internal/c/rwagg.c, maps.h; M2 step 11,
// decision #25) and the layout of its value, struct iormesh_rw_state:
// rwSlots slots of struct iormesh_rw_slot (56 bytes each: first_ns,
// last_ns, bytes, latency_ns as u64, then fd, file_ident, trace_id,
// streak, count, reserved as 32-bit words), then the in-flight markers and
// the pid. TestParseRWState pins the offsets against a hand-built value.
const (
	rwAggMapName  = "rw_agg_map"
	rwAggStopName = "rw_agg_stop"
	rwSlots       = 4
	rwSlotSize    = 56
	rwStateSize   = 256
	rwStatePidOff = rwSlots*rwSlotSize + 24
)

// StopAggregation freezes the kernel's read/write aggregation for the end of
// the session (rw_agg_stop, maps.h): from now on every read and write is
// sent as its own records, and no hook folds into or flushes rw_agg_map, so
// what PendingAggregates reads later is exactly what never reached the
// ring. Call it before the last records in flight are handled.
func (t *Tracer) StopAggregation() error {
	if t.closed() {
		return errClosed
	}
	m, err := t.module.GetMap(rwAggStopName)
	if err != nil {
		return fmt.Errorf("get %s: %w", rwAggStopName, err)
	}
	key, stop := uint32(0), uint32(1)
	if err := m.Update(unsafe.Pointer(&key), unsafe.Pointer(&stop)); err != nil {
		return fmt.Errorf("set %s: %w", rwAggStopName, err)
	}
	return nil
}

// PendingAggregates returns the folded read/write calls rw_agg_map still
// holds, one record per key with pending calls, oldest first. They are the
// calls of threads that have not reached a flush point when the session
// ends: a thread that folded and then blocked in an untraced call, or one
// still running when a stop or the duration ended the trace. Call it after
// the records in flight were handled and before Close — the map dies with
// the tracer — and hand the records to the correlator like any other.
//
// Call StopAggregation first: once it is set no hook folds or flushes, so
// the sums read here are frozen and none of them also reached the ring.
func (t *Tracer) PendingAggregates() ([]*types.RwAggregateEvent, error) {
	if t.closed() {
		return nil, errClosed
	}
	m, err := t.module.GetMap(rwAggMapName)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", rwAggMapName, err)
	}
	var recs []*types.RwAggregateEvent
	it := m.Iterator()
	for it.Next() {
		tid := binary.LittleEndian.Uint32(it.Key())
		raw, err := m.GetValue(unsafe.Pointer(&tid))
		if err != nil {
			continue // the thread exited and its entry went meanwhile
		}
		recs = append(recs, parseRWState(tid, raw)...)
	}
	if err := it.Err(); err != nil {
		return recs, fmt.Errorf("iterate %s: %w", rwAggMapName, err)
	}
	slices.SortFunc(recs, func(a, b *types.RwAggregateEvent) int {
		return cmp.Compare(a.Time, b.Time)
	})
	return recs, nil
}

// parseRWState turns one thread's struct iormesh_rw_state into a record
// per slot with pending calls — the record rwagg.c's iormesh_rw_send
// would have sent. A value of the wrong size yields nothing: it is not
// the layout this package knows.
func parseRWState(tid uint32, raw []byte) []*types.RwAggregateEvent {
	if len(raw) != rwStateSize {
		return nil
	}
	pid := binary.LittleEndian.Uint32(raw[rwStatePidOff:])
	var recs []*types.RwAggregateEvent
	for i := range rwSlots {
		s := raw[i*rwSlotSize : (i+1)*rwSlotSize]
		u64 := func(off int) uint64 { return binary.LittleEndian.Uint64(s[off:]) }
		u32 := func(off int) uint32 { return binary.LittleEndian.Uint32(s[off:]) }
		traceID, count := u32(40), u32(48)
		if traceID == 0 || count == 0 {
			continue
		}
		recs = append(recs, &types.RwAggregateEvent{
			EventType: types.RW_AGGREGATE_EVENT, TraceId: types.TraceId(traceID),
			Time: u64(0), EndTime: u64(8), TotalBytes: u64(16), LatencyNs: u64(24),
			Pid: pid, Tid: tid, Fd: int32(u32(32)), FileIdent: u32(36), Count: count,
		})
	}
	return recs
}
