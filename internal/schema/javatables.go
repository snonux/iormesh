package schema

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
)

// Method is one entry of a trace's method table, methods.ndjson (M3, task
// 2b4): one distinct Java stack frame. Event.Java.Stack lists these ids,
// innermost first. A frame is a method plus the line it was at, so two
// call sites in one method are two entries; the id is what keeps every
// event's stack a short list of integers. The table is its own file, like
// stacks.ndjson, because a frame is shared by every event made through it:
// written once, before the first event naming it. The parquet tags are
// the M4 segment writer's mapping (PLAN.md: "methods" parquet file).
//
// Ids are per trace and keyed by Frame alone: the same frame text in two
// JVMs gets one id (the class-load map that would tell their JARs apart
// is a later M3 step).
type Method struct {
	SchemaVersion uint32 `json:"schema_version" parquet:"schema_version"`
	MethodID      uint64 `json:"method_id" parquet:"method_id"`
	// Frame is the agent's frame text, "class#method(file:line)".
	Frame  string `json:"frame" parquet:"frame"`
	Class  string `json:"class" parquet:"class,dict"`
	Method string `json:"method" parquet:"method,dict"`
	// File is the source file, absent when the class has no debug info.
	File string `json:"file,omitempty" parquet:"file,optional"`
	// Line is the source line, absent when unknown (no line table, or a
	// native method).
	Line *uint32 `json:"line,omitempty" parquet:"line,optional"`
}

// Thread is one entry of a trace's Java thread table, threads.ndjson (M3,
// task 2b4): the agent's THREAD record of a traced JVM, mapping a Java
// thread to its kernel tid and name. The agent's own watermark thread
// is listed too (iormesh-agent-watermark), so its syscalls can be told
// apart from the target's.
type Thread struct {
	SchemaVersion uint32 `json:"schema_version" parquet:"schema_version"`
	PID           uint32 `json:"pid" parquet:"pid"`
	PIDInc        uint64 `json:"pid_incarnation" parquet:"pid_incarnation"`
	// TID is the kernel tid; 0 when the agent could not look it up. For
	// a virtual thread it is the carrier of its first bracket only.
	TID          uint32 `json:"tid" parquet:"tid"`
	JavaThreadID uint64 `json:"java_thread_id" parquet:"java_thread_id"`
	Name         string `json:"name" parquet:"name"`
	// Virtual marks a virtual thread (JDK 21+), whose I/O events carry
	// java.carrier.
	Virtual bool `json:"virtual,omitempty" parquet:"virtual,optional"`
}

// unknownSource is what StackWalker frames say for a class without a
// source file attribute; the agent writes it verbatim.
const unknownSource = "Unknown Source"

// ParseFrame splits the agent's "class#method(file:line)" into a Method
// (without id). File and Line stay empty where the frame says they are
// unknown ("Unknown Source", a line ≤ 0). An error means the text does
// not have that shape.
func ParseFrame(frame string) (Method, error) {
	cls, rest, ok := strings.Cut(frame, "#")
	open := strings.LastIndexByte(rest, '(')
	if !ok || cls == "" || open <= 0 || !strings.HasSuffix(rest, ")") {
		return Method{}, invalidf("frame %q: want class#method(file:line)", frame)
	}
	m := Method{SchemaVersion: Version, Frame: frame, Class: cls, Method: rest[:open]}
	src := rest[open+1 : len(rest)-1]
	file, line, ok := cutLast(src, ':')
	if !ok {
		return Method{}, invalidf("frame %q: no :line", frame)
	}
	if file != unknownSource {
		m.File = file
	}
	if n, err := strconv.ParseInt(line, 10, 32); err != nil {
		return Method{}, invalidf("frame %q: line %q", frame, line)
	} else if n > 0 {
		v := uint32(n)
		m.Line = &v
	}
	return m, nil
}

// cutLast is strings.Cut at the last sep: file names may hold a colon.
func cutLast(s string, sep byte) (before, after string, found bool) {
	i := strings.LastIndexByte(s, sep)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+1:], true
}

