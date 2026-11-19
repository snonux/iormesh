//+build ignore

// The Java mark (iormesh, M3 "Join to kernel events", task td4; S1,
// docs/spikes/s1-java-join.md). Included by iormesh.bpf.c after filter.c
// (it uses ior_count_ringbuf_drop) and before exec.c, whose thread-exit
// handler resets a thread's mark, and the generated handlers, whose enter
// side calls iormesh_java_mark_on_enter once the enter is known to be
// emitted (internal/generate/bpfhandler.go, renderJavaMarkHook).
//
// Why: the agent's bracket around a JDK I/O method contains more than the
// native's I/O - the JVM loads classes, reads its runtime image, maps memory
// inside it (S1 rule 2). The hook's syscall allowlist rejects the kinds the
// native never issues; the mark rejects the rest: a syscall the native
// itself issued has the native on its thread's mark, JVM work of an allowed
// kind inside the same bracket does not.
//
// How: userspace attaches iormesh_java_native_exit and then _enter as
// uprobe_multi links to the hooked natives of one JVM (scoped to its pid, so
// other JVMs of the same JDK install never trap), each native's BPF cookie
// its id. Entry pushes {cookie, entry time, stack pointer} on the thread's
// mark; return pops it. A syscall enter of a marked thread sends the
// innermost frame ahead of its enter record (struct java_mark_event).
//
// Why the stack pointer: the return probe pops every frame entered at or
// below its own stack pointer - the returning native's and any left behind
// deeper - and never an outer native's, whose frame sits higher on the
// stack. So a return without its push (a native entered after the return
// probe was attached but before the entry probe was, which is why the
// return probe goes first) pops nothing that belongs to another native,
// and a push whose return probe never runs (a native that never returns
// normally) is cleared by the next return at its depth or above. A stale
// frame that survives anyway is caught by userspace: its entry lies outside
// the agent bracket the syscall sits in.
//
// The uretprobe's own syscall (x86_64 nr 335, kernel >= 6.11) runs inside
// the native's mark; its kind is in no allowlist, so it never joins.

// iormesh_java_native_enter pushes the native the thread entered. The probe
// is attached with the JVM's pid, so only that JVM's threads get here;
// target_map is not consulted, a mark costs nothing until a traced enter
// reads it.
SEC("uprobe.multi")
int iormesh_java_native_enter(struct pt_regs *ctx) {
    struct iormesh_java_mark *m, init = {};
    __u32 tid = (__u32)bpf_get_current_pid_tgid();
    __u32 d;

    m = bpf_map_lookup_elem(&java_mark_map, &tid);
    if (!m) {
        // BPF_NOEXIST: another probe of this thread cannot race this one,
        // so a failure is only a full map losing an entry to LRU.
        bpf_map_update_elem(&java_mark_map, &tid, &init, BPF_NOEXIST);
        m = bpf_map_lookup_elem(&java_mark_map, &tid);
        if (!m)
            return 0;
    }
    d = m->depth;
    if (d >= IORMESH_JAVA_MARK_DEPTH) {
        m->lost = 1;
        return 0;
    }
    m->frames[d].cookie = bpf_get_attach_cookie(ctx);
    m->frames[d].entry = bpf_ktime_get_ns();
    m->frames[d].sp = ctx->sp;
    m->depth = d + 1;
    return 0;
}

// iormesh_java_native_exit pops the frames the returning native ends: every
// frame entered at or below the current stack pointer (on x86_64 the return
// has popped the return address, so the native's own entry sp is 8 below).
// With every kept frame gone, a lost one is gone too.
SEC("uretprobe.multi")
int iormesh_java_native_exit(struct pt_regs *ctx) {
    __u32 tid = (__u32)bpf_get_current_pid_tgid();
    struct iormesh_java_mark *m;
    __u64 sp = ctx->sp;
    __u32 d;

    m = bpf_map_lookup_elem(&java_mark_map, &tid);
    if (!m)
        return 0;
    d = m->depth;
    if (d > IORMESH_JAVA_MARK_DEPTH)
        d = IORMESH_JAVA_MARK_DEPTH;
    for (int i = 0; i < IORMESH_JAVA_MARK_DEPTH; i++) {
        // The mask (the depth is a power of two) is for the verifier: it
        // does not carry d's bounds across the loop's iterations.
        if (d == 0 || m->frames[(d - 1) & (IORMESH_JAVA_MARK_DEPTH - 1)].sp > sp)
            break;
        d--;
    }
    m->depth = d;
    if (d == 0)
        m->lost = 0;
    return 0;
}

// iormesh_java_mark_on_enter sends the innermost native of the current
// thread for the syscall enter the handler is about to send: a
// java_mark_event stamped with the enter's own time (now), so userspace
// pairs the two by tid and time, as it pairs stack records. A thread
// outside every hooked native sends nothing - the common case, one lookup.
// A full ring counts one drop like every other lost record.
static __always_inline void iormesh_java_mark_on_enter(__u32 pid, __u32 tid, __u64 now) {
    struct iormesh_java_mark *m;
    struct java_mark_event *ev;
    __u32 d;

    if (!IORMESH_JAVA_MARK)
        return;
    m = bpf_map_lookup_elem(&java_mark_map, &tid);
    if (!m)
        return;
    d = m->depth;
    if (d == 0 || d > IORMESH_JAVA_MARK_DEPTH)
        return;
    ev = bpf_ringbuf_reserve(&event_map, sizeof(struct java_mark_event), 0);
    if (!ev) {
        ior_count_ringbuf_drop();
        return;
    }
    ev->event_type = JAVA_MARK_EVENT;
    ev->trace_id = 0;
    ev->time = now;
    ev->pid = pid;
    ev->tid = tid;
    // A lost frame is deeper than every kept one: the innermost native is
    // then unknown, which cookie 0 (never a native's id) says.
    ev->cookie = m->lost ? 0 : m->frames[d - 1].cookie;
    ev->entry = m->lost ? 0 : m->frames[d - 1].entry;
    bpf_ringbuf_submit(ev, 0);
}

// iormesh_java_mark_forget drops an exiting thread's mark, so a recycled tid
// starts without one. One lookup-and-miss for a thread that never entered a
// hooked native; nothing at all with the mark off.
static __always_inline void iormesh_java_mark_forget(__u32 tid) {
    if (!IORMESH_JAVA_MARK)
        return;
    bpf_map_delete_elem(&java_mark_map, &tid);
}
