//+build ignore

// Syscall dispatch through the classic raw_syscalls tracepoints (task 8e4).
//
// The generated handlers (generated_tracepoints.c) are written for the
// per-syscall tracepoints syscalls:sys_enter_<name> and sys_exit_<name>, but
// they are not attached there any more. Linux 7.2 runs a program on such a
// tracepoint through perf_call_bpf_enter -> trace_call_bpf_faultable ->
// bpf_prog_run_array_sleepable: with migration disabled but preemption
// enabled, behind a per-program, per-CPU recursion counter. A task preempted
// inside, say, handle_sys_enter_read keeps that counter raised, and every
// other task on that CPU whose read enters before it resumes is skipped -
// whole time slices of calls, the target's among them, counted only as the
// program's recursion_misses. Raw tracepoints (raw_tracepoint/tp_btf,
// __bpf_trace_run) take the same preemptible per-program guard. Measured on
// 7.2.15 with a traced read loop sharing its CPU with an untraced one: the
// per-syscall and the raw-tracepoint hook each skipped ~83k runs and lost
// 32k-46k of the target's reads; the classic raw_syscalls:sys_enter hook
// skipped none and saw every read.
//
// The classic raw_syscalls events run their program from perf_trace_##call,
// which disables preemption first (guard(preempt_notrace)), through
// trace_call_bpf: a program run is never preempted, so it cannot be skipped
// by its own other runs. What can still skip it is the host-wide
// bpf_prog_active counter that trace_call_bpf shares with every kprobe and
// classic tracepoint program: held by another BPF program on the same CPU,
// or by a task preempted inside a bpf() map syscall (which raises it with
// only migration disabled). Those skips stay visible as the dispatchers'
// recursion_misses (internal/bpf/progmiss.go), possible loss.
//
// So two dispatchers attach to raw_syscalls:sys_enter and sys_exit and
// tail-call the generated handler of the syscall's number from a program
// array userspace fills (internal/bpf/attach.go): an empty slot is a syscall
// that is not traced and falls through. The handlers stay the per-syscall
// programs they were - tracepoint programs, verified one by one - because
// the raw_syscalls record has their context's layout: the arguments
// (sys_enter) and the return value (sys_exit) sit at offset 16 in both, and
// the first 8 bytes carry the pt_regs pointer the stack helpers read. Only
// the number differs (long id where the per-syscall record has int nr, the
// same low half on a little-endian host), and no handler reads it.
//
// The design follows ior's raw dispatch prototype (ior internal/c/rawsyscall.c
// @ b21afc50328c, docs/raw-tracepoint-design.md), with the classic
// raw_syscalls events in place of raw tracepoints - those are exactly what
// keeps the guard above preemptible.

// IORMESH_SYSCALL_SLOTS bounds the syscall numbers the program arrays cover:
// x86_64's and arm64's tables end below 512. A larger number (an x32
// syscall, which has bit 30 set) or a negative one (rt_sigreturn leaves -1 in
// orig_ax, so its exit reports -1) is not traced, exactly as the per-syscall
// tracepoints skip every number outside the table.
#define IORMESH_SYSCALL_SLOTS 512

// syscall_enter_progs and syscall_exit_progs are the tail-call tables,
// indexed by syscall number; userspace stores the generated handlers' fds in
// the slots of the traced syscalls, the exit slot first.
struct {
    __uint(type, BPF_MAP_TYPE_PROG_ARRAY);
    __uint(max_entries, IORMESH_SYSCALL_SLOTS);
    __type(key, __u32);
    __type(value, __u32);
} syscall_enter_progs SEC(".maps");

struct {
    __uint(type, BPF_MAP_TYPE_PROG_ARRAY);
    __uint(max_entries, IORMESH_SYSCALL_SLOTS);
    __type(key, __u32);
    __type(value, __u32);
} syscall_exit_progs SEC(".maps");

// iormesh_raw_syscall is the head both raw_syscalls records share: the
// common trace_entry (whose first 8 bytes perf overwrites with the pt_regs
// pointer) and the syscall number. Declared here instead of taken from
// vmlinux.h, whose trace_event_raw_sys_enter the build must not depend on.
struct iormesh_raw_syscall {
    __u64 regs;
    long id;
};

// IORMESH_ARM64_TIF_32BIT is arm64's thread_info.flags bit of an AArch32
// (compat) task (arch/arm64/include/asm/thread_info.h).
#define IORMESH_ARM64_TIF_32BIT 22

