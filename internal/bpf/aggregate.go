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
	rwSlots       = 4
	rwSlotSize    = 56
	rwStateSize   = 256
	rwStatePidOff = rwSlots*rwSlotSize + 24
)

// PendingAggregates returns the folded read/write calls rw_agg_map still
// holds, one record per key with pending calls, oldest first. They are the
// calls of threads that have not reached a flush point when the session
// ends: a thread that folded and then blocked in an untraced call, or one
// still running when a stop or the duration ended the trace. Call it after
// the records in flight were handled and before Close — the map dies with
// the tracer — and hand the records to the correlator like any other.
//
// The probes are still attached, so a thread still running may fold one
// more call into a key between this read and Close; that call is lost like
// any record in flight at Close, uncounted (Close).
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
