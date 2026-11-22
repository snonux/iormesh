package segment

import (
	"math/rand/v2"
	"path/filepath"
	"strings"
	"testing"

	parquetgo "github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/encoding/plain"

	"iormesh/internal/schema"
)

// randomEvent sets every field of the event, nested objects and lists
// included, to a random value, its zero value or nil, so the columnar
// encoder meets every null and level combination the schema has. It
// need not be a valid event: the encoder maps fields, it does not judge
// them.
func randomEvent(r *rand.Rand, seq uint64) schema.Event {
	ev := burstEvent(seq)
	ev.Comm = pick(r, "java", "curl", "")
	ev.Event = pick(r, schema.KindRead, schema.KindConnect, schema.KindExec)
	ev.Err = pick(r, "", "EAGAIN")
	ev.FD, ev.Ret, ev.BootNS = int32(r.IntN(9))-1, r.Int64()-r.Int64(), r.Uint64()
	ev.ConnID = pick(r, "", "0123456789abcdef")
	ev.Args = randomArgs(r)
	ev.StackID = maybe(r, uint32(r.IntN(9)))
	if r.IntN(2) == 0 {
		ev.Java = &schema.Java{Thread: pick(r, "", "main"), Stack: randomList(r, uint64(7)),
			Carrier: r.IntN(2) == 0}
	}
	if r.IntN(3) == 0 {
		ev.HTTP = &schema.HTTP{Method: pick(r, "", "GET"), Scheme: pick(r, "", "https"),
			Host: pick(r, "", "h"), Path: pick(r, "", "/x?a=***"), Status: uint16(r.IntN(2) * 200),
			ReqSeq: uint32(r.IntN(3)), Version: pick(r, "", "HTTP/2"),
			ReqHeaders: randomList(r, "a: b"), RespHeaders: randomList(r, "c: d"),
			ReqBytes: maybe(r, r.Uint64()), RespBytes: maybe(r, uint64(0)), Held: r.IntN(2) == 0}
	}
	if r.IntN(4) == 0 {
		ev.Session = &schema.Session{BootID: "b", FQDN: pick(r, "", "f"), Kernel: "k", Arch: "x86_64"}
	}
	if r.IntN(3) == 0 {
		ev.Otel = &schema.Otel{TraceID: "t", SpanID: pick(r, "", "s")}
	}
	if r.IntN(3) == 0 {
		// Addrs never empty: a reader returns [] for an absent list, which
		// NDJSON tells from null only here (a valid dns event has addresses).
		ev.DNS = &schema.DNS{Host: pick(r, "", "example.org"), Addrs: []string{"::1", "::2"}[:1+r.IntN(2)]}
	}
	if r.IntN(2) == 0 {
		// Task sh4: an in-pod tracer's pod, with or without a container.
		ev.Pod = &schema.Pod{Namespace: pick(r, "prod", "dev"), Name: pick(r, "web", "web-7d9f"),
			ContainerID: pick(r, "", strings.Repeat("ab", 32))}
	}
	return ev
}

func randomArgs(r *rand.Rand) schema.Args {
	return schema.Args{Path: maybe(r, "/p"), NewPath: maybe(r, ""), Flags: maybe(r, uint32(r.Uint32())),
		Cmd: maybe(r, uint32(0)), Offset: maybe(r, r.Int64()-r.Int64()), LocalAddr: maybe(r, "::1"),
		LocalPort: maybe(r, uint16(r.Uint32())), RemoteAddr: maybe(r, "::2"),
		RemotePort: maybe(r, uint16(0)), SockState: maybe(r, "established"),
		Marker: maybe(r, "m"), Lost: maybe(r, r.Uint64()), DropReason: maybe(r, "d"),
		EndReason: maybe(r, "e"), Argv: randomList(r, "arg"), FD2: maybe(r, int32(-1)),
		FD2Gen: maybe(r, uint32(3)), Signal: maybe(r, int32(9)), TargetPID: maybe(r, r.Uint32()),
		SenderPID: maybe(r, uint32(0)), Count: maybe(r, uint64(2)), SndUna: maybe(r, r.Uint32()),
		RcvNxt: maybe(r, r.Uint32()), BytesAcked: maybe(r, r.Uint64()),
		BytesReceived: maybe(r, uint64(0)), TCPSeq: maybe(r, r.Uint32()),
		Pattern: maybe(r, "app"), ParentPID: maybe(r, r.Uint32()), ParentInc: maybe(r, r.Uint64()),
		SockSnapshot: maybe(r, "final"), BytesPartial: maybe(r, true)}
}

func pick[T any](r *rand.Rand, vs ...T) T { return vs[r.IntN(len(vs))] }

func maybe[T any](r *rand.Rand, v T) *T {
	if r.IntN(2) == 0 {
		return nil
	}
	return &v
}

