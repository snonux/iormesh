package bpf

import (
	"encoding/binary"
	"testing"

	"iormesh/internal/types"
)

// rwSlotBytes encodes one struct iormesh_rw_slot (maps.h) by hand, field by
// field, so the parser's offsets are checked against the C layout rather
// than against themselves.
func rwSlotBytes(first, last, bytes, latency uint64, fd int32, ident, traceID, streak, count uint32) []byte {
	b := make([]byte, 0, rwSlotSize)
	for _, v := range []uint64{first, last, bytes, latency} {
		b = binary.LittleEndian.AppendUint64(b, v)
	}
	for _, v := range []uint32{uint32(fd), ident, traceID, streak, count, 0} {
		b = binary.LittleEndian.AppendUint32(b, v)
	}
	return b
}

// TestParseRWState: a struct iormesh_rw_state yields one record per slot
// with pending calls; free slots, slots with nothing folded and a value of
// another size yield none.
func TestParseRWState(t *testing.T) {
	var raw []byte
	raw = append(raw, rwSlotBytes(100, 900, 4000, 70, 5, 9, uint32(types.SYS_ENTER_WRITE), 4, 1000)...)
	raw = append(raw, rwSlotBytes(0, 0, 0, 0, 0, 0, 0, 0, 0)...)                            // free
	raw = append(raw, rwSlotBytes(1, 2, 0, 0, 6, 0, uint32(types.SYS_ENTER_READ), 3, 0)...) // streak only
	raw = append(raw, rwSlotBytes(200, 300, 7, 3, 6, 0, uint32(types.SYS_ENTER_READ), 4, 7)...)
	// cur_enter_ns, cur_slot, cur_held, cur_fd, cur_ident, pid, reserved.
	raw = binary.LittleEndian.AppendUint64(raw, 55)
	for _, v := range []uint32{1, 1, 5, 9, 4711, 0} {
		raw = binary.LittleEndian.AppendUint32(raw, v)
	}
	if len(raw) != rwStateSize {
		t.Fatalf("fixture is %d bytes, want %d (struct iormesh_rw_state)", len(raw), rwStateSize)
	}
	recs := parseRWState(4719, raw)
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2: %+v", len(recs), recs)
	}
	w, r := recs[0], recs[1]
	want := types.RwAggregateEvent{EventType: types.RW_AGGREGATE_EVENT, TraceId: types.SYS_ENTER_WRITE,
		Time: 100, EndTime: 900, TotalBytes: 4000, LatencyNs: 70, Pid: 4711, Tid: 4719, Fd: 5,
		FileIdent: 9, Count: 1000}
	if *w != want {
		t.Fatalf("write record %+v, want %+v", *w, want)
	}
	if r.TraceId != types.SYS_ENTER_READ || r.Fd != 6 || r.Count != 7 || r.TotalBytes != 7 || r.Time != 200 {
		t.Fatalf("read record %+v", *r)
	}
	if got := parseRWState(1, raw[:rwStateSize-8]); got != nil {
		t.Fatalf("a short value parsed: %+v", got)
	}
}
