package bpf

import (
	"debug/elf"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// fakeJVM builds a fake /proc for pid 42 whose maps list libjava.so (and
// nothing else of the JDK) under /jdk/lib, with libjava's map_files entry
// made by mapped, and returns the fake /proc and the JVM's root in it.
func fakeJVM(t *testing.T, mapped func(path string) error) (proc, root string) {
	t.Helper()
	proc = t.TempDir()
	dir := filepath.Join(proc, "42")
	root = filepath.Join(dir, "root")
	for _, d := range []string{filepath.Join(dir, "map_files"), filepath.Join(root, "jdk", "lib")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	maps := "55d0c0a00000-55d0c0a01000 r--p 00000000 00:21 123 /jdk/bin/java\n" +
		"7f1c2b000000-7f1c2b008000 r--p 00000000 00:21 789 /jdk/lib/libjava.so\n" +
		"7ffd00000000-7ffd00021000 rw-p 00000000 00:00 0 [stack]\n"
	if err := os.WriteFile(filepath.Join(dir, "maps"), []byte(maps), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mapped(filepath.Join(dir, "map_files", "7f1c2b000000-7f1c2b008000")); err != nil {
		t.Fatal(err)
	}
	return proc, root
}

// TestOpenJavaLibs (review of task td4): the JDK libraries are opened as
// the traced process sees them and only as regular files - libjava.so
// through its map_files entry, an unmapped library inside the JVM's root,
// where an absolute symlink resolves against that root, not the tracer's.
// A FIFO in the lib directory is skipped at once instead of hanging the
// open, and so is a FIFO behind the map_files entry.
func TestOpenJavaLibs(t *testing.T) {
	proc, root := fakeJVM(t, func(p string) error { return os.WriteFile(p, []byte("java"), 0o644) })
	lib := filepath.Join(root, "jdk", "lib")
	if err := unix.Mkfifo(filepath.Join(lib, "libnio.so"), 0o644); err != nil {
		t.Fatal(err)
	}
	// /real exists only inside the JVM's root.
	if err := os.MkdirAll(filepath.Join(root, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "real", "libnet.so"), []byte("net"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/real/libnet.so", filepath.Join(lib, "libnet.so")); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, proc); !slices.Equal(got, []string{"java", "net"}) {
		t.Errorf("opened %q, want libjava's and libnet's contents", got)
	}
	fifo, _ := fakeJVM(t, func(p string) error { return unix.Mkfifo(p, 0o644) })
	if got := readAll(t, fifo); len(got) != 0 {
		t.Errorf("a FIFO behind map_files was opened: %q", got)
	}
}

// readAll opens the fake JVM's libraries and returns their contents.
func readAll(t *testing.T, proc string) []string {
	t.Helper()
	files, err := openJavaLibs(proc, 42)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range files {
		data, err := io.ReadAll(f)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(data))
		_ = f.Close()
	}
	return out
}

// TestLibMappings: only an absolute path names a library, and a process
// without a libjava.so mapping is no JVM.
func TestLibMappings(t *testing.T) {
	m := libMappings([]byte("7f1c2b000000-7f1c2b008000 r--p 00000000 00:21 789 /jdk/lib/libjava.so\n" +
		"7f1c2b008000-7f1c2b018000 r-xp 00008000 00:21 789 /jdk/lib/libjava.so\n" +
		"7f1c2c000000-7f1c2c008000 r--p 00000000 00:21 790 lib/libnio.so\n"))
	if got := m["libjava.so"]; got.start != 0x7f1c2b000000 || got.end != 0x7f1c2b008000 || len(m) != 1 {
		t.Errorf("mappings %+v", m)
	}
	proc := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proc, "7"), 0o755); err != nil {
		t.Fatal(err)
	}
	maps := []byte("55d0c0a00000-55d0c0a01000 r--p 00000000 00:21 123 /usr/bin/bash\n")
	if err := os.WriteFile(filepath.Join(proc, "7", "maps"), maps, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := openJavaLibs(proc, 7); !errors.Is(err, errNoLibjava) {
		t.Errorf("no JVM: %v", err)
	}
}

// TestFuncOffsets: a function symbol's file offset is where its code is in
// the file — checked against the test binary's own libc, whose exported
// write must begin inside an executable PT_LOAD segment at the offset its
// address maps to. A file that is not ELF yields nothing, without a panic.
func TestFuncOffsets(t *testing.T) {
	libc := mappedLibc(t)
	f, err := os.Open(libc)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	offs := safeFuncOffsets(f)
	off, ok := offs["write"]
	if !ok || off == 0 {
		t.Fatalf("%s: no offset for write (%d symbols)", libc, len(offs))
	}
	if !inExecSegment(t, f, "write", off) {
		t.Errorf("%s: write's offset %#x is not where its address maps to", libc, off)
	}
	if got := safeFuncOffsets(strings.NewReader("not an ELF file")); got != nil {
		t.Errorf("offsets of a non-ELF file: %v", got)
	}
	_, _, names := nativeOffsets(f, []JavaNative{{Symbol: "write", Cookie: 1},
		{Symbol: "Java_no_such_native", Cookie: 2}})
	if len(names) != 1 || names[0] != "write" {
		t.Errorf("nativeOffsets found %v, want write only", names)
	}
}

// inExecSegment reports whether file offset off of the ELF file f lies in
// an executable PT_LOAD segment and maps to the address of symbol name.
func inExecSegment(t *testing.T, f *os.File, name string, off uint64) bool {
	t.Helper()
	ef, err := elf.NewFile(f)
	if err != nil {
		t.Fatal(err)
	}
	syms, err := ef.DynamicSymbols()
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(syms, func(s elf.Symbol) bool { return s.Name == name && s.Value != 0 })
	if i < 0 {
		t.Fatalf("no symbol %s", name)
	}
	for _, p := range ef.Progs {
		if p.Type == elf.PT_LOAD && p.Flags&elf.PF_X != 0 && off >= p.Off && off < p.Off+p.Filesz {
			return off-p.Off+p.Vaddr == syms[i].Value
		}
	}
	return false
}

// mappedLibc is the libc this (cgo) test binary runs with.
func mappedLibc(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("/proc/self/maps")
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.Lines(string(data)) {
		fields := strings.Fields(line)
		if len(fields) == 6 && strings.Contains(fields[5], "/libc.so") {
			return fields[5]
		}
	}
	t.Fatal("no libc mapped")
	return ""
}
