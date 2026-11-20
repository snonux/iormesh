package segment

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math/rand/v2"
	"os"
	"testing"

	parquetgo "github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/encoding/thrift"
	"github.com/parquet-go/parquet-go/format"

	"iormesh/internal/schema"
)

// TestEventFileMetadata checks the metadata the encoder writes by hand
// against the file itself (task ee4): parquet-go's reader ignores most
// of it, other readers act on it (TestIndependentReaders). Per column
// chunk, the pages laid end to end from its first page fill exactly its
// compressed size, their headers and bodies sum to its uncompressed
// size, and the data pages' num_values and num_nulls sum to the chunk's
// num_values and null count — which equal the slots and nulls the
// definition levels hold, as parquet-go decodes them. Row groups and
// chunks tile the file from the magic to the footer.
func TestEventFileMetadata(t *testing.T) {
	r := rand.New(rand.NewPCG(9, 10))
	evs := richEvents()
	for i := range 2*eventRowsPerRowGroup + 300 {
		evs = append(evs, randomEvent(r, uint64(i+100)))
	}
	path := writeEventFile(t, evs)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pf, err := parquetgo.OpenFile(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	md := pf.Metadata()
	off := int64(len(parquetMagic))
	for g, rg := range md.RowGroups {
		if rg.FileOffset != off {
			t.Fatalf("row group %d at %d, want %d", g, rg.FileOffset, off)
		}
		var unc int64
		for c, cc := range rg.Columns {
			checkChunkPages(t, raw, &cc.MetaData, off)
			if cc.FileOffset != off {
				t.Fatalf("row group %d column %d at %d, want %d", g, c, cc.FileOffset, off)
			}
			leaf, _ := pf.Schema().Lookup(cc.MetaData.PathInSchema...)
			checkChunkLevels(t, pf.RowGroups()[g].ColumnChunks()[c], &cc.MetaData,
				byte(leaf.MaxDefinitionLevel))
			off += cc.MetaData.TotalCompressedSize
			unc += cc.MetaData.TotalUncompressedSize
		}
		if rg.TotalCompressedSize != off-rg.FileOffset || rg.TotalByteSize != unc {
			t.Fatalf("row group %d: sizes %d/%d, want %d/%d", g, rg.TotalCompressedSize,
				rg.TotalByteSize, off-rg.FileOffset, unc)
		}
	}
	footer := int64(binary.LittleEndian.Uint32(raw[len(raw)-8:]))
	if off+footer+8 != int64(len(raw)) {
		t.Fatalf("pages end at %d, footer of %d bytes, file %d", off, footer, len(raw))
	}
}

// checkChunkPages walks a chunk's page headers from off.
func checkChunkPages(t *testing.T, raw []byte, md *format.ColumnMetaData, off int64) {
	t.Helper()
	first := md.DataPageOffset
	if md.DictionaryPageOffset != 0 {
		first = md.DictionaryPageOffset
	}
	if first != off {
		t.Fatalf("%v: first page at %d, want %d", md.PathInSchema, first, off)
	}
	end := off + md.TotalCompressedSize
	var unc, values, nulls int64
	for off < end {
		var hdr format.PageHeader
		br := bytes.NewReader(raw[off:end])
		if err := thrift.NewDecoder(new(thrift.CompactProtocol).NewReader(br)).Decode(&hdr); err != nil {
			t.Fatalf("%v: page header at %d: %v", md.PathInSchema, off, err)
		}
		hlen := int64(len(raw[off:end]) - br.Len())
		unc += hlen + int64(hdr.UncompressedPageSize)
		off += hlen + int64(hdr.CompressedPageSize)
		if hdr.DataPageHeaderV2.Valid {
			values += int64(hdr.DataPageHeaderV2.V.NumValues)
			nulls += int64(hdr.DataPageHeaderV2.V.NumNulls)
		}
	}
	if off != end || unc != md.TotalUncompressedSize || values != md.NumValues ||
		nulls != md.Statistics.NullCount {
		t.Fatalf("%v: pages end %d, uncompressed %d, values %d, nulls %d; metadata %d, %d, %d, %d",
			md.PathInSchema, off, unc, values, nulls, end, md.TotalUncompressedSize,
			md.NumValues, md.Statistics.NullCount)
	}
}

// checkChunkLevels decodes a chunk's pages with parquet-go and counts
// the slots and nulls their definition levels hold (a null is a level
// below maxDef), independently of the counts in the headers.
func checkChunkLevels(t *testing.T, cc parquetgo.ColumnChunk, md *format.ColumnMetaData, maxDef byte) {
	t.Helper()
	pages := cc.Pages()
	defer func() { _ = pages.Close() }()
	var slots, nulls int64
	for {
		p, err := pages.ReadPage()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("%v: %v", md.PathInSchema, err)
		}
		if defs := p.DefinitionLevels(); maxDef > 0 {
			slots += int64(len(defs))
			nulls += int64(len(defs) - bytes.Count(defs, []byte{maxDef}))
		} else {
			slots += p.NumValues()
		}
		parquetgo.Release(p)
	}
	if slots != md.NumValues || nulls != md.Statistics.NullCount {
		t.Fatalf("%v: decoded %d slots, %d nulls; metadata %d, %d", md.PathInSchema,
			slots, nulls, md.NumValues, md.Statistics.NullCount)
	}
}

// TestEventFileDeterministic: the same events give the same bytes (the
// footer's key-value pairs are sorted, nothing depends on map order or
// the clock).
func TestEventFileDeterministic(t *testing.T) {
	evs := richEvents()
	a, err := os.ReadFile(writeEventFile(t, evs))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(writeEventFile(t, evs))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("two writes of the same events differ")
	}
}

// TestEventColumnsRejectMismatch: a column list that disagrees with the
// schema parquet-go derives fails the writer's start.
func TestEventColumnsRejectMismatch(t *testing.T) {
	e := &eventColumns{schema: parquetgo.SchemaOf(schema.Event{})}
	var c column
	for _, tc := range []struct {
		spec columnSpec
		path []string
	}{
		{columnSpec{"seq", kindInt64}, []string{"boot_ns"}},            // wrong path
		{columnSpec{"seq", kindInt32}, []string{"seq"}},                // wrong type
		{columnSpec{"nope", kindBytes}, []string{"nope"}},              // no such column
		{columnSpec{"args.path", kindInt64}, []string{"args", "path"}}, // wrong type
	} {
		if err := e.initColumn(&c, tc.spec, tc.path); err == nil {
			t.Errorf("spec %+v for %v accepted", tc.spec, tc.path)
		}
	}
}
