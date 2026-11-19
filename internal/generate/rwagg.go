package generate

import (
	"fmt"
	"strings"
)

// aggregatedSyscalls are the syscalls whose hot loops the kernel folds into
// rw_aggregate_event records (internal/c/rwagg.c, M2 step 11, decision
// #25): read and write, the calls of S3's syscall burst and of every
// stream copy loop. Their records are a plain fd_event enter and a
// ret_event exit, so a count and a byte sum say everything a run of them
// said. pread64/pwrite64 (an offset per call) and the vectored calls are
// left out on purpose: a sum would lose what their records carry.
var aggregatedSyscalls = map[string]bool{
	"read":  true,
	"write": true,
}

// aggregates reports whether syscall's handlers fold through rwagg.c.
func aggregates(syscall string) bool {
	return aggregatedSyscalls[syscall]
}

// renderRWEnterHook writes the enter hook of a folded kind. The per-tid hook
// is ior_on_rw_syscall_enter, the one enter hook that does not flush the
// thread's folded calls (filter.c): this call may continue them. The
// descriptor and its file identity are read once into locals, the key
// iormesh_rw_on_enter needs; the record's body copies the identity
// (generateExtraFd) and sets ev->fd from the argument itself, as every
// fd_event does. A held call returns before the reserve.
func renderRWEnterHook(b *strings.Builder, h handlerSpec) {
	b.WriteString(clockReadLine)
	fmt.Fprintf(b, "    if (!ior_on_rw_syscall_enter(tid, %s, now))\n", h.enterName)
	b.WriteString("        return 0;\n")
	fmt.Fprintf(b, "    __s32 rw_fd = (__s32)ctx->args[%d];\n", h.rwFdArg)
	b.WriteString("    __u32 file_ident = ior_file_ident(rw_fd);\n")
	fmt.Fprintf(b, "    if (!iormesh_rw_on_enter(pid, tid, %s, rw_fd, file_ident, now))\n", h.enterName)
	b.WriteString("        return 0;\n")
}

// renderRWExitHook writes the exit hook of a folded kind: the per-tid exit
// hook's verdict goes into iormesh_rw_on_exit, which clears the call's
// in-flight markers whatever it is, then folds a held call (no exit record)
// or lets the exit through, having sent a held call's owed enter first.
func renderRWExitHook(b *strings.Builder, h handlerSpec) {
	b.WriteString(clockReadLine)
	fmt.Fprintf(b, "    int emits = ior_on_syscall_exit(tid, %s, ctx->ret, now);\n", h.enterName)
	fmt.Fprintf(b, "    if (!iormesh_rw_on_exit(pid, tid, %s, ctx->ret, now, emits))\n", h.enterName)
	b.WriteString("        return 0;\n")
}

// rwFdArgForHandler returns the descriptor argument of a folded kind's
// handlers (both sides: the exit only needs to know it folds), or -1 for
// every other handler. A folded kind whose enter is not an fd_event - a
// kernel that changed read's format - is not folded: its record would not
// be the one rwagg.c sends for a held enter.
func rwFdArgForHandler(tp GeneratedTracepoint, isEnter bool) int {
	kind := tp.Classification.Kind
	if !isEnter {
		kind = tp.EnterKind
	}
	if !aggregates(syscallName(tp.Format.Name)) || kind != KindFd {
		return -1
	}
	if !isEnter {
		return 0
	}
	return fdArgumentIndex(tp.Format)
}
