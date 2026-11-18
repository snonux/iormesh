//+build ignore

// In-kernel aggregation of hot read/write loops (iormesh, M2 step 11,
// decision #25; docs/implementation.md "M2", step 11). Spike S3 measured a
// tight read/write loop losing 16-42 % of its ring records: one consumer
// cannot keep up with two records per syscall from every thread. ior solves
// the same problem with syscall_aggregate_map (ior internal/c/filter.c @
// b21afc50328c, ior_update_syscall_aggregate): calls it does not send are
// summed in the kernel. iormesh keeps the idea, not the shape - ior's rows
// are per syscall and polled, which loses which fd and thread a call was
// on; here the sums are per thread and per fd instance, and they are sent
// through event_map as rw_aggregate_event records at well-defined points,
// so the trace keeps every call (as a count) in its thread's order.
//
// What folds: read and write only (the generator's aggregatedSyscalls).
// pread64/pwrite64 and the vectored calls stay individual: their records
// have other shapes (an offset, an iovec) a sum would lose.
//
// Keys and threshold. A thread's rw_agg_map entry holds IORMESH_RW_SLOTS
// keys (enter trace id, fd, file identity). The first IORMESH_RW_INDIVIDUAL
// hot calls of a key are sent as usual; from the next one on, a hot call's
// enter record is held back (cur_held) and its exit folds it into the key's
// pending sums instead of sending anything. A call is hot when it starts
// within IORMESH_RW_HOT_NS of the key's previous call's exit, returns > 0
// and takes at most IORMESH_RW_HOT_NS itself. So a scenario's few declared
// reads and writes stay single events with their exact byte counts, and a
// request/response loop that waits between calls is never folded at all.
//
// Flush points - where pending sums become a record:
//   - every traced syscall of the thread that is not a folded kind
//     (ior_on_syscall_enter and its variants in filter.c call
//     iormesh_rw_flush first): all its keys, so every state-changing event
//     - open, close, dup, connect, accept, markers, exec, ... - follows the
//     calls before it in the stream;
//   - a call on a key whose previous call is no longer hot, and a held
//     call that turns out not hot (EOF, an error, a slow call): that key,
//     right before the call's own records, which are then sent (a held
//     enter first, iormesh_rw_send_held_enter);
//   - a key reaching IORMESH_RW_SPAN_NS or IORMESH_RW_MAX_COUNT;
//   - a fifth key on the thread: the least recently used key is evicted;
//   - signal_generate and task_newtask in the thread's context (signal.c,
//     exec.c), sched_process_exec and the thread's sched_process_exit
//     (iormesh_rw_forget, which also removes the entry);
//   - the session end: userspace reads what is left (internal/bpf,
//     aggregate.go) once the records in flight are handled.
// A thread that folds and then blocks in an untraced call keeps its sums
// until one of these, so a folded call can reach the stream late; its time
// stays the first call's. Another thread's close of the fd meanwhile is
// sorted out in userspace by time (internal/correlate, aggregate.go).
//
// A flush whose record cannot be reserved is counted like any other ring
// buffer drop (ior_count_ringbuf_drop): one lost record, which hid count
// calls. Each thread's entry is touched only by that thread (and by its own
// exit/exec handlers, which run in its context), so there are no
// cross-CPU races on an entry while the probes are attached.

// IORMESH_RW_INDIVIDUAL hot calls of a key are sent one by one before
// folding starts; the goldens declare at most one in a row.
#define IORMESH_RW_INDIVIDUAL 4
// IORMESH_RW_HOT_NS bounds both the gap since the key's previous call and
// a folded call's own duration: 1 ms.
#define IORMESH_RW_HOT_NS 1000000ULL
// IORMESH_RW_SPAN_NS bounds how long one record's calls may span (100 ms),
// so a loop that runs for minutes still shows up as it runs.
#define IORMESH_RW_SPAN_NS 100000000ULL
// IORMESH_RW_MAX_COUNT keeps count far from the u32 limit.
#define IORMESH_RW_MAX_COUNT (1U << 20)

// iormesh_rw_send sends key s's pending sums as one rw_aggregate_event and
// clears them. The sums are cleared before the submit: the session-end
// reader of rw_agg_map must never find them a second time.
static __always_inline void iormesh_rw_send(struct iormesh_rw_state *st, struct iormesh_rw_slot *s,
                                            __u32 tid) {
    struct rw_aggregate_event *ev;

    if (!s->count)
        return;
    ev = bpf_ringbuf_reserve(&event_map, sizeof(*ev), 0);
    if (ev) {
        ev->event_type = RW_AGGREGATE_EVENT;
        ev->trace_id = s->trace_id;
        ev->time = s->first_ns;
        ev->end_time = s->last_ns;
        ev->total_bytes = s->bytes;
        ev->latency_ns = s->latency_ns;
        ev->pid = st->pid;
        ev->tid = tid;
        ev->fd = s->fd;
        ev->file_ident = s->file_ident;
        ev->count = s->count;
        ev->reserved = 0;
    } else {
        ior_count_ringbuf_drop();
    }
    s->count = 0;
    s->bytes = 0;
    s->latency_ns = 0;
    if (ev)
        bpf_ringbuf_submit(ev, 0);
}