// Validate checks a method table entry: ids start at 1, and the parsed
// parts agree with the frame text, so a reader may use either.
func (m *Method) Validate() error {
	switch {
	case m.SchemaVersion != Version:
		return invalidf("method: schema_version %d, want %d", m.SchemaVersion, Version)
	case m.MethodID == 0:
		return invalidf("method: method_id 0: ids start at 1")
	}
	want, err := ParseFrame(m.Frame)
	if err != nil {
		return err
	}
	want.MethodID = m.MethodID
	if !reflect.DeepEqual(want, *m) {
		return invalidf("method %d: parts disagree with frame %q", m.MethodID, m.Frame)
	}
	return nil
}

// Validate checks a thread table entry: the process and Java thread are
// named.
func (t *Thread) Validate() error {
	switch {
	case t.SchemaVersion != Version:
		return invalidf("thread: schema_version %d, want %d", t.SchemaVersion, Version)
	case t.PID == 0:
		return invalidf("thread %d: pid 0", t.JavaThreadID)
	}
	return nil
}

// tableRow is what a TableWriter writes: a validated table entry.
type tableRow interface {
	Method | Thread
}

// TableWriter writes one NDJSON table (methods.ndjson, threads.ndjson),
// one validated entry per line. Entries are rare next to events, so it
// uses encoding/json and flushes each line, as StackWriter does: a crash
// never loses an entry an already written event names.
type TableWriter[T tableRow] struct {
	w      *bufio.Writer
	closer io.Closer
	err    error
}

// NewTableWriter writes to w; Close closes w when it is an io.Closer.
func NewTableWriter[T tableRow](w io.Writer) *TableWriter[T] {
	t := &TableWriter[T]{w: bufio.NewWriter(w)}
	if c, ok := w.(io.Closer); ok {
		t.closer = c
	}
	return t
}

// Write validates and writes one entry. The first error sticks.
func (t *TableWriter[T]) Write(row T) error {
	if t.err != nil {
		return t.err
	}
	if err := validateRow(&row); err != nil {
		t.err = fmt.Errorf("table: %w", err)
		return t.err
	}
	line, err := json.Marshal(&row)
	if err == nil {
		_, err = t.w.Write(append(line, '\n'))
	}
	if err == nil {
		err = t.w.Flush()
	}
	if err != nil {
		t.err = fmt.Errorf("table: write: %w", err)
	}
	return t.err
}

// Close flushes and closes the underlying writer if it is one.
func (t *TableWriter[T]) Close() error {
	err := t.err
	if ferr := t.w.Flush(); ferr != nil && err == nil {
		err = ferr
	}
	if t.closer != nil {
		err = errors.Join(err, t.closer.Close())
		t.closer = nil
	}
	return err
}

func validateRow[T tableRow](row *T) error {
	switch v := any(row).(type) {
	case *Method:
		return v.Validate()
	case *Thread:
		return v.Validate()
	}
	return nil
}

// ReadTable decodes a whole methods.ndjson or threads.ndjson strictly
// (the rules of DecodeEvent: unknown keys, missing required fields,
// another schema version all reject the line) and validates each entry.
func ReadTable[T tableRow](r io.Reader) ([]T, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), maxLine)
	var rows []T
	for line := 1; sc.Scan(); line++ {
		var row T
		if err := checkKeys(sc.Bytes(), reflect.TypeFor[T]()); err != nil {
			return rows, fmt.Errorf("table line %d: %w", line, err)
		}
		if err := decodeStrict(sc.Bytes(), &row); err != nil {
			return rows, fmt.Errorf("table line %d: %w", line, err)
		}
		if err := validateRow(&row); err != nil {
			return rows, fmt.Errorf("table line %d: %w", line, err)
		}
		rows = append(rows, row)
	}
	if err := sc.Err(); err != nil {
		return rows, fmt.Errorf("table: %w", err)
	}
	return rows, nil
}
