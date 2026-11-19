package stacks

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

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

// fakeProc builds a /proc tree for one process: a stat line with the
// incarnation inc (ns) and image tag, a maps file and map_files entries
// made by each setup function from its range name ("400000-4ac000").
func fakeProc(t *testing.T, pid uint32, inc, tag uint64, maps string,
	files map[string]func(path string) error) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, fmt.Sprint(pid))
	if err := os.MkdirAll(filepath.Join(dir, "map_files"), 0o755); err != nil {
		t.Fatal(err)
	}
	stat := fmt.Sprintf("%d (x) S 1%s %d%s %d%s\n", pid, strings.Repeat(" 0", 17), inc/10_000_000,
		strings.Repeat(" 0", 25), tag, strings.Repeat(" 0", 4))
	writeFile(t, filepath.Join(dir, "stat"), stat)
	writeFile(t, filepath.Join(dir, "maps"), maps)
	for name, setup := range files {
		if err := setup(filepath.Join(dir, "map_files", name)); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// selfMaps is a maps line mapping the running test binary at 0x400000.
func selfMaps(t *testing.T) (line, exe string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return "00400000-00500000 r-xp 00000000 00:25 1234 " + exe + "\n", exe
}

const (
	fakePid = 4242
	fakeInc = 7_000_000_000
	fakeTag = 0x7ffd1000
)

// TestSnapshotOpensThroughMapFiles: the module file is opened through
// /proc/<pid>/map_files, never by the maps path, parsed on the resolver
// and the frame gets module, offset and the binary's build id.
func TestSnapshotOpensThroughMapFiles(t *testing.T) {
	line, exe := selfMaps(t)
	root := fakeProc(t, fakePid, fakeInc, fakeTag, line, map[string]func(string) error{
		"400000-500000": func(p string) error { return os.Symlink(exe, p) }})
	c := newMapsCache(root)
	maps, files := c.snapshot(image{fakePid, fakeInc, fakeTag}, []uint64{0x401000})
	if len(files) != 1 || maps[0].mod == "" {
		t.Fatalf("files %v, maps %+v: want the binary opened and keyed", files, maps)
	}
	f := newResolver().frames(maps, files, []uint64{0x401000})[0]
	if f.Module != exe || f.Offset == nil || *f.Offset != 0x1000 || f.BuildID == "" {
		t.Fatalf("frame %+v, want %s at offset 0x1000 with a build id", f, exe)
	}
}

// TestSnapshotRefusesNonRegularFiles: a FIFO where the mapped file should
// be neither blocks the correlator nor reaches the ELF parser; the frame
// keeps module and offset only.
func TestSnapshotRefusesNonRegularFiles(t *testing.T) {
	line, _ := selfMaps(t)
	root := fakeProc(t, fakePid, fakeInc, fakeTag, line, map[string]func(string) error{
		"400000-500000": func(p string) error { return unix.Mkfifo(p, 0o600) }})
	type result struct {
		maps  []mapping
		files map[string]*os.File
	}
	done := make(chan result, 1)
	go func() {
		m, f := newMapsCache(root).snapshot(image{fakePid, fakeInc, fakeTag}, []uint64{0x401000})
		done <- result{m, f}
	}()
	select {
	case r := <-done:
		if len(r.files) != 0 || r.maps[0].mod != "" {
			t.Fatalf("a FIFO was kept as a module: %+v %v", r.maps, r.files)
		}
		f := newResolver().frames(r.maps, r.files, []uint64{0x401000})[0]
		if f.Offset == nil || f.BuildID != "" || f.Symbol != "" {
			t.Fatalf("frame %+v, want module and offset only", f)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("snapshot blocked on a FIFO")
	}
}

// TestSnapshotOnlyOfTheRecordsImage: maps are believed only while stat
// shows the record's incarnation and image tag — after an exec (another
// tag) or a pid reuse (another start) the frames stay bare addresses.
func TestSnapshotOnlyOfTheRecordsImage(t *testing.T) {
	line, exe := selfMaps(t)
	root := fakeProc(t, fakePid, fakeInc, fakeTag, line, map[string]func(string) error{
		"400000-500000": func(p string) error { return os.Symlink(exe, p) }})
	for name, img := range map[string]image{
		"exec'd":     {fakePid, fakeInc, fakeTag + 0x1000},
		"pid reused": {fakePid, fakeInc + 10_000_000, fakeTag},
		"no tag":     {fakePid, fakeInc, 0},
		"gone":       {fakePid + 1, fakeInc, fakeTag},
	} {
		maps, files := newMapsCache(root).snapshot(img, []uint64{0x401000})
		frames := newResolver().frames(maps, files, []uint64{0x401000})
		if maps != nil || files != nil || !reflect.DeepEqual(frames, []schema.StackFrame{{Addr: "0x401000"}}) {
			t.Errorf("%s: maps %+v, frames %+v; want a bare address", name, maps, frames)
		}
	}
}

// TestUncoveredAddressesReadOnce: an address no mapping covers re-reads
// the maps once; later stacks with it alone do not, a new one does.
func TestUncoveredAddressesReadOnce(t *testing.T) {
	line, _ := selfMaps(t)
	root := fakeProc(t, fakePid, fakeInc, fakeTag, line, nil)
	c := newMapsCache(root)
	img := image{fakePid, fakeInc, fakeTag}
	c.snapshot(img, []uint64{0x401000, 0x900000}) // 0x900000: uncovered, remembered
	mapsPath := filepath.Join(root, fmt.Sprint(fakePid), "maps")
	writeFile(t, mapsPath, line+"00900000-00a00000 r-xp 00000000 00:00 0 \n")
	if maps, _ := c.snapshot(img, []uint64{0x900000}); len(maps) != 1 {
		t.Fatalf("a known miss re-read the maps: %+v", maps)
	}
	if maps, _ := c.snapshot(img, []uint64{0xa00000}); len(maps) != 2 {
		t.Fatalf("a new miss did not re-read the maps: %+v", maps)
	}
}

// TestSafeReadModuleSurvivesGarbage: non-ELF, truncated and corrupted ELF
// input yields no module and no panic.
func TestSafeReadModuleSurvivesGarbage(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	head := make([]byte, 4096)
	f, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.ReadAt(head, 0)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	corrupt := append([]byte(nil), head...)
	for i := 0x20; i < 0x40; i++ { // section and program header offsets
		corrupt[i] = 0xff
	}
	for name, data := range map[string][]byte{"empty": nil, "text": []byte("#!/bin/sh\n"),
		"truncated": head[:100], "corrupt headers": corrupt} {
		if mod := safeReadModule(bytes.NewReader(data)); mod != nil && len(mod.syms) != 0 {
			t.Errorf("%s: parsed symbols out of garbage", name)
		}
	}
}

// FuzzSafeReadModule: no input panics the parser's caller, and whatever
// it yields symbolizes without panicking either.
func FuzzSafeReadModule(f *testing.F) {
	f.Add([]byte("\x7fELF\x02\x01\x01"))
	f.Add([]byte{})
	f.Fuzz(func(_ *testing.T, data []byte) {
		if mod := safeReadModule(bytes.NewReader(data)); mod != nil {
			_ = mod.symbolize(0x1000)
		}
	})
}

// TestTableIDs drives the id rules: one id per stack and image, a new
// image (another tag) or a forgotten process gets new ids, failed walks
// and unreadable stacks are counted by reason, stacks are written once in
// id order.
func TestTableIDs(t *testing.T) {
	var written []schema.Stack
	lookup := func(kid uint32) ([]uint64, error) {
		if kid == 99 {
			return nil, errors.New("gone")
		}
		return []uint64{0x1000 + uint64(kid)}, nil
	}
	tab := NewTable(lookup, "/nonexistent", func(s schema.Stack) error { written = append(written, s); return nil })
	steps := []struct {
		pid  uint32
		kid  int32
		tag  uint64
		want uint32
	}{{7, 1, 1, 1}, {7, 1, 1, 1}, {8, 1, 1, 2}, {7, 2, 1, 3}, {7, 1, 2, 4}, {7, -14, 1, 0}, {7, -17, 1, 0}, {7, 99, 1, 0}}
	for _, s := range steps {
		id, ok, err := tab.ID(s.pid, 5, s.kid, s.tag)
		if err != nil || ok != (s.want != 0) || id != s.want {
			t.Fatalf("ID(%d, %d, tag %d) = %d %v %v, want %d", s.pid, s.kid, s.tag, id, ok, err, s.want)
		}
	}
	tab.Forget(7)
	if id, _, _ := tab.ID(7, 5, 1, 1); id != 5 {
		t.Fatalf("stack 1 after Forget got id %d, want a new one (5)", id)
	}
	if id, _, _ := tab.ID(8, 5, 1, 1); id != 2 {
		t.Fatalf("Forget(7) dropped pid 8's id: got %d, want 2", id)
	}
	if err := tab.Close(); err != nil {
		t.Fatal(err)
	}
	want := Stats{Stacks: 5, Uncaptured: 3, Collisions: 1, Faults: 1, Other: 1}
	if st := tab.Stats(); st != want || len(written) != 5 {
		t.Fatalf("stats %+v, %d written; want %+v, 5 written", st, len(written), want)
	}
	for i, s := range written {
		if s.StackID != uint32(i+1) || s.Frames[0].Addr == "" {
			t.Errorf("written[%d] = %+v", i, s)
		}
	}
}

// TestTableInlineIDs: a stack sent inline gets one id per distinct frames
// and image, never shares one with a stack map stack (even kernel id 0,
// whose frames could be the same), and is counted as inline; an empty one
// is uncaptured.
func TestTableInlineIDs(t *testing.T) {
	var written []schema.Stack
	lookup := func(kid uint32) ([]uint64, error) { return []uint64{0x10, 0x20}, nil }
	tab := NewTable(lookup, "/nonexistent", func(s schema.Stack) error { written = append(written, s); return nil })
	ips := []uint64{0x10, 0x20}
	steps := []struct {
		ips  []uint64
		tag  uint64
		want uint32
	}{{ips, 1, 1}, {ips, 1, 1}, {[]uint64{0x10, 0x21}, 1, 2}, {ips, 2, 3}, {nil, 1, 0}}
	for _, s := range steps {
		id, ok, err := tab.InlineID(7, 5, s.ips, s.tag)
		if err != nil || ok != (s.want != 0) || id != s.want {
			t.Fatalf("InlineID(%#x, tag %d) = %d %v %v, want %d", s.ips, s.tag, id, ok, err, s.want)
		}
	}
	if id, _, _ := tab.ID(7, 5, 0, 1); id != 4 {
		t.Fatalf("stack map id 0 got trace id %d, want its own (4)", id)
	}
	if err := tab.Close(); err != nil {
		t.Fatal(err)
	}
	want := Stats{Stacks: 4, Inline: 3, Uncaptured: 1, Other: 1}
	if st := tab.Stats(); st != want || len(written) != 4 {
		t.Fatalf("stats %+v, %d written; want %+v, 4 written", st, len(written), want)
	}
	if got := written[1].Frames[1].Addr; got != "0x21" {
		t.Fatalf("inline stack 2's outer frame %q, want 0x21", got)
	}
}

// TestTableWriteErrorEndsTrace: a failed write comes back from a later ID
// and from Close, so the trace ends instead of naming stacks it lacks.
func TestTableWriteErrorEndsTrace(t *testing.T) {
	boom := errors.New("disk full")
	tab := NewTable(func(uint32) ([]uint64, error) { return []uint64{1}, nil }, "/nonexistent",
		func(schema.Stack) error { return boom })
	if _, _, err := tab.ID(1, 1, 1, 1); err != nil {
		t.Fatalf("first ID: %v", err)
	}
	if err := tab.Close(); !errors.Is(err, boom) {
		t.Fatalf("Close = %v, want %v", err, boom)
	}
	if _, _, err := tab.ID(1, 1, 2, 1); !errors.Is(err, boom) {
		t.Fatalf("ID after a failed write = %v, want %v", err, boom)
	}
}
