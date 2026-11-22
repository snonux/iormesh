package tracepoints

// DefaultSyscalls returns the syscalls attached when -trace-families is
// not given: those of DefaultFamilies (decision #7: file and socket on) —
// M1's walking-skeleton set (PLAN.md, "M1 — Walking skeleton", Probes),
// M2's file surface (fileSurfaceSyscalls) and the socket calls that carry
// addresses (socketSyscalls).
//
// Only attachment is narrowed. The generator still emits handlers for every
// syscall the kernel has, so widening the set later is a change here, not a
// regeneration; the committed result file keeps pinning the whole kernel
// tracepoint set.
//
// A fresh slice per call, so a caller cannot change the default for others.
func DefaultSyscalls() []string {
	return SyscallsFor(DefaultFamilies())
}

// m1FileSyscalls is the file half of M1's set: the opens (also how the
// scenario markers arrive) and the fd I/O.
func m1FileSyscalls() []string {
	return []string{"open", "openat", "openat2", "read", "write", "close"}
}

// socketSyscalls is the socket family: M1's connect and accepts, which pin
// a TCP socket to its fd, plus M2 step 7's bind (a unix socket's name, a
// server's port) and the datagram calls, whose address argument is a UDP
// peer (internal/c/sockaddr.c). read, write and close come along so the
// family alone still sees a connection's data. sendmsg/recvmsg (and their
// mmsg forms) are not attached: their addresses sit inside a msghdr the
// hooks do not walk yet, and the JDK's DatagramChannel uses sendto and
// recvfrom.
func socketSyscalls() []string {
	return []string{"read", "write", "close",
		"connect", "accept", "accept4", "bind", "sendto", "recvfrom"}
}

// fileSurfaceSyscalls is M2 step 4's file surface, on by default
// (decision #7: file, socket and lifecycle on, the rest opt-in): what
// PLAN.md's "What we trace" lists for the filesystem — create and delete,
// rename and links, the stat family, truncate, timestamps, chmod/chown,
// xattrs and positional data I/O — plus the fd lifecycle (M2 step 6):
// dup*, which file-attrs' golden declares, and fcntl, whose F_DUPFD forms
// copy an fd and whose F_SETFD/F_GETFD forms tell the correlator which
// fds an exec closes. A JVM calls fcntl when it sets a descriptor up, not
// per I/O; the #25 measurement due before M2's acceptance covers it.
//
// Left out on purpose: access/faccessat (existence probes the JDK fires
// at syscall frequency, decision #25's per-event cost), getdents, lseek,
// fsync, mounts and other admin calls, the zero-copy calls (the opt-in
// zerocopy family, trace_family.go) and close_range: the JDK 25 child
// closes its fds one by one through close (strace-measured). A program that does use close_range
// keeps those fds in the correlator's table, so a number it reuses
// through an untraced syscall keeps the closed instance's generation.
//
// Every name here needs a schema kind (schema/kinds_file.go) and a
// correlator shape.
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
		"dup", "dup2", "dup3", "fcntl",
	}
}
