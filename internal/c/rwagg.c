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
//   - every read or write the thread sends as single events - one of a
//     key's first hot calls, a call after a gap, a held call that turns out
//     not hot (EOF, an error, a slow call): all its keys' pending sums,
//     right before the call's own records (a held call's owed enter first,
//     iormesh_rw_send_held_enter), so a single read or write also follows
//     every folded call before it. Only folded records of different keys
//     are spans that may overlap each other;
//   - a key reaching IORMESH_RW_SPAN_NS or IORMESH_RW_MAX_COUNT, checked
//     when a call folds - so a loop whose next call blocks is sent when
//     that call returns (and is not hot);
//   - a fifth key on the thread: the least recently used key is evicted;
//   - a signal the thread itself sends (kill, or a write's SIGPIPE;
//     signal.c), task_newtask in the creator's context (exec.c),
//     sched_process_exec and the thread's sched_process_exit
//     (iormesh_rw_forget, which also removes the entry);
//   - the session end: userspace sets rw_agg_stop, which freezes every
//     entry's sums (no fold, no flush), handles the records in flight,
//     then reads what is left (internal/bpf, aggregate.go).
// A thread that folds and then blocks in an untraced call keeps its sums
// until one of these, so a folded call can reach the stream late; its time
// stays the first call's. The key is the fd number (the file identity is 0
// while IOR_FILE_IDENT is off, which iormesh leaves it), so another
// thread's close, dup2 or reopen of the number can make one record span
// two fd instances; userspace resolves a record by its first and last
// call's times and counts one that straddles a change as lost
// (internal/correlate, aggregate.go), never naming the wrong instance.
//
// A flush whose record cannot be reserved is counted like any other ring
// buffer drop (ior_count_ringbuf_drop): one lost record, which hid count
// calls. Each thread's entry is touched only by programs running in that
// thread's task context: its read/write hooks, the enter hooks of its other
// syscalls, and the exec, exit, new-task and sender-side signal handlers,
// all of which fire from its own system calls or exit path. The one
// handler that can fire in interrupt context, signal_generate, flushes only
// for a signal the current task sent itself (SIGNAL_SCOPE_SENDER), which
// is raised from that task's own syscall, never from an interrupt that
// could land inside one of these hooks.

// IORMESH_RW_INDIVIDUAL hot calls of a key are sent one by one before
// folding starts; the goldens declare at most one in a row.
#define IORMESH_RW_INDIVIDUAL 4
// IORMESH_RW_HOT_NS bounds both the gap since the key's previous call and
// a folded call's own duration: 1 ms.
#define IORMESH_RW_HOT_NS 1000000ULL
// IORMESH_RW_SPAN_NS bounds how long one record's calls may span (100 ms),
// so a loop that runs for minutes still shows up as it runs.
#define IORMESH_RW_SPAN_NS 100000000ULL
// IORMESH_RW_MAX_COUNT keeps count far from the u32 limit. Overridable so
// the host-compiled harness (rwagg_harness_test.go) can reach it.
#ifndef IORMESH_RW_MAX_COUNT
#define IORMESH_RW_MAX_COUNT (1U << 20)
#endif

// iormesh_rw_stopped reports whether userspace froze the aggregation for
// the session's end (rw_agg_stop, maps.h): nothing folds or flushes any
// more, so the sums it reads from rw_agg_map cannot also reach the ring.
static __always_inline int iormesh_rw_stopped(void) {
    __u32 key = 0;
    __u32 *stop = bpf_map_lookup_elem(&rw_agg_stop, &key);

    return stop && *stop;
}

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

// iormesh_rw_send_all sends the pending sums of every key of the thread and
// keeps the keys and their streaks: what a read or write sent as single
// events must follow (see "Flush points").
static __always_inline void iormesh_rw_send_all(struct iormesh_rw_state *st, __u32 tid) {
    for (int i = 0; i < IORMESH_RW_SLOTS; i++) {
        if (st->slot[i].trace_id)
            iormesh_rw_send(st, &st->slot[i], tid);
    }
}

// iormesh_rw_send_held_enter sends the enter record a held call owes once
// its exit shows it cannot be folded, stamped with the call's own enter
// time, so userspace pairs it with the exit that follows as usual (and the
// restart fold, which matches by time, still finds it). fd and identity
// come from the in-flight markers, not the slot, which a flush inside the
// call may have freed.
static __always_inline void iormesh_rw_send_held_enter(__u32 pid, __u32 tid, __u32 trace_id, __s32 fd,
                                                       __u32 ident, __u64 enter_ns) {
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
    ev->fd = fd;
    ev->file_ident = ident;
    bpf_ringbuf_submit(ev, 0);
}

