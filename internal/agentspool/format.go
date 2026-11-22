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
// that does not decode, or longer than maxRecord (64 KiB, never decoded),
// is skipped and counted (Losses.Malformed), as is a
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
	// Version is the only ring format version this reader accepts. 2
	// (task 2b4) added HOOK records and THREAD's flags word, 3 (task 3b4)
	// HTTP records, 4 (task 4b4) OTEL ids: a BRACKET of bracketOtelLen
	// and three more words in HTTP's fixed part, 5 (task wd4) the HTTP
	// request's start stamp and body sizes, and DNS records; 6 (task wd4,
	// review round 2) HTTP's async-marker and java.net.http flags; 7
	// (task fe4) CLASS records, a class id per frame in STACK, and
	// sixteen drop counters, which moves dropped bytes and abandoned; 8
	// (task mb4 review) HTTP's held flag. The reader epoch (offEpoch,
	// task 5j4) did not bump it: it lives in a header word every older
	// agent left zero and never read, so either side may meet an older
	// peer and both go on as before (docs/java-attach.md, "Second
	// sessions").
	Version = 8
	// magic is "IORMRING" as a little-endian word; the agent stores it
	// last, so a reader that sees it sees a complete header.
	magic = 0x474e49524d524f49

	offVersion    = 8
	offHeaderSize = 12
	offCapacity   = 16
	offPID        = 24
	offCreated    = 32
	// offEpoch is the reader epoch (task 5j4): every reader adds one when
	// it opens the ring. The agent writes its dictionary records (HOOK,
	// THREAD, STACK, CLASS) once and a reader consumes what it reads, so
	// a second session on a running JVM would never see them; the agent's
	// watermark thread polls this word and resends the dictionary when it
	// moved past 1, or moved after an older tracer had drained the ring
	// (the first reader finds everything still in the ring).
	// Agents before 5j4 never read it and stay silent: a second session
	// on such a JVM gets no hook table, as before.
	offEpoch        = 40
	offHead         = 64
	offTail         = 128
	offDropped      = 192 // u64 per record type, droppedSlots of them
	offDroppedBytes = offDropped + 8*droppedSlots
	offAbandoned    = offDroppedBytes + 8
	headerSize      = 4096

	// The accepted data capacities (Ring.MIN_CAPACITY, MAX_CAPACITY). The
	// cap is a bound on the tracer's heap, not only on the mapping (task
	// yi4): one drain decodes every record a ring holds before handing
	// them out, copying their strings, so a ring can put about its
	// capacity on the heap at once. 64 MiB is eight times the agent's
	// default ring and well inside the 1 GiB a started tracer may use
	// (MemoryMax); a header naming more is corrupt.
	minCapacity = 1 << 16
	maxCapacity = 1 << 26

	// maxRecord bounds a record other than PAD (task yi4): one that is
	// longer is skipped and counted malformed without being decoded, so
	// no single record allocates more than this. The agent writes none
	// past Ring.MAX_RECORD (16 KiB: strings cut to 1024 chars, an HTTP
	// record losing its headers first, a stack depth cap), so 64 KiB
	// leaves room and an honest trace never meets it. A PAD may fill the
	// rest of a lap and is never decoded, so it is exempt.
	maxRecord = 64 << 10

	// wordLen is the record framing word: len | type << 32.
	wordLen = 8
	// droppedSlots is the number of per-type drop counters in the header.
	droppedSlots = 16
)

// Record types, as written in the framing word's upper half.
const (
	typePad = iota
	typeBracket
	typeStack
	typeThread
	typeWatermark
	typeHook
	typeHTTP
	typeDNS
	typeClass
)

// threadVirtual is THREAD's flag for a virtual thread (JDK 21+); every
// other flag bit is reserved and must be zero.
const threadVirtual = 1

// httpServer is HTTP's flag for a request answered (a server's), not
// sent; httpUnordered (task wd4 review) marks a record stamped out of its
// connection's request order (a sendAsync completion). Every other flag
// bit is reserved and must be zero.
const (
	httpServer    = 1
	httpUnordered = 2
	// httpAsyncMark marks no request but HttpClient.sendAsync's entry
	// (task wd4 review round 2): see HTTP.AsyncMark.
	httpAsyncMark = 4
	// httpNetHTTP marks a request of java.net.http's HttpClient.
	httpNetHTTP = 8
	// httpHeld marks a request its recording thread worked on alone
	// from start to completion (task mb4 review): see HTTP.Held.
	httpHeld  = 16
	httpFlags = httpServer | httpUnordered | httpAsyncMark | httpNetHTTP | httpHeld
)

// hookStrings is the number of strings in a HOOK record: kind, class,
// method, native, syscall allowlist.
const hookStrings = 5

// classStrings is the number of strings in a CLASS record (task fe4):
// binary name, loader label, code source.
const classStrings = 3

// Fixed record lengths, framing word included.
const (
	bracketLen = wordLen + 40
	// bracketOtelLen is a BRACKET whose enter ran inside an OTEL span:
	// bracketLen plus the trace id halves and the span id (u64 each).
	// The agent writes it only then, so an app without OTEL pays nothing.
	bracketOtelLen = bracketLen + 24
	watermarkLen   = wordLen + 8
	// stringsHead is a STACK, THREAD, HOOK or CLASS record's fixed part:
	// the word, a u64 id, a u32 count or tid and a u32 of flags
	// (THREAD's; zero in the others). A STACK's class ids follow it.
	stringsHead = wordLen + 16
	// httpHead is an HTTP record's fixed part: the word, mono, stack id
	// and Java thread id (u64 each), ktid, status, the string count, the
	// request-header count, flags and the exchange count (u32 each), then
	// the OTEL trace id halves and span id (u64 each, zero: no span), the
	// start stamp (u64, 0: unknown) and the request and response body
	// sizes (i64 each, -1: unknown).
	httpHead = wordLen + 96
	// dnsHead is a DNS record's fixed part (task wd4): the word, mono,
	// stack id and Java thread id (u64 each), ktid and the string count
	// (u32 each), then the OTEL trace id halves and span id.
	dnsHead = wordLen + 56
	// httpFixed is the number of leading strings of an HTTP record:
	// method, scheme, host, path, version, local and remote address.
	httpFixed = 7
)

// Errors Open and Drain return; test with errors.Is.
var (
	// ErrNotReady: the file exists but the agent has not finished its
	// header yet (shorter than the header, or no magic). Retry later.
	ErrNotReady = errors.New("agentspool: ring header not written yet")
	// ErrVersion: the ring was written by an agent of another format.
	ErrVersion = errors.New("agentspool: unsupported ring version")
	// ErrTruncated: the file is shorter (or longer) than its header says.
	ErrTruncated = errors.New("agentspool: ring file size does not match its header")
	// ErrNotRing: the path is not a plain file (a symlink, a device, a
	// hard link): never mapped.
	ErrNotRing = errors.New("agentspool: not a ring file")
	// ErrCorrupt: the header or a record's framing is invalid; nothing
	// after it can be read.
	ErrCorrupt = errors.New("agentspool: corrupt ring")
)
