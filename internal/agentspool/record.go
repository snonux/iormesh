package agentspool

import (
	"encoding/binary"
	"errors"
	"strings"
	"unicode/utf8"
)

// Record is one decoded ring record: a Bracket, Stack, Thread,
// Watermark, Hook, HTTP, DNS or Class.
type Record interface {
	isRecord()
}

// Bracket is one closed agent bracket: a hooked JDK method ran on thread
// ThreadID (kernel tid KTID) from EnterMono to ExitMono, CLOCK_MONOTONIC
// ns (System.nanoTime, the clock bpf_ktime_get_ns reads), called from the
// stack StackID (0: no stack walked). Otel is the OTEL span current at
// the enter, zero when none was (or the app has no OTEL).
type Bracket struct {
	Hook      int32
	KTID      int32
	EnterMono uint64
	ExitMono  uint64
	StackID   uint64
	ThreadID  uint64
	Otel      Otel
}

// Stack names the frames of a stack id, innermost first, each
// "class#method(file:line)". Written the first time the agent sees the
// id — usually ahead of the Brackets naming it, but not always: another
// thread on the same call site may publish its Bracket first, and a
// dropped Stack is retried later. Resolve stack ids against everything
// read, not only what came before.
type Stack struct {
	ID     uint64
	Frames []string
	// Classes are the frames' class ids (task fe4), one per frame: the
	// Class record describing the frame's class, 0 when the agent could
	// not describe it. Like stack ids, resolve them against everything
	// read: a retried Class may follow the Stack naming it.
	Classes []uint64
}

// Class is one entry of the agent's class-load map (task fe4): a class
// some recorded frame names, with its class loader and code source. Its
// id is per JVM, from 1.
type Class struct {
	ID   uint64
	Name string // binary name, as in the frames
	// Loader is "bootstrap", the loader's name, or its class name.
	Loader string
	// CodeSource is the JAR or class directory path, "jrt:/<module>",
	// another location URL without credentials, or "" for none.
	CodeSource string
}

// Thread maps a Java thread id to its kernel tid (0: lookup failed) and
// name. Written at the thread's first bracket, ahead of its Brackets
// unless it was dropped and retried later (so, like Stack, resolve by id
// over everything read). The agent's own
// watermark thread has one too, so its syscalls can be told apart.
type Thread struct {
	ID   uint64
	KTID int32
	Name string
	// Virtual marks a virtual thread (JDK 21+). Its KTID is the carrier
	// of its first bracket only; each of its Brackets carries the carrier
	// it ran on, or 0 when it parked and moved carriers inside it.
	Virtual bool
}

