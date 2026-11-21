package bpf

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"iormesh/internal/types"
)

// finalRaw encodes ev as sock_final_map holds it.
func finalRaw(t *testing.T, ev types.TcpStateEvent) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := binary.Write(&b, binary.NativeEndian, ev); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// TestFinalRecordsDecodes: each socket the program took a snapshot of
// gives one record with its counters; a socket it declined (nil) gives
// none.
func TestFinalRecordsDecodes(t *testing.T) {
	want := types.TcpStateEvent{EventType: types.TCP_STATE_EVENT,
		SchemaVersion: types.TCP_STATE_EVENT_SCHEMA_VERSION, Sk: 0xa0, Pid: 7, Fd: 3,
		Oldstate: types.TCP_STATE_FINAL, Newstate: 1, BytesAcked: 6, BytesReceived: 8}
	snap := func(sk uint64) ([]byte, error) {
		if sk != want.Sk {
			return nil, nil
		}
		return finalRaw(t, want), nil
	}
	recs, err := finalRecords([]uint64{0xb0, 0xa0}, snap)
	if err != nil || len(recs) != 1 {
		t.Fatalf("finalRecords = %d records, %v; want 1", len(recs), err)
	}
	if *recs[0] != want {
		t.Fatalf("record %+v, want %+v", *recs[0], want)
	}
}

// TestFinalRecordsStopsOnError: a failing run ends the walk with the
// records taken so far and says which socket failed.
func TestFinalRecordsStopsOnError(t *testing.T) {
	boom := errors.New("boom")
	good := finalRaw(t, types.TcpStateEvent{EventType: types.TCP_STATE_EVENT,
		Oldstate: types.TCP_STATE_FINAL})
	snap := func(sk uint64) ([]byte, error) {
		if sk == 2 {
			return nil, boom
		}
		return good, nil
	}
	recs, err := finalRecords([]uint64{1, 2, 3}, snap)
	if !errors.Is(err, boom) || len(recs) != 1 {
		t.Fatalf("finalRecords = %d records, %v; want 1 and boom", len(recs), err)
	}
}

// TestDecodeFinalRejects: a value of another size, another record type or
// a record that is no final snapshot is an error, not a record.
func TestDecodeFinalRejects(t *testing.T) {
	ok := types.TcpStateEvent{EventType: types.TCP_STATE_EVENT, Oldstate: types.TCP_STATE_FINAL}
	notFinal := ok
	notFinal.Oldstate = types.TCP_STATE_SNAPSHOT
	otherType := ok
	otherType.EventType = types.TCP_SEQ_EVENT
	for name, raw := range map[string][]byte{
		"short":      finalRaw(t, ok)[:100],
		"not final":  finalRaw(t, notFinal),
		"other type": finalRaw(t, otherType),
	} {
		if _, err := decodeFinal(raw); !errors.Is(err, errFinalLayout) {
			t.Errorf("%s: err %v, want errFinalLayout", name, err)
		}
	}
	if _, err := decodeFinal(finalRaw(t, ok)); err != nil {
		t.Errorf("a final record: %v", err)
	}
}
