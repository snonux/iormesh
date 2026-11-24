package tracepoints

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

// sysnumKeyRe matches one entry of a generated syscall number table,
// `"name": nr,`.
var sysnumKeyRe = regexp.MustCompile(`(?m)^\s*"([a-z0-9_]+)":\s*\d+,$`)

// archSyscallTable reads the syscall names of internal/bpf/sysnum_<goarch>.go,
// the loader's per-arch number table. It is generated from golang.org/x/sys's
// zsysnum_linux_<goarch>.go (magefiles/magefile_sysnum.go), which for arm64
// is the kernel's generic unistd (include/uapi/asm-generic/unistd.h). Read as
// text, so both arches' tables are checked on whichever host runs the test.
func archSyscallTable(t *testing.T, goarch string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "bpf", "sysnum_"+goarch+".go"))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, m := range sysnumKeyRe.FindAllStringSubmatch(string(data), -1) {
		names[m[1]] = true
	}
	if len(names) < 100 {
		t.Fatalf("sysnum_%s.go: only %d syscalls parsed", goarch, len(names))
	}
	return names
}

// TestArchAbsentSyscallsMatchTable: the documented list of family syscalls
// an arch lacks is exactly the family syscalls its number table has no
// entry for - no more (an entry would excuse a probe the arch can attach,
// so TestLoaderAttachesEveryFamily would let a real skip pass) and no less
// (the root test would fail on a skip the arch cannot avoid). On amd64 the
// list is empty, so the root test stays as strict as before (task jl4).
func TestArchAbsentSyscallsMatchTable(t *testing.T) {
	for _, goarch := range []string{"amd64", "arm64"} {
		table := archSyscallTable(t, goarch)
		var want []string
		for _, name := range SyscallsFor(AllFamilies()) {
			if !table[name] {
				want = append(want, name)
			}
		}
		got := ArchAbsentSyscalls(goarch)
		slices.Sort(want)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s: ArchAbsentSyscalls = %v, want the family syscalls without a number %v",
				goarch, got, want)
		}
	}
}

// TestArchAbsentSyscallsReplacements: every syscall arm64 lacks has its
// replacement in the families, so dropping the legacy call loses no
// operation there - opens, creates and deletes go through the *at calls,
// dup2 through dup3, pipe through pipe2, poll through ppoll, and so on.
func TestArchAbsentSyscallsReplacements(t *testing.T) {
	replacement := map[string]string{
		"open": "openat", "creat": "openat", "mkdir": "mkdirat", "mknod": "mknodat",
		"rmdir": "unlinkat", "unlink": "unlinkat", "rename": "renameat2",
		"link": "linkat", "symlink": "symlinkat", "readlink": "readlinkat",
		"newstat": "newfstatat", "newlstat": "newfstatat",
		"utime": "utimensat", "utimes": "utimensat", "futimesat": "utimensat",
		"chmod": "fchmodat", "chown": "fchownat", "lchown": "fchownat",
		"dup2": "dup3", "pipe": "pipe2", "epoll_create": "epoll_create1",
		"epoll_wait": "epoll_pwait", "poll": "ppoll",
	}
	all := SyscallsFor(AllFamilies())
	absent := ArchAbsentSyscalls("arm64")
	if len(absent) == 0 {
		t.Fatal("arm64: no absent syscalls listed")
	}
	for _, name := range absent {
		repl, ok := replacement[name]
		if !ok {
			t.Errorf("arm64 lacks %s: no replacement recorded", name)
			continue
		}
		if !slices.Contains(all, repl) || slices.Contains(absent, repl) {
			t.Errorf("arm64 lacks %s: replacement %s is not a family syscall arm64 has", name, repl)
		}
	}
	if got := ArchAbsentSyscalls("riscv64"); got != nil {
		t.Errorf("unknown arch: got %v, want nil", got)
	}
}
