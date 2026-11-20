package query

import (
	"fmt"
	"io"
	"math"
	"strconv"

	"iormesh/internal/schema"
)

// truncatedReserve is the longest truncation record: both counts at
// twenty digits. Every line is placed so that this much room is left
// whenever another line could still be cut, so the record always fits
// inside MaxBytes.
var truncatedReserve = len(truncatedLine(math.MaxUint64, math.MaxUint64))

// MinMaxBytes is the smallest cap Run accepts: room for the truncation
// record alone.
func MinMaxBytes() int64 { return int64(truncatedReserve) }

// cappedWriter writes whole lines while they fit in max bytes, the last
// of them a truncation record if anything was cut. A line that would
// fit only by eating into the record's reserve is held back (pending)
// until the next line shows whether the result goes on: at the end it
// is written, on a cut it is dropped and counted as remaining. The
// output is always a prefix of the matching events, never a selection
// of the small ones.
type cappedWriter struct {
	w       io.Writer
	max     int64
	written int64
	pending []byte
	// pendingN counts the lines in pending.
	pendingN int
	cut      bool
	stats    Stats
}

// newCappedWriter caps output to w at max bytes (≥ MinMaxBytes).
func newCappedWriter(w io.Writer, max int64) *cappedWriter {
	return &cappedWriter{w: w, max: max}
}

// line offers one complete NDJSON line (newline included).
func (c *cappedWriter) line(b []byte) error {
	if c.cut {
		c.stats.Remaining++
		return nil
	}
	size := int64(len(b))
	held := int64(len(c.pending))
	switch {
	case c.written+held+size+int64(truncatedReserve) <= c.max:
		if err := c.flushPending(); err != nil {
			return err
		}
		return c.write(b, 1)
	case c.written+held+size <= c.max:
		c.pending = append(c.pending, b...)
		c.pendingN++
		return nil
	}
	// Does not fit: what is pending would leave no room for the record.
	c.cut = true
	c.stats.Remaining += uint64(c.pendingN) + 1
	c.pending, c.pendingN = nil, 0
	return nil
}

// close writes what is pending, or the truncation record after a cut.
func (c *cappedWriter) close() (Stats, error) {
	if !c.cut {
		return c.stats, c.flushPending()
	}
	c.stats.Truncated = true
	_, err := io.WriteString(c.w, truncatedLine(c.stats.Emitted, c.stats.Remaining))
	return c.stats, err
}

// flushPending writes the held-back lines.
func (c *cappedWriter) flushPending() error {
	if c.pendingN == 0 {
		return nil
	}
	err := c.write(c.pending, c.pendingN)
	c.pending, c.pendingN = c.pending[:0], 0
	return err
}

// write writes b, holding n lines.
func (c *cappedWriter) write(b []byte, n int) error {
	if _, err := c.w.Write(b); err != nil {
		return fmt.Errorf("query: %w", err)
	}
	c.written += int64(len(b))
	c.stats.Emitted += uint64(n)
	return nil
}

// truncatedLine is the truncation record: the schema version, the
// synthetic kind and the counts, as one NDJSON line.
func truncatedLine(emitted, remaining uint64) string {
	return `{"schema_version":` + strconv.FormatUint(uint64(schema.Version), 10) +
		`,"event":"truncated","args":{"emitted":` +
		strconv.FormatUint(emitted, 10) + `,"remaining":` + strconv.FormatUint(remaining, 10) + "}}\n"
}
