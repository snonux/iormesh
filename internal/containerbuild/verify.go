package containerbuild

import (
	"debug/elf"
	"debug/macho"
	"fmt"
	"strings"
)

// elfMachines and machoCPUs map a GOARCH to the header value its binaries
// carry.
var (
	elfMachines = map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}
	machoCPUs   = map[string]macho.Cpu{"amd64": macho.CpuAmd64, "arm64": macho.CpuArm64}
)

// VerifyBinary checks that the file at path really is b: the object format
// of b.GOOS, the CPU of b.GOARCH and, for a static binary, no ELF
// interpreter. A cross build that silently fell back to the host arch, or
// a cgo link that went dynamic, fails here rather than on a server.
func VerifyBinary(path string, b Binary) error {
	switch b.GOOS {
	case "linux":
		return verifyELF(path, b)
	case "darwin":
		return verifyMachO(path, b)
	default:
		return fmt.Errorf("%s: no check for GOOS %q", path, b.GOOS)
	}
}

func verifyELF(path string, b Binary) error {
	f, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("%s: not an ELF binary: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	if want, ok := elfMachines[b.GOARCH]; !ok || f.Machine != want {
		return fmt.Errorf("%s: ELF machine %v, want GOARCH %s", path, f.Machine, b.GOARCH)
	}
	if !b.Static {
		return nil
	}
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			return fmt.Errorf("%s: dynamically linked (has an ELF interpreter), want static", path)
		}
	}
	return nil
}

func verifyMachO(path string, b Binary) error {
	f, err := macho.Open(path)
	if err != nil {
		return fmt.Errorf("%s: not a Mach-O binary: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	if want, ok := machoCPUs[b.GOARCH]; !ok || f.Cpu != want {
		return fmt.Errorf("%s: Mach-O CPU %v, want GOARCH %s", path, f.Cpu, b.GOARCH)
	}
	return nil
}

// CheckSmokeOutput judges a --help smoke run: the binary must have started
// on its target platform and printed its usage.
func CheckSmokeOutput(name, stdout string) error {
	if !strings.HasPrefix(stdout, "usage:") {
		return fmt.Errorf("%s --help: want usage on stdout, got %q", name, stdout)
	}
	return nil
}
