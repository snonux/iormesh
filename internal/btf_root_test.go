package internal_test

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"iormesh/internal"
	"iormesh/internal/corereloc"
)

// el9BTF is the Rocky 9.9 kernel's BTF (5.14.0-741.22.1.el9_9.x86_64),
// trimmed by `bpftool gen min_core_btf` to the types iormesh.bpf.o's CO-RE
// relocations name. It came from the kernel-core RPM's vmlinuz
// (scripts/extract-vmlinux, then the .BTF section); the same trim of iom1's
// /sys/kernel/btf/vmlinux is byte-identical. Regenerate it when the
// programs below gain relocations against types the file lacks
// (docs/testing.md, "el9 record layout").
var el9BTF = filepath.Join("testdata", "btf", "rocky9.9-5.14.0-741.22.1.el9_9.x86_64.min.btf")

// rawSyscallPrograms are the programs whose raw_syscalls record loads
// TestEl9RecordLayout checks, each with the record field it reads: the two
// dispatchers read id, close's enter handler args[0] and its exit handler
// ret.
var rawSyscallPrograms = map[string]string{
	"handle_raw_sys_enter":   "id",
	"handle_raw_sys_exit":    "id",
	"handle_sys_enter_close": "args",
	"handle_sys_exit_close":  "ret",
}

// TestEl9RecordLayout is the el9 regression test of task lb4 that runs on
// any kernel (task of4). RHEL 9's trace_entry is 12 bytes, which moves the
// raw_syscalls record's id to offset 16 and args and ret to 24; upstream's
// is 8 (id 8, args/ret 16). dispatch.c's record structs are CO-RE flavors,
// so libbpf relocates each field to the running kernel's offset. Relocating
// the object against the committed el9 BTF in place of the host's (libbpf's
// btf_custom_path) must therefore yield record loads at 16 and 24, and
// relocating it against the host BTF the offsets of the host's own tracefs
// format file - which on a 7.x host are the other layout, so a
// fixed-offset struct cannot pass both.
func TestEl9RecordLayout(t *testing.T) {
	requireRoot(t)
	el9 := ctxLoadOffsets(t, el9BTF)
	want := map[string][]int16{"id": {16}, "args": {24}, "ret": {24}}
	for prog, field := range rawSyscallPrograms {
		if !reflect.DeepEqual(el9[prog], want[field]) {
			t.Errorf("el9 BTF: %s reads the record at %v, want %s at %v", prog, el9[prog], field, want[field])
		}
	}
	host := ctxLoadOffsets(t, "")
	hostWant := map[string][]int16{
		"id":   {rawSyscallsOffset(t, "sys_enter", "id")},
		"args": {rawSyscallsOffset(t, "sys_enter", "args")},
		"ret":  {rawSyscallsOffset(t, "sys_exit", "ret")},
	}
	for prog, field := range rawSyscallPrograms {
		if !reflect.DeepEqual(host[prog], hostWant[field]) {
			t.Errorf("host BTF: %s reads the record at %v, want %s at %v (tracefs format)",
				prog, host[prog], field, hostWant[field])
		}
	}
	t.Logf("record loads with el9 BTF %v, with host BTF %v", el9, host)
}

// ctxLoadOffsets relocates rawSyscallPrograms in the embedded object
// against btfPath ("" = the host's BTF) and returns each one's record load
// offsets. With a foreign BTF the host's kernel may refuse the programs
// (internal/corereloc says why); the relocation is what is checked.
func ctxLoadOffsets(t *testing.T, btfPath string) map[string][]int16 {
	t.Helper()
	progs := make([]string, 0, len(rawSyscallPrograms))
	for name := range rawSyscallPrograms {
		progs = append(progs, name)
	}
	res, err := corereloc.Relocate(internal.EmbeddedBPFObject(), "iormesh.bpf.o", btfPath, progs)
	if err != nil {
		t.Fatalf("relocate against BTF %q: %v", btfPath, err)
	}
	if res.LoadErr != nil {
		t.Logf("BTF %q: %v (the relocation is what is checked)", btfPath, res.LoadErr)
	}
	got := map[string][]int16{}
	for name, insns := range res.Insns {
		got[name] = recordLoads(insns)
	}
	return got
}

