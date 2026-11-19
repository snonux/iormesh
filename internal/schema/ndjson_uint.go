package schema

import "math/bits"

// digitPairs holds "00" to "99": one load writes two digits.
const digitPairs = "00010203040506070809" +
	"10111213141516171819" +
	"20212223242526272829" +
	"30313233343536373839" +
	"40414243444546474849" +
	"50515253545556575859" +
	"60616263646566676869" +
	"70717273747576777879" +
	"80818283848586878889" +
	"90919293949596979899"

// pairTens and pairOnes split digitPairs by digit, sized 128 so that an
// index known to be below 128 (a 64-bit value shifted right by 57) needs
// no bounds check.
var pairTens, pairOnes = func() (tens, ones [128]byte) {
	for i := range 100 {
		tens[i], ones[i] = digitPairs[2*i], digitPairs[2*i+1]
	}
	return tens, ones
}()

// eightDigits writes n (< 1e8) as exactly eight digits, leading zeros
// included. y holds n / 1e6 as a fixed-point number with 57 fraction
// bits; its integer part is the leading digit pair, and each ×100 of the
// fraction moves the next pair into the integer part — four
// multiplications instead of strconv's four divisions by 100. The
// multiplier rounds 2^57/1e6 up, so y never falls short of the exact
// value and overshoots it by under n < 1e8 units of 2^-57 — far less
// than the 1e-6 a step of the fraction needs to change a digit, so every
// floor is exact (TestAppendUintMatchesStrconv checks the boundaries).
func eightDigits(b *[8]byte, n uint32) {
	const frac = 57
	const mask = 1<<frac - 1
	y := uint64(n) * 0x218def416c // ⌈2^57 / 1e6⌉
	b[0], b[1] = pairTens[y>>frac], pairOnes[y>>frac]
	y = (y & mask) * 100
	b[2], b[3] = pairTens[y>>frac], pairOnes[y>>frac]
	y = (y & mask) * 100
	b[4], b[5] = pairTens[y>>frac], pairOnes[y>>frac]
	y = (y & mask) * 100
	b[6], b[7] = pairTens[y>>frac], pairOnes[y>>frac]
}

// appendUint is strconv.AppendUint(dst, v, 10), byte for byte, at about
// twice its speed on the 19-digit boot_ns and wall_ns every event carries
// (task ud4: integer formatting was a third of AppendEvent, decision
// #25's budget). v splits into up to three groups of eight digits,
// written right-aligned into one buffer; the number's own digits are
// then appended in one copy, the leading group's zeros left behind.
func appendUint(dst []byte, v uint64) []byte {
	if v < 10 {
		return append(dst, byte('0'+v))
	}
	if v < 100 {
		return append(dst, pairTens[v], pairOnes[v])
	}
	var buf [24]byte
	lo := (*[8]byte)(buf[16:])
	if v < 1e8 {
		eightDigits(lo, uint32(v))
	} else if q := v / 1e8; q < 1e8 {
		eightDigits(lo, uint32(v-q*1e8))
		eightDigits((*[8]byte)(buf[8:]), uint32(q))
	} else {
		eightDigits(lo, uint32(v-q*1e8))
		eightDigits((*[8]byte)(buf[8:]), uint32(q%1e8))
		eightDigits((*[8]byte)(buf[:]), uint32(q/1e8)) // ≤ 1844
	}
	return append(dst, buf[24-decimalLen(v):]...)
}

// appendInt is strconv.AppendInt(dst, v, 10).
func appendInt(dst []byte, v int64) []byte {
	if v < 0 {
		// -v overflows for MinInt64, but its uint64 is still the magnitude.
		return appendUint(append(dst, '-'), uint64(-v))
	}
	return appendUint(dst, uint64(v))
}

// powersOf10 are the thresholds of decimalLen.
var powersOf10 = [...]uint64{1, 10, 100, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9, 1e10,
	1e11, 1e12, 1e13, 1e14, 1e15, 1e16, 1e17, 1e18, 1e19}

// decimalLen is the number of decimal digits of v (1 for 0): the bit
// length times log10(2) (1233/4096) gives it or one less, and one
// comparison settles which.
func decimalLen(v uint64) int {
	n := (bits.Len64(v) * 1233) >> 12
	if v >= powersOf10[n] {
		n++
	}
	return max(n, 1)
}
