// Adapted from ior internal/parquet/zstdcodec.go @ b21afc50328c.
// Changes: package segment; one codec per segment Writer, released on Close.

package segment

import (
	"fmt"
	"sync"

	"github.com/klauspost/compress/zstd"
	parquetgo "github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress"
	"github.com/parquet-go/parquet-go/format"
)

// zstdWindowSize is the match window of the segment encoder. A page is
// compressed on its own (Encode gets one page's bytes and no shared
// history), and pages are cut at the page buffer size (256 KiB), so a
// window larger than one page can never find a match a smaller one would
// miss. The library default for SpeedDefault is 8 MiB, which pins about
// 16.8 MB per encoder; 1 MiB keeps the whole page in one window and pins
// about 2 MB.
const zstdWindowSize = 1 << 20

// zstdCodec is the parquet compression codec segments are written with
// (decision #16: parquet-internal zstd pages). It replaces the library's
// parquetgo.Zstd, whose encoders live in a package-global sync.Pool that
// pins an 8 MiB-window encoder per CPU and is emptied by every GC cycle —
// on a server, inside the tracer's cgroup memory cap, that is memory
// the trace does not need.
//
// Pages are compressed one at a time by the writer's goroutine, so this
// codec owns exactly ONE encoder, created on first use, and serialises
// callers on a mutex (the compress.Codec contract still demands
// concurrent safety). Decoding is not on the tracer's path; it is
// delegated to the library codec.
type zstdCodec struct {
	mu      sync.Mutex
	encoder *zstd.Encoder
}

var _ compress.Codec = (*zstdCodec)(nil)

// newZstdCodec returns a codec with no encoder yet; the first Encode
// makes it.
func newZstdCodec() *zstdCodec { return &zstdCodec{} }

func (c *zstdCodec) String() string { return "ZSTD" }

func (c *zstdCodec) CompressionCodec() format.CompressionCodec { return format.Zstd }

// Encode compresses src into dst[:0] and returns it. The encoder is
// created on first use, so a writer that never flushes a page never pays
// for one.
func (c *zstdCodec) Encode(dst, src []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.encoder == nil {
		e, err := newZstdEncoder()
		if err != nil {
			return nil, fmt.Errorf("create zstd encoder: %w", err)
		}
		c.encoder = e
	}
	return c.encoder.EncodeAll(src, dst[:0]), nil
}

// Decode delegates to the library codec (see the type comment).
func (c *zstdCodec) Decode(dst, src []byte) ([]byte, error) {
	return parquetgo.Zstd.Decode(dst, src)
}

// release drops the encoder so its window and history buffers become
// garbage once the trace ends. The codec stays usable: a later Encode
// builds a new encoder.
func (c *zstdCodec) release() {
	c.mu.Lock()
	c.encoder = nil
	c.mu.Unlock()
}

// newZstdEncoder builds the one encoder. Level, zero-length frames and
// the missing CRC are what the library codec uses, so files keep its
// format; only the window (and so the memory) differs. Each frame header
// states the window it needs, so any zstd decoder reads the output.
func newZstdEncoder() (*zstd.Encoder, error) {
	return zstd.NewWriter(nil,
		zstd.WithEncoderConcurrency(1),
		zstd.WithEncoderLevel(zstd.SpeedDefault),
		zstd.WithWindowSize(zstdWindowSize),
		zstd.WithZeroFrames(true),
		zstd.WithEncoderCRC(false),
	)
}
