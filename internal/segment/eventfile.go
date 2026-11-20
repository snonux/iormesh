package segment

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	parquetgo "github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/encoding/thrift"
	"github.com/parquet-go/parquet-go/format"

	"iormesh/internal/schema"
)

// eventRowsPerRowGroup is the events file's row-group length, the
// tables' 8192. Measured on a real -spool burst (task ee4): 4096 rows
// pay the ~70 pages' fixed cost (zstd frame and tables, page headers)
// twice as often, while 16384 and 32768 rows save instructions but
// spend more cycles — the open row group's columns (~1 MiB at 8192)
// then outgrow the core's L2 cache.
const eventRowsPerRowGroup = maxRowsPerRowGroup

// eventRowGroupBytes cuts a row group early when its buffered columns
// pass this size: each column chunk is written as a single page, and the
// open row group's buffers are the writer's memory (a few MiB, far
// inside the tracer unit's 1 GiB MemoryMax) even when events carry long
// paths or argv.
const eventRowGroupBytes = 4 << 20

// parquetMagic opens and closes every parquet file.
const parquetMagic = "PAR1"

// eventStream is the active segment's events file, written by the
// segment package's own columnar encoder (task ee4) instead of
// parquet-go's GenericWriter, whose per-row reflection over the ~70 leaf
// columns cost the tracer ~3× the NDJSON sink per event. The format is
// plain parquet any reader takes: per row group and column one optional
// dictionary page and one data page v2 (levels RLE, values PLAIN,
// DELTA_BINARY_PACKED or RLE_DICTIONARY, zstd), then a footer whose schema, column orders and
// created_by are parquet-go's own for schema.Event (footerTemplate).
// Not written: page statistics beyond null counts, column and offset
// indexes, bloom filters — a segment is read whole by the controller's
// merge, never filtered page by page.
type eventStream struct {
	path   string
	f      *os.File
	bw     *bufio.Writer
	off    int64 // bytes written so far, the next page's offset
	codec  *zstdCodec
	cols   *eventColumns
	tmpl   *footerTemplate
	groups []format.RowGroup
	page   encodedPage
	zbuf   []byte
	rows   int64
}

// footerTemplate is what an events file's footer shares with every
// other: parquet-go's schema elements and column orders for
// schema.Event and its created_by, taken from an empty file it wrote,
// so this writer's files describe themselves exactly as the library's.
type footerTemplate struct {
	schema    []format.SchemaElement
	orders    []format.ColumnOrder
	createdBy string
}

// newFooterTemplate writes an empty events file with parquet-go to
// memory and keeps its footer's schema part.
func newFooterTemplate() (*footerTemplate, error) {
	var buf bytes.Buffer
	pw := parquetgo.NewGenericWriter[schema.Event](&buf, parquetgo.CreatedBy("iormesh", "", ""))
	if err := pw.Close(); err != nil {
		return nil, fmt.Errorf("segment: footer template: %w", err)
	}
	pf, err := parquetgo.OpenFile(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		return nil, fmt.Errorf("segment: footer template: %w", err)
	}
	md := pf.Metadata()
	return &footerTemplate{schema: md.Schema, orders: md.ColumnOrders, createdBy: md.CreatedBy}, nil
}

// openEventStream creates path for a segment's events (see openStream
// for O_EXCL and the mode).
func openEventStream(path string, codec *zstdCodec, tmpl *footerTemplate) (*eventStream, error) {
	cols, err := newEventColumns()
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, fmt.Errorf("segment: %w", err)
	}
	if err := f.Chmod(0o644); err != nil {
		return nil, errors.Join(fmt.Errorf("segment: %w", err), f.Close(), os.Remove(path))
	}
	s := &eventStream{path: path, f: f, bw: bufio.NewWriterSize(f, pageBufferSize),
		codec: codec, cols: cols, tmpl: tmpl}
	if err := s.write([]byte(parquetMagic)); err != nil {
		return nil, errors.Join(err, s.abort())
	}
	return s, nil
}

