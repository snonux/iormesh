package schema

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
)

// Stack is one entry of a trace's native stack table, stacks.ndjson (M2
// step 9, iormesh run -native-stacks): the user stack a traced thread had
// when it entered a syscall. Events reference it by Event.StackID. The
// table is its own file rather than an event kind because a stack is
// shared by every call made from the same place: it is written once, the
// first time an event needs it, and events stay lean.
//
// A stack belongs to one process image: StackID is unique within the trace,
// and (PID, PIDInc) says whose address space the addresses are in (an exec
// starts a new image, so the same kernel stack after it gets a new id).
type Stack struct {
	SchemaVersion uint32 `json:"schema_version"`
	StackID       uint32 `json:"stack_id"`
	PID           uint32 `json:"pid"`
	PIDInc        uint64 `json:"pid_incarnation"`
	// Frames lists the stack innermost first: the frame that made the
	// syscall, then its callers.
	Frames []StackFrame `json:"frames"`
}

// StackFrame is one frame of a native stack, symbolized as far as the
// tracer could (internal/stacks): Addr always; Module and Offset when the
// address lay in a file mapping of the process; Symbol when that file's
// ELF symbol tables cover it; BuildID when the file carries a GNU build
// id. An offline symbolizer needs only (BuildID or Module, Offset).
type StackFrame struct {
	// Addr is the instruction address in the process, as a hex string
	// ("0x4a5b2c"): a JSON number cannot hold 64 bits exactly.
	Addr string `json:"addr"`
	// Module is the mapped file's path as /proc/<pid>/maps names it, or a
	// pseudo mapping ("[vdso]"); absent for anonymous memory (JIT code).
	Module string `json:"module,omitempty"`
	// Offset is Addr's offset in Module's file; set exactly when Module
	// is a file.
	Offset *uint64 `json:"offset,omitempty"`
	// BuildID is the module's NT_GNU_BUILD_ID, lower-case hex.
	BuildID string `json:"build_id,omitempty"`
	// Symbol is the enclosing function and the offset into it,
	// "main.accept+0x2f".
	Symbol string `json:"symbol,omitempty"`
}

// Validate checks a stack table entry: ids start at 1 like every
// referenced id, the process is named, every frame has its address and a
// file offset only comes with its module.
func (s *Stack) Validate() error {
	switch {
	case s.SchemaVersion != Version:
		return invalidf("stack: schema_version %d, want %d", s.SchemaVersion, Version)
	case s.StackID == 0:
		return invalidf("stack: stack_id 0: ids start at 1")
	case s.PID == 0:
		return invalidf("stack %d: pid 0", s.StackID)
	case len(s.Frames) == 0:
		return invalidf("stack %d: no frames", s.StackID)
	}
	for i, f := range s.Frames {
		if !strings.HasPrefix(f.Addr, "0x") {
			return invalidf("stack %d frame %d: addr %q, want 0x-prefixed hex", s.StackID, i, f.Addr)
		}
		if f.Offset != nil && f.Module == "" {
			return invalidf("stack %d frame %d: offset without a module", s.StackID, i)
		}
	}
	return nil
}

// StackWriter writes stacks.ndjson, one validated Stack per line. Stacks
// are rare next to events (one per distinct call site), so it uses
// encoding/json and flushes each line: a crash never loses a stack an
// already written event names.
type StackWriter struct {
	w      *bufio.Writer
	closer io.Closer
	err    error
}

// NewStackWriter writes to w; Close closes w when it is an io.Closer.
func NewStackWriter(w io.Writer) *StackWriter {
	s := &StackWriter{w: bufio.NewWriter(w)}
	if c, ok := w.(io.Closer); ok {
		s.closer = c
	}
	return s
}

// Stack validates and writes one entry. The first error sticks.
func (s *StackWriter) Stack(st Stack) error {
	if s.err != nil {
		return s.err
	}
	if err := st.Validate(); err != nil {
		return fmt.Errorf("stacks: %w", err)
	}
	line, err := json.Marshal(&st)
	if err == nil {
		_, err = s.w.Write(append(line, '\n'))
	}
	if err == nil {
		err = s.w.Flush()
	}
	if err != nil {
		s.err = fmt.Errorf("stacks: write stack %d: %w", st.StackID, err)
	}
	return s.err
}

// Close flushes and closes the underlying writer if it is one.
func (s *StackWriter) Close() error {
	err := s.err
	if ferr := s.w.Flush(); ferr != nil && err == nil {
		err = ferr
	}
	if s.closer != nil {
		err = errors.Join(err, s.closer.Close())
		s.closer = nil
	}
	return err
}

// DecodeStack parses one stacks.ndjson line strictly (the rules of
// DecodeEvent) and validates it.
func DecodeStack(data []byte) (Stack, error) {
	var st Stack
	if err := checkKeys(data, reflect.TypeFor[Stack]()); err != nil {
		return Stack{}, err
	}
	if err := decodeStrict(data, &st); err != nil {
		return Stack{}, err
	}
	if err := st.Validate(); err != nil {
		return Stack{}, err
	}
	return st, nil
}

// ReadStacks decodes a whole stack table strictly.
func ReadStacks(r io.Reader) ([]Stack, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), maxLine)
	var stacks []Stack
	for line := 1; sc.Scan(); line++ {
		st, err := DecodeStack(sc.Bytes())
		if err != nil {
			return stacks, fmt.Errorf("stacks line %d: %w", line, err)
		}
		stacks = append(stacks, st)
	}
	if err := sc.Err(); err != nil {
		return stacks, fmt.Errorf("stacks: %w", err)
	}
	return stacks, nil
}
