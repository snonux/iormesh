package agentspool

import (
	"errors"
	"fmt"
)

// Visitor receives a drain's records by type. A Bracket and a Watermark
// come with every hooked call, so they are handed over as they are —
// boxing each into a Record cost an allocation per record, a measurable
// share of the joined event's budget (decision #25, task ee4); every
// other record type is rare and comes as a Record.
type Visitor interface {
	Bracket(*Bracket)
	Watermark(Watermark)
	Record(Record)
}

// Dispatch hands rec to the matching method of v: what DrainTo does for
// every record, for a caller that has Records already (a fake ring).
func Dispatch(v Visitor, rec Record) {
	switch r := rec.(type) {
	case Bracket:
		v.Bracket(&r)
	case Watermark:
		v.Watermark(r)
	default:
		v.Record(rec)
	}
}

// entry is one drained record, kept unboxed for the hot types.
type entry struct {
	typ uint32 // typeBracket, typeWatermark, or another type in rec
	b   Bracket
	wm  Watermark
	rec Record
}

// Drain is DrainTo with every record boxed into a Record for fn.
func (r *Ring) Drain(fn func(Record)) (int, error) {
	return r.DrainTo(funcVisitor(fn))
}

// funcVisitor adapts a func(Record) to a Visitor.
type funcVisitor func(Record)

func (f funcVisitor) Bracket(b *Bracket)    { f(*b) }
func (f funcVisitor) Watermark(w Watermark) { f(w) }
func (f funcVisitor) Record(rec Record)     { f(rec) }

// DrainTo consumes every record published so far, in ring order, handing
// each decoded one to v, and returns how many records (padding included)
// it consumed. It stops at the first unpublished slot: records reserved
// after it wait for the next drain. Malformed and unknown records are
// skipped and counted in Losses. A framing error, or a fault reading the
// mapping (the file was truncated under it), marks the ring corrupt:
// this and every later drain return ErrCorrupt, after handing out what
// was read before it.
func (r *Ring) DrainTo(v Visitor) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	// The records are read under the fault guard and handed out after
	// it, so v's own faults are never taken for the ring's.
	r.batch = r.batch[:0]
	n := 0
	err := guarded(func() error {
		for {
			ok, err := r.next()
			if err != nil || !ok {
				return err
			}
			n++
		}
	})
	for i := range r.batch {
		e := &r.batch[i]
		switch e.typ {
		case typeBracket:
			v.Bracket(&e.b)
		case typeWatermark:
			v.Watermark(e.wm)
		default:
			v.Record(e.rec)
		}
	}
	clear(r.batch)
	if err != nil {
		r.err = err
	}
	return n, err
}

// next consumes the record at the tail into the batch (nothing for
// padding and for a skipped record): ok is false when it is not
// published yet.
func (r *Ring) next() (ok bool, err error) {
	pos := r.tail & r.mask
	off := headerSize + int(pos)
	// Acquire: the agent fills the body, then publishes this word with a
	// release store, so the body is complete once the word is non-zero.
	w := r.word(off)
	if w == 0 {
		return false, nil
	}
	length, typ := uint64(uint32(w)), uint32(w>>32)
	if length < wordLen || length%8 != 0 || pos+length > r.hdr.Capacity {
		return false, fmt.Errorf("record at %d: length %d, type %d: %w",
			r.tail, length, typ, ErrCorrupt)
	}
	if head := r.word(offHead); r.tail+length > head {
		return false, fmt.Errorf("record at %d of length %d runs past head %d: %w",
			r.tail, length, head, ErrCorrupt)
	}
	r.decodeAt(typ, r.mem[off:off+int(length)])
	r.consume(off, length)
	return true, nil
}

// decodeAt decodes one record in place, before consume zeroes it, and
// appends it to the batch; the entry holds copies (strings), never the
// ring's memory. Brackets and watermarks are decoded into the entry
// itself, unboxed. A record longer than maxRecord is skipped as
// malformed before any of it is decoded (task yi4): its framing is sound,
// so the ring goes on, but its strings could be as long as the ring and
// would be copied to the heap.
func (r *Ring) decodeAt(typ uint32, b []byte) {
	if typ == typePad {
		return
	}
	if typ > typeClass {
		r.losses.Unknown++
		return
	}
	if len(b) > maxRecord {
		r.losses.Malformed++
		return
	}
	e := entry{typ: typ}
	var err error
	switch typ {
	case typeBracket:
		e.b, err = decodeBracketValue(b)
	case typeWatermark:
		e.wm, err = decodeWatermark(b)
	default:
		e.rec, err = decode(typ, b)
	}
	if errors.Is(err, errMalformed) {
		r.losses.Malformed++
		return
	}
	r.batch = append(r.batch, e)
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