// add appends one event; a full row group is encoded and written.
func (s *eventStream) add(ev *schema.Event) error {
	s.cols.add(ev)
	s.rows++
	// The byte cap is looked at every 64 rows only: summing ~70 columns'
	// buffers per event cost more than the event's own encoding, and 64
	// events overshoot a 4 MiB cap by little.
	if s.cols.rows < eventRowsPerRowGroup &&
		(s.cols.rows%64 != 0 || s.cols.bufferedBytes() < eventRowGroupBytes) {
		return nil
	}
	return s.flushRowGroup()
}

// size is the file so far plus the open row group unencoded: an upper
// bound of what publishing now would write, for the size cap.
func (s *eventStream) size() int64 { return s.off + int64(s.cols.bufferedBytes()) }

func (s *eventStream) write(b []byte) error {
	n, err := s.bw.Write(b)
	s.off += int64(n)
	if err != nil {
		return fmt.Errorf("segment: write %s: %w", filepath.Base(s.path), err)
	}
	return nil
}

// flushRowGroup writes the open row group's column chunks and records
// its metadata for the footer.
func (s *eventStream) flushRowGroup() error {
	if s.cols.rows == 0 {
		return nil
	}
	// Ordinal is an int16 in the format. A segment rotates every minute
	// or 64 MiB, which at 8192 rows per group needs a sustained 4.4 M
	// events a second — a hundred times decision #25's — to reach 32767
	// groups; past that the ordinal (informational) would wrap, never
	// the data.
	start := s.off
	rg := format.RowGroup{NumRows: int64(s.cols.rows), FileOffset: start,
		Ordinal: int16(len(s.groups)), Columns: make([]format.ColumnChunk, 0, numEventColumns)}
	for i := range s.cols.cols {
		cc, err := s.writeChunk(&s.cols.cols[i], int64(s.cols.rows))
		if err != nil {
			return err
		}
		rg.TotalByteSize += cc.MetaData.TotalUncompressedSize
		rg.Columns = append(rg.Columns, cc)
	}
	rg.TotalCompressedSize = s.off - start
	s.groups = append(s.groups, rg)
	s.cols.reset()
	return nil
}

// writeChunk writes one column's chunk: the dictionary page if it has
// one, then its data page.
func (s *eventStream) writeChunk(c *column, numRows int64) (format.ColumnChunk, error) {
	c.encode(&s.page, int(numRows))
	p := &s.page
	md := format.ColumnMetaData{Type: c.kind.physical(), PathInSchema: c.path,
		Codec: format.Zstd, NumValues: int64(p.slots), Encoding: chunkEncodings(c, p.encoding)}
	md.Statistics.NullCount = int64(p.nulls)
	if c.kind == kindDict {
		md.DictionaryPageOffset = s.off
		hdr := format.PageHeader{Type: format.DictionaryPage}
		hdr.DictionaryPageHeader.Valid = true
		hdr.DictionaryPageHeader.V = format.DictionaryPageHeader{NumValues: c.dict.n, Encoding: format.Plain}
		if err := s.writePage(&md, &hdr, nil, p.dict); err != nil {
			return format.ColumnChunk{}, err
		}
	}
	md.DataPageOffset = s.off
	hdr := format.PageHeader{Type: format.DataPageV2}
	hdr.DataPageHeaderV2.Valid = true
	hdr.DataPageHeaderV2.V = format.DataPageHeaderV2{NumValues: int32(p.slots),
		NumNulls: int32(p.nulls), NumRows: int32(numRows), Encoding: p.encoding,
		DefinitionLevelsByteLength: int32(len(p.def)),
		RepetitionLevelsByteLength: int32(len(p.rep)),
		Statistics:                 format.Statistics{NullCount: int64(p.nulls)}}
	// v2 stores repetition levels, then definition levels, then values.
	levels := append(p.rep, p.def...)
	if err := s.writePage(&md, &hdr, levels, p.values); err != nil {
		return format.ColumnChunk{}, err
	}
	first := md.DataPageOffset
	if c.kind == kindDict {
		first = md.DictionaryPageOffset
	}
	return format.ColumnChunk{FileOffset: first, MetaData: md}, nil
}

