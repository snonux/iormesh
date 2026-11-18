package tracepoints

// DefaultSyscalls returns the syscalls attached when no -trace-* selector
// is given: M1's walking-skeleton set (PLAN.md, "M1 — Walking skeleton",
// Probes) — file open/read/write/close and the TCP connect/accept that pin a
// socket to its fd — plus M2's file surface (fileSurfaceSyscalls).
//
// Only attachment is narrowed. The generator still emits handlers for every
// syscall the kernel has, so widening the set later is a change here, not a
// regeneration; the committed result file keeps pinning the whole kernel
// tracepoint set.
//
// A fresh slice per call, so a caller cannot change the default for others.
func DefaultSyscalls() []string {
	m1 := []string{
		"open", "openat", "openat2",
		"read", "write", "close",
		"connect", "accept", "accept4",
	}
	return append(m1, fileSurfaceSyscalls()...)
}

// fileSurfaceSyscalls is M2 step 4's file surface, on by default
// (decision #7: file, socket and lifecycle on, the rest opt-in): what
// PLAN.md's "What we trace" lists for the filesystem — create and delete,
// rename and links, the stat family, truncate, timestamps, chmod/chown,
// xattrs and positional data I/O — plus dup*, which file-attrs' golden
// declares. Left out on purpose: access/faccessat (existence probes the
// JDK fires at syscall frequency, decision #25's per-event cost), getdents,
// lseek, fsync and fcntl (M2 step 6 decides fcntl), mounts and other
// admin calls, and the zero-copy calls (opt-in, M2 step 7). Every name
// here needs a schema kind (schema/kinds_file.go) and a correlator shape.
func fileSurfaceSyscalls() []string {
	return []string{
		"creat", "mkdir", "mkdirat", "mknod", "mknodat",
		"rmdir", "unlink", "unlinkat",
		"rename", "renameat", "renameat2", "link", "linkat",
		"symlink", "symlinkat", "readlink", "readlinkat",
		"newstat", "newlstat", "newfstat", "newfstatat", "statx",
		"truncate", "ftruncate", "fallocate",
		"utime", "utimes", "futimesat", "utimensat",
		"chmod", "fchmod", "fchmodat", "fchmodat2",
		"chown", "lchown", "fchown", "fchownat",
		"setxattr", "lsetxattr", "fsetxattr", "getxattr", "lgetxattr", "fgetxattr",
		"listxattr", "llistxattr", "flistxattr",
		"removexattr", "lremovexattr", "fremovexattr",
		"pread64", "pwrite64", "readv", "writev",
		"preadv", "pwritev", "preadv2", "pwritev2",
		"dup", "dup2", "dup3",
	}
}

// defaultSyscallSet is DefaultSyscalls as a set, the shape the selector's
// allowlist uses.
func defaultSyscallSet() map[string]struct{} {
	names := DefaultSyscalls()
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		set[name] = struct{}{}
	}
	return set
}
