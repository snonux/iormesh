// Package agentspool reads the Java agent's ring (decision #27): one
// memory-mapped file per JVM, <spool dir>/<pid>.ring, written lock-free
// by the agent's threads (agent/bridge/src/iormesh/bridge/Ring.java) and
// drained here by the tracer. It is cgo-free: the ring is plain shared
// memory, read with sync/atomic loads and stores at the agreed offsets.
//
// The reader is strict. A record whose framing is wrong (a length that is
// not a multiple of 8 or runs past the end of the data area, a head behind
// the tail) leaves no way to find the next record, so the ring is marked
// corrupt for good (ErrCorrupt). A record with sound framing but a body
// that does not decode is skipped and counted (Losses.Malformed), as is a
// record of a type this reader does not know (Losses.Unknown). Records the
// agent dropped because the reader lagged are counted by the agent in the
// ring header and surface in Losses too, so delivered + lost always equals
// what the agent tried to write.
//
// Joining the records to syscalls is the correlator's job
// (internal/correlate); this package only transports them.
package agentspool

import "errors"

// The ring format, mirrored from Ring.java; Version changes with any of it.
const (
	// Version is the only ring format version this reader accepts.
	Version = 1
	// magic is "IORMRING" as a little-endian word; the agent stores it
	// last, so a reader that sees it sees a complete header.
	magic = 0x474e49524d524f49

	offVersion      = 8
	offHeaderSize   = 12
	offCapacity     = 16
	offPID          = 24
	offCreated      = 32
	offHead         = 64
	offTail         = 128
	offDropped      = 192 // u64 per record type, 8 of them
	offDroppedBytes = 256
	offAbandoned    = 264
	headerSize      = 4096

	minCapacity = 1 << 16
	maxCapacity = 1 << 30

	// wordLen is the record framing word: len | type << 32.
	wordLen = 8
	// droppedSlots is the number of per-type drop counters in the header.
	droppedSlots = 8
)

// Record types, as written in the framing word's upper half.
const (
	typePad = iota
	typeBracket
	typeStack
	typeThread
	typeWatermark
)

// Fixed record lengths, framing word included.
const (
	bracketLen   = wordLen + 40
	watermarkLen = wordLen + 8
	// stringsHead is a STACK or THREAD record's fixed part: the word, a
	// u64 id, a u32 count or tid and four reserved zero bytes.
	stringsHead = wordLen + 16
)

// Errors Open and Drain return; test with errors.Is.
var (
	// ErrNotReady: the file exists but the agent has not finished its
	// header yet (no magic). Retry later.
	ErrNotReady = errors.New("agentspool: ring header not written yet")
	// ErrVersion: the ring was written by an agent of another format.
	ErrVersion = errors.New("agentspool: unsupported ring version")
	// ErrTruncated: the file is shorter (or longer) than its header says.
	ErrTruncated = errors.New("agentspool: ring file size does not match its header")
	// ErrCorrupt: the header or a record's framing is invalid; nothing
	// after it can be read.
	ErrCorrupt = errors.New("agentspool: corrupt ring")
)
