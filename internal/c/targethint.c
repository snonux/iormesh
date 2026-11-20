//+build ignore

// Target hints: a bitmap in front of target_map (task 9f4, decision #25).
//
// Why: filter() runs at the enter and the exit of every traced kind's
// syscall of every task on the host, and nearly all of them belong to no
// target. Its target_map lookup was the largest single BPF cost such a
// bystander call paid: perf on a close(-1) loop beside a tracer attributed
// ~21 % of the call's kernel instructions to __htab_map_lookup_elem and its
// bucket walk, and the hint took 109 of the ~1000 instructions the tracer
// added per call (task 9f4, s3-overhead.md). A bit per pid answers "surely
// not a target" with one inlined array access; only a set bit goes on to the
// hash lookup, which stays the authority (state, gap counting). A bit set
// without an entry - a pid userspace removed, a pending exec it dismissed -
// costs only that lookup; an entry without a bit would be a target nobody
// traces, so every insert sets the bit first.
//
// Two bitmaps, by writer. The kernel's (target_hint_kern) has one bit per
// possible pid and is written without a lock by whoever owns the bit: the
// task_newtask handler sets a child's bit before the child exists, the
// sched_process_exec handler its own process's, sched_process_exit clears it
// when the last thread dies. Those events are ordered by the process itself,
// with one exception: on kernels whose sched_process_exit lacks group_dead,
// two exiting threads can both take the group for dead (exec.c,
// ior_exit_group_dead), so the clear is gated on target_map's delete, which
// succeeds once (iormesh_forget_target). A word's other bits are other
// processes', and the atomic add (BPF_XADD, Linux 5.8's floor - the atomic
// or/and need 5.12) keeps them. Adding the bit is exact only because a bit
// is added only while clear and subtracted only while set, by its single
// writer of the moment: a second subtraction would borrow from the next set
// bit, another process's. Userspace's
// (target_hint_user) holds AddTarget's pids, written under the tracer's
// mutex (internal/bpf/targets.go): it never shares a word with the kernel,
// whose writes userspace could not order its read-modify-write against. Its
// bits are never cleared - AddTarget's pids are few, and a stale bit only
// costs the lookup it saves - so it is folded to 2^16 bits (8 KiB).
//
// Keys are target_map's: pids in the tracer's PID namespace (pidns.c).

// IORMESH_HINT_KERN_WORDS covers PID_MAX_LIMIT (4194304, 2^22) at 64 pids a
// word: 512 KiB. A pid beyond it is never ruled out.
#define IORMESH_HINT_KERN_WORDS (1U << 16)
// IORMESH_HINT_USER_WORDS folds userspace's pids to 2^16 bits; it must
// equal internal/bpf's hintUserWords.
#define IORMESH_HINT_USER_WORDS (1U << 10)

struct {
    __uint(type, BPF_MAP_TYPE_ARRAY);
    __uint(max_entries, IORMESH_HINT_KERN_WORDS);
    __type(key, __u32);
    __type(value, __u64);
} target_hint_kern SEC(".maps");

struct {
    __uint(type, BPF_MAP_TYPE_ARRAY);
    __uint(max_entries, IORMESH_HINT_USER_WORDS);
    __type(key, __u32);
    __type(value, __u64);
} target_hint_user SEC(".maps");

// iormesh_hint_kern_word is pid's word of the kernel bitmap, NULL for a pid
// beyond it.
static __always_inline __u64 *iormesh_hint_kern_word(__u32 pid) {
    __u32 w = pid >> 6;

    if (w >= IORMESH_HINT_KERN_WORDS)
        return NULL;
    return bpf_map_lookup_elem(&target_hint_kern, &w);
}

// iormesh_target_hint reports whether pid may be in target_map; 0 means it
// surely is not.
static __always_inline int iormesh_target_hint(__u32 pid) {
    __u64 bit = 1ULL << (pid & 63);
    __u32 uw = (pid >> 6) & (IORMESH_HINT_USER_WORDS - 1);
    __u64 *k, *u;

    k = iormesh_hint_kern_word(pid);
    if (!k || (*k & bit))
        return 1;
    u = bpf_map_lookup_elem(&target_hint_user, &uw);
    return u && (*u & bit);
}

// iormesh_hint_set sets pid's kernel bit, before its target_map insert. Only
// the bit's owner calls it (see above), so the test and the add do not race.
static __always_inline void iormesh_hint_set(__u32 pid) {
    __u64 bit = 1ULL << (pid & 63);
    __u64 *k = iormesh_hint_kern_word(pid);

    if (k && !(*k & bit))
        __sync_fetch_and_add(k, bit);
}

// iormesh_hint_clear clears pid's kernel bit, at its process's end - only
// after target_map's delete of pid succeeded, so once (iormesh_forget_target).
static __always_inline void iormesh_hint_clear(__u32 pid) {
    __u64 bit = 1ULL << (pid & 63);
    __u64 *k = iormesh_hint_kern_word(pid);

    if (k && (*k & bit))
        __sync_fetch_and_add(k, -bit);
}
