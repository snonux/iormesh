package agentspool

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

const testCap = minCapacity

func openRing(t *testing.T, path string) *Ring {
	t.Helper()
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// drainAll drains once and fails the test on an error.
func drainAll(t *testing.T, r *Ring) []Record {
	t.Helper()
	var out []Record
	if _, err := r.Drain(func(rec Record) { out = append(out, rec) }); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRoundTrip(t *testing.T) {
	path, w := newTestRing(t, testCap)
	want := []Record{
		Hook{ID: 3, Kind: "read", Class: "java/io/FileInputStream", Method: "read",
			Native: "readBytes", Syscalls: []string{"read", "pread64"}},
		Thread{ID: 1, KTID: 4243, Name: "main"},
		Thread{ID: 2, KTID: 4244, Name: "", Virtual: true},
		Stack{ID: 99, Frames: []string{"a.B#c(B.java:3)", "größe#ü(X.java:1)"}},
		Stack{ID: 100, Frames: []string{}},
		Bracket{Hook: 3, KTID: 4243, EnterMono: 10, ExitMono: 20, StackID: 99, ThreadID: 1},
		Watermark{Mono: 21},
	}
	for _, rec := range want {
		if !write(w, rec) {
			t.Fatalf("write %+v dropped", rec)
		}
	}
	r := openRing(t, path)
	if got := r.Header(); got != (Header{Version: Version, Capacity: testCap, PID: 4242, CreatedMono: 77}) {
		t.Errorf("header %+v", got)
	}
	if got := drainAll(t, r); !reflect.DeepEqual(got, want) {
		t.Errorf("records\n got %+v\nwant %+v", got, want)
	}
	if r.Pending() != 0 || r.Losses() != (Losses{}) {
		t.Errorf("pending %d, losses %+v", r.Pending(), r.Losses())
	}
}

func write(w *testWriter, rec Record) bool {
	switch v := rec.(type) {
	case Bracket:
		return w.bracket(v)
	case Stack:
		return w.stack(v)
	case Thread:
		return w.thread(v)
	case Watermark:
		return w.watermark(v)
	case Hook:
		return w.hook(v)
	}
	panic("unknown record")
}

// TestLaggingReader: with no reader the ring fills, the rest is dropped
// and counted; draining frees the space for new records.
func TestLaggingReader(t *testing.T) {
	path, w := newTestRing(t, testCap)
	const written = 3000
	for i := range written {
		w.bracket(Bracket{Hook: 1, EnterMono: uint64(i), ExitMono: uint64(i)})
	}
	r := openRing(t, path)
	got := drainAll(t, r)
	if want := testCap / bracketLen; len(got) != want {
		t.Errorf("delivered %d, want a full ring of %d", len(got), want)
	}
	l := r.Losses()
	if uint64(len(got))+l.Dropped() != written || l.DroppedBrackets != l.Dropped() {
		t.Errorf("delivered %d + dropped %+v != written %d", len(got), l, written)
	}
	if l.DroppedBytes != l.DroppedBrackets*bracketLen {
		t.Errorf("dropped bytes %d for %d brackets", l.DroppedBytes, l.DroppedBrackets)
	}
	if !w.bracket(Bracket{Hook: 2}) || len(drainAll(t, r)) != 1 {
		t.Error("no room after draining")
	}
}

// TestWraparound: records of varied sizes, read as they come, lap the
// ring many times; the PADs at each lap's end never surface and nothing
// is lost or reordered.
func TestWraparound(t *testing.T) {
	path, w := newTestRing(t, testCap)
	r := openRing(t, path)
	var wrote int
	for i := range 20000 {
		rec := Record(Bracket{Hook: 1, StackID: uint64(i), ExitMono: 1})
		if i%3 == 0 {
			rec = Stack{ID: uint64(i), Frames: []string{string(make([]byte, i%50+1))}}
		}
		if !write(w, rec) {
			t.Fatalf("record %d dropped with a reader keeping up", i)
		}
		wrote++
		got := drainAll(t, r)
		if len(got) != 1 || !reflect.DeepEqual(got[0], rec) {
			t.Fatalf("record %d: got %+v, want %+v", i, got, rec)
		}
	}
	if laps := uint64(wrote) * bracketLen / testCap; laps < 10 {
		t.Errorf("only %d laps", laps)
	}
}

// TestConcurrentWriters: eight writers race on head while the reader
// drains; each writer's records arrive in its own order and delivered +
// dropped equals written.
func TestConcurrentWriters(t *testing.T) {
	const writers, per = 8, 20000
	path, w := newTestRing(t, testCap)
	r := openRing(t, path)
	var wg sync.WaitGroup
	done := make(chan struct{})
	for id := range writers {
		wg.Go(func() {
			for seq := range per {
				w.bracket(Bracket{Hook: int32(id + 1), StackID: uint64(seq + 1)})
			}
		})
	}
	go func() { wg.Wait(); close(done) }()
	last := make([]uint64, writers+1)
	delivered := 0
	check := func(rec Record) {
		b := rec.(Bracket)
		if b.StackID <= last[b.Hook] {
			t.Errorf("writer %d: seq %d after %d", b.Hook, b.StackID, last[b.Hook])
		}
		last[b.Hook] = b.StackID
		delivered++
	}
	for finished := false; !finished; {
		select {
		case <-done:
			finished = true
		default:
		}
		if _, err := r.Drain(check); err != nil {
			t.Fatal(err)
		}
	}
	if l := r.Losses(); uint64(delivered)+l.Dropped() != writers*per || l.Total() != l.Dropped() {
		t.Errorf("delivered %d + losses %+v != written %d", delivered, l, writers*per)
	}
	if r.Pending() != 0 {
		t.Errorf("pending %d after the writers finished", r.Pending())
	}
}

// malformedCase is a record with sound framing but a body (or type) the
// reader must skip and count, not deliver.
type malformedCase struct {
	typ     int
	body    []byte
	unknown bool
}

func malformedCases() map[string]malformedCase {
	good := bracketBody(Bracket{Hook: 1, KTID: 2, EnterMono: 3, ExitMono: 4})
	bad := func(f func(b []byte) []byte) []byte { return f(append([]byte(nil), good...)) }
	return map[string]malformedCase{
		"bracket too long":   {typeBracket, append(append([]byte(nil), good...), make([]byte, 8)...), false},
		"bracket hook 0":     {typeBracket, bad(func(b []byte) []byte { le.PutUint32(b, 0); return b }), false},
		"bracket ktid < 0":   {typeBracket, bad(func(b []byte) []byte { le.PutUint32(b[4:], 1<<31); return b }), false},
		"bracket exit<enter": {typeBracket, bad(func(b []byte) []byte { le.PutUint64(b[16:], 2); return b }), false},
		"watermark long":     {typeWatermark, make([]byte, 16), false},
		"stack short":        {typeStack, make([]byte, 8), false},
		"stack bad utf8":     {typeStack, stringsBody(1, 1, []string{"\xff\xfe"}), false},
		"stack count high":   {typeStack, stringsBody(1, 9, []string{"x"}), false},
		"stack reserved":     {typeStack, setByte(stringsBody(1, 1, []string{"x"}), 12, 1), false},
		"stack padding":      {typeStack, setByte(stringsBody(1, 1, []string{"x"}), 21, 7), false},
		"stack extra word":   {typeStack, append(stringsBody(1, 1, []string{"x"}), make([]byte, 8)...), false},
		"string too long":    {typeStack, setByte(stringsBody(1, 1, []string{"x"}), 16, 200), false},
		"thread ktid < 0":    {typeThread, stringsBody(1, 1<<31, []string{"t"}), false},
		"thread flag":        {typeThread, flagsBody(1, 1, 2, []string{"t"}), false},
		"hook 4 strings":     {typeHook, stringsBody(1, 4, []string{"a", "b", "c", "d"}), false},
		"hook empty string":  {typeHook, stringsBody(1, 5, []string{"a", "b", "", "d", "e"}), false},
		"hook id 0":          {typeHook, hookBody(Hook{Kind: "a", Class: "b", Method: "c", Native: "d", Syscalls: []string{"e"}}), false},
		"hook id too big":    {typeHook, hookBody(Hook{ID: -1, Kind: "a", Class: "b", Method: "c", Native: "d", Syscalls: []string{"e"}}), false},
		"hook flags":         {typeHook, flagsBody(1, 5, 1, []string{"a", "b", "c", "d", "e"}), false},
		"unknown type":       {9, make([]byte, 8), true},
	}
}

func TestMalformedSkipped(t *testing.T) {
	for name, c := range malformedCases() {
		t.Run(name, func(t *testing.T) {
			path, w := newTestRing(t, testCap)
			w.raw(c.typ, c.body)
			w.bracket(Bracket{Hook: 5, ExitMono: 1})
			r := openRing(t, path)
			got := drainAll(t, r)
			if len(got) != 1 || got[0].(Bracket).Hook != 5 {
				t.Errorf("records %+v, want only the valid bracket after it", got)
			}
			want := Losses{Malformed: 1}
			if c.unknown {
				want = Losses{Unknown: 1}
			}
			if l := r.Losses(); l != want {
				t.Errorf("losses %+v, want %+v", l, want)
			}
		})
	}
}

func setByte(b []byte, i int, v byte) []byte {
	b[i] = v
	return b
}

// TestCorruptFraming: a bad framing word leaves no way to the next
// record; Drain fails with ErrCorrupt and keeps failing.
func TestCorruptFraming(t *testing.T) {
	cases := map[string]struct {
		word uint64
		head uint64
	}{
		"length 0":         {typeBracket << 32, 8},
		"length unaligned": {12 | typeBracket<<32, 16},
		"past lap end":     {(testCap + 8) | typeBracket<<32, testCap},
		"past head":        {bracketLen | typeBracket<<32, 8},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			path, w := newTestRing(t, testCap)
			le.PutUint64(w.mem[offHead:], c.head)
			le.PutUint64(w.mem[headerSize:], c.word)
			r := openRing(t, path)
			for range 2 {
				if _, err := r.Drain(func(Record) {}); !errors.Is(err, ErrCorrupt) {
					t.Fatalf("Drain: %v, want ErrCorrupt", err)
				}
			}
		})
	}
}