// Hook is one row of the agent's hook table, written at install ahead of
// every bracket: the joiner needs a hook's syscall allowlist (S1: a
// syscall of another kind inside the bracket is JVM work, not the
// method's I/O), and the ring carries it so the tracer follows whatever
// table variant the JVM chose.
type Hook struct {
	ID     int32
	Kind   string // open, read, write, …: the I/O the bracket stands for
	Class  string // internal name, java/io/FileInputStream
	Method string
	Native string // the JNI native the method reaches
	// Syscalls are the syscall kinds the native issues (openat, read,
	// pread64, …); never empty.
	Syscalls []string
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
func (Hook) isRecord()      {}
func (HTTP) isRecord()      {}
func (DNS) isRecord()       {}
func (Class) isRecord()     {}

// errMalformed marks a body that does not decode; the record is skipped
// and counted, the ring stays readable.
var errMalformed = errors.New("agentspool: malformed record")

var le = binary.LittleEndian

// decode turns a record (framing word included) of a known type into a
// Record; errMalformed if its body breaks the format.
func decode(typ uint32, rec []byte) (Record, error) {
	switch typ {
	case typeBracket:
		return boxed(decodeBracketValue(rec))
	case typeWatermark:
		return boxed(decodeWatermark(rec))
	case typeStack:
		return decodeStack(rec)
	case typeThread:
		return decodeThread(rec)
	case typeHook:
		return decodeHook(rec)
	case typeHTTP:
		return decodeHTTP(rec)
	case typeDNS:
		return decodeDNS(rec)
	case typeClass:
		return decodeClass(rec)
	}
	return nil, errMalformed
}

// boxed turns a typed decoder's result into decode's: a Record, nil on
// error.
func boxed[T Record](v T, err error) (Record, error) {
	if err != nil {
		return nil, err
	}
	return v, nil
}

// decodeWatermark reads a WATERMARK record.
func decodeWatermark(rec []byte) (Watermark, error) {
	if len(rec) != watermarkLen {
		return Watermark{}, errMalformed
	}
	return Watermark{Mono: le.Uint64(rec[8:])}, nil
}

// decodeBracketValue checks what the agent guarantees: a positive hook
// id, a non-negative tid (0 = unknown), an exit not before the enter, and
// in the long form (bracketOtelLen) a valid span. It returns the bracket
// unboxed (DrainTo's hot path).
func decodeBracketValue(rec []byte) (Bracket, error) {
	if len(rec) != bracketLen && len(rec) != bracketOtelLen {
		return Bracket{}, errMalformed
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
		return Bracket{}, errMalformed
	}
	if len(rec) == bracketOtelLen {
		if b.Otel, _ = decodeOtel(rec, bracketLen); !b.Otel.Valid() {
			return Bracket{}, errMalformed
		}
	}
	return b, nil
}

// decodeStack reads n class ids after the fixed part, then n frames.
// The count is checked against the record's length before anything is
// allocated: n ids alone take 8n bytes.
func decodeStack(rec []byte) (Record, error) {
	if len(rec) < stringsHead {
		return nil, errMalformed
	}
	n := le.Uint32(rec[16:])
	if le.Uint32(rec[20:]) != 0 || uint64(n) > uint64(len(rec)-stringsHead)/8 {
		return nil, errMalformed
	}
	classes := make([]uint64, n)
	for i := range classes {
		classes[i] = le.Uint64(rec[stringsHead+8*i:])
	}
	frames, err := decodeStrings(rec, stringsHead+8*int(n), n)
	if err != nil {
		return nil, err
	}
	return Stack{ID: le.Uint64(rec[8:]), Frames: frames, Classes: classes}, nil
}

// decodeClass checks a class-load map entry: a non-zero id, exactly three
// strings, a name and a loader (the code source may be empty).
func decodeClass(rec []byte) (Record, error) {
	if len(rec) < stringsHead || le.Uint32(rec[16:]) != classStrings || le.Uint32(rec[20:]) != 0 {
		return nil, errMalformed
	}
	id := le.Uint64(rec[8:])
	f, err := decodeStrings(rec, stringsHead, classStrings)
	if err != nil || id == 0 || f[0] == "" || f[1] == "" {
		return nil, errMalformed
	}
	return Class{ID: id, Name: f[0], Loader: f[1], CodeSource: f[2]}, nil
}

func decodeThread(rec []byte) (Record, error) {
	if len(rec) < stringsHead {
		return nil, errMalformed
	}
	ktid := int32(le.Uint32(rec[16:]))
	flags := le.Uint32(rec[20:])
	name, err := decodeStrings(rec, stringsHead, 1)
	if err != nil || ktid < 0 || flags&^threadVirtual != 0 {
		return nil, errMalformed
	}
	return Thread{ID: le.Uint64(rec[8:]), KTID: ktid, Name: name[0],
		Virtual: flags&threadVirtual != 0}, nil
}

// decodeHook checks a hook-table row: a positive id that fits the
// bracket's i32, exactly five strings, none empty.
func decodeHook(rec []byte) (Record, error) {
	if len(rec) < stringsHead || le.Uint32(rec[16:]) != hookStrings || le.Uint32(rec[20:]) != 0 {
		return nil, errMalformed
	}
	id := le.Uint64(rec[8:])
	f, err := decodeStrings(rec, stringsHead, hookStrings)
	if err != nil || id == 0 || id > 1<<31-1 {
		return nil, errMalformed
	}
	for _, s := range f {
		if s == "" {
			return nil, errMalformed
		}
	}
	return Hook{ID: int32(id), Kind: f[0], Class: f[1], Method: f[2], Native: f[3],
		Syscalls: strings.Split(f[4], ",")}, nil
}

// decodeStrings reads n strings (u32 byte length + UTF-8) after the
// fixed head of head bytes (whose fields the caller checks). The strings
// must be valid UTF-8, and what follows them only zero padding to the
// next multiple of 8 — anything else means the writer and reader
// disagree on the format.
func decodeStrings(rec []byte, head int, n uint32) ([]string, error) {
	if len(rec) < head || int(n) > (len(rec)-head)/4 {
		return nil, errMalformed
	}
	out := make([]string, 0, n)
	p := head
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
