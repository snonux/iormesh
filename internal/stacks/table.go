// Package stacks builds a trace's native stack table (M2 step 9, iormesh
// run -native-stacks; docs/schema.md "Native stacks"): it turns the
// kernel's stack ids into the trace's own, reads each distinct stack once
// from the kernel's stack map, symbolizes it in userspace and writes it to
// stacks.ndjson. It is cgo-free: the stack map is read through a Lookup
// function the caller wires to internal/bpf.
package stacks

import (
	"encoding/binary"
	"os"
	"slices"
	"sync"
	"syscall"

	"iormesh/internal/schema"
)

// jobQueue bounds the stacks waiting for the worker. A full queue blocks
// the correlator — backpressure, as a full record channel is — rather than
// dropping a stack an event already names.
const jobQueue = 1024

// Lookup returns the raw instruction pointers behind a kernel stack id,
// innermost first (bpf.Tracer.StackTrace).
type Lookup func(kernelID uint32) ([]uint64, error)

// Stats counts what the table saw, for the run's summary line.
type Stats struct {
	// Stacks is how many distinct stacks the table assigned ids to.
	Stacks uint64
	// Uncaptured counts stack records the kernel could not deliver a stack
	// for or whose stack could not be read back: their events carry no
	// stack_id. A colliding stack map bucket alone does not count: such a
	// stack comes inline (Inline). It is the sum of the three reasons
	// below.
	Uncaptured uint64
	// Collisions counts -EEXIST that reached userspace: the stack's bucket
	// held another stack and the inline fallback's bpf_get_stack returned
	// no frames either (a fallback that failed with -EFAULT counts under
	// Faults).
	Collisions uint64
	// Faults counts -EFAULT: the walk yielded no frame, in
	// bpf_get_stackid or in the inline fallback.
	Faults uint64
	// Other counts every other kernel error and stacks the lookup could
	// not read back.
	Other uint64
	// Inline counts the distinct stacks (of Stacks) the kernel sent with
	// their frames because the stack map refused them, mostly bucket
	// collisions (struct stack_inline_event, internal/c/stack.c): each
	// would have been lost before.
	Inline uint64
}

// key is one kernel stack in one process image. The kernel's id names
// raw addresses only; the same addresses in two images may be two
// different call sites, so the trace's id is per image. A stack sent
// inline has no kernel id: frames holds its addresses (frameKey) and kid
// is 0, so it never matches a stack map stack, whose frames is "".
type key struct {
	img    image
	kid    uint32
	frames string
}

// job is one new stack for the worker: the entry without its frames, and
// what the frames are made from.
type job struct {
	stack schema.Stack
	maps  []mapping
	files map[string]*os.File // newly opened module files, the worker's to close
	ips   []uint64
}

// Table assigns trace stack ids on the correlator's goroutine (ID,
// Forget, Stats are not safe for concurrent use) and hands each new stack
// to its worker goroutine, which symbolizes and writes it. The split keeps
// the slow part — parsing a 20 MB libjvm.so once, say — from holding up
// the correlator while short-lived processes exit and take their
// /proc/<pid>/maps with them.
type Table struct {
	lookup Lookup
	maps   *mapsCache
	ids    map[uint32]map[key]uint32 // by pid, so Forget drops a process at once
	next   uint32
	stats  Stats
	jobs   chan job
	done   chan struct{}
	mu     sync.Mutex
	err    error // the worker's first write error
}

// NewTable starts a table that reads stacks with lookup, /proc under
// procRoot ("/proc"), and writes each new stack with write on its worker.
// Close stops the worker.
func NewTable(lookup Lookup, procRoot string, write func(schema.Stack) error) *Table {
	t := &Table{lookup: lookup, maps: newMapsCache(procRoot), ids: map[uint32]map[key]uint32{},
		jobs: make(chan job, jobQueue), done: make(chan struct{})}
	go t.work(newResolver(), write)
	return t
}

