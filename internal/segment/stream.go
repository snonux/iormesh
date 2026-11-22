package segment

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	parquetgo "github.com/parquet-go/parquet-go"
)

// Row-group and page layout of the segment files, ior's defaults
// (internal/parquet/writer.go): 8192 rows per row group keeps the
// in-memory buffer of an open row group small, and 256 KiB pages match
// the zstd window (zstdcodec.go).
const (
	maxRowsPerRowGroup = 8192
	pageBufferSize     = 256 * 1024
)

// stream is one open parquet file of a segment's tables, written with
// parquet-go's GenericWriter: their rows are rare (a method, thread,
// class or stack seen for the first time), so its per-row reflection
// does not matter there. The events file, written per event, has its own
// encoder (eventStream). Both are written under their active names in
// the project directory and published by a rename into ready/ on the
// same filesystem.
type stream[T any] struct {
	path string
	f    *os.File
	pw   *parquetgo.GenericWriter[T]
	rows int64
}

// tableFile is what Writer needs of a table of any row type at rotation:
// the files of one segment are published together, tables first
// (Writer.publish).
type tableFile interface {
	tableName() string
	publish(readyPath string, meta map[string]string) error
	abort() error
}

// openStream creates path for a segment's rows. O_EXCL as a last guard:
// the project lock (lockDir) is what keeps a second writer out, and New
// swept stale active files under it, so an existing file here is a bug,
// never a file to share. The file is made 0644
// whatever the umask: once published, the deploying user's collector
// must read it.
func openStream[T any](path string, codec *zstdCodec) (*stream[T], error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, fmt.Errorf("segment: %w", err)
	}
	if err := f.Chmod(0o644); err != nil {
		return nil, errors.Join(fmt.Errorf("segment: %w", err), f.Close(), os.Remove(path))
	}
	pw := parquetgo.NewGenericWriter[T](f,
		parquetgo.Compression(codec),
		parquetgo.CreatedBy("iormesh", "", ""),
		parquetgo.MaxRowsPerRowGroup(maxRowsPerRowGroup),
		parquetgo.PageBufferSize(pageBufferSize))
	return &stream[T]{path: path, f: f, pw: pw}, nil
}

// write appends rows; the library copies them into its column buffers,
// so the caller may reuse the slice.
func (s *stream[T]) write(rows []T) error {
	n, err := s.pw.Write(rows)
	s.rows += int64(n)
	if err != nil {
		return fmt.Errorf("segment: write %s: %w", filepath.Base(s.path), err)
	}
	return nil
}

// publish writes the footer with meta, syncs and closes the file, and
// renames it to readyPath. The sync comes first so a crash after the
// rename cannot leave a published name over unwritten blocks: ready/ is
// the promise that a file there is whole (PLAN.md M4 "Trace files").
func (s *stream[T]) publish(readyPath string, meta map[string]string) error {
	for k, v := range meta {
		s.pw.SetKeyValueMetadata(k, v)
	}
	if err := s.pw.Close(); err != nil {
		return errors.Join(fmt.Errorf("segment: close %s: %w", filepath.Base(s.path), err), s.abort())
	}
	if err := s.f.Sync(); err != nil {
		return errors.Join(fmt.Errorf("segment: sync %s: %w", filepath.Base(s.path), err), s.abort())
	}
	if err := s.f.Close(); err != nil {
		return errors.Join(fmt.Errorf("segment: close %s: %w", filepath.Base(s.path), err),
			os.Remove(s.path))
	}
	// Same filesystem (ready/ is a subdirectory), so the rename is atomic:
	// a collector sees the whole segment or none of it.
	if err := os.Rename(s.path, readyPath); err != nil {
		return fmt.Errorf("segment: publish: %w", err)
	}
	return nil
}

// abort closes and removes the active file: a segment that is not
// published must not linger as a footer-less file.
func (s *stream[T]) abort() error {
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
