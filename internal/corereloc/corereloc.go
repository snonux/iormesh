// Package corereloc shows how libbpf CO-RE relocates a BPF object's
// programs against a given kernel BTF, without that kernel: the el9
// record-layout test (internal/btf_root_test.go, task of4) loads
// iormesh.bpf.o on a 7.x host with Rocky 9's BTF and reads the field
// offsets the programs ended up with. VerifiedInsns reads the running
// kernel's verifier cost of chosen programs for the mmsg headroom test
// (internal/msgcalls_root_test.go, task oh4).
//
// Why not libbpfgo and the loaded programs' xlated instructions: relocated
// against el9's BTF, the programs' BTF-typed task_struct loads
// (bpf_get_current_task_btf()->tgid, pidns.c) carry el9's offsets, which
// the host's verifier checks against the host's task_struct and rejects.
// libbpf relocates every program before it loads any (bpf_object_load:
// bpf_object__relocate, then bpf_object__load_progs) and keeps the
// relocated instructions until the object is closed, so they can be read
// with bpf_program__insns whether the kernel then accepts them or not.
// libbpfgo does not expose that call; this package is the few lines of cgo
// that do, and nothing but tests imports it (internal/buildgate
// enforces that, task bg4).
package corereloc

/*
#include <stdarg.h>
#include <stdlib.h>
#include <bpf/bpf.h>
#include <bpf/libbpf.h>

// corereloc_quiet drops libbpf's output while Relocate loads: a rejected
// program's verifier log is the expected outcome there, not news.
static int corereloc_quiet(enum libbpf_print_level level, const char *fmt, va_list args) {
    return 0;
}

static libbpf_print_fn_t corereloc_silence(void) {
    return libbpf_set_print(corereloc_quiet);
}

static void corereloc_restore(libbpf_print_fn_t fn) {
    libbpf_set_print(fn);
}

// corereloc_open opens the object in buf, relocating against btf_path, or
// against the running kernel's BTF when it is NULL.
static struct bpf_object *corereloc_open(const void *buf, size_t size, const char *name, const char *btf_path) {
    LIBBPF_OPTS(bpf_object_open_opts, opts, .object_name = name, .btf_custom_path = btf_path);

    return bpf_object__open_mem(buf, size, &opts);
}

// corereloc_verified_insns returns how many instructions the verifier
// processed for the loaded program fd (bpf_prog_info.verified_insns,
// Linux 5.16+), or -1 when the info cannot be read.
static long long corereloc_verified_insns(int fd) {
    struct bpf_prog_info info = {};
    __u32 len = sizeof(info);

    if (bpf_prog_get_info_by_fd(fd, &info, &len) != 0)
        return -1;
    return info.verified_insns;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

// insnSize is the size of one struct bpf_insn.
const insnSize = 8

// Result is what Relocate found.
type Result struct {
	// Insns are each requested program's instructions as libbpf relocated
	// them, raw struct bpf_insn bytes in the host's byte order.
	Insns map[string][]byte
	// LoadErr is the kernel's verdict on loading them, for the log: an
	// error is expected when the BTF is not the running kernel's.
	LoadErr error
}

// Relocate opens the BPF object obj (named name), relocates the programs
// named in progs - and no other: every other program's autoload is off -
// against the kernel BTF at btfPath ("" = the running kernel's), tries to
// load them and returns their relocated instructions. It needs the
// privilege to load BPF programs: libbpf probes the kernel before it
// relocates. An error is returned only when no instructions can be read;
// an object whose load stopped before relocation returns the unrelocated
// instructions, which any offset check then rejects.
func Relocate(obj []byte, name, btfPath string, progs []string) (Result, error) {
	buf := C.CBytes(obj)
	defer C.free(buf)
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	var cbtf *C.char
	if btfPath != "" {
		cbtf = C.CString(btfPath)
		defer C.free(unsafe.Pointer(cbtf))
	}
	old := C.corereloc_silence()
	defer C.corereloc_restore(old)
	o, err := C.corereloc_open(buf, C.size_t(len(obj)), cname, cbtf)
	if o == nil {
		return Result{}, fmt.Errorf("open %s: %w", name, err)
	}
	defer C.bpf_object__close(o)
	if err := keepOnly(o, progs); err != nil {
		return Result{}, err
	}
	res := Result{Insns: map[string][]byte{}}
	if rc := C.bpf_object__load(o); rc != 0 {
		res.LoadErr = fmt.Errorf("load %s: %w", name, syscall.Errno(-rc))
	}
	for _, p := range progs {
		insns, err := programInsns(o, p)
		if err != nil {
			return Result{}, err
		}
		res.Insns[p] = insns
	}
	return res, nil
}

// VerifiedInsns loads the programs named in progs of the BPF object obj
// (named name) on the running kernel, against its own BTF, and returns how
// many instructions its verifier processed for each (verified_insns,
// Linux 5.16+; task oh4's headroom check of the mmsg loop). That is this
// kernel's verifier, not another one's: on another kernel the count
// differs. A load the kernel refuses is an error.
func VerifiedInsns(obj []byte, name string, progs []string) (map[string]int64, error) {
	buf := C.CBytes(obj)
	defer C.free(buf)
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	o, err := C.corereloc_open(buf, C.size_t(len(obj)), cname, nil)
	if o == nil {
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	defer C.bpf_object__close(o)
	if err := keepOnly(o, progs); err != nil {
		return nil, err
	}
	if rc := C.bpf_object__load(o); rc != 0 {
		return nil, fmt.Errorf("load %s: %w", name, syscall.Errno(-rc))
	}
	out := map[string]int64{}
	for _, p := range progs {
		cp := C.CString(p)
		prog := C.bpf_object__find_program_by_name(o, cp)
		C.free(unsafe.Pointer(cp))
		if prog == nil {
			return nil, errors.New("no program " + p)
		}
		n := int64(C.corereloc_verified_insns(C.bpf_program__fd(prog)))
		if n < 0 {
			return nil, errors.New("no program info for " + p)
		}
		out[p] = n
	}
	return out, nil
}

// keepOnly turns autoload off for every program of o not in progs, so
// libbpf neither relocates nor loads them.
func keepOnly(o *C.struct_bpf_object, progs []string) error {
	want := map[string]bool{}
	for _, p := range progs {
		want[p] = true
	}
	for prog := C.bpf_object__next_program(o, nil); prog != nil; prog = C.bpf_object__next_program(o, prog) {
		name := C.GoString(C.bpf_program__name(prog))
		if want[name] {
			delete(want, name)
			continue
		}
		if rc := C.bpf_program__set_autoload(prog, false); rc != 0 {
			return fmt.Errorf("autoload off %s: %w", name, syscall.Errno(-rc))
		}
	}
	for name := range want {
		return fmt.Errorf("no program %s in the object", name)
	}
	return nil
}

// programInsns copies prog's current instructions out of libbpf.
func programInsns(o *C.struct_bpf_object, name string) ([]byte, error) {
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	prog := C.bpf_object__find_program_by_name(o, cname)
	if prog == nil {
		return nil, errors.New("no program " + name)
	}
	n := int(C.bpf_program__insn_cnt(prog))
	insns := C.bpf_program__insns(prog)
	if n == 0 || insns == nil {
		return nil, errors.New("program " + name + " has no instructions")
	}
	return C.GoBytes(unsafe.Pointer(insns), C.int(n*insnSize)), nil
}
