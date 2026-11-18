package tracepoints

// DefaultSyscalls returns the syscalls attached when no -trace-* selector
// is given: M1's walking-skeleton set (PLAN.md, "M1 — Walking skeleton",
// Probes) — file open/read/write/close and the TCP connect/accept that pin a
// socket to its fd. M2 widens it to the full surface.
//
// Only attachment is narrowed. The generator still emits handlers for every
// syscall the kernel has, so widening the set later is a change here, not a
// regeneration; the committed result file keeps pinning the whole kernel
// tracepoint set.
//
// A fresh slice per call, so a caller cannot change the default for others.
func DefaultSyscalls() []string {
	return []string{
		"open", "openat", "openat2",
		"read", "write", "close",
		"connect", "accept", "accept4",
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
