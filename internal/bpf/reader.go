package bpf

import (
	"errors"
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
	t.rb = rb
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

// Close detaches every probe first, so no new record is produced, then
// stops the ring buffer and closes Records. Records still in flight when
// Close is called are dropped. It is safe to call more than once.
func (t *Tracer) Close() error {
	var err error
	t.closing.Do(func() {
		close(t.done)
		err = t.destroyLinks()
		if t.rb != nil {
			t.rb.Close()
			t.decoded.Wait()
		}
		if t.module != nil {
			t.module.Close()
		}
	})
	return err
}

// destroyLinks detaches every attached program. Links are destroyed before
// the module is closed: libbpf frees the programs with the object, and a
// link outliving its program is a use-after-free (ior's probe manager keeps
// the same order).
func (t *Tracer) destroyLinks() error {
	var errs []error
	for _, l := range t.links {
		if err := l.Destroy(); err != nil {
			errs = append(errs, err)
		}
	}
	t.links = nil
	return errors.Join(errs...)
}