// iormesh_rw_send_held_enter sends the enter record a held call owes once
// its exit shows it cannot be folded, stamped with the call's own enter
// time, so userspace pairs it with the exit that follows as usual (and the
// restart fold, which matches by time, still finds it). fd and identity
// come from the in-flight markers, not the slot, which a flush inside the
// call may have freed.
static __always_inline void iormesh_rw_send_held_enter(struct iormesh_rw_state *st, __u32 pid, __u32 tid,
                                                       __u32 trace_id, __u64 enter_ns) {
    struct fd_event *ev = bpf_ringbuf_reserve(&event_map, sizeof(*ev), 0);

    if (!ev) {
        ior_count_ringbuf_drop();
        return;
    }
    ev->event_type = ENTER_FD_EVENT;
    ev->trace_id = trace_id;
    ev->time = enter_ns;
    ev->pid = pid;
    ev->tid = tid;
    ev->fd = st->cur_fd;
    ev->file_ident = st->cur_ident;
    bpf_ringbuf_submit(ev, 0);
}

// iormesh_rw_flush sends every pending key of tid and frees its slots, so
// the thread's next read or write starts a fresh streak. The in-flight
// markers stay: a flush from signal_generate can run inside a held call,
// whose exit still needs them.
static __always_inline void iormesh_rw_flush(__u32 tid) {
    struct iormesh_rw_state *st;

    if (!IORMESH_RW_AGGREGATE)
        return;
    st = bpf_map_lookup_elem(&rw_agg_map, &tid);
    if (!st)
        return;
    for (int i = 0; i < IORMESH_RW_SLOTS; i++) {
        struct iormesh_rw_slot *s = &st->slot[i];

        if (!s->trace_id)
            continue;
        iormesh_rw_send(st, s, tid);
        s->trace_id = 0;
        s->streak = 0;
    }
}

// iormesh_rw_forget flushes tid and removes its entry: the thread exited,
// or exec replaced its program (and maybe its tid).
static __always_inline void iormesh_rw_forget(__u32 tid) {
    if (!IORMESH_RW_AGGREGATE)
        return;
    iormesh_rw_flush(tid);
    bpf_map_delete_elem(&rw_agg_map, &tid);
}

// iormesh_rw_state_of returns tid's entry, creating it on the thread's
// first read or write; NULL when the map is full (no aggregation then).
static __always_inline struct iormesh_rw_state *iormesh_rw_state_of(__u32 pid, __u32 tid) {
    struct iormesh_rw_state *st = bpf_map_lookup_elem(&rw_agg_map, &tid);
    struct iormesh_rw_state fresh = {};

    if (st)
        return st;
    fresh.pid = pid;
    bpf_map_update_elem(&rw_agg_map, &tid, &fresh, BPF_NOEXIST);
    return bpf_map_lookup_elem(&rw_agg_map, &tid);
}

// iormesh_rw_lru returns the slot whose key ran least recently.
static __always_inline __u32 iormesh_rw_lru(struct iormesh_rw_state *st) {
    __u32 oldest = 0;

    for (int i = 1; i < IORMESH_RW_SLOTS; i++) {
        if (st->slot[i].last_ns < st->slot[oldest & (IORMESH_RW_SLOTS - 1)].last_ns)
            oldest = i;
    }
    return oldest & (IORMESH_RW_SLOTS - 1);
}

// iormesh_rw_slot_for returns the slot of key (trace_id, fd, ident),
// claiming a free one - or the least recently used, flushed first - for a
// new key. The result is always < IORMESH_RW_SLOTS.
static __always_inline __u32 iormesh_rw_slot_for(struct iormesh_rw_state *st, __u32 tid, __u32 trace_id,
                                                 __s32 fd, __u32 ident) {
    __u32 free = IORMESH_RW_SLOTS;
    struct iormesh_rw_slot *s;

    for (int i = 0; i < IORMESH_RW_SLOTS; i++) {
        s = &st->slot[i];
        if (s->trace_id == trace_id && s->fd == fd && s->file_ident == ident)
            return i;
        if (!s->trace_id && free == IORMESH_RW_SLOTS)
            free = i;
    }
    if (free >= IORMESH_RW_SLOTS) {
        free = iormesh_rw_lru(st);
        iormesh_rw_send(st, &st->slot[free], tid);
    }
    s = &st->slot[free];
    s->trace_id = trace_id;
    s->fd = fd;
    s->file_ident = ident;
    s->streak = 0;
    s->last_ns = 0;
    return free;
}

