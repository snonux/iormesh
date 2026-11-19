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

// IORMESH_EFAULT is EFAULT (asm-generic/errno-base.h): vmlinux.h carries no
// errno macros. bpf_get_stackid returns it when the walk unwound nothing.
#define IORMESH_EFAULT 14

// iormesh_stack_inline sends the current thread's user stack with its
// frames inline (struct stack_inline_event) for an enter whose stack
// stack_map refused with err (bpf_get_stackid's -EEXIST or -ENOMEM). Why
// not a bigger map or BPF_F_REUSE_STACKID: bucket collisions grow with the
// square of the distinct stacks whatever the size, and overwriting would
// change what an id already in the trace means. The record costs 552 ring
// bytes instead of 40, only for the enters of the few colliding stacks.
// Returns 0 when the record was sent or the ring was full (counted as a
// drop), else the error the plain record should carry: bpf_get_stack's
// own when its walk failed too, else err.
static __always_inline long iormesh_stack_inline(void *ctx, __u32 pid, __u32 tid, __u64 now, long err) {
    struct stack_inline_event *ev;
    struct task_struct *task;
    long n;

    ev = bpf_ringbuf_reserve(&event_map, sizeof(struct stack_inline_event), 0);
    if (!ev) {
        ior_count_ringbuf_drop();
        return 0;
    }
    n = bpf_get_stack(ctx, ev->ips, sizeof(ev->ips), BPF_F_USER_STACK);
    if (n <= 0) {
        bpf_ringbuf_discard(ev, 0);
        return n < 0 ? n : err;
    }
    ev->event_type = STACK_INLINE_EVENT;
    ev->trace_id = 0;
    ev->time = now;
    ev->pid = pid;
    ev->tid = tid;
    ev->stack_id = (__s32)err;
    ev->nr = (__u32)n / sizeof(__u64);
    task = (struct task_struct *)bpf_get_current_task();
    ev->image = BPF_CORE_READ(task, mm, arg_start);
    bpf_ringbuf_submit(ev, 0);
    return 0;
}

// iormesh_stack_on_enter sends the current thread's user stack id for the
// syscall enter the handler is about to send: a stack_event stamped with
// the enter's own time (now), so userspace pairs the two by tid and time.
// A stack stack_map cannot take (a colliding bucket, a full map) goes
// inline instead (iormesh_stack_inline). A walk that fails anyway still
// sends the record, carrying the negative errno, so userspace can count
// stacks it did not get; a full ring counts one drop like every other lost
// record. The record carries the image tag (see struct stack_event):
// mm->arg_start, read through CO-RE.
static __always_inline void iormesh_stack_on_enter(void *ctx, __u32 pid, __u32 tid, __u64 now) {
    struct task_struct *task;
    struct stack_event *ev;
    long id;

    if (!IORMESH_NATIVE_STACKS)
        return;
    id = bpf_get_stackid(ctx, &stack_map, BPF_F_USER_STACK);
    // -EFAULT means nothing unwound: bpf_get_stack would fail the same.
    if (id < 0 && id != -IORMESH_EFAULT) {
        id = iormesh_stack_inline(ctx, pid, tid, now, id);
        if (id == 0)
            return;
    }
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
