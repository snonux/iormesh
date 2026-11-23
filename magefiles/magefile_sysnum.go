//go:build mage

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/magefile/mage/sh"

	"iormesh/internal/generate"
)

// sysnumArches are the arches the tracer builds for (docs/build.md); each
// gets its table of syscall numbers, internal/bpf/sysnum_<arch>.go.
var sysnumArches = []string{"amd64", "arm64"}

// GenerateSyscallNumbersGo regenerates the per-arch syscall number tables
// of the raw_syscalls dispatch (task 8e4, internal/c/dispatch.c) from the
// generated C handlers and golang.org/x/sys's zsysnum files at the version
// go.mod pins. A handler without a number on the generation host's arch
// fails the target - its syscall could never be traced - and needs a newer
// x/sys or an alias (internal/generate/sysnum.go); on another arch the
// syscall is just absent, like a tracepoint that kernel lacks.
func GenerateSyscallNumbersGo() error {
	fmt.Println("Generating Go syscall number tables...")
	dir, err := sh.Output("go", "list", "-m", "-f", "{{.Dir}} {{.Version}}", "golang.org/x/sys")
	if err != nil {
		return fmt.Errorf("locate golang.org/x/sys: %w", err)
	}
	modDir, version, ok := strings.Cut(strings.TrimSpace(dir), " ")
	if !ok {
		return fmt.Errorf("locate golang.org/x/sys: unexpected %q", dir)
	}
	for _, arch := range sysnumArches {
		if err := generateSyscallNumbers(modDir, version, arch); err != nil {
			return err
		}
	}
	return nil
}

// generateSyscallNumbers writes one arch's table.
func generateSyscallNumbers(modDir, version, arch string) error {
	name := "zsysnum_linux_" + arch + ".go"
	zsysnum, err := os.Open(filepath.Join(modDir, "unix", name))
	if err != nil {
		return fmt.Errorf("open x/sys %s: %w", name, err)
	}
	defer func() { _ = zsysnum.Close() }()
	cSource, err := os.Open(tracepointsCPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", tracepointsCPath, err)
	}
	defer func() { _ = cSource.Close() }()
	origin := "golang.org/x/sys " + version + " unix/" + name
	table, err := generate.GenerateSyscallNumbersGo(cSource, zsysnum, arch, origin)
	if err != nil {
		return err
	}
	if len(table.Unresolved) > 0 {
		if arch == runtime.GOARCH {
			return fmt.Errorf("syscalls without a %s number in %s: %s", arch, origin,
				strings.Join(table.Unresolved, ", "))
		}
		fmt.Printf("%s: %d syscalls without a number, left out: %s\n", arch,
			len(table.Unresolved), strings.Join(table.Unresolved, ", "))
	}
	return writeFormattedGo("internal/bpf/sysnum_"+arch+".go", table.Source)
}
