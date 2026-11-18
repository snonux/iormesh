package agentspool

import (
	"fmt"
	"os"
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
	losses Losses // reader-side counters (Unknown, Malformed)
	err    error  // sticky ErrCorrupt
}

// Open maps the ring file read-write (the reader zeroes what it consumed
// and moves the tail) and checks its header. ErrNotReady means the agent
// has not finished the header yet; retry. The mapping outlives the file
// descriptor, which is closed before Open returns.
func Open(path string) (*Ring, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("agentspool: %w", err)
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("agentspool: %w", err)
	}
	if st.Size() < headerSize {
		return nil, fmt.Errorf("%s: %d bytes, no room for the header: %w", path, st.Size(),
			ErrTruncated)
	}
	mem, err := syscall.Mmap(int(f.Fd()), 0, int(st.Size()),
		syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("agentspool: mmap %s: %w", path, err)
	}
	r := &Ring{mem: mem}
	if err := r.checkHeader(); err != nil {
		_ = syscall.Munmap(mem)
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
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

// Header returns the ring's header.
func (r *Ring) Header() Header {
	return r.hdr
}

// Pending is the bytes the agent reserved that Drain has not consumed:
// records not yet published, or published since the last Drain. Still
// non-zero after the JVM exited means a writer died mid-record; those
// records are lost.
func (r *Ring) Pending() uint64 {
	return r.word(offHead) - r.tail
}

// Losses returns the cumulative loss counters: the agent's, read from the
// header now, and the reader's own.
func (r *Ring) Losses() Losses {
	l := r.losses
	l.DroppedBrackets = r.word(offDropped + 8*typeBracket)
	l.DroppedStacks = r.word(offDropped + 8*typeStack)
	l.DroppedThreads = r.word(offDropped + 8*typeThread)
	l.DroppedWatermarks = r.word(offDropped + 8*typeWatermark)
	for _, t := range []int{typePad, 5, 6, 7} {
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
