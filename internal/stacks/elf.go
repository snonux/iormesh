package stacks

import (
	"bytes"
	"cmp"
	"debug/elf"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"slices"
)

// ntGNUBuildID is the note type of a GNU build id (elf.h NT_GNU_BUILD_ID).
const ntGNUBuildID = 3

// module is what symbolization needs of one ELF file: its loadable
// segments (to turn a file offset into the address its symbols use), its
// function symbols sorted by address and its build id.
type module struct {
	loads   []elf.ProgHeader
	syms    []symbol
	buildID string
}

// symbol is one function symbol of a module.
type symbol struct {
	start, size uint64
	name        string
}

// readModule parses the ELF file r. Both symbol tables are read: .symtab
// (Go binaries and unstripped C) and .dynsym (what a stripped shared
// library such as libc keeps); a file with neither still yields its build
// id and segments, so frames keep module and offset.
func readModule(r io.ReaderAt) (*module, error) {
	f, err := elf.NewFile(r)
	if err != nil {
		return nil, fmt.Errorf("elf: %w", err)
	}
	// No f.Close: an elf.File from NewFile owns nothing to close; the
	// caller closes r.
	m := &module{buildID: buildID(f)}
	for _, p := range f.Progs {
		if p.Type == elf.PT_LOAD {
			m.loads = append(m.loads, p.ProgHeader)
		}
	}
	static, _ := f.Symbols()
	dynamic, _ := f.DynamicSymbols()
	for _, s := range slices.Concat(static, dynamic) {
		if elf.ST_TYPE(s.Info) == elf.STT_FUNC && s.Value != 0 && s.Name != "" {
			m.syms = append(m.syms, symbol{start: s.Value, size: s.Size, name: s.Name})
		}
	}
	slices.SortFunc(m.syms, func(a, b symbol) int {
		if a.start != b.start {
			return cmp.Compare(a.start, b.start)
		}
		return cmp.Compare(b.size, a.size) // the widest first, the innermost last
	})
	return m, nil
}

// vaddr turns a file offset into the virtual address the symbol tables
// use, through the PT_LOAD segment that maps it.
func (m *module) vaddr(off uint64) (uint64, bool) {
	for _, p := range m.loads {
		if off >= p.Off && off < p.Off+p.Filesz {
			return off - p.Off + p.Vaddr, true
		}
	}
	return 0, false
}

// symbolize names the function containing the file offset off, as
// "name+0xN". A symbol without a size (hand-written assembly) is taken to
// run up to the next one.
func (m *module) symbolize(off uint64) string {
	va, ok := m.vaddr(off)
	if !ok {
		return ""
	}
	i, _ := slices.BinarySearchFunc(m.syms, va+1, func(s symbol, t uint64) int {
		return cmp.Compare(s.start, t)
	})
	// syms[i-1] is the last symbol starting at or before va.
	if i == 0 {
		return ""
	}
	s := m.syms[i-1]
	if s.size != 0 && va >= s.start+s.size {
		return ""
	}
	return fmt.Sprintf("%s+0x%x", s.name, va-s.start)
}

// buildID reads the NT_GNU_BUILD_ID note, from its usual section or any
// PT_NOTE segment; "" when the file has none (a Go binary built without
// an external linker, say).
func buildID(f *elf.File) string {
	if s := f.Section(".note.gnu.build-id"); s != nil {
		if data, err := s.Data(); err == nil {
			if id := noteBuildID(data, f.ByteOrder); id != "" {
				return id
			}
		}
	}
	for _, p := range f.Progs {
		if p.Type != elf.PT_NOTE {
			continue
		}
		data, err := io.ReadAll(p.Open())
		if err == nil {
			if id := noteBuildID(data, f.ByteOrder); id != "" {
				return id
			}
		}
	}
	return ""
}

// noteBuildID walks ELF notes (namesz, descsz, type, name, desc, each
// padded to 4 bytes) for the GNU build id.
func noteBuildID(data []byte, bo binary.ByteOrder) string {
	for len(data) >= 12 {
		namesz, descsz, typ := bo.Uint32(data), bo.Uint32(data[4:]), bo.Uint32(data[8:])
		name, desc := align4(namesz), align4(descsz)
		if uint64(len(data)) < 12+name+desc {
			return ""
		}
		if typ == ntGNUBuildID && bytes.Equal(data[12:12+namesz], []byte("GNU\x00")) {
			return hex.EncodeToString(data[12+name : 12+name+uint64(descsz)])
		}
		data = data[12+name+desc:]
	}
	return ""
}

func align4(n uint32) uint64 {
	return (uint64(n) + 3) &^ 3
}
