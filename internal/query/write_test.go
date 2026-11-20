package query

import (
	"bytes"
	"strings"
	"testing"
)

// TestCappedWriter: lines that fit only in the record's reserve are held
// back and written when nothing follows, dropped when a cut does; the
// output never passes the cap and is always a prefix.
func TestCappedWriter(t *testing.T) {
	line := "1234567\n" // 8 bytes
	reserve := int64(truncatedReserve)
	for _, tc := range []struct {
		name      string
		max       int64
		lines     int
		wantLines int
		truncated bool
	}{
		{"all fit outside the reserve", reserve + 64, 4, 4, false},
		{"last lines held, then written", reserve + 20, 4, 4, false},
		{"held lines dropped on the cut", reserve + 20, 20, 2, true},
		{"one line fits whole", reserve, 1, 1, false},
		{"nothing fits beside the record", reserve, 20, 0, true},
		{"no lines", reserve, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			c := newCappedWriter(&buf, tc.max)
			for range tc.lines {
				if err := c.line([]byte(line)); err != nil {
					t.Fatal(err)
				}
			}
			st, err := c.close()
			if err != nil {
				t.Fatal(err)
			}
			if int64(buf.Len()) > tc.max {
				t.Errorf("%d bytes over the cap %d", buf.Len(), tc.max)
			}
			if got := strings.Count(buf.String(), line); got != tc.wantLines || st.Emitted != uint64(got) {
				t.Errorf("%d lines (stats %+v), want %d", got, st, tc.wantLines)
			}
			if st.Truncated != tc.truncated || st.Emitted+st.Remaining != uint64(tc.lines) {
				t.Errorf("stats %+v, want truncated=%v over %d lines", st, tc.truncated, tc.lines)
			}
		})
	}
}
