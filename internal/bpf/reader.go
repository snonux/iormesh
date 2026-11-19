package bpf

import (
	"fmt"
	"time"
)

// maxBatch bounds one Records batch: large enough that a burst crosses
// the channel (and wakes its reader) once per few hundred records instead
// of once per record, small enough that a stop or a loss poll waits for
// little work.
const maxBatch = 256

// batchPause is how long the reader lets records gather after it drained
// the ring. A reader that keeps up would otherwise be woken for nearly
// every record — each wakeup an epoll return, a futex and a scheduler
// round trip costing more CPU than the record's whole handling (task
// wc4) — while a pause lets the next batch fill. At the ~2 M records/s
// of a four-thread syscall-burst it gathers ~400 records of ~40 bytes,
// nothing next to the 16 MiB ring, so the pause never turns into loss;
// and it delays a record by at most this much, nothing next to the
// correlator's own hold-back.
const batchPause = 200 * time.Microsecond

// startReader maps event_map (ringReader) and starts the goroutine that
// turns its records into batches of typed ones on t.records. buffer is
// how many decoded records may wait for the consumer, in batches; a full
// channel stops the reading, the ring fills and the kernel counts drops.
func (t *Tracer) startReader(buffer int) error {
	if buffer <= 0 {
		buffer = defaultBuffer
	}
	m, err := t.module.GetMap(ringbufMapName)
	if err != nil {
		return fmt.Errorf("ring buffer %s: %w", ringbufMapName, err)
	}
	if t.ring, err = newRingReader(m.FileDescriptor(), int(m.MaxEntries())); err != nil {
		return fmt.Errorf("ring buffer %s: %w", ringbufMapName, err)
	}
	t.records = make(chan []any, max(1, buffer/maxBatch))
	t.decoded.Add(1)
	go t.readLoop()
	return nil
}

// readLoop reads the ring until Close. It sends a batch of whatever was
// committed, up to maxBatch — one channel handoff per batch instead of
// per record (task wc4, decision #25). After a full batch it reads on at
// once; after a partial one it lets the next batch gather for batchPause;
// on an empty ring it blocks in epoll, woken by the kernel, so an idle
// trace costs nothing. A failing epoll ends the stream: the consumer then
// sees Records close and stops the trace with an error instead of
// silently tracing nothing.
func (t *Tracer) readLoop() {
	defer t.decoded.Done()
	defer close(t.records)
	for !t.stopping() {
		batch := t.collect()
		if len(batch) > 0 && !t.deliver(batch) {
			return
		}
		switch {
		case len(batch) == maxBatch:
		case len(batch) > 0:
			time.Sleep(batchPause)
		default:
			if t.ring.wait(pollTimeoutMs) != nil {
				return
			}
		}
	}
}

// collect decodes up to maxBatch committed records, counting those that
// do not decode.
func (t *Tracer) collect() []any {
	var batch []any
	t.ring.read(maxBatch, func(raw []byte) { batch = t.add(batch, raw) })
	return batch
}

// deliver sends batch to the consumer; false when Close came first.
func (t *Tracer) deliver(batch []any) bool {
	select {
	case t.records <- batch:
		return true
	case <-t.done:
		return false
	}
}

// stopping reports whether Close has begun.
func (t *Tracer) stopping() bool {
	select {
	case <-t.done:
		return true
	default:
		return false
	}
}

// add decodes r onto batch, or counts why it cannot.
func (t *Tracer) add(batch []any, r []byte) []any {
	rec, why := decode(r)
	if why != decodedOK {
		t.losses.count(why)
		return batch
	}
	return append(batch, rec)
}

// Close stops the trace and closes Records; records in flight are dropped.
// It is safe to call more than once. The reader goes first (it notices
// done within pollTimeoutMs, or at once when blocked on a full Records),
// then its mapping, then the Java mark's links (DetachJavaMarks, which
// libbpfgo does not track), then libbpfgo's Module.Close, which detaches
// every link still attached and frees the object — so links are never
// destroyed here a second time: a failed bpf_link__destroy has already freed the
// link, and destroying it again would be a double free.
func (t *Tracer) Close() error {
	var err error
	t.closing.Do(func() {
		close(t.done)
		t.decoded.Wait()
		if t.ring != nil {
			err = t.ring.close()
		}
		// The Java mark's uprobe_multi links are not the module's: libbpfgo
		// does not record them, so they are destroyed here, once each.
		t.detachAllJavaMarks()
		if t.module != nil {
			t.module.Close()
		}
	})
	return err
}