// TestOpenRejects: a header Open cannot trust is an error, never a guess.
func TestOpenRejects(t *testing.T) {
	cases := map[string]struct {
		edit func(path string, mem []byte) error
		want error
	}{
		"no magic yet":       {func(_ string, m []byte) error { le.PutUint64(m, 0); return nil }, ErrNotReady},
		"bad magic":          {func(_ string, m []byte) error { m[0] ^= 1; return nil }, ErrCorrupt},
		"version 1":          {func(_ string, m []byte) error { le.PutUint32(m[offVersion:], 1); return nil }, ErrVersion},
		"header size":        {func(_ string, m []byte) error { le.PutUint32(m[offHeaderSize:], 64); return nil }, ErrCorrupt},
		"capacity not 2^k":   {func(_ string, m []byte) error { le.PutUint64(m[offCapacity:], testCap-8); return nil }, ErrCorrupt},
		"capacity too big":   {func(_ string, m []byte) error { le.PutUint64(m[offCapacity:], testCap*2); return nil }, ErrTruncated},
		"head behind tail":   {func(_ string, m []byte) error { le.PutUint64(m[offTail:], 8); return nil }, ErrCorrupt},
		"head a lap ahead":   {func(_ string, m []byte) error { le.PutUint64(m[offHead:], testCap+8); return nil }, ErrCorrupt},
		"file truncated":     {func(p string, _ []byte) error { return os.Truncate(p, headerSize+testCap/2) }, ErrTruncated},
		"no room for header": {func(p string, _ []byte) error { return os.Truncate(p, 100) }, ErrNotReady},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			path, w := newTestRing(t, testCap)
			if err := c.edit(path, w.mem); err != nil {
				t.Fatal(err)
			}
			r, err := Open(path)
			if !errors.Is(err, c.want) {
				if r != nil {
					_ = r.Close()
				}
				t.Fatalf("Open: %v, want %v", err, c.want)
			}
		})
	}
	if _, err := Open(filepath.Join(t.TempDir(), "missing.ring")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: %v", err)
	}
}
