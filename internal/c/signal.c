//+build ignore

// Signals sent by or to the traced processes (iormesh, M2 step 7; PLAN.md
// "What we trace": signals, delivery with sender PID). Included by
// iormesh.bpf.c after filter.c. Attached only when the signal family is
// selected (internal/tracepoints, -trace-families signal).
//
// Why signal_generate: it fires once per signal queued for a task, inside
// the call that queued it - kill(2), tgkill(2), a child's exit (SIGCHLD), a
// fault - so the current task is the sender of every task-sent signal, and
// one record names both ends. A kernel-raised one (its siginfo says so) has
// no sender: the current task merely ran when it fired. signal_deliver (restart.c) fires in the
// receiver only and says nothing about who sent it.
//
// Why a raw tracepoint: the classic tracepoint names the target by its
// thread id only, and a tgkill's target thread is not in target_map (which
// holds tgids); the raw tracepoint hands over the task_struct, whose tgid is
// the process. Its arguments are TP_PROTO(int sig, struct kernel_siginfo
// *info, struct task_struct *task, int group, int result), one u64 each.

// iormesh_signal_args are the raw tracepoint's arguments, declared as a
// struct so each read names what it reads (exec.c's task_rename does the
// same).
struct iormesh_signal_args {
    __u64 sig;
    __u64 info;
    struct task_struct *task;
    __u64 group;
    __u64 result;
};

// Special siginfo pointer values (include/linux/sched/signal.h): no info
// means a task's kill(2)-style send, PRIV a kernel-internal one.
#define IORMESH_SEND_SIG_NOINFO 0
#define IORMESH_SEND_SIG_PRIV 1
// si_code values of a signal a task sent (include/uapi/asm-generic/
// siginfo.h): kill(2), sigqueue(3), tkill(2)/tgkill(2). The other
// non-positive codes - SI_TIMER, SI_MESGQ, SI_ASYNCIO, SI_SIGIO - are
// raised by the kernel on the process's behalf, like every positive one
// (a fault, CLD_EXITED, POLL_IN).
#define IORMESH_SI_USER 0
#define IORMESH_SI_QUEUE (-1)
#define IORMESH_SI_TKILL (-6)

// iormesh_signal_from_task reports whether the current task sent the
// signal. Only then is it a sender: a timer's SIGALRM, a SIGCHLD from a
// child's exit or a SIGIO fires in whatever task happens to run, and naming
// that task would invent a signaled edge.
static __always_inline int iormesh_signal_from_task(__u64 info) {
    int code;

    if (info == IORMESH_SEND_SIG_NOINFO)
        return 1;
    if (info == IORMESH_SEND_SIG_PRIV)
        return 0;
    code = BPF_CORE_READ((struct kernel_siginfo *)info, si_code);
    return code == IORMESH_SI_USER || code == IORMESH_SI_QUEUE || code == IORMESH_SI_TKILL;
}

// iormesh_signal_scope says which ends of a signal are traced and whether a
// task sent it: SIGNAL_SCOPE_USER for a task-sent signal, SIGNAL_SCOPE_SENDER
// when that task (the current one, never the tracer) is traced too,
// SIGNAL_SCOPE_TARGET when the target is. 0 or USER alone = nothing to
// report.
static __always_inline __u32 iormesh_signal_scope(__u32 sender, __u32 target, __u64 info) {
    __u32 scope = 0;

    if (iormesh_signal_from_task(info)) {
        scope |= SIGNAL_SCOPE_USER;
        if (sender != IOR_PID_FILTER && iormesh_pid_targeted(sender))
            scope |= SIGNAL_SCOPE_SENDER;
    }
    if (iormesh_pid_targeted(target))
        scope |= SIGNAL_SCOPE_TARGET;
    return scope;
}

SEC("raw_tracepoint/signal_generate")
// ctx is void *, cast straight to the argument struct, as in
// handle_task_rename: no CO-RE relocation, no ctx pointer arithmetic.
int handle_signal_generate(void *ctx) {
    struct iormesh_signal_args *args = ctx;
    struct task_struct *task = args->task;
    __u64 pid_tgid = bpf_get_current_pid_tgid();
    __u32 target = (__u32)BPF_CORE_READ(task, tgid);
    __u32 scope = iormesh_signal_scope(pid_tgid >> 32, target, args->info);
    struct signal_event *ev;

    if (!(scope & (SIGNAL_SCOPE_SENDER | SIGNAL_SCOPE_TARGET)))
        return 0;
    // A signal the current task sent itself (kill, a write's SIGPIPE) comes
    // after its folded reads and writes, so they go first (rwagg.c). Only
    // then: a kernel-raised signal (an itimer's SIGALRM) can fire in an
    // interrupt that landed inside one of the current task's rwagg.c hooks,
    // and a flush from there would race the hook on its own entry. Another
    // thread's entry is never this program's to touch.
    if (scope & SIGNAL_SCOPE_SENDER)
        iormesh_rw_flush((__u32)pid_tgid);
    ev = bpf_ringbuf_reserve(&event_map, sizeof(*ev), 0);
    if (!ev) {
        ior_count_ringbuf_drop();
        return 0;
    }
    ev->event_type = SIGNAL_EVENT;
    // Not a syscall tracepoint: there is no enter/exit trace id.
    ev->trace_id = 0;
    ev->time = bpf_ktime_get_boot_ns();
    // pid/tid are the current task: the sender when scope has USER, else
    // just whoever ran when the kernel raised it.
    ev->pid = pid_tgid >> 32;
    ev->tid = (__u32)pid_tgid;
    ev->target_pid = target;
    ev->target_tid = (__u32)BPF_CORE_READ(task, pid);
    ev->sig = (__s32)args->sig;
    ev->result = (__s32)args->result;
    ev->group = (__u32)args->group;
    ev->scope = scope;
    bpf_ringbuf_submit(ev, 0);
    return 0;
}