// ID returns the trace's stack id for kernel stack kid (negative: the
// kernel's walk failed) of process pid, incarnation inc, in the image the
// record's tag names; ok is false when there is no stack to name. A stack
// new to its image is read from the kernel and its process's mappings
// snapshotted here, before the process can exit; the worker writes it.
// The error is a failed write of an earlier stack: the table is part of
// the trace, so it ends the trace.
func (t *Table) ID(pid uint32, inc uint64, kid int32, tag uint64) (id uint32, ok bool, err error) {
	if err := t.workerErr(); err != nil {
		return 0, false, err
	}
	if kid < 0 {
		t.uncaptured(-kid)
		return 0, false, nil
	}
	k := key{img: image{pid: pid, inc: inc, tag: tag}, kid: uint32(kid)}
	return t.assign(k, func() ([]uint64, error) { return t.lookup(k.kid) })
}

// InlineID is ID for a stack the kernel sent with its frames, ips
// (innermost first), because the stack map refused it: the same stack in
// the same image always gets the same id, by its frames. ips is copied;
// the caller's record may be recycled.
func (t *Table) InlineID(pid uint32, inc uint64, ips []uint64, tag uint64) (id uint32, ok bool, err error) {
	if err := t.workerErr(); err != nil {
		return 0, false, err
	}
	k := key{img: image{pid: pid, inc: inc, tag: tag}, frames: frameKey(ips)}
	return t.assign(k, func() ([]uint64, error) { return slices.Clone(ips), nil })
}

// assign returns k's id, or gives k the next one and queues its stack,
// read with read, for the worker; a stack read can't deliver is counted
// as uncaptured.
func (t *Table) assign(k key, read func() ([]uint64, error)) (uint32, bool, error) {
	pid := k.img.pid
	if id, ok := t.ids[pid][k]; ok {
		return id, true, nil
	}
	ips, err := read()
	if err != nil || len(ips) == 0 {
		t.uncaptured(0)
		return 0, false, nil
	}
	t.next++
	if t.ids[pid] == nil {
		t.ids[pid] = map[key]uint32{}
	}
	t.ids[pid][k] = t.next
	t.stats.Stacks++
	if k.frames != "" {
		t.stats.Inline++
	}
	maps, files := t.maps.snapshot(k.img, ips)
	t.jobs <- job{maps: maps, files: files, ips: ips,
		stack: schema.Stack{SchemaVersion: schema.Version, StackID: t.next, PID: pid, PIDInc: k.img.inc}}
	return t.next, true, nil
}

// Forget drops what the table keeps for pid — its ids and mappings —
// because it exec'd or exited: its later stacks are another image's (or
// another process's, once the pid is reused), so a long session holds
// only the live processes' state. The stacks already written stay.
func (t *Table) Forget(pid uint32) {
	delete(t.ids, pid)
	t.maps.forget(pid)
}

// Stats returns the counts so far.
func (t *Table) Stats() Stats {
	return t.stats
}

// Close waits until the worker wrote every queued stack and returns its
// first write error. The table must not be used afterwards.
func (t *Table) Close() error {
	close(t.jobs)
	<-t.done
	return t.workerErr()
}

// work symbolizes and writes queued stacks in id order. After a failed
// write it only drains the queue: ID reports the error.
func (t *Table) work(r *resolver, write func(schema.Stack) error) {
	defer close(t.done)
	for j := range t.jobs {
		if t.workerErr() != nil {
			closeAll(j.files)
			continue
		}
		j.stack.Frames = r.frames(j.maps, j.files, j.ips)
		if err := write(j.stack); err != nil {
			t.mu.Lock()
			t.err = err
			t.mu.Unlock()
		}
	}
}

// uncaptured counts a stack an event will lack, by the kernel's errno (0:
// the stack could not be read back).
func (t *Table) uncaptured(errno int32) {
	t.stats.Uncaptured++
	switch syscall.Errno(errno) {
	case syscall.EEXIST:
		t.stats.Collisions++
	case syscall.EFAULT:
		t.stats.Faults++
	default:
		t.stats.Other++
	}
}

// frameKey packs an inline stack's addresses into a map key: the bytes
// of the array, so equal stacks are equal keys.
func frameKey(ips []uint64) string {
	b := make([]byte, 0, 8*len(ips))
	for _, ip := range ips {
		b = binary.LittleEndian.AppendUint64(b, ip)
	}
	return string(b)
}

func (t *Table) workerErr() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}
