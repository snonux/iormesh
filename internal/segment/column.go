package segment

import (
	"bytes"
	"math/bits"

	"github.com/parquet-go/parquet-go/encoding/delta"
	"github.com/parquet-go/parquet-go/encoding/plain"
	"github.com/parquet-go/parquet-go/encoding/rle"
	"github.com/parquet-go/parquet-go/format"
)

// colKind is how a column's values are buffered and encoded.
type colKind uint8

const (
	kindInt32   colKind = iota // PLAIN int32 (also uint16/uint32 bit patterns)
	kindInt64                  // PLAIN int64 (also uint64 bit patterns)
	kindBool                   // PLAIN booleans, bit-packed at encode
	kindBytes                  // PLAIN byte arrays
	kindDict                   // dictionary-encoded byte arrays (the dict tag)
	kindDelta64                // DELTA_BINARY_PACKED int64 (see columnSpec)
)

// physical is the parquet physical type a kind is stored as.
func (k colKind) physical() format.Type {
	switch k {
	case kindInt32:
		return format.Int32
	case kindInt64, kindDelta64:
		return format.Int64
	case kindBool:
		return format.Boolean
	default:
		return format.ByteArray
	}
}

// column buffers one leaf column of the open row group (task ee4). The
// row path only appends to typed slices — no parquet.Value, no
// reflection — and the page is encoded once per row group (encode). For
// an optional or repeated leaf every slot (a value or a null) gets a
// definition level and, for a list, a repetition level; values are kept
// for the non-null slots only, as parquet stores them.
type column struct {
	path           []string
	kind           colKind
	maxDef, maxRep byte
	def, rep       []byte
	i32            []int32 // kindInt32 values; kindDict indices
	i64            []int64 // kindInt64 and kindDelta64 values
	bools          []bool
	plain          []byte // kindBytes values, PLAIN-encoded as they come
	dict           dictionary
}

// dictionary maps a dict column's strings to their index in the row
// group's dictionary page. The last hit is cached: on the hot path the
// same comm, kind, project and host repeat event after event, and a
// string compare is cheaper than a map lookup.
type dictionary struct {
	idx     map[string]int32
	values  []byte // the dictionary page, PLAIN-encoded
	n       int32
	last    string
	lastIdx int32
}

// The row-path appenders, one per shape. A set optional value is at
// maxDef (the leaf is the deepest optional level); nullDef is the level
// a null stops at — how far its parents were set. They only append: the
// slot and null counts the page header needs are derived from the
// levels at encode, once per row group, not counted per row — most of a
// syscall event's ~70 columns are nulls, so the null path is the hot
// one.

func (c *column) putInt32(v int32) { c.i32 = append(c.i32, v) }

func (c *column) putInt64(v int64) { c.i64 = append(c.i64, v) }

func (c *column) putString(s string) { c.plain = plain.AppendByteArrayString(c.plain, s) }

func (c *column) putDict(s string) { c.i32 = append(c.i32, c.dict.index(s)) }

// null records an absent value at level nullDef in a non-repeated
// column; nullList is the same for a list column.
func (c *column) null(nullDef byte) { c.def = append(c.def, nullDef) }

func (c *column) nullList(nullDef byte) {
	c.def = append(c.def, nullDef)
	c.rep = append(c.rep, 0)
}

// present records the level of a set optional value; the caller appends
// the value itself.
func (c *column) present() { c.def = append(c.def, c.maxDef) }

func (c *column) optInt32(v int32, set bool, nullDef byte) {
	if !set {
		c.null(nullDef)
		return
	}
	c.present()
	c.i32 = append(c.i32, v)
}

func (c *column) optInt64(v int64, set bool, nullDef byte) {
	if !set {
		c.null(nullDef)
		return
	}
	c.present()
	c.i64 = append(c.i64, v)
}

func (c *column) optString(s string, set bool, nullDef byte) {
	if !set {
		c.null(nullDef)
		return
	}
	c.present()
	c.plain = plain.AppendByteArrayString(c.plain, s)
}

func (c *column) optBool(v bool, set bool, nullDef byte) {
	if !set {
		c.null(nullDef)
		return
	}
	c.present()
	c.bools = append(c.bools, v)
}

// listStrings writes a LIST of required strings: an empty (or absent)
// list is one null at nullDef, otherwise one slot per element, the first
// starting the row (repetition level 0), the rest continuing it.
func (c *column) listStrings(vs []string, nullDef byte) {
	if len(vs) == 0 {
		c.nullList(nullDef)
		return
	}
	for i, v := range vs {
		c.listSlot(i)
		c.plain = plain.AppendByteArrayString(c.plain, v)
	}
}

