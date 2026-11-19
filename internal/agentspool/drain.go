package agentspool

import (
	"errors"
	"fmt"
)

// Drain consumes every record published so far, in ring order, handing
// each decoded one to fn, and returns how many records (padding
// included) it consumed. It stops at the first unpublished slot: records
// reserved after it wait for the next Drain. Malformed and unknown
// records are skipped and counted in Losses. A framing error, or a fault
// reading the mapping (the file was truncated under it), marks the ring
// corrupt: this and every later Drain return ErrCorrupt, after handing
// out what was read before it.
func (r *Ring) Drain(fn func(Record)) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	// The records are read under the fault guard and handed out after
	// it, so fn's own faults are never taken for the ring's.
	r.batch = r.batch[:0]
	n := 0
	err := guarded(func() error {
		for {
			rec, ok, err := r.next()
			if err != nil || !ok {
				return err
			}
			n++
			if rec != nil {
				r.batch = append(r.batch, rec)
			}
		}
	})
	for _, rec := range r.batch {
		fn(rec)
	}
	clear(r.batch)
	if err != nil {
		r.err = err
	}
	return n, err
}

// next consumes the record at the tail: ok is false when it is not
// published yet; rec is nil for padding and for a skipped record.
func (r *Ring) next() (rec Record, ok bool, err error) {
	pos := r.tail & r.mask
	off := headerSize + int(pos)
	// Acquire: the agent fills the body, then publishes this word with a
	// release store, so the body is complete once the word is non-zero.
	w := r.word(off)
	if w == 0 {
		return nil, false, nil
	}
	length, typ := uint64(uint32(w)), uint32(w>>32)
	if length < wordLen || length%8 != 0 || pos+length > r.hdr.Capacity {
		return nil, false, fmt.Errorf("record at %d: length %d, type %d: %w",
			r.tail, length, typ, ErrCorrupt)
	}
	if head := r.word(offHead); r.tail+length > head {
		return nil, false, fmt.Errorf("record at %d of length %d runs past head %d: %w",
			r.tail, length, head, ErrCorrupt)
	}
	rec = r.decodeAt(typ, r.mem[off:off+int(length)])
	r.consume(off, length)
	return rec, true, nil
}

// decodeAt decodes one record in place, before consume zeroes it; the
// Record holds copies (strings), never the ring's memory.
func (r *Ring) decodeAt(typ uint32, b []byte) Record {
	if typ == typePad {
		return nil
	}
	if typ > typeClass {
		r.losses.Unknown++
		return nil
	}
	rec, err := decode(typ, b)
	if errors.Is(err, errMalformed) {
		r.losses.Malformed++
		return nil
	}
	return rec
}

// consume zeroes the record — the agent relies on every lap starting
// from zeros, since a non-zero word means "published" — then releases
// the space by moving the tail with an atomic store, ordered after the
// zeroing, which the agent reads with an acquire load before reusing it.
func (r *Ring) consume(off int, length uint64) {
	clear(r.mem[off : off+int(length)])
	r.tail += length
	r.storeWord(offTail, r.tail)
}
