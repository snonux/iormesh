package agentspool

import (
	"encoding/binary"
	"errors"
	"unicode/utf8"
)

// Record is one decoded ring record: a Bracket, Stack, Thread or
// Watermark.
type Record interface {
	isRecord()
}

// Bracket is one closed agent bracket: a hooked JDK method ran on thread
// ThreadID (kernel tid KTID) from EnterMono to ExitMono, CLOCK_MONOTONIC
// ns (System.nanoTime, the clock bpf_ktime_get_ns reads), called from the
// stack StackID (0: no stack walked).
type Bracket struct {
	Hook      int32
	KTID      int32
	EnterMono uint64
	ExitMono  uint64
	StackID   uint64
	ThreadID  uint64
}

// Stack names the frames of a stack id, innermost first, each
// "class#method(file:line)". Written the first time the agent sees the
// id, so it precedes every Bracket that names it.
type Stack struct {
	ID     uint64
	Frames []string
}

// Thread maps a Java thread id to its kernel tid (0: lookup failed) and
// name. Written before the thread's first Bracket. The agent's own
// watermark thread has one too, so its syscalls can be told apart.
type Thread struct {
	ID   uint64
	KTID int32
	Name string
}

// Watermark promises that every bracket that exited before Mono is
// already in the ring ahead of this record, or counted as dropped.
type Watermark struct {
	Mono uint64
}

func (Bracket) isRecord()   {}
func (Stack) isRecord()     {}
func (Thread) isRecord()    {}
func (Watermark) isRecord() {}

// errMalformed marks a body that does not decode; the record is skipped
// and counted, the ring stays readable.
var errMalformed = errors.New("agentspool: malformed record")

var le = binary.LittleEndian

// decode turns a record (framing word included) of a known type into a
// Record; errMalformed if its body breaks the format.
func decode(typ uint32, rec []byte) (Record, error) {
	switch typ {
	case typeBracket:
		return decodeBracket(rec)
	case typeWatermark:
		if len(rec) != watermarkLen {
			return nil, errMalformed
		}
		return Watermark{Mono: le.Uint64(rec[8:])}, nil
	case typeStack:
		return decodeStack(rec)
	case typeThread:
		return decodeThread(rec)
	}
	return nil, errMalformed
}

// decodeBracket checks what the agent guarantees: a positive hook id, a
// non-negative tid (0 = unknown) and an exit not before the enter.
func decodeBracket(rec []byte) (Record, error) {
	if len(rec) != bracketLen {
		return nil, errMalformed
	}
	b := Bracket{
		Hook:      int32(le.Uint32(rec[8:])),
		KTID:      int32(le.Uint32(rec[12:])),
		EnterMono: le.Uint64(rec[16:]),
		ExitMono:  le.Uint64(rec[24:]),
		StackID:   le.Uint64(rec[32:]),
		ThreadID:  le.Uint64(rec[40:]),
	}
	if b.Hook <= 0 || b.KTID < 0 || b.ExitMono < b.EnterMono {
		return nil, errMalformed
	}
	return b, nil
}

func decodeStack(rec []byte) (Record, error) {
	if len(rec) < stringsHead {
		return nil, errMalformed
	}
	n := le.Uint32(rec[16:])
	frames, err := decodeStrings(rec, n)
	if err != nil {
		return nil, err
	}
	return Stack{ID: le.Uint64(rec[8:]), Frames: frames}, nil
}

func decodeThread(rec []byte) (Record, error) {
	if len(rec) < stringsHead {
		return nil, errMalformed
	}
	ktid := int32(le.Uint32(rec[16:]))
	name, err := decodeStrings(rec, 1)
	if err != nil || ktid < 0 {
		return nil, errMalformed
	}
	return Thread{ID: le.Uint64(rec[8:]), KTID: ktid, Name: name[0]}, nil
}

// decodeStrings reads n strings (u32 byte length + UTF-8) after the
// fixed head. The reserved bytes must be zero, the strings valid UTF-8,
// and what follows them only zero padding to the next multiple of 8 —
// anything else means the writer and reader disagree on the format.
func decodeStrings(rec []byte, n uint32) ([]string, error) {
	if le.Uint32(rec[20:]) != 0 || int(n) > (len(rec)-stringsHead)/4 {
		return nil, errMalformed
	}
	out := make([]string, 0, n)
	p := stringsHead
	for range n {
		if len(rec)-p < 4 {
			return nil, errMalformed
		}
		l := int(le.Uint32(rec[p:]))
		p += 4
		if l > len(rec)-p || !utf8.Valid(rec[p:p+l]) {
			return nil, errMalformed
		}
		out = append(out, string(rec[p:p+l]))
		p += l
	}
	if len(rec)-p >= 8 || !allZero(rec[p:]) {
		return nil, errMalformed
	}
	return out, nil
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
