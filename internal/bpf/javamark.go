package bpf

import (
	"bufio"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	bpf "github.com/aquasecurity/libbpfgo"
)

// The Java mark's BPF side (internal/c/javamark.c, M3, task td4).
const (
	javaMarkFlagName  = "IORMESH_JAVA_MARK"
	javaMarkMapName   = "java_mark_map"
	javaMarkEnterProg = "iormesh_java_native_enter"
	javaMarkExitProg  = "iormesh_java_native_exit"
)

// javaLibs are the JDK libraries whose natives the hook table reaches
// (S1: java.io's natives are libjava's, sun.nio.ch's and sun.nio.fs's are
// libnio's; libnet is searched for the socket natives a JDK may keep there).
// All live in the JDK's lib directory, next to libjava.so.
var javaLibs = []string{"libjava.so", "libnio.so", "libnet.so"}

// JavaNative is one JDK native whose syscalls the mark names: its JNI
// symbol (agentspool.Hook.Symbol) and the cookie its uprobe carries, which
// a *types.JavaMarkEvent reports. Cookies are the caller's; 0 is reserved
// (the mark's "innermost native unknown").
type JavaNative struct {
	Symbol string
	Cookie uint64
}

// setJavaMarks sets the load-time switch and, when it is off, shrinks
// java_mark_map to one entry and keeps the uprobe programs from loading:
// a session without the Java join neither pays the map's memory nor
// needs a kernel with uprobe_multi links (Linux 6.6).
func (t *Tracer) setJavaMarks(on bool) error {
	flag := uint32(0)
	if on {
		flag = 1
	}
	if err := t.module.InitGlobalVariable(javaMarkFlagName, flag); err != nil {
		return fmt.Errorf("set %s: %w", javaMarkFlagName, err)
	}
	if on {
		return nil
	}
	m, err := t.module.GetMap(javaMarkMapName)
	if err != nil {
		return fmt.Errorf("get %s: %w", javaMarkMapName, err)
	}
	if err := m.SetMaxEntries(1); err != nil {
		return fmt.Errorf("shrink %s: %w", javaMarkMapName, err)
	}
	for _, name := range []string{javaMarkEnterProg, javaMarkExitProg} {
		prog, err := t.module.GetProgram(name)
		if err != nil {
			return fmt.Errorf("program %s: %w", name, err)
		}
		if err := prog.SetAutoload(false); err != nil {
			return fmt.Errorf("no autoload for %s: %w", name, err)
		}
	}
	return nil
}

// AttachJavaMarks attaches the mark's uprobes to the natives of the JVM
// pid (Config.JavaMarks must be on): an entry and a return probe per
// native, in the libraries of the JDK the JVM runs (found through its
// libjava.so mapping), scoped to pid so no other process traps. missing
// lists the natives found in none of the libraries: their syscalls carry
// no mark. Call it from one goroutine, before Close (libbpfgo's link
// bookkeeping is not locked, see attachProbes).
func (t *Tracer) AttachJavaMarks(pid uint32, natives []JavaNative) (missing []string, err error) {
	if t.closed() {
		return nil, errClosed
	}
	dir, err := jdkLibDir(pid)
	if err != nil {
		return nil, err
	}
	found := map[string]bool{}
	for _, lib := range javaLibs {
		path := filepath.Join(dir, lib)
		offsets, cookies, names := nativeOffsets(path, natives)
		if len(offsets) == 0 {
			continue
		}
		if err := t.attachJavaLib(pid, path, offsets, cookies); err != nil {
			return nil, err
		}
		for _, n := range names {
			found[n] = true
		}
	}
	for _, n := range natives {
		if !found[n.Symbol] {
			missing = append(missing, n.Symbol)
		}
	}
	return missing, nil
}

