package agentspool

import (
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// Header is the ring's fixed metadata, as the agent wrote it at create.
type Header struct {
	Version  uint32
	Capacity uint64 // data bytes, a power of two
	// PID is the JVM's pid in its own pid namespace (the file name's).
	PID uint64
	// CreatedMono is CLOCK_MONOTONIC ns when the agent created the ring.
	CreatedMono uint64
}

// Ring is the reader side of one agent ring. It is the ring's only
// reader and must be used from one goroutine; the agent's writers run
// concurrently in the JVM.
type Ring struct {
	mem    []byte
	hdr    Header
	tail   uint64
	mask   uint64
	losses Losses   // reader-side counters (Unknown, Malformed)
	err    error    // sticky ErrCorrupt
	owner  uint32   // the file's uid
	batch  []Record // Drain's records, reused between drains
}

// Open maps the ring file read-write (the reader zeroes what it consumed
// and moves the tail) and checks its header. ErrNotReady means the agent
// has not finished the header yet; retry. The mapping outlives the file
// descriptor, which is closed before Open returns. A file shorter than
// the header is also ErrNotReady: the agent has created but not yet
// sized it.
func Open(path string) (*Ring, error) {
	return OpenOwned(path, nil)
}

// OpenOwned is Open with the file's owner checked before anything is
// mapped: owner gets the uid from fstat of the open descriptor, and an
// error from it rejects the file unmapped.
//
// The tracer runs as root and the spool directory is writable by the
// targets, so the file is opened without following a symlink and must be
// a regular file with one link: mapping a planted link to another file
// read-write would let a target have the tracer zero that file's bytes.
func OpenOwned(path string, owner func(uid uint32) error) (*Ring, error) {
	f, st, err := openRingFile(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if owner != nil {
		if err := owner(st.Uid); err != nil {
			return nil, err
		}
	}
	if st.Size < headerSize {
		// The agent sizes the file right after creating it: a file this
		// short is one caught in between, not a damaged ring.
		return nil, fmt.Errorf("%s: %d bytes, header not written yet: %w", path, st.Size,
			ErrNotReady)
	}
	mem, err := syscall.Mmap(int(f.Fd()), 0, int(st.Size),
		syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("agentspool: mmap %s: %w", path, err)
	}
	r := &Ring{mem: mem, owner: st.Uid}
	if err := guarded(r.checkHeader); err != nil {
		_ = syscall.Munmap(mem)
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
}

// openRingFile opens path without following a symlink (and without
// blocking on a FIFO) and requires a regular file with one link.
func openRingFile(path string) (*os.File, syscall.Stat_t, error) {
	var st syscall.Stat_t
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, syscall.ELOOP) {
		return nil, st, fmt.Errorf("%s: a symlink: %w", path, ErrNotRing)
	}
	if err != nil {
		return nil, st, fmt.Errorf("agentspool: %w", err)
	}
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		return nil, st, errors.Join(fmt.Errorf("agentspool: %w", err), f.Close())
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Nlink != 1 {
		return nil, st, errors.Join(fmt.Errorf("%s: not a regular file with one link: %w",
			path, ErrNotRing), f.Close())
	}
	return f, st, nil
}

// guarded runs fn, which reads the mapping, with memory faults turned
// into ErrCorrupt instead of a crash: the ring file belongs to the
// traced user, who can truncate it under the tracer's MAP_SHARED mapping,
// and the access past the new end raises SIGBUS. SetPanicOnFault is per
// goroutine, so it is set and restored around the access by the
// goroutine making it. Only the mapping's own reads run inside — never
// a caller's callback, whose nil dereference must stay a crash.
func guarded(fn func() error) (err error) {
	old := debug.SetPanicOnFault(true)
	defer debug.SetPanicOnFault(old)
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		if _, fault := r.(interface{ Addr() uintptr }); !fault {
			panic(r)
		}
		err = fmt.Errorf("fault reading the mapped ring (file truncated?): %w", ErrCorrupt)
	}()
	return fn()
}

// Close unmaps the ring. The file stays: it is the tracer's to delete.
func (r *Ring) Close() error {
	if r.mem == nil {
		return nil
	}
	err := syscall.Munmap(r.mem)
	r.mem = nil
	return err
}

// Owner is the uid owning the ring file: the JVM that created it, unless
// someone else planted it.
func (r *Ring) Owner() uint32 {
	return r.owner
}

// Header returns the ring's header.
func (r *Ring) Header() Header {
	return r.hdr
}

// Pending is the bytes the agent reserved that Drain has not consumed:
// records not yet published, or published since the last Drain. Still
// non-zero after the JVM exited means a writer died mid-record; those
// records are lost.
func (r *Ring) Pending() uint64 {
	var n uint64
	if err := guarded(func() error { n = r.word(offHead) - r.tail; return nil }); err != nil {
		r.err = err
		return 0
	}
	return n
}

// Losses returns the cumulative loss counters: the agent's, read from the
// header now, and the reader's own.
func (r *Ring) Losses() Losses {
	l := r.losses
	if err := guarded(func() error { l = r.agentLosses(l); return nil }); err != nil {
		r.err = err
		return r.losses
	}
	return l
}

// agentLosses adds the agent's header counters to l.
func (r *Ring) agentLosses(l Losses) Losses {
	l.DroppedBrackets = r.word(offDropped + 8*typeBracket)
	l.DroppedStacks = r.word(offDropped + 8*typeStack)
	l.DroppedThreads = r.word(offDropped + 8*typeThread)
	l.DroppedWatermarks = r.word(offDropped + 8*typeWatermark)
	l.DroppedHooks = r.word(offDropped + 8*typeHook)
	l.DroppedHTTP = r.word(offDropped + 8*typeHTTP)
	for _, t := range []int{typePad, 7} {
		l.DroppedOther += r.word(offDropped + 8*t)
	}
	l.DroppedBytes = r.word(offDroppedBytes)
	l.Abandoned = r.word(offAbandoned)
	return l
}

// checkHeader validates the header and loads the reader's state from it.
func (r *Ring) checkHeader() error {
	switch m := r.word(0); {
	case m == 0:
		return ErrNotReady
	case m != magic:
		return fmt.Errorf("bad magic %#x: %w", m, ErrCorrupt)
	}
	r.hdr = Header{
		Version:     le.Uint32(r.mem[offVersion:]),
		Capacity:    le.Uint64(r.mem[offCapacity:]),
		PID:         le.Uint64(r.mem[offPID:]),
		CreatedMono: le.Uint64(r.mem[offCreated:]),
	}
	if r.hdr.Version != Version {
		return fmt.Errorf("version %d, want %d: %w", r.hdr.Version, Version, ErrVersion)
	}
	c := r.hdr.Capacity
	if hs := le.Uint32(r.mem[offHeaderSize:]); hs != headerSize ||
		c < minCapacity || c > maxCapacity || c&(c-1) != 0 {
		return fmt.Errorf("header size %d, capacity %d: %w", hs, c, ErrCorrupt)
	}
	if uint64(len(r.mem)) != headerSize+c {
		return fmt.Errorf("%d bytes for a %d byte ring: %w", len(r.mem), c, ErrTruncated)
	}
	r.mask = c - 1
	r.tail = r.word(offTail)
	if head := r.word(offHead); head < r.tail || head-r.tail > c || r.tail%8 != 0 {
		return fmt.Errorf("head %d, tail %d: %w", head, r.tail, ErrCorrupt)
	}
	return nil
}

// word atomically loads the u64 at off: header counters and framing words
// are written concurrently by the agent. off is 8-aligned and the mapping
// page-aligned, as sync/atomic requires.
func (r *Ring) word(off int) uint64 {
	return atomic.LoadUint64((*uint64)(unsafe.Pointer(&r.mem[off])))
}

func (r *Ring) storeWord(off int, v uint64) {
	atomic.StoreUint64((*uint64)(unsafe.Pointer(&r.mem[off])), v)
}
