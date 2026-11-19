package bpf

import (
	"bufio"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// javaLibs are the JDK libraries whose natives the hook table reaches
// (S1: java.io's natives are libjava's, sun.nio.ch's and sun.nio.fs's are
// libnio's; libnet is searched for the socket natives a JDK may keep there).
// All live in the JDK's lib directory, next to libjava.so.
var javaLibs = []string{"libjava.so", "libnio.so", "libnet.so"}

// errNoLibjava: the process maps no libjava.so — not a JVM, or one whose
// JDK is statically linked (no Java_* symbols to probe; S1's untested case).
var errNoLibjava = errors.New("no libjava.so mapped")

// libMapping is a file mapping of /proc/<pid>/maps: its address range,
// which names it under /proc/<pid>/map_files, and its path.
type libMapping struct {
	start, end uint64
	path       string
}

// openJavaLibs opens the JDK libraries of process pid under procRoot
// (/proc, or a fake one in tests), each checked to be a regular file. The
// traced process controls what these paths lead to, so nothing is opened
// by plain path: a library the JVM has mapped is opened through
// /proc/<pid>/map_files — the very file mapped — and one it has not mapped
// yet (libnio and libnet load lazily) inside the JVM's root with
// RESOLVE_IN_ROOT, so an absolute symlink resolves against the JVM's root,
// never the tracer's. Every open is non-blocking, so a FIFO planted in the
// lib directory cannot hang the tracer. A library that is missing or not
// a regular file is left out; no libjava.so mapping is an error.
func openJavaLibs(procRoot string, pid uint32) ([]*os.File, error) {
	dir := filepath.Join(procRoot, strconv.FormatUint(uint64(pid), 10))
	data, err := os.ReadFile(filepath.Join(dir, "maps"))
	if err != nil {
		return nil, fmt.Errorf("java mark: %w", err)
	}
	mapped := libMappings(data)
	java, ok := mapped["libjava.so"]
	if !ok {
		return nil, fmt.Errorf("java mark, pid %d: %w", pid, errNoLibjava)
	}
	var files []*os.File
	for _, lib := range javaLibs {
		var f *os.File
		if m, ok := mapped[lib]; ok && filepath.Dir(m.path) == filepath.Dir(java.path) {
			f, err = openRegular(-1, filepath.Join(dir, "map_files", fmt.Sprintf("%x-%x", m.start, m.end)), 0)
		} else {
			f, err = openInRoot(filepath.Join(dir, "root"), filepath.Join(filepath.Dir(java.path), lib))
		}
		if err == nil {
			files = append(files, f)
		}
	}
	return files, nil
}

// libMappings maps the base names of javaLibs to their first mapping in a
// /proc/<pid>/maps listing. Only absolute paths count: a deleted file's
// "(deleted)" suffix or a pseudo-file never names a library.
func libMappings(maps []byte) map[string]libMapping {
	out := map[string]libMapping{}
	sc := bufio.NewScanner(strings.NewReader(string(maps)))
	for sc.Scan() {
		// address perms offset dev inode path; a path may hold spaces,
		// which no JDK install needs, so only the sixth field is read.
		fields := strings.Fields(sc.Text())
		if len(fields) != 6 || !filepath.IsAbs(fields[5]) {
			continue
		}
		base := filepath.Base(fields[5])
		lo, hi, ok := strings.Cut(fields[0], "-")
		start, err1 := strconv.ParseUint(lo, 16, 64)
		end, err2 := strconv.ParseUint(hi, 16, 64)
		if _, seen := out[base]; seen || !ok || err1 != nil || err2 != nil {
			continue
		}
		out[base] = libMapping{start: start, end: end, path: fields[5]}
	}
	return out
}

// openInRoot opens path (absolute in the process's view) inside root with
// RESOLVE_IN_ROOT, as a non-blocking regular file.
func openInRoot(root, path string) (*os.File, error) {
	rfd, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(rfd) }()
	return openRegular(rfd, strings.TrimPrefix(path, "/"), unix.RESOLVE_IN_ROOT|unix.RESOLVE_NO_MAGICLINKS)
}

// openRegular opens path (relative to dirfd, or absolute with dirfd -1)
// read-only and non-blocking with resolve flags, and keeps it only if it
// is a regular file.
func openRegular(dirfd int, path string, resolve uint64) (*os.File, error) {
	if dirfd < 0 {
		dirfd = unix.AT_FDCWD
	}
	how := unix.OpenHow{Flags: unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOCTTY,
		Resolve: resolve}
	fd, err := unix.Openat2(dirfd, path, &how)
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("%s: not a regular file", path)
	}
	return os.NewFile(uintptr(fd), path), nil
}

// nativeOffsets returns the file offsets (what a uprobe attaches at) and
// cookies of the natives defined in the library f, and their symbols. A
// library that does not parse yields none: its natives are then reported
// missing, never guessed.
func nativeOffsets(f io.ReaderAt, natives []JavaNative) (offsets, cookies []uint64, names []string) {
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

// safeFuncOffsets is funcOffsets for a file the traced process controls:
// debug/elf is not hardened against hostile input and may panic, which
// would end the tracer. A panic, like a parse error, yields no symbols.
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