// iormesh_rw_flush sends every pending key of tid and frees its slots, so
// the thread's next read or write starts a fresh streak. The in-flight
// markers stay: a flush from signal_generate can run inside a held call,
// whose exit still needs them.
static __always_inline void iormesh_rw_flush(__u32 tid) {
    struct iormesh_rw_state *st;

    if (!IORMESH_RW_AGGREGATE || iormesh_rw_stopped())
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
// or exec replaced its program (and maybe its tid). Once stopped, the entry
// stays with its frozen sums for the session-end read.
static __always_inline void iormesh_rw_forget(__u32 tid) {
    if (!IORMESH_RW_AGGREGATE || iormesh_rw_stopped())
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
// call is no longer hot starts a new streak, and a call sent as a single
// event first sends every pending sum of the thread. Once stopped, every
// call is sent and the sums stay as they are.
static __always_inline int iormesh_rw_on_enter(__u32 pid, __u32 tid, __u32 trace_id, __s32 fd, __u32 ident,
                                               __u64 now) {
    struct iormesh_rw_state *st;
    struct iormesh_rw_slot *s;
    __u32 idx;

    if (!IORMESH_RW_AGGREGATE || iormesh_rw_stopped())
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
    if (now > s->last_ns + IORMESH_RW_HOT_NS)
        s->streak = 0;
    if (s->streak < IORMESH_RW_INDIVIDUAL) {
        iormesh_rw_send_all(st, tid);
        return 1;
    }
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

// iormesh_rw_settle decides a call on its live key at exit (see
// iormesh_rw_on_exit). The pending sums end at the last folded exit (their
// end_time), so last_ns moves on only after they were sent.
static __always_inline int iormesh_rw_settle(struct iormesh_rw_state *st, struct iormesh_rw_slot *s, __u32 pid,
                                             __u32 tid, __s64 ret, __u64 enter_ns, __u64 now, __u32 held) {
    int hot = ret > 0 && now <= enter_ns + IORMESH_RW_HOT_NS;

    if (!held) {
        s->last_ns = now;
        s->streak = hot ? s->streak + 1 : 0;
        return 1;
    }
    if (!hot) {
        iormesh_rw_send_all(st, tid);
        s->last_ns = now;
        s->streak = 0;
        iormesh_rw_send_held_enter(pid, tid, s->trace_id, s->fd, s->file_ident, enter_ns);
        return 1;
    }
    s->last_ns = now;
    iormesh_rw_fold(st, s, tid, ret, enter_ns, now);
    return 0;
}

// iormesh_rw_on_exit is the exit hook of the folded kinds, called with the
// per-tid exit hook's verdict (emits): it returns 1 when the exit record is
// to be sent and 0 when it is not - the call was folded, or the per-tid
// hook sheds it. The in-flight markers are cleared whatever the verdict,
// so a shed exit never leaves them to the thread's next call. A sent call
// that was hot extends its key's streak, any other resets it. A held call
// that is not hot sends every pending sum of the thread, then the enter it
// owes, then its exit. Once stopped, a held call is only sent.
static __always_inline int iormesh_rw_on_exit(__u32 pid, __u32 tid, __u32 trace_id, __s64 ret, __u64 now,
                                              int emits) {
    struct iormesh_rw_state *st;
    struct iormesh_rw_slot *s;
    __u32 idx, held, ident;
    __u64 enter_ns;
    __s32 fd;

    if (!IORMESH_RW_AGGREGATE)
        return emits;
    st = bpf_map_lookup_elem(&rw_agg_map, &tid);
    if (!st)
        return emits;
    idx = st->cur_slot - 1;
    held = st->cur_held;
    enter_ns = st->cur_enter_ns;
    fd = st->cur_fd;
    ident = st->cur_ident;
    st->cur_slot = 0;
    st->cur_held = 0;
    if (!emits || idx >= IORMESH_RW_SLOTS)
        return emits;
    s = &st->slot[idx];
    // Stopped, or a flush inside the call (a SIGPIPE the write raised)
    // freed the slot or another key took it: the call can only be sent.
    if (iormesh_rw_stopped() || s->trace_id != trace_id) {
        if (held)
            iormesh_rw_send_held_enter(pid, tid, trace_id, fd, ident, enter_ns);
        return 1;
    }
    return iormesh_rw_settle(st, s, pid, tid, ret, enter_ns, now, held);
}