// recordLoads returns the sorted, distinct offsets of the memory loads
// through a register holding the context (r1 at entry, every register
// copied from it, and every register reloaded from a stack slot it was
// spilled to), at offset 8 and beyond: the record's fields. The first 8
// bytes are the pt_regs pointer the stack helpers read, not a field. The
// scan is linear and forgets the context at each exit (a subprogram's r1
// is its own): the dispatchers and close's handlers save their context once
// at entry, to a register or a stack slot, which a linear scan follows.
func recordLoads(insns []byte) []int16 {
	const (
		classMask = 0x07
		classLD   = 0x00
		classLDX  = 0x01
		classALU  = 0x04
		classALU6 = 0x07
		modeMask  = 0xe0
		modeMEM   = 0x60
		mov64X    = 0xbf
		call      = 0x85
		exit      = 0x95
		ldImm64   = 0x18
		stxDW     = 0x7b
		frame     = 10
	)
	ctx := map[byte]bool{1: true}
	spilled := map[int16]bool{}
	seen := map[int16]bool{}
	for i := 0; i+8 <= len(insns); i += 8 {
		code, dst, src := insns[i], insns[i+1]&0x0f, insns[i+1]>>4
		off := int16(binary.LittleEndian.Uint16(insns[i+2:]))
		switch {
		case code == stxDW && dst == frame:
			spilled[off] = ctx[src]
		case code&classMask == classLDX && code&modeMask == modeMEM && src == frame:
			ctx[dst] = spilled[off]
		case code&classMask == classLDX && code&modeMask == modeMEM:
			if ctx[src] && off >= 8 {
				seen[off] = true
			}
			delete(ctx, dst)
		case code == mov64X:
			ctx[dst] = ctx[src]
		case code == call:
			for r := byte(0); r <= 5; r++ {
				delete(ctx, r)
			}
		case code == exit:
			ctx, spilled = map[byte]bool{}, map[int16]bool{}
		case code == ldImm64:
			delete(ctx, dst)
			i += 8
		case code&classMask == classLD || code&classMask == classALU || code&classMask == classALU6:
			delete(ctx, dst)
		}
	}
	offs := make([]int16, 0, len(seen))
	for off := range seen {
		offs = append(offs, off)
	}
	sort.Slice(offs, func(a, b int) bool { return offs[a] < offs[b] })
	return offs
}

// rawSyscallsOffset reads field's offset in the host's raw_syscalls event
// record from its tracefs format file.
func rawSyscallsOffset(t *testing.T, event, field string) int16 {
	t.Helper()
	for _, root := range []string{"/sys/kernel/tracing", "/sys/kernel/debug/tracing"} {
		off, err := formatOffset(filepath.Join(root, "events", "raw_syscalls", event, "format"), field)
		if err == nil {
			return off
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("raw_syscalls/%s: %v", event, err)
		}
	}
	t.Fatalf("raw_syscalls/%s: no tracefs format file", event)
	return 0
}

// formatOffset returns the offset of field in a tracefs event format file
// ("field:long id;\toffset:8;\tsize:8;..."; an array's name carries its
// length, "args[6]"). A missing file or field is os.ErrNotExist.
func formatOffset(path, field string) (int16, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.Split(strings.TrimSpace(line), ";")
		if len(parts) < 2 || !strings.HasPrefix(parts[0], "field:") {
			continue
		}
		decl := strings.Fields(parts[0])
		name, _, _ := strings.Cut(decl[len(decl)-1], "[")
		if name != field {
			continue
		}
		off, err := strconv.ParseInt(strings.TrimPrefix(strings.TrimSpace(parts[1]), "offset:"), 10, 16)
		if err != nil {
			return 0, fmt.Errorf("%s: field %s: %w", path, field, err)
		}
		return int16(off), nil
	}
	return 0, fmt.Errorf("%s: field %s: %w", path, field, os.ErrNotExist)
}
