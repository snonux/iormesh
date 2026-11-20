package servicedb

import (
	"math"
	"testing"

	"iormesh/internal/schema"
)

// TestOverflowingEndSaturates: an event whose wall_ns + latency_ns
// overflows (only a forged trace has one) ends at int64's maximum, a
// window Import accepts, instead of wrapping around into one it rejects
// and failing the whole build.
func TestOverflowingEndSaturates(t *testing.T) {
	for _, tc := range []struct{ wall, lat, want uint64 }{
		{5, 7, 12},
		{5, math.MaxUint64 - 2, math.MaxInt64},
		{math.MaxInt64 + 1, 0, math.MaxInt64},
	} {
		end := endOf(&schema.Event{WallNS: tc.wall, LatencyNS: tc.lat})
		if end != tc.want || validWindow(min(tc.wall, end), end) != nil {
			t.Errorf("endOf(%d, %d) = %d", tc.wall, tc.lat, end)
		}
	}
}
