package stacks

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"
)

// mapping is one executable line of /proc/<pid>/maps: the address range,
// the file offset it starts at, the backing file's device and inode (the
// module cache key: one path may name different files in different mount
// namespaces) and its path ("" for anonymous memory such as JIT code,
// "[vdso]" for pseudo mappings).
type mapping struct {
	start, end, pgoff uint64
	dev               string
	inode             uint64
	path              string
}

// isFile reports whether the mapping is backed by a file whose ELF can be
// read: not anonymous, not a pseudo mapping, not a deleted file.
func (m mapping) isFile() bool {
	return m.inode != 0 && strings.HasPrefix(m.path, "/") && !strings.HasSuffix(m.path, " (deleted)")
}

// key names the mapped file for the module cache: device and inode.
func (m mapping) key() string {
	return m.dev + ":" + strconv.FormatUint(m.inode, 10)
}

// parseMaps reads the executable mappings of a /proc/<pid>/maps file;
// only they can hold an instruction address. A malformed line is skipped:
// a frame it would have covered stays unresolved, never misattributed.
func parseMaps(data []byte) []mapping {
	var out []mapping
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	for sc.Scan() {
		if m, ok := parseMapsLine(sc.Text()); ok {
			out = append(out, m)
		}
	}
	return out
}

// parseMapsLine parses "start-end perms offset dev inode [path]"; the path
// is the rest of the line and may contain spaces.
func parseMapsLine(line string) (mapping, bool) {
	f := strings.Fields(line)
	if len(f) < 5 || len(f[1]) < 3 || f[1][2] != 'x' {
		return mapping{}, false
	}
	lo, hi, ok := strings.Cut(f[0], "-")
	if !ok {
		return mapping{}, false
	}
	var m mapping
	var err [4]error
	m.start, err[0] = strconv.ParseUint(lo, 16, 64)
	m.end, err[1] = strconv.ParseUint(hi, 16, 64)
	m.pgoff, err[2] = strconv.ParseUint(f[2], 16, 64)
	m.inode, err[3] = strconv.ParseUint(f[4], 10, 64)
	for _, e := range err {
		if e != nil {
			return mapping{}, false
		}
	}
	m.dev = f[3]
	m.path = afterFields(line, 5)
	return m, true
}

// afterFields returns line without its first n whitespace-separated
// fields and the blanks after them: the maps path, spaces inside it kept.
func afterFields(line string, n int) string {
	rest := line
	for range n {
		rest = strings.TrimLeft(rest, " \t")
		i := strings.IndexAny(rest, " \t")
		if i < 0 {
			return ""
		}
		rest = rest[i:]
	}
	return strings.TrimSpace(rest)
}

// find returns the mapping containing addr.
func find(maps []mapping, addr uint64) (mapping, bool) {
	for _, m := range maps {
		if addr >= m.start && addr < m.end {
			return m, true
		}
	}
	return mapping{}, false
}
