//+build ignore

// Native user stacks of syscall enters (iormesh, M2 step 9; PLAN.md M2
// "Native stacks are opt-in"). Included by iormesh.bpf.c after filter.c (it
// uses ior_count_ringbuf_drop) and before the generated handlers, whose
// enter side calls iormesh_stack_on_enter once the enter is known to be
// emitted (internal/generate/bpfhandler.go, renderStackHook).
//
// Why user stacks only: at a sys_enter tracepoint the kernel stack is the
// syscall entry path (entry_SYSCALL_64, do_syscall_64) and says nothing
// about who made the call; the user stack is the caller's code.
//
// Why this is for non-JVM targets: bpf_get_stackid walks frame pointers.
// Go and C code built with them unwind; JIT-compiled Java frames and a
// libjvm built without frame pointers do not, so a JVM's stacks stop in or
// near libc. Java attribution comes from the agent (M3), not from here.
//
// Why a side record instead of a field on every enter record: the
// generated records stay as they are, and with the switch off
// (IORMESH_NATIVE_STACKS, flags.h) the verifier prunes this function from
// every handler, so the default costs nothing per call (decision #25).

// iormesh_stack_on_enter sends the current thread's user stack id for the
// syscall enter the handler is about to send: a stack_event stamped with
// the enter's own time (now), so userspace pairs the two by tid and time.
// A failed walk still sends the record, carrying the negative errno, so
// userspace can count stacks it did not get; a full ring counts one drop
// like every other lost record. The record carries the image tag (see
// struct stack_event): mm->arg_start, read through CO-RE.
static __always_inline void iormesh_stack_on_enter(void *ctx, __u32 pid, __u32 tid, __u64 now) {
    struct task_struct *task;
    struct stack_event *ev;
    long id;

    if (!IORMESH_NATIVE_STACKS)
        return;
    id = bpf_get_stackid(ctx, &stack_map, BPF_F_USER_STACK);
    ev = bpf_ringbuf_reserve(&event_map, sizeof(struct stack_event), 0);
    if (!ev) {
        ior_count_ringbuf_drop();
        return;
    }
    ev->event_type = STACK_EVENT;
    ev->trace_id = 0;
    ev->time = now;
    ev->pid = pid;
    ev->tid = tid;
    ev->stack_id = (__s32)id;
    ev->reserved = 0;
    task = (struct task_struct *)bpf_get_current_task();
    ev->image = BPF_CORE_READ(task, mm, arg_start);
    bpf_ringbuf_submit(ev, 0);
}
