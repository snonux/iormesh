package bpf

import (
	"fmt"
	"os"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The kernel's BPF ring buffer record header (kernel/bpf/ringbuf.c,
// include/uapi/linux/bpf.h): a u32 length whose top bits flag a record
// still being written (busy) or thrown away (discard), then a u32 page
// offset only the kernel uses. Records start 8-byte aligned.
const (
	ringBusyBit    = 1 << 31
	ringDiscardBit = 1 << 30
	ringHdrSize    = 8
)

// ringReader consumes event_map straight from its memory mapping, the way
// libbpf's ring_buffer__consume does, but in Go: no cgo callback, no
// copy into a fresh []byte and no channel hop per record. Those were the
// largest per-event cost left once the correlator and encoder were cheap
// (task wc4, decision #25): libbpfgo hands every record through a cgo
// callback, a GoBytes allocation and a channel send that woke a
// goroutine.
//
// The mapping follows the kernel's layout: one read-write page holding
// the consumer position, then a read-only page holding the producer
// position followed by the data area mapped twice in a row, so a record
// that wraps around the end is still one contiguous slice.
type ringReader struct {
	consumerPage []byte
	producerPage []byte // producer page + data area twice
	data         []byte
	mask         uint64
	epfd         int
}

// newRingReader maps the ring buffer map fd of size bytes (its
// max_entries, a power-of-two multiple of the page size) and registers it
// with an epoll instance for wait.
func newRingReader(fd int, size int) (_ *ringReader, err error) {
	page := os.Getpagesize()
	if size <= 0 || size%page != 0 || size&(size-1) != 0 {
		return nil, fmt.Errorf("ring buffer size %d: want a power-of-two multiple of %d", size, page)
	}
	r := &ringReader{mask: uint64(size - 1), epfd: -1}
	defer func() {
		if err != nil {
			_ = r.close()
		}
	}()
	if r.consumerPage, err = unix.Mmap(fd, 0, page, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED); err != nil {
		return nil, fmt.Errorf("mmap ring consumer page: %w", err)
	}
	if r.producerPage, err = unix.Mmap(fd, int64(page), page+2*size, unix.PROT_READ, unix.MAP_SHARED); err != nil {
		return nil, fmt.Errorf("mmap ring data: %w", err)
	}
	r.data = r.producerPage[page:]
	if r.epfd, err = unix.EpollCreate1(unix.EPOLL_CLOEXEC); err != nil {
		return nil, fmt.Errorf("epoll: %w", err)
	}
	ev := unix.EpollEvent{Events: unix.EPOLLIN, Fd: int32(fd)}
	if err := unix.EpollCtl(r.epfd, unix.EPOLL_CTL_ADD, fd, &ev); err != nil {
		return nil, fmt.Errorf("epoll add ring buffer: %w", err)
	}
	return r, nil
}

// close unmaps the ring and closes the epoll instance. It is called once,
// after the reading goroutine is gone.
func (r *ringReader) close() error {
	var errs []error
	for _, m := range [][]byte{r.consumerPage, r.producerPage} {
		if m != nil {
			errs = append(errs, unix.Munmap(m))
		}
	}
	if r.epfd >= 0 {
		errs = append(errs, unix.Close(r.epfd))
	}
	r.consumerPage, r.producerPage, r.data, r.epfd = nil, nil, nil, -1
	for _, err := range errs {
		if err != nil {
			return fmt.Errorf("ring buffer close: %w", err)
		}
	}
	return nil
}

// read hands up to limit committed records to add, in ring order, and then
// releases their space to the producer. add must copy what it keeps (the
// decoders copy into their pooled records): the bytes belong to the
// kernel again as soon as read returns. A record still being written
// ends the read; it is taken next time.
func (r *ringReader) read(limit int, add func([]byte)) int {
	consumer := (*uint64)(unsafe.Pointer(&r.consumerPage[0]))
	producer := (*uint64)(unsafe.Pointer(&r.producerPage[0]))
	return consume(r.data, r.mask, consumer, producer, limit, add)
}

// wait blocks until the ring holds data or timeoutMs passed. The ring's
// epoll readiness is level-triggered on "producer ahead of consumer", so
// a record committed between a read and this wait returns at once.
func (r *ringReader) wait(timeoutMs int) error {
	var events [1]unix.EpollEvent
	_, err := unix.EpollWait(r.epfd, events[:], timeoutMs)
	if err != nil && err != unix.EINTR {
		return fmt.Errorf("epoll wait: %w", err)
	}
	return nil
}

// consume is read's loop over plain memory, so tests can drive it with a
// fake ring. The producer position is loaded with acquire semantics (Go's
// atomics are sequentially consistent) before any header behind it is
// read, and each header is loaded atomically, as the kernel commits a
// record by clearing its busy bit with release semantics. The consumer
// position is stored once at the end: the kernel only reads it to find
// free space.
func consume(data []byte, mask uint64, consumer, producer *uint64, limit int, add func([]byte)) int {
	cons := atomic.LoadUint64(consumer)
	prod := atomic.LoadUint64(producer)
	n := 0
	for cons < prod && n < limit {
		off := cons & mask
		hdr := atomic.LoadUint32((*uint32)(unsafe.Pointer(&data[off])))
		if hdr&ringBusyBit != 0 {
			break
		}
		size := uint64(hdr &^ (ringBusyBit | ringDiscardBit))
		if hdr&ringDiscardBit == 0 {
			add(data[off+ringHdrSize : off+ringHdrSize+size])
			n++
		}
		cons += (size + ringHdrSize + 7) &^ 7
	}
	atomic.StoreUint64(consumer, cons)
	return n
}
