package bpf

import (
	"fmt"
)

// startReader starts libbpfgo's poll goroutine on event_map and a decode
// goroutine that turns its raw records into typed ones on t.records.
func (t *Tracer) startReader(buffer int) error {
	if buffer <= 0 {
		buffer = defaultBuffer
	}
	raw := make(chan []byte, buffer)
	rb, err := t.module.InitRingBuf(ringbufMapName, raw)
	if err != nil {
		return fmt.Errorf("ring buffer %s: %w", ringbufMapName, err)
	}
	t.records = make(chan any, buffer)
	t.decoded.Add(1)
	go t.decodeLoop(raw)
	rb.Poll(pollTimeoutMs)
	return nil
}

// decodeLoop runs until libbpfgo closes raw (RingBuffer.Stop). After Close
// signals done it keeps draining raw without delivering, so neither it nor
// libbpfgo's callback can block on a reader that is gone.
func (t *Tracer) decodeLoop(raw <-chan []byte) {
	defer t.decoded.Done()
	defer close(t.records)
	for r := range raw {
		rec, why := decode(r)
		if why != decodedOK {
			t.losses.count(why)
			continue
		}
		select {
		case t.records <- rec:
		case <-t.done:
		}
	}
}

// Close stops the trace and closes Records; records in flight are dropped.
// It is safe to call more than once. libbpfgo's Module.Close does the
// teardown in the right order — ring buffer (its Stop drains and closes
// the raw channel, which ends decodeLoop), then every link still attached,
// then the object — so links are never destroyed here a second time: a
// failed bpf_link__destroy has already freed the link, and destroying it
// again would be a double free.
func (t *Tracer) Close() error {
	t.closing.Do(func() {
		close(t.done)
		if t.module != nil {
			t.module.Close()
		}
		t.decoded.Wait()
	})
	return nil
}