// writePage writes a page header and body; the values are compressed,
// the levels (data page v2) are not. It adds the page to md's sizes.
// The header's sizes and, for a data page, its is_compressed flag are
// filled in here.
func (s *eventStream) writePage(md *format.ColumnMetaData, hdr *format.PageHeader, levels, values []byte) error {
	body, err := s.codec.Encode(s.zbuf, values)
	if err != nil {
		return fmt.Errorf("segment: compress %s: %w", strings.Join(md.PathInSchema, "."), err)
	}
	s.zbuf = body
	hdr.UncompressedPageSize = int32(len(levels) + len(values))
	hdr.CompressedPageSize = int32(len(levels) + len(body))
	if hdr.DataPageHeaderV2.Valid {
		hdr.DataPageHeaderV2.V.IsCompressed = thrift.New(true)
	}
	h, err := thrift.Marshal(new(thrift.CompactProtocol), hdr)
	if err != nil {
		return fmt.Errorf("segment: page header: %w", err)
	}
	md.TotalUncompressedSize += int64(len(h)) + int64(hdr.UncompressedPageSize)
	md.TotalCompressedSize += int64(len(h)) + int64(hdr.CompressedPageSize)
	return errors.Join(s.write(h), s.write(levels), s.write(body))
}

// chunkEncodings lists a chunk's encodings, sorted as parquet-go lists
// them: the values' (PLAIN, DELTA_BINARY_PACKED or RLE_DICTIONARY), PLAIN
// for a dictionary page, RLE for levels.
func chunkEncodings(c *column, values format.Encoding) []format.Encoding {
	encs := []format.Encoding{values}
	if c.kind == kindDict {
		encs = append(encs, format.Plain)
	}
	if c.maxDef > 0 || c.maxRep > 0 {
		encs = append(encs, format.RLE)
	}
	slices.Sort(encs)
	return encs
}

// publish writes the last row group and the footer with meta, then
// syncs, closes and renames the file as stream.publish does.
func (s *eventStream) publish(readyPath string, meta map[string]string) error {
	if err := errors.Join(s.flushRowGroup(), s.writeFooter(meta), s.bw.Flush()); err != nil {
		return errors.Join(err, s.abort())
	}
	if err := s.f.Sync(); err != nil {
		return errors.Join(fmt.Errorf("segment: sync %s: %w", filepath.Base(s.path), err), s.abort())
	}
	if err := s.f.Close(); err != nil {
		return errors.Join(fmt.Errorf("segment: close %s: %w", filepath.Base(s.path), err),
			os.Remove(s.path))
	}
	if err := os.Rename(s.path, readyPath); err != nil {
		return fmt.Errorf("segment: publish: %w", err)
	}
	return nil
}

// writeFooter writes the file metadata, its length and the closing
// magic. Key-value pairs are sorted by key, so a file's bytes do not
// depend on map order.
func (s *eventStream) writeFooter(meta map[string]string) error {
	kv := make([]format.KeyValue, 0, len(meta))
	for k, v := range meta {
		kv = append(kv, format.KeyValue{Key: k, Value: v})
	}
	slices.SortFunc(kv, func(a, b format.KeyValue) int { return strings.Compare(a.Key, b.Key) })
	md := format.FileMetaData{Version: 2, Schema: s.tmpl.schema, NumRows: s.rows,
		RowGroups: s.groups, KeyValueMetadata: kv, CreatedBy: s.tmpl.createdBy,
		ColumnOrders: s.tmpl.orders}
	b, err := thrift.Marshal(new(thrift.CompactProtocol), &md)
	if err != nil {
		return fmt.Errorf("segment: footer: %w", err)
	}
	b = binary.LittleEndian.AppendUint32(b, uint32(len(b)))
	return s.write(append(b, parquetMagic...))
}

// abort closes and removes the active file (see stream.abort).
func (s *eventStream) abort() error {
	err := s.f.Close()
	if errors.Is(err, os.ErrClosed) {
		err = nil
	}
	rmErr := os.Remove(s.path)
	if errors.Is(rmErr, os.ErrNotExist) {
		rmErr = nil
	}
	return errors.Join(err, rmErr)
}
