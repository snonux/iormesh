package schema

import (
	"math"
	"math/rand/v2"
	"strconv"
	"testing"
)

// uintCases are the values appendUint must get right: every power of ten
// and its neighbours (the digit-count and group boundaries), the extremes,
// and a few typical event fields.
func uintCases() []uint64 {
	vs := []uint64{0, 1, 9, 10, 99, 100, 101, math.MaxUint32, math.MaxUint64,
		math.MaxUint64 - 1, 1_763_500_000_123_456_789, 9_876_543_210_123, 99_999_999,
		9_999_999_999_999_999_999, 18_446_744_069_999_999_999}
	for p := uint64(1); p <= 1e19; p *= 10 {
		vs = append(vs, p-1, p, p+1, 2*p-1, 5*p)
		if p == 1e19 {
			break
		}
	}
	return vs
}

// TestAppendUintMatchesStrconv holds appendUint and appendInt to strconv
// byte for byte: on the boundary cases, on random values of every digit
// count, and on every value below 2^20 (the fixed-point digit pairs'
// small range, where a rounding slip would show first).
func TestAppendUintMatchesStrconv(t *testing.T) {
	check := func(v uint64) {
		if got, want := string(appendUint(nil, v)), strconv.FormatUint(v, 10); got != want {
			t.Fatalf("appendUint(%d) = %q, want %q", v, got, want)
		}
		i := int64(v)
		if got, want := string(appendInt([]byte("x"), i)), "x"+strconv.FormatInt(i, 10); got != want {
			t.Fatalf("appendInt(%d) = %q, want %q", i, got, want)
		}
	}
	for _, v := range uintCases() {
		check(v)
	}
	for v := range uint64(1 << 20) {
		check(v)
	}
	r := rand.New(rand.NewPCG(1, 2))
	for range 1_000_000 {
		v := r.Uint64()
		check(v >> r.UintN(64)) // every magnitude, not mostly 20-digit ones
	}
	check(uint64(math.MaxInt64))
	check(1 << 63) // appendInt: MinInt64
}

// TestEightDigitsExhaustiveGroups checks eightDigits on every multiple
// of 9_973 below 1e8 and on each value ending a run of nines, where the
// fixed point's rounding up would carry into the wrong digit if it were
// too coarse.
func TestEightDigitsExhaustiveGroups(t *testing.T) {
	var b [8]byte
	for n := uint32(0); n < 1e8; n += 9_973 {
		for _, v := range []uint32{n, min(n+99, 1e8-1), min(n+9_999, 1e8-1)} {
			eightDigits(&b, v)
			if got, want := string(b[:]), strconv.FormatUint(uint64(v)+1e8, 10)[1:]; got != want {
				t.Fatalf("eightDigits(%d) = %q, want %q", v, got, want)
			}
		}
	}
}

// BenchmarkAppendUint and BenchmarkStrconvAppendUint compare the encoder's
// integer formatting with strconv's on the event fields' typical sizes.
func BenchmarkAppendUint(b *testing.B) {
	benchUint(b, appendUint)
}

func BenchmarkStrconvAppendUint(b *testing.B) {
	benchUint(b, func(dst []byte, v uint64) []byte { return strconv.AppendUint(dst, v, 10) })
}

func benchUint(b *testing.B, f func([]byte, uint64) []byte) {
	ev := burstEvent()
	vs := []uint64{ev.Seq, ev.BootNS, ev.WallNS, uint64(ev.PID), ev.PIDInc, uint64(ev.TID),
		uint64(ev.FD), uint64(ev.FDGen), uint64(ev.Ret), ev.LatencyNS, ev.Bytes}
	buf := make([]byte, 0, 256)
	for b.Loop() {
		buf = buf[:0]
		for _, v := range vs {
			buf = f(buf, v)
		}
	}
}
