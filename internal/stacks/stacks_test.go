package stacks

import (
	"debug/elf"
	"encoding/binary"
	"errors"
	"os"
	"reflect"
	"testing"

	"iormesh/internal/schema"
)

func TestParseMapsLine(t *testing.T) {
	cases := map[string]struct {
		line string
		want mapping
		ok   bool
	}{
		"file": {"00400000-004ac000 r-xp 00001000 fd:01 1234   /usr/bin/x",
			mapping{start: 0x400000, end: 0x4ac000, pgoff: 0x1000, dev: "fd:01", inode: 1234, path: "/usr/bin/x"}, true},
		"path with spaces": {"7f00-7f10 r-xp 0 00:25 9 /opt/my app/lib.so",
			mapping{start: 0x7f00, end: 0x7f10, dev: "00:25", inode: 9, path: "/opt/my app/lib.so"}, true},
		"anonymous jit": {"7f00-7f10 rwxp 00000000 00:00 0 ",
			mapping{start: 0x7f00, end: 0x7f10, dev: "00:00"}, true},
		"pseudo":         {"7ffc-7ffd r-xp 0 00:00 0 [vdso]", mapping{start: 0x7ffc, end: 0x7ffd, dev: "00:00", path: "[vdso]"}, true},
		"not executable": {"00400000-004ac000 r--p 0 fd:01 1234 /usr/bin/x", mapping{}, false},
		"malformed":      {"zz-004ac000 r-xp 0 fd:01 1234 /usr/bin/x", mapping{}, false},
		"short":          {"00400000-004ac000 r-xp", mapping{}, false},
	}
	for name, c := range cases {
		got, ok := parseMapsLine(c.line)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: got %+v %v, want %+v %v", name, got, ok, c.want, c.ok)
		}
	}
}

func TestMappingIsFile(t *testing.T) {
	for path, want := range map[string]bool{"/usr/lib/libc.so.6": true, "": false, "[vdso]": false,
		"/tmp/gone (deleted)": false} {
		if got := (mapping{inode: 1, path: path}).isFile(); got != want {
			t.Errorf("%q: isFile %v, want %v", path, got, want)
		}
	}
}

func TestNoteBuildID(t *testing.T) {
	note := []byte{4, 0, 0, 0, 3, 0, 0, 0, 3, 0, 0, 0, 'G', 'N', 'U', 0, 0xab, 0xcd, 0xef, 0}
	other := []byte{4, 0, 0, 0, 4, 0, 0, 0, 1, 0, 0, 0, 'G', 'o', 0, 0, 1, 2, 3, 4}
	if got := noteBuildID(append(other, note...), binary.LittleEndian); got != "abcdef" {
		t.Errorf("build id %q, want abcdef", got)
	}
	if got := noteBuildID(note[:15], binary.LittleEndian); got != "" {
		t.Errorf("truncated note gave %q", got)
	}
}

// TestResolvesOwnMapping resolves this very function's address in the
// running test binary through /proc/self/maps and the file it maps: the
// whole userspace path, the frame-pointer walk being the only kernel part
// left out. `go test` strips its binary's symbol table, so the symbol
// lookup itself is TestModuleSymbolize's.
func TestResolvesOwnMapping(t *testing.T) {
	pc := uint64(reflect.ValueOf(TestResolvesOwnMapping).Pointer()) + 8
	self := uint32(os.Getpid())
	c := newMapsCache("/proc", func(pid uint32, _ uint64) bool { return pid == self })
	maps, files := c.snapshot(image{pid: self}, []uint64{pc})
	if len(files) == 0 {
		t.Fatal("the test binary's file was not opened")
	}
	m, ok := find(maps, pc)
	if !ok {
		t.Fatalf("no mapping covers %#x", pc)
	}
	frames := newResolver().frames(maps, files, []uint64{pc})
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := frames[0]
	if f.Module != exe || f.Offset == nil || *f.Offset != pc-m.start+m.pgoff {
		t.Fatalf("frame %+v, want module %s at offset %#x", f, exe, pc-m.start+m.pgoff)
	}
	if again, files := c.snapshot(image{pid: self}, []uint64{pc}); len(files) != 0 || len(again) != len(maps) {
		t.Error("a covered snapshot re-read the maps or reopened its files")
	}
}

