//+build ignore

// Signals sent by or to the traced processes (iormesh, M2 step 7; PLAN.md
// "What we trace": signals, delivery with sender PID). Included by
// iormesh.bpf.c after filter.c. Attached only when the signal family is
// selected (internal/tracepoints, -trace-families signal).
//
// Why signal_generate: it fires once per signal queued for a task, inside
// the call that queued it - kill(2), tgkill(2), a child's exit (SIGCHLD), a
// fault - so the current task is the sender for every user-sent signal, and
// one record names both ends. signal_deliver (restart.c) fires in the
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

// iormesh_signal_scope says which ends of a signal are traced: the sender
// (the current task; never the tracer itself) and the target. 0 = neither.
static __always_inline __u32 iormesh_signal_scope(__u32 sender, __u32 target) {
    __u32 scope = 0;

    if (sender != IOR_PID_FILTER && iormesh_pid_targeted(sender))
        scope |= SIGNAL_SCOPE_SENDER;
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
    __u32 scope = iormesh_signal_scope(pid_tgid >> 32, target);
    struct signal_event *ev;

    if (!scope)
        return 0;
    ev = bpf_ringbuf_reserve(&event_map, sizeof(*ev), 0);
    if (!ev) {
        ior_count_ringbuf_drop();
        return 0;
    }
    ev->event_type = SIGNAL_EVENT;
    // Not a syscall tracepoint: there is no enter/exit trace id.
    ev->trace_id = 0;
    ev->time = bpf_ktime_get_boot_ns();
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