// iormesh_in_compat_syscall reports whether the current task is inside a
// 32-bit syscall. The per-syscall tracepoints never report one (x86 and arm64
// define ARCH_TRACE_IGNORE_COMPAT_SYSCALLS), but raw_syscalls sees it with
// its 32-bit number, which is another syscall's 64-bit number. x86 tests
// TS_COMPAT, arm64 TIF_32BIT. Every syscall on the host pays this check, so
// the task is read by a direct load of the BTF-typed current task (Linux
// 5.11's bpf_get_current_task_btf) where the kernel has that helper, and by
// ior_exec_into_compat's probe read (exec.c) elsewhere; the enum relocation
// is a constant after load, so the verifier prunes the branch not taken. A
// kernel without the field reports no compat syscall.
static __always_inline int iormesh_in_compat_syscall(void) {
#if defined(__TARGET_ARCH_arm64)
    struct task_struct *task = (struct task_struct *)bpf_get_current_task();

    if (!bpf_core_field_exists(struct thread_info, flags))
        return 0;
    return (BPF_CORE_READ(task, thread_info.flags) >> IORMESH_ARM64_TIF_32BIT) & 1;
#else
    if (!bpf_core_field_exists(struct thread_info, status))
        return 0;
    if (bpf_core_enum_value_exists(enum bpf_func_id, BPF_FUNC_get_current_task_btf))
        return (bpf_get_current_task_btf()->thread_info.status & IOR_TS_COMPAT) != 0;
    return ior_exec_into_compat();
#endif
}

// syscall_enter_traced and syscall_exit_traced mirror the program arrays:
// 1 in the slot of every syscall whose handler userspace stored there
// (internal/bpf/dispatch.go writes both; a mirror and a slot that disagree
// for a moment only skip a handler, as an empty slot does).
// Why: a program array cannot be asked whether a slot is filled except by the
// tail call itself, and the scope test below must come before that call. A
// syscall of a kind no family traces - most of a host's - is told apart by
// one inlined array load and pays no scope test (a getppid loop's dispatcher
// cost rose by ~115 kernel instructions per call in host mode, ~200 in a
// namespace, with the test ahead of the tail call and no mirror, task 9f4).
struct {
    __uint(type, BPF_MAP_TYPE_ARRAY);
    __uint(max_entries, IORMESH_SYSCALL_SLOTS);
    __type(key, __u32);
    __type(value, __u32);
} syscall_enter_traced SEC(".maps");

struct {
    __uint(type, BPF_MAP_TYPE_ARRAY);
    __uint(max_entries, IORMESH_SYSCALL_SLOTS);
    __type(key, __u32);
    __type(value, __u32);
} syscall_exit_traced SEC(".maps");

// iormesh_dispatch_in_scope is filter()'s answer before the tail call, as
// far as it can be given without the hash lookup: 0 for the tracer itself
// and for a task the target hint (targethint.c) rules out. Why: nearly
// every traced-kind call on the host is a bystander's, and a tail call into
// a handler that only runs filter() to return cost ~30 kernel instructions
// per dispatcher run more than this test (close(-1) loop, task 9f4). The
// handler's filter() still decides - the hash lookup, the gap count of a
// pending process, TID_FILTER - for whatever passes here.
static __always_inline int iormesh_dispatch_in_scope(void) {
    __u32 pid = iormesh_dispatch_tgid();

    return pid && pid != IOR_PID_FILTER && iormesh_target_hint(pid);
}

// iormesh_dispatch tail-calls the handler in progs at the record's syscall
// number. It returns only when there is none: a number out of range, an
// untraced syscall (traced, progs' mirror, has no 1 there), a compat
// syscall, or a task out of scope - whose handler would only have returned
// at its filter().
static __always_inline int iormesh_dispatch(void *ctx, void *progs, void *traced) {
    long id = ((struct iormesh_raw_syscall *)ctx)->id;
    __u32 nr, *on;

    if (id < 0 || id >= IORMESH_SYSCALL_SLOTS)
        return 0;
    nr = (__u32)id;
    on = bpf_map_lookup_elem(traced, &nr);
    if (!on || !*on)
        return 0;
    if (iormesh_in_compat_syscall())
        return 0;
    if (!iormesh_dispatch_in_scope())
        return 0;
    bpf_tail_call(ctx, progs, nr);
    return 0;
}

SEC("tracepoint/raw_syscalls/sys_enter")
int handle_raw_sys_enter(void *ctx) {
    // A namespace tracer learns its namespace's level here and remembers an
    // exec's namespace tid, before any handler, for sched_process_exec
    // (pidns.c, M7); both pruned in host mode.
    iormesh_pidns_learn();
    iormesh_note_exec_tid(((struct iormesh_raw_syscall *)ctx)->id);
    return iormesh_dispatch(ctx, &syscall_enter_progs, &syscall_enter_traced);
}

SEC("tracepoint/raw_syscalls/sys_exit")
int handle_raw_sys_exit(void *ctx) {
    return iormesh_dispatch(ctx, &syscall_exit_progs, &syscall_exit_traced);
}