// randomList is nil, empty or one to three copies of v.
func randomList[T any](r *rand.Rand, v T) []T {
	switch n := r.IntN(5); n {
	case 0:
		return nil
	case 1:
		return []T{}
	default:
		out := make([]T, n-1)
		for i := range out {
			out[i] = v
		}
		return out
	}
}

// slotValues renders a column's buffered slots back as parquet values
// with their levels, as Deconstruct produces them.
func slotValues(t *testing.T, c *column, rows int) []parquetgo.Value {
	t.Helper()
	var out []parquetgo.Value
	var nv int
	plainBytes, dictBytes := splitPlain(t, c.plain), splitPlain(t, c.dict.values)
	slots := rows
	if c.maxDef > 0 {
		slots = len(c.def)
	}
	for i := range slots {
		var def, rep byte
		if c.maxDef > 0 {
			def = c.def[i]
		}
		if c.maxRep > 0 {
			rep = c.rep[i]
		}
		v := parquetgo.Value{}
		if def == c.maxDef {
			v = c.valueAt(nv, plainBytes, dictBytes)
			nv++
		}
		out = append(out, v.Level(int(rep), int(def), 0))
	}
	return out
}

func (c *column) valueAt(i int, plainBytes, dictBytes [][]byte) parquetgo.Value {
	switch c.kind {
	case kindInt32:
		return parquetgo.Int32Value(c.i32[i])
	case kindInt64, kindDelta64:
		return parquetgo.Int64Value(c.i64[i])
	case kindBool:
		return parquetgo.BooleanValue(c.bools[i])
	case kindBytes:
		return parquetgo.ByteArrayValue(plainBytes[i])
	default:
		return parquetgo.ByteArrayValue(dictBytes[c.i32[i]])
	}
}

func splitPlain(t *testing.T, b []byte) [][]byte {
	t.Helper()
	var out [][]byte
	err := plain.RangeByteArray(b, func(v []byte) error {
		out = append(out, v)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestEventColumnsMatchDeconstruct holds the hand-written mapping to
// parquet-go's own: for every column, the slots the encoder buffered
// equal the values and levels the library's reflection-based
// Deconstruct gives the same events.
func TestEventColumnsMatchDeconstruct(t *testing.T) {
	cols, err := newEventColumns()
	if err != nil {
		t.Fatal(err)
	}
	r := rand.New(rand.NewPCG(1, 2))
	evs := richEvents()
	for i := range 3000 {
		evs = append(evs, randomEvent(r, uint64(i+100)))
	}
	want := make([][]parquetgo.Value, numEventColumns)
	for i := range evs {
		cols.add(&evs[i])
		row := cols.schema.Deconstruct(nil, &evs[i])
		row.Range(func(col int, vs []parquetgo.Value) bool {
			want[col] = append(want[col], vs...)
			return true
		})
	}
	for i := range cols.cols {
		c := &cols.cols[i]
		got := slotValues(t, c, cols.rows)
		if len(got) != len(want[i]) {
			t.Fatalf("column %v: %d slots, want %d", c.path, len(got), len(want[i]))
		}
		for j := range got {
			g, w := got[j], want[i][j]
			if g.DefinitionLevel() != w.DefinitionLevel() || g.RepetitionLevel() != w.RepetitionLevel() ||
				!parquetgo.Equal(g, w) {
				t.Fatalf("column %v slot %d: got %v (r%d d%d), want %v (r%d d%d)", c.path, j,
					g, g.RepetitionLevel(), g.DefinitionLevel(), w, w.RepetitionLevel(), w.DefinitionLevel())
			}
		}
	}
}

// TestEventFileRoundTrip writes random events over several row groups
// and reads them back with parquet-go: the file is plain parquet, and
// every event survives it unchanged.
func TestEventFileRoundTrip(t *testing.T) {
	tmpl, err := newFooterTemplate()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s, err := openEventStream(filepath.Join(dir, "active"), newZstdCodec(), tmpl)
	if err != nil {
		t.Fatal(err)
	}
	r := rand.New(rand.NewPCG(3, 4))
	evs := richEvents()
	for i := range 2*eventRowsPerRowGroup + 77 {
		evs = append(evs, randomEvent(r, uint64(i+100)))
	}
	for i := range evs {
		if err := s.add(&evs[i]); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "ready.parquet")
	if err := s.publish(path, map[string]string{"iormesh.session": "a1b2c3d4"}); err != nil {
		t.Fatal(err)
	}
	// Three by the row count at least; the byte cap may cut more.
	if got := len(s.groups); got < 3 {
		t.Errorf("%d row groups, want 3 or more", got)
	}
	sameNDJSON(t, readEvents(t, path), evs)
	assertZstd(t, path)
}
