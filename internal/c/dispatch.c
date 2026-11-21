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
// programs they were - tracepoint programs, verified one by one - but read
// their context as the raw_syscalls record they are handed
// (trace_event_raw_sys_enter___iormesh below, CO-RE relocated): upstream it
// matches the per-syscall record (args and ret at offset 16), RHEL 9's
// differs (task lb4). The first 8 bytes carry the pt_regs pointer the stack
// helpers read in both.
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

// trace_event_raw_sys_enter___iormesh and trace_event_raw_sys_exit___iormesh
// are the raw_syscalls records as the dispatchers and every tail-called
// handler read them (internal/generate handlerCtxStruct): CO-RE flavors, so
// libbpf relocates each field to the running kernel's offset. Declared here
// instead of taken from vmlinux.h, whose trace_event_raw_sys_enter the
// build must not depend on. The first 8 bytes of the common trace_entry are
// the pt_regs pointer perf writes there (the stack helpers read it) and are
// never read as fields.
//
// Why relocated and not fixed: upstream puts id at 8 and args/ret at 16, but
// RHEL 9 (5.14 with the rt-merge backport) grew trace_entry by
// preempt_lazy_count to 12 bytes, which moves id to 16 and args and ret to
// 24 (/sys/kernel/tracing/events/raw_syscalls/sys_enter/format on Rocky
// 9.9). With fixed offsets the dispatcher read id as 0 - nearly every
// syscall went to the read handler - and the handlers read id as args[0]
// and ret (task lb4).
//
// Keep preserve_access_index on both. The structs leave out the 8-byte
// trace_entry header, so without the attribute nothing relocates them and
// every field is read at its declared offset: id at 0 - the pt_regs
// pointer slot - and args/ret at 8. That compiles, loads and attaches
// without a word on every kernel, and silently dispatches by a pointer's
// low bits. TestEl9RecordLayout (internal/btf_root_test.go, task of4)
// relocates the object against both layouts and catches it on any host.
struct trace_event_raw_sys_enter___iormesh {
    long id;
    unsigned long args[6];
} __attribute__((preserve_access_index));

struct trace_event_raw_sys_exit___iormesh {
    long id;
    long ret;
} __attribute__((preserve_access_index));

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
static __always_inline int iormesh_dispatch(void *ctx, long id, void *progs, void *traced) {
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
    long id = ((struct trace_event_raw_sys_enter___iormesh *)ctx)->id;

    iormesh_note_exec_tid(id);
    return iormesh_dispatch(ctx, id, &syscall_enter_progs, &syscall_enter_traced);
}

SEC("tracepoint/raw_syscalls/sys_exit")
int handle_raw_sys_exit(void *ctx) {
    long id = ((struct trace_event_raw_sys_exit___iormesh *)ctx)->id;

    return iormesh_dispatch(ctx, id, &syscall_exit_progs, &syscall_exit_traced);
}