// TestModuleSymbolize: an offset maps through its PT_LOAD segment to the
// innermost enclosing function; a sizeless symbol runs to the next one;
// past a sized symbol, or outside every segment, there is no name.
func TestModuleSymbolize(t *testing.T) {
	m := &module{
		loads: []elf.ProgHeader{{Off: 0x1000, Vaddr: 0x401000, Filesz: 0x1000}},
		syms: []symbol{{start: 0x401000, size: 0x100, name: "outer"},
			{start: 0x401200, size: 0, name: "asm"}, {start: 0x401800, size: 0x10, name: "tiny"}},
	}
	for off, want := range map[uint64]string{0x1010: "outer+0x10", 0x1150: "",
		0x1250: "asm+0x50", 0x1805: "tiny+0x5", 0x1900: "", 0x10: "", 0x2000: ""} {
		if got := m.symbolize(off); got != want {
			t.Errorf("symbolize(%#x) = %q, want %q", off, got, want)
		}
	}
}

// TestDeadProcessKeepsRawAddresses: without a live process nothing is read
// and the frames are bare addresses, never someone else's mappings.
func TestDeadProcessKeepsRawAddresses(t *testing.T) {
	c := newMapsCache("/proc", func(uint32, uint64) bool { return false })
	maps, files := c.snapshot(image{pid: uint32(os.Getpid())}, []uint64{0x401000})
	frames := newResolver().frames(maps, files, []uint64{0x401000, 0x402000})
	want := []schema.StackFrame{{Addr: "0x401000"}, {Addr: "0x402000"}}
	if !reflect.DeepEqual(frames, want) {
		t.Fatalf("frames %+v, want %+v", frames, want)
	}
}

// TestTableIDs drives the id rules: one id per stack and image, a new
// image after an exec, failed walks and unreadable stacks counted, stacks
// written once in id order.
func TestTableIDs(t *testing.T) {
	var written []schema.Stack
	lookup := func(kid uint32) ([]uint64, error) {
		if kid == 99 {
			return nil, errors.New("gone")
		}
		return []uint64{0x1000 + uint64(kid)}, nil
	}
	tab := NewTable(lookup, "/nonexistent", func(uint32, uint64) bool { return false },
		func(s schema.Stack) error { written = append(written, s); return nil })
	steps := []struct {
		pid  uint32
		kid  int32
		want uint32
	}{{7, 1, 1}, {7, 1, 1}, {8, 1, 2}, {7, 2, 3}, {7, -14, 0}, {7, 99, 0}}
	for _, s := range steps {
		id, ok, err := tab.ID(s.pid, 5, s.kid)
		if err != nil || ok != (s.want != 0) || id != s.want {
			t.Fatalf("ID(%d, %d) = %d %v %v, want %d", s.pid, s.kid, id, ok, err, s.want)
		}
	}
	tab.Exec(7)
	if id, _, _ := tab.ID(7, 5, 1); id != 4 {
		t.Fatalf("stack 1 after exec got id %d, want a new one (4)", id)
	}
	if err := tab.Close(); err != nil {
		t.Fatal(err)
	}
	if st := tab.Stats(); st.Stacks != 4 || st.Uncaptured != 2 || len(written) != 4 {
		t.Fatalf("stats %+v, %d written; want 4 stacks, 2 uncaptured, 4 written", st, len(written))
	}
	for i, s := range written {
		if s.StackID != uint32(i+1) || s.Frames[0].Addr == "" {
			t.Errorf("written[%d] = %+v", i, s)
		}
	}
}

// TestTableWriteErrorEndsTrace: a failed write comes back from a later ID
// and from Close, so the trace ends instead of naming stacks it lacks.
func TestTableWriteErrorEndsTrace(t *testing.T) {
	boom := errors.New("disk full")
	tab := NewTable(func(uint32) ([]uint64, error) { return []uint64{1}, nil }, "/nonexistent",
		func(uint32, uint64) bool { return false }, func(schema.Stack) error { return boom })
	if _, _, err := tab.ID(1, 1, 1); err != nil {
		t.Fatalf("first ID: %v", err)
	}
	if err := tab.Close(); !errors.Is(err, boom) {
		t.Fatalf("Close = %v, want %v", err, boom)
	}
	if _, _, err := tab.ID(1, 1, 2); !errors.Is(err, boom) {
		t.Fatalf("ID after a failed write = %v, want %v", err, boom)
	}
}
