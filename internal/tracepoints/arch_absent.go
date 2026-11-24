package tracepoints

import "slices"

// arm64AbsentSyscalls are the family syscalls arm64 has no syscall for.
// arm64 uses the kernel's generic syscall table
// (include/uapi/asm-generic/unistd.h), which leaves out the legacy calls
// that have a newer form: the path calls without a dirfd (open, mkdir,
// rename, ...; their *at forms remain), the old stat and time-setting
// calls (newfstatat and utimensat remain), dup2 (dup3), pipe (pipe2),
// epoll_create and epoll_wait (epoll_create1, epoll_pwait) and poll
// (ppoll). The loader has no dispatch slot for them there, so their probes
// are skipped (internal/bpf/dispatch.go, errNoSyscallNumber). The
// replacements are family syscalls themselves, so no operation goes
// untraced. TestArchAbsentSyscallsMatchTable pins this list to the
// generated arm64 number table, which comes from that generic table.
var arm64AbsentSyscalls = []string{
	"open", "creat", "mkdir", "mknod", "rmdir", "unlink", "rename", "link",
	"symlink", "readlink", "newstat", "newlstat",
	"utime", "utimes", "futimesat", "chmod", "chown", "lchown",
	"dup2", "pipe", "epoll_create", "epoll_wait", "poll",
}

// ArchAbsentSyscalls returns the family syscalls (SyscallsFor(AllFamilies()))
// that goarch has no syscall for, so the loader skips both of their probes
// there. It is nil for amd64, which has every one - there a skipped family
// probe is a defect - and for an arch iormesh does not build for. The
// slice is fresh.
func ArchAbsentSyscalls(goarch string) []string {
	if goarch == "arm64" {
		return slices.Clone(arm64AbsentSyscalls)
	}
	return nil
}