// attachJavaLib attaches the entry and the return probe to one library's
// natives. The entry goes first: a native entered between the two attaches
// pushes a frame no return pops, which the next return at its depth or
// above clears (javamark.c), while the reverse order could pop an outer
// native's frame at a return whose entry was never seen.
func (t *Tracer) attachJavaLib(pid uint32, path string, offsets, cookies []uint64) error {
	for _, p := range []struct {
		name string
		ret  bool
	}{{javaMarkEnterProg, false}, {javaMarkExitProg, true}} {
		prog, err := t.module.GetProgram(p.name)
		if err != nil {
			return fmt.Errorf("program %s: %v", p.name, err)
		}
		var link *bpf.BPFLink
		if p.ret {
			link, err = prog.AttachURetprobeMulti(int(pid), path, offsets, cookies)
		} else {
			link, err = prog.AttachUprobeMulti(int(pid), path, offsets, cookies)
		}
		if err != nil {
			return fmt.Errorf("attach %s to %s: %w", p.name, path, err)
		}
		t.links = append(t.links, link)
	}
	return nil
}

// jdkLibDir is the lib directory of the JDK pid runs: the directory of
// its libjava.so mapping, reached through /proc/<pid>/root, so the path
// names the JVM's file even when its mount namespace differs from the
// tracer's. libjava.so is mapped before any Java code runs, so before
// the agent writes its ring; libnio and libnet may be mapped only later,
// which a uprobe does not mind: it attaches to the file.
func jdkLibDir(pid uint32) (string, error) {
	root := fmt.Sprintf("/proc/%d", pid)
	f, err := os.Open(root + "/maps")
	if err != nil {
		return "", fmt.Errorf("java mark: %w", err)
	}
	defer func() { _ = f.Close() }()
	lib, err := libjavaPath(f)
	if err != nil {
		return "", fmt.Errorf("java mark, pid %d: %w", pid, err)
	}
	return filepath.Join(root, "root", filepath.Dir(lib)), nil
}

// errNoLibjava: the process maps no libjava.so — not a JVM, or one whose
// JDK is statically linked (no Java_* symbols to probe; S1's untested case).
var errNoLibjava = errors.New("no libjava.so mapped")

// libjavaPath returns the path of the libjava.so mapping in a
// /proc/<pid>/maps listing.
func libjavaPath(maps io.Reader) (string, error) {
	sc := bufio.NewScanner(maps)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		// address perms offset dev inode path; a path may hold spaces,
		// which no JDK install needs, so only the sixth field is read.
		if len(fields) == 6 && filepath.Base(fields[5]) == "libjava.so" && filepath.IsAbs(fields[5]) {
			return fields[5], nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", errNoLibjava
}

// nativeOffsets returns the file offsets (what a uprobe attaches at) and
// cookies of the natives defined in the library at path, and their
// symbols. A library that is missing or does not parse yields none: its
// natives are then reported missing, never guessed.
func nativeOffsets(path string, natives []JavaNative) (offsets, cookies []uint64, names []string) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, nil
	}
	defer func() { _ = f.Close() }()
	syms := safeFuncOffsets(f)
	for _, n := range natives {
		if off, ok := syms[n.Symbol]; ok {
			offsets = append(offsets, off)
			cookies = append(cookies, n.Cookie)
			names = append(names, n.Symbol)
		}
	}
	return offsets, cookies, names
}

// safeFuncOffsets is funcOffsets for a file the traced process controls
// (its mount namespace decides what /proc/<pid>/root shows): debug/elf is
// not hardened against hostile input and may panic, which would end the
// tracer. A panic, like a parse error, yields no symbols.
func safeFuncOffsets(r io.ReaderAt) (syms map[string]uint64) {
	defer func() {
		if recover() != nil {
			syms = nil
		}
	}()
	syms, _ = funcOffsets(r)
	return syms
}

// funcOffsets maps the exported function symbols of the ELF file r to
// their file offsets: the symbol's address, moved from its section's
// address to the section's place in the file (libbpf's elf_sym_offset).
func funcOffsets(r io.ReaderAt) (map[string]uint64, error) {
	f, err := elf.NewFile(r)
	if err != nil {
		return nil, err
	}
	dyn, err := f.DynamicSymbols()
	if err != nil {
		return nil, err
	}
	out := make(map[string]uint64, len(dyn))
	for _, s := range dyn {
		if elf.ST_TYPE(s.Info) != elf.STT_FUNC || s.Value == 0 || int(s.Section) >= len(f.Sections) {
			continue
		}
		sec := f.Sections[s.Section]
		if s.Value < sec.Addr || s.Value-sec.Addr >= sec.Size {
			continue
		}
		out[s.Name] = s.Value - sec.Addr + sec.Offset
	}
	return out, nil
}
