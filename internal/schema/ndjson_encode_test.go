package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// awkwardStrings are the values encoding/json escapes or must not: HTML
// characters, quotes and backslashes, every named control escape and an
// unnamed one, invalid UTF-8 (a lone byte and a cut sequence), the two
// JSONP separators, multi-byte runes and the empty string.
var awkwardStrings = []string{
	"", "a", "/tmp/run1/demo.dat", "<script>&amp;</script>", `"quoted" \back\`,
	"\b\f\n\r\t", "\x00\x01\x1f\x7f", "\xff", "ab\xe2\x82", "\u2028x\u2029",
	"é漢字🙂", "mixed é<\x02>\xc3", "java", "-Dpassword=***",
	// Longer than a word, the escape past appendJSONString's 8-byte fast
	// path or inside its first words (task ud4).
	"0123456789abcdef0123456789abcdef", "abcdefgh<", "abcdefghijklmno\x00p", "abcdefg\"h",
	"abcdefgh\u2028", "abcdefghijklmnop\xff", "a&cdefghijklmnopqrstuvwx",
}

// fill sets v (addressable) to a random value, recursing through structs,
// pointers and slices. It knows every kind the schema types use and
// panics on any other, so a field of a new kind fails the test instead
// of going unchecked.
func fill(r *rand.Rand, v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		v.SetString(awkwardStrings[r.IntN(len(awkwardStrings))])
	case reflect.Bool:
		v.SetBool(r.IntN(2) == 0)
	case reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(randUint(r) & (1<<(8*v.Type().Size()) - 1))
	case reflect.Int32, reflect.Int64:
		v.SetInt(int64(randUint(r)) >> (64 - 8*v.Type().Size()))
	case reflect.Pointer:
		if r.IntN(3) == 0 {
			return // nil: omitted
		}
		v.Set(reflect.New(v.Type().Elem()))
		if r.IntN(4) != 0 { // else a pointer to the zero value: present
			fill(r, v.Elem())
		}
	case reflect.Slice:
		fillSlice(r, v)
	case reflect.Struct:
		for i := range v.NumField() {
			fill(r, v.Field(i))
		}
	default:
		panic(fmt.Sprintf("fill: unhandled kind %s (teach AppendEvent and this test)", v.Kind()))
	}
}

// fillSlice leaves the slice nil, makes it empty but non-nil (omitempty
// drops both) or fills one to three elements.
func fillSlice(r *rand.Rand, v reflect.Value) {
	switch n := r.IntN(5); n {
	case 0:
	case 1:
		v.Set(reflect.MakeSlice(v.Type(), 0, 0))
	default:
		v.Set(reflect.MakeSlice(v.Type(), n-1, n-1))
		for i := range n - 1 {
			fill(r, v.Index(i))
		}
	}
}

// randUint favours the edges: 0 (omitempty), small values and the
// extremes, where a hand-written integer format would break first.
func randUint(r *rand.Rand) uint64 {
	switch r.IntN(4) {
	case 0:
		return 0
	case 1:
		return uint64(r.IntN(1000))
	case 2:
		return math.MaxUint64 - uint64(r.IntN(2))
	}
	return r.Uint64()
}

func randEvent(r *rand.Rand) Event {
	var ev Event
	fill(r, reflect.ValueOf(&ev).Elem())
	return ev
}

// invalidMark stands in for each invalid UTF-8 byte in the copy of an
// event that json.Marshal encodes: a private-use rune every Go version
// writes as its raw three bytes, which the reference then turns into the
// documented `\ufffd` escape. fill never produces it; the fuzzer skips
// inputs that contain it.
const invalidMark = "\ue000"

// marshalReference is the encoder's expected output for ev: json.Marshal
// for everything json.Marshal writes the same on every Go version, and
// the six-byte escape `\ufffd` for each invalid UTF-8 byte, as
// docs/schema.md ("NDJSON framing") defines it. json.Marshal itself is no
// reference there: Go 1.26 writes `\ufffd`, Go 1.27 the raw U+FFFD bytes
// (task 8m4).
func marshalReference(t *testing.T, ev Event) []byte {
	t.Helper()
	markInvalid(reflect.ValueOf(&ev).Elem())
	out, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.ReplaceAll(out, []byte(invalidMark), []byte(`\ufffd`))
}

// markInvalid replaces, in every string reachable from v, each invalid
// UTF-8 byte with invalidMark. Slices are copied first, so the caller's
// event (whose slices the copy shares) stays unchanged.
func markInvalid(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		if !utf8.ValidString(v.String()) {
			v.SetString(replaceInvalid(v.String()))
		}
	case reflect.Pointer:
		if !v.IsNil() {
			p := reflect.New(v.Type().Elem())
			p.Elem().Set(v.Elem())
			markInvalid(p.Elem())
			v.Set(p)
		}
	case reflect.Slice:
		if !v.IsNil() {
			c := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
			reflect.Copy(c, v)
			for i := range c.Len() {
				markInvalid(c.Index(i))
			}
			v.Set(c)
		}
	case reflect.Struct:
		for i := range v.NumField() {
			markInvalid(v.Field(i))
		}
	}
}

// replaceInvalid is s with each invalid UTF-8 byte (one byte per
// utf8.RuneError of size 1, as the decoder steps) replaced by invalidMark.
func replaceInvalid(s string) string {
	var b []byte
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b = append(b, invalidMark...)
		} else {
			b = append(b, s[i:i+size]...)
		}
		i += size
	}
	return string(b)
}

func assertSameAsMarshal(t *testing.T, ev Event) {
	t.Helper()
	want := marshalReference(t, ev)
	if got := AppendEvent(nil, &ev); !bytes.Equal(got, want) {
		t.Fatalf("AppendEvent differs from the json.Marshal reference:\n got %s\nwant %s", got, want)
	}
}

// TestAppendEventMatchesMarshal is the encoder's contract: on randomly
// filled events (every field of Event, Args, Java, HTTP and Session, nil
// and zero and extreme values, awkward strings) its bytes equal
// json.Marshal's, so swapping the encoder cannot change a trace file or
// a golden — except for invalid UTF-8, where the reference is the
// project's documented `\ufffd` escape, not the installed Go's choice
// (marshalReference).
func TestAppendEventMatchesMarshal(t *testing.T) {
	r := rand.New(rand.NewPCG(25, 4))
	assertSameAsMarshal(t, Event{})
	assertSameAsMarshal(t, sampleEvent(1))
	for range 20000 {
		assertSameAsMarshal(t, randEvent(r))
	}
}

// TestAppendEventAppends: the encoder extends dst, keeping what is there.
func TestAppendEventAppends(t *testing.T) {
	ev := sampleEvent(7)
	got := AppendEvent([]byte("prefix"), &ev)
	want := marshalReference(t, ev)
	if !bytes.Equal(got, append([]byte("prefix"), want...)) {
		t.Fatalf("got %s", got)
	}
}

// invalidUTF8Cases are invalid-UTF-8 strings with their documented
// encoding (docs/schema.md, "NDJSON framing"): each invalid byte as the
// six-byte escape `\ufffd`, a valid U+FFFD as its raw bytes. The expected
// bytes are fixed here, not taken from json.Marshal, whose choice changed
// in Go 1.27 (task 8m4).
var invalidUTF8Cases = []struct{ in, want string }{
	{"\xff", `"\ufffd"`},
	{"ab\xe2\x82", `"ab\ufffd\ufffd"`},
	{"mixed é<\x02>\xc3", `"mixed é\u003c\u0002\u003e\ufffd"`},
	{"abcdefghijklmnop\xff", `"abcdefghijklmnop\ufffd"`},
	{"\xed\xa0\x80", `"\ufffd\ufffd\ufffd"`}, // a UTF-16 surrogate
	{"\xc0\xaf", `"\ufffd\ufffd"`},           // an overlong encoding
	{"\ufffd\xff", "\"\ufffd\\ufffd\""},      // valid U+FFFD stays raw
}

// TestAppendJSONStringInvalidUTF8 holds the encoder to the documented
// bytes for invalid UTF-8, whichever Go is installed.
func TestAppendJSONStringInvalidUTF8(t *testing.T) {
	for _, c := range invalidUTF8Cases {
		if got := string(appendJSONString(nil, c.in)); got != c.want {
			t.Errorf("appendJSONString(%q) = %s, want %s", c.in, got, c.want)
		}
		if got := string(marshalReference(t, Event{Comm: c.in})); !strings.Contains(got, `"comm":`+c.want) {
			t.Errorf("marshalReference(%q) = %s, want comm %s", c.in, got, c.want)
		}
	}
}

// TestInvalidUTF8RoundTrip: the installed Go's json.Unmarshal reads the
// encoder's `\ufffd` back to the same string it reads from json.Marshal's
// own output (U+FFFD per invalid byte), so a reader on any Go version
// sees what the encoder meant.
func TestInvalidUTF8RoundTrip(t *testing.T) {
	for _, c := range invalidUTF8Cases {
		var fromEncoder, fromMarshal string
		if err := json.Unmarshal(appendJSONString(nil, c.in), &fromEncoder); err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		std, err := json.Marshal(c.in)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(std, &fromMarshal); err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if fromEncoder != fromMarshal {
			t.Errorf("%q: encoder decodes to %q, json.Marshal's output to %q", c.in, fromEncoder, fromMarshal)
		}
		if want := strings.ReplaceAll(replaceInvalid(c.in), invalidMark, "\ufffd"); fromEncoder != want {
			t.Errorf("%q: decodes to %q, want %q", c.in, fromEncoder, want)
		}
	}
}

// FuzzAppendEventStrings feeds arbitrary bytes through every escaping
// path: an envelope string, an optional args string and an argv element.
func FuzzAppendEventStrings(f *testing.F) {
	for _, s := range awkwardStrings {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if strings.Contains(s, invalidMark) {
			t.Skip("contains marshalReference's stand-in rune")
		}
		ev := sampleEvent(1)
		ev.Comm, ev.Args.Path, ev.Args.Argv = s, &s, []string{s, s}
		assertSameAsMarshal(t, ev)
	})
}

// TestSafeWordMatchesTable holds the 8-byte test of appendJSONString's
// fast path to the byte table: every byte value in every lane of an
// otherwise safe word, and random words.
func TestSafeWordMatchesTable(t *testing.T) {
	byTable := func(w uint64) bool {
		for k := range 8 {
			b := byte(w >> (8 * k))
			if b >= utf8.RuneSelf || !jsonSafe(b) {
				return false
			}
		}
		return true
	}
	const safe = 0x6162636465666768 // "hgfedcba"
	for lane := range 8 {
		for b := range 256 {
			w := safe&^(0xff<<(8*lane)) | uint64(b)<<(8*lane)
			if safeWord(w) != byTable(w) {
				t.Fatalf("lane %d byte %#x: safeWord %v", lane, b, safeWord(w))
			}
		}
	}
	r := rand.New(rand.NewPCG(8, 8))
	for range 1_000_000 {
		w := r.Uint64() & 0x7f7f7f7f7f7f7f7f // mostly ASCII, so both answers occur
		if r.IntN(4) == 0 {
			w = r.Uint64()
		}
		if safeWord(w) != byTable(w) {
			t.Fatalf("word %#x: safeWord %v", w, safeWord(w))
		}
	}
}

// BenchmarkAppendJSONString is the escaper on an event's typical strings.
func BenchmarkAppendJSONString(b *testing.B) {
	ss := []string{"bench", "0123456789abcdef0123456789abcdef", "java", "write", "none",
		"syscall-burst-0"}
	buf := make([]byte, 0, 256)
	for b.Loop() {
		buf = buf[:0]
		for _, s := range ss {
			buf = appendJSONString(buf, s)
		}
	}
}
