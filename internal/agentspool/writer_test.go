package agentspool

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"unsafe"
)

// testWriter is a Go copy of the agent's writer (Ring.java), for the
// reader's tests: same header, same CAS reservation, same release
// publish. The cross-language tests (java_test.go) check that the real
// agent agrees with it.
type testWriter struct {
	mem []byte
	cap uint64
}

// newTestRing creates a ring file of capacity data bytes in a temp dir,
// header complete, and maps it for writing.
func newTestRing(t testing.TB, capacity uint64) (string, *testWriter) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "4242.ring")
	hdr := make([]byte, headerSize)
	le.PutUint64(hdr[0:], magic)
	le.PutUint32(hdr[offVersion:], Version)
	le.PutUint32(hdr[offHeaderSize:], headerSize)
	le.PutUint64(hdr[offCapacity:], capacity)
	le.PutUint64(hdr[offPID:], 4242)
	le.PutUint64(hdr[offCreated:], 77)
	if err := os.WriteFile(path, append(hdr, make([]byte, capacity)...), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, mapWriter(t, path, capacity)
}

func mapWriter(t testing.TB, path string, capacity uint64) *testWriter {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	mem, err := syscall.Mmap(int(f.Fd()), 0, int(headerSize+capacity),
		syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Munmap(mem) })
	return &testWriter{mem: mem, cap: capacity}
}

func (w *testWriter) ptr(off int) *uint64 {
	return (*uint64)(unsafe.Pointer(&w.mem[off]))
}

// reserve mirrors Ring.reserve: a PAD for the rest of the lap if the
// record does not fit, a counted drop if the reader is a ring behind.
func (w *testWriter) reserve(length, typ int) int {
	for {
		head := atomic.LoadUint64(w.ptr(offHead))
		tail := atomic.LoadUint64(w.ptr(offTail))
		pos := head & (w.cap - 1)
		var pad uint64
		if pos+uint64(length) > w.cap {
			pad = w.cap - pos
		}
		if head+pad+uint64(length)-tail > w.cap {
			atomic.AddUint64(w.ptr(offDropped+8*typ), 1)
			atomic.AddUint64(w.ptr(offDroppedBytes), uint64(length))
			return -1
		}
		if atomic.CompareAndSwapUint64(w.ptr(offHead), head, head+pad+uint64(length)) {
			if pad > 0 {
				w.publish(headerSize+int(pos), int(pad), typePad)
				return headerSize
			}
			return headerSize + int(pos)
		}
	}
}

func (w *testWriter) publish(off, length, typ int) {
	atomic.StoreUint64(w.ptr(off), uint64(length)|uint64(typ)<<32)
}

// raw writes a record of type typ with body (padded to 8 by the caller).
func (w *testWriter) raw(typ int, body []byte) bool {
	length := wordLen + len(body)
	off := w.reserve(length, typ)
	if off < 0 {
		return false
	}
	copy(w.mem[off+wordLen:], body)
	w.publish(off, length, typ)
	return true
}

func (w *testWriter) bracket(b Bracket) bool {
	return w.raw(typeBracket, bracketBody(b))
}

func (w *testWriter) stack(s Stack) bool {
	return w.raw(typeStack, stackBody(s.ID, uint32(len(s.Frames)), s.Classes, s.Frames))
}

func (w *testWriter) class(c Class) bool {
	return w.raw(typeClass, classBody(c))
}

// stackBody is a STACK body: id, the count, zero flags, the class ids
// (as given: tests pass too few or too many on purpose), the frames.
func stackBody(id uint64, n uint32, classes []uint64, frames []string) []byte {
	head := flagsBody(id, n, 0, nil)
	for _, c := range classes {
		head = le.AppendUint64(head, c)
	}
	return append(head, flagsBody(0, 0, 0, frames)[16:]...)
}

func classBody(c Class) []byte {
	return stringsBody(c.ID, classStrings, []string{c.Name, c.Loader, c.CodeSource})
}

func (w *testWriter) thread(th Thread) bool {
	var flags uint32
	if th.Virtual {
		flags = threadVirtual
	}
	return w.raw(typeThread, flagsBody(th.ID, uint32(th.KTID), flags, []string{th.Name}))
}

func (w *testWriter) hook(h Hook) bool {
	return w.raw(typeHook, hookBody(h))
}

// hookBody is a HOOK body: the five strings, the allowlist comma-joined.
func hookBody(h Hook) []byte {
	return stringsBody(uint64(h.ID), hookStrings,
		[]string{h.Kind, h.Class, h.Method, h.Native, strings.Join(h.Syscalls, ",")})
}

func (w *testWriter) watermark(m Watermark) bool {
	return w.raw(typeWatermark, le.AppendUint64(nil, m.Mono))
}

func bracketBody(b Bracket) []byte {
	body := make([]byte, 0, bracketLen-wordLen)
	body = le.AppendUint32(body, uint32(b.Hook))
	body = le.AppendUint32(body, uint32(b.KTID))
	for _, v := range []uint64{b.EnterMono, b.ExitMono, b.StackID, b.ThreadID} {
		body = le.AppendUint64(body, v)
	}
	if b.Otel != (Otel{}) {
		body = appendOtel(body, b.Otel) // the long form, as Ring.bracket writes it in a span
	}
	return body
}

// appendOtel appends the three OTEL id words.
func appendOtel(body []byte, o Otel) []byte {
	for _, v := range []uint64{o.TraceHi, o.TraceLo, o.Span} {
		body = le.AppendUint64(body, v)
	}
	return body
}

// stringsBody is a THREAD, HOOK or CLASS body with zero flags.
func stringsBody(id uint64, word uint32, strs []string) []byte {
	return flagsBody(id, word, 0, strs)
}

// flagsBody is a strings record's body: id, a u32, the flags word, then
// the strings, zero-padded to 8.
func flagsBody(id uint64, word, flags uint32, strs []string) []byte {
	body := le.AppendUint64(nil, id)
	body = le.AppendUint32(body, word)
	body = le.AppendUint32(body, flags)
	for _, s := range strs {
		body = le.AppendUint32(body, uint32(len(s)))
		body = append(body, s...)
	}
	for len(body)%8 != 0 {
		body = append(body, 0)
	}
	return body
}
