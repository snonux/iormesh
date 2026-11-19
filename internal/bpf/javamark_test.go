package bpf

import (
	"debug/elf"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

// TestLibjavaPath: the libjava.so mapping of a /proc/<pid>/maps listing
// names the JDK's lib directory; a listing without one (not a JVM) says
// so, and a relative or look-alike path is not taken for it.
func TestLibjavaPath(t *testing.T) {
	const jvm = `55d0c0a00000-55d0c0a01000 r--p 00000000 00:21 123 /usr/lib/jvm/java-25/bin/java
7f1c2a000000-7f1c2a010000 r--p 00000000 00:21 456 /usr/lib/jvm/java-25/lib/libjimage.so
7f1c2b000000-7f1c2b008000 r--p 00000000 00:21 789 /usr/lib/jvm/java-25/lib/libjava.so
7f1c2b008000-7f1c2b018000 r-xp 00008000 00:21 789 /usr/lib/jvm/java-25/lib/libjava.so
7ffd00000000-7ffd00021000 rw-p 00000000 00:00 0 [stack]
`
	got, err := libjavaPath(strings.NewReader(jvm))
	if err != nil || got != "/usr/lib/jvm/java-25/lib/libjava.so" {
		t.Fatalf("libjavaPath = %q, %v", got, err)
	}
	for name, maps := range map[string]string{
		"no JVM":     "55d0c0a00000-55d0c0a01000 r--p 00000000 00:21 123 /usr/bin/bash\n",
		"look-alike": "7f1c2b000000-7f1c2b008000 r--p 00000000 00:21 789 /opt/libjava.so.bak\n",
		"relative":   "7f1c2b000000-7f1c2b008000 r--p 00000000 00:21 789 lib/libjava.so\n",
	} {
		if got, err := libjavaPath(strings.NewReader(maps)); !errors.Is(err, errNoLibjava) {
			t.Errorf("%s: libjavaPath = %q, %v; want errNoLibjava", name, got, err)
		}
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
	_, _, names := nativeOffsets(libc, []JavaNative{{Symbol: "write", Cookie: 1},
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