// listInt64s is listStrings for a LIST of uint64 (the Java stack).
func (c *column) listInt64s(vs []uint64, nullDef byte) {
	if len(vs) == 0 {
		c.nullList(nullDef)
		return
	}
	for i, v := range vs {
		c.listSlot(i)
		c.i64 = append(c.i64, int64(v))
	}
}

func (c *column) listSlot(i int) {
	rep := c.maxRep
	if i == 0 {
		rep = 0
	}
	c.rep = append(c.rep, rep)
	c.def = append(c.def, c.maxDef)
}

// index returns s's dictionary index, adding it on first sight.
func (d *dictionary) index(s string) int32 {
	if d.n > 0 && s == d.last {
		return d.lastIdx
	}
	i, ok := d.idx[s]
	if !ok {
		i = d.n
		d.idx[s] = i
		d.values = plain.AppendByteArrayString(d.values, s)
		d.n++
	}
	d.last, d.lastIdx = s, i
	return i
}

// reset empties the column for the next row group, keeping its buffers.
// A dictionary is per column chunk, so it restarts too.
func (c *column) reset() {
	c.def, c.rep = c.def[:0], c.rep[:0]
	c.i32, c.i64, c.bools, c.plain = c.i32[:0], c.i64[:0], c.bools[:0], c.plain[:0]
	if c.kind == kindDict {
		clear(c.dict.idx)
		c.dict.values, c.dict.n, c.dict.last, c.dict.lastIdx = c.dict.values[:0], 0, "", 0
	}
}

// bufferedBytes estimates the column's unencoded size, for the size cap.
func (c *column) bufferedBytes() int {
	return len(c.def) + len(c.rep) + 4*len(c.i32) + 8*len(c.i64) + len(c.bools) +
		len(c.plain) + len(c.dict.values)
}

// encodedPage is one column chunk's pages before compression: the
// levels (data page v2 keeps them uncompressed) and the values.
type encodedPage struct {
	slots    int // values and nulls: the levels, or the rows of a required column
	nulls    int
	rep, def []byte
	values   []byte
	dict     []byte // PLAIN dictionary page, kindDict only
	encoding format.Encoding
}

// encode renders the buffered row group of the column (numRows rows)
// into p, reusing p's buffers.
func (c *column) encode(p *encodedPage, numRows int) {
	p.rep, p.def, p.dict = p.rep[:0], p.def[:0], p.dict[:0]
	p.slots, p.nulls = numRows, 0
	if c.maxDef > 0 {
		p.slots = len(c.def)
		p.nulls = p.slots - bytes.Count(c.def, []byte{c.maxDef})
	}
	if c.maxRep > 0 {
		p.rep = encodeLevels(p.rep, c.rep, c.maxRep)
	}
	if c.maxDef > 0 {
		p.def = encodeLevels(p.def, c.def, c.maxDef)
	}
	p.encoding = format.Plain
	var enc plain.Encoding
	switch c.kind {
	case kindInt32:
		p.values, _ = enc.EncodeInt32(p.values, c.i32)
	case kindInt64:
		p.values, _ = enc.EncodeInt64(p.values, c.i64)
	case kindDelta64:
		var delta delta.BinaryPackedEncoding
		p.values, _ = delta.EncodeInt64(p.values, c.i64)
		p.encoding = format.DeltaBinaryPacked
	case kindBool:
		p.values = packBools(p.values[:0], c.bools)
	case kindBytes:
		p.values = append(p.values[:0], c.plain...)
	case kindDict:
		var dict rle.DictionaryEncoding
		p.values, _ = dict.EncodeInt32(p.values, c.i32)
		p.dict = append(p.dict, c.dict.values...)
		p.encoding = format.RLEDictionary
	}
}

// encodeLevels is the RLE/bit-packed hybrid of data page v2's levels
// (no length prefix: the header carries the byte lengths). The encoders
// cannot fail on levels within the bit width, which max guarantees.
func encodeLevels(dst, levels []byte, maxLevel byte) []byte {
	enc := rle.Encoding{BitWidth: bits.Len8(maxLevel)}
	out, _ := enc.EncodeLevels(dst, levels)
	return out
}

// packBools is PLAIN booleans: one bit each, least significant first.
func packBools(dst []byte, vs []bool) []byte {
	for i := 0; i < len(vs); i += 8 {
		var b byte
		for j := 0; j < 8 && i+j < len(vs); j++ {
			if vs[i+j] {
				b |= 1 << j
			}
		}
		dst = append(dst, b)
	}
	return dst
}