// iormesh_rw_on_enter is the enter hook of the folded kinds, called after
// the per-tid enter hook: it returns 1 when the enter record is to be sent
// and 0 when the call is held (its exit may fold it). A key whose previous
// call is no longer hot is flushed and starts a new streak.
static __always_inline int iormesh_rw_on_enter(__u32 pid, __u32 tid, __u32 trace_id, __s32 fd, __u32 ident,
                                               __u64 now) {
    struct iormesh_rw_state *st;
    struct iormesh_rw_slot *s;
    __u32 idx;

    if (!IORMESH_RW_AGGREGATE)
        return 1;
    st = iormesh_rw_state_of(pid, tid);
    if (!st)
        return 1;
    idx = iormesh_rw_slot_for(st, tid, trace_id, fd, ident);
    s = &st->slot[idx & (IORMESH_RW_SLOTS - 1)];
    st->cur_slot = idx + 1;
    st->cur_enter_ns = now;
    st->cur_fd = fd;
    st->cur_ident = ident;
    st->cur_held = 0;
    if (now > s->last_ns + IORMESH_RW_HOT_NS) {
        iormesh_rw_send(st, s, tid);
        s->streak = 0;
    }
    if (s->streak < IORMESH_RW_INDIVIDUAL)
        return 1;
    st->cur_held = 1;
    return 0;
}

// iormesh_rw_fold adds a held call to its key's pending sums and flushes
// the key once it spans IORMESH_RW_SPAN_NS or reaches the count cap.
static __always_inline void iormesh_rw_fold(struct iormesh_rw_state *st, struct iormesh_rw_slot *s, __u32 tid,
                                            __s64 ret, __u64 enter_ns, __u64 now) {
    if (!s->count)
        s->first_ns = enter_ns;
    s->count += 1;
    s->bytes += (__u64)ret;
    s->latency_ns += now > enter_ns ? now - enter_ns : 0;
    if (s->count >= IORMESH_RW_MAX_COUNT || now >= s->first_ns + IORMESH_RW_SPAN_NS)
        iormesh_rw_send(st, s, tid);
}

// iormesh_rw_on_exit is the exit hook of the folded kinds, called after the
// per-tid exit hook: it returns 1 when the exit record is to be sent and 0
// when the call was folded. A sent call that was hot extends its key's
// streak, any other resets it. A held call that is not hot flushes its key
// and sends the enter it owes, so the stream reads: the key's earlier
// calls, then this call's enter and exit.
static __always_inline int iormesh_rw_on_exit(__u32 pid, __u32 tid, __u32 trace_id, __s64 ret, __u64 now) {
    struct iormesh_rw_state *st;
    struct iormesh_rw_slot *s;
    __u32 idx, held;
    __u64 enter_ns;
    int hot;

    if (!IORMESH_RW_AGGREGATE)
        return 1;
    st = bpf_map_lookup_elem(&rw_agg_map, &tid);
    if (!st)
        return 1;
    idx = st->cur_slot - 1;
    held = st->cur_held;
    enter_ns = st->cur_enter_ns;
    st->cur_slot = 0;
    st->cur_held = 0;
    if (idx >= IORMESH_RW_SLOTS)
        return 1;
    s = &st->slot[idx];
    // A flush (signal_generate in the call, say) freed the slot, or another
    // key took it: the call's key is gone, so it can only be sent.
    if (s->trace_id != trace_id) {
        if (held)
            iormesh_rw_send_held_enter(st, pid, tid, trace_id, enter_ns);
        return 1;
    }
    hot = ret > 0 && now <= enter_ns + IORMESH_RW_HOT_NS;
    if (!held) {
        s->last_ns = now;
        s->streak = hot ? s->streak + 1 : 0;
        return 1;
    }
    // The pending sums end at the last folded exit (their end_time), so
    // last_ns moves on only after they were sent.
    if (!hot) {
        iormesh_rw_send(st, s, tid);
        s->last_ns = now;
        s->streak = 0;
        iormesh_rw_send_held_enter(st, pid, tid, trace_id, enter_ns);
        return 1;
    }
    s->last_ns = now;
    iormesh_rw_fold(st, s, tid, ret, enter_ns, now);
    return 0;
}
