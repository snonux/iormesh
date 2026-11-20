//+build ignore

// PID-namespace-aware identities (M7 step 1, decision #13, task 4f4).
//
// Why: a tracer in a pod runs in the pod's PID namespace. Its -pid targets,
// its /proc, a JVM's agent ring name (<pid>.ring) and the agent's thread ids
// all speak that namespace's numbers, while bpf_get_current_pid_tgid() and
// task->tgid/pid are the host's. Keyed by host tgids, target_map would hold
// numbers the in-pod tracer can neither name nor look up in its /proc. So
// every pid and tid the BPF side keys a map by or hands to userspace is the
// task's number at the TRACER's namespace level: target_map keys, the per-tid
// state maps and every record's pid/tid. A task above or beside that
// namespace (the node's processes, another pod's) has no number there and
// gets 0 - filter()'s "no" - so the tracer is confined to its pod by
// construction.
//
// bpf_get_ns_current_pid_tgid() alone is not enough: it matches only a
// task's innermost namespace, so a process in a namespace nested inside the
// pod (a sandbox, a container runtime) would be lost. It serves as the fast
// path for the current task (iormesh_ns_current_ids); the walk below reads
// pids[...]->numbers[level], as ior_current_namespace_tgid (filter.c) does,
// at the tracer's level, and checks that level's namespace is the tracer's.
//
// Host mode: userspace leaves IORMESH_PIDNS_INUM 0 when the tracer runs in
// the initial PID namespace. Every helper here then is the plain host read
// it replaced; as a load-time constant the verifier prunes the walk, so host
// sessions emit the same records at the same cost as before.

// IORMESH_PIDNS_INUM is the inode of the tracer's PID namespace
// (stat /proc/self/ns/pid), 0 in the initial namespace (internal/bpf,
// pidns.go). The walk compares only the inode, not the nsfs device
// bpf_get_ns_current_pid_tgid also checks: namespace inodes come from one
// kernel-wide allocator (proc_alloc_inum), so on one kernel the inode alone
// names the namespace.
const volatile u32 IORMESH_PIDNS_INUM = 0;
// IORMESH_PIDNS_DEV is that namespace file's device (the nsfs st_dev), for
// bpf_get_ns_current_pid_tgid's fast path in iormesh_current_pid_tgid.
const volatile u64 IORMESH_PIDNS_DEV = 0;

// IORMESH_MAX_PID_NS_LEVEL is the kernel's MAX_PID_NS_LEVEL (filter.c's
// IOR_MAX_PID_NS_LEVEL, which is defined after this file is included).
#define IORMESH_MAX_PID_NS_LEVEL 32

// IORMESH_PIDNS_LEVEL_UNKNOWN marks a level not discovered yet.
#define IORMESH_PIDNS_LEVEL_UNKNOWN 0xFFFFFFFFU

// iormesh_pidns_level is the tracer namespace's level (0 = initial).
// Userspace cannot tell it: from inside a namespace, NS_GET_PARENT stops at
// the namespace's own boundary. The kernel learns it instead, two ways: the
// sys_enter dispatcher (dispatch.c) searches the levels of every task until
// one is in or below the namespace (iormesh_pidns_learn) - the tracer's own
// first syscall, at the latest - and any hook meeting a task whose innermost
// namespace is the tracer's takes that task's level (iormesh_pid_nr), which
// covers sessions without syscall probes. Until then every task reads as
// outside: a task nested below the namespace is unseen for the moment between
// attach and the tracer's first syscall. A global, not rodata: written once,
// then read by every call; every writer stores the same value, so the
// unordered stores are harmless.
__u32 iormesh_pidns_level = IORMESH_PIDNS_LEVEL_UNKNOWN;

// iormesh_upid_ns_inum is the namespace inode of p's number at level.
static __always_inline __u32 iormesh_upid_ns_inum(struct pid *p, __u32 level) {
    struct pid_namespace *ns = NULL;

    if (bpf_core_read(&ns, sizeof(ns), &p->numbers[level].ns) || !ns)
        return 0;
    return BPF_CORE_READ(ns, ns.inum);
}

// iormesh_pidns_learn looks for the tracer's namespace among the current
// task's levels and caches the level it found. Only the sys_enter
// dispatcher runs the search: inlined into every handler, the loop roughly
// tripled the object's load time (the verifier walks it in each of the
// ~1500 programs), and the dispatcher sees every task's syscalls, the
// tracer's own among them.
static __always_inline void iormesh_pidns_learn(void) {
    struct task_struct *task;
    struct pid *p;
    __u32 level;

    if (!IORMESH_PIDNS_INUM || iormesh_pidns_level != IORMESH_PIDNS_LEVEL_UNKNOWN)
        return;
    task = (struct task_struct *)bpf_get_current_task();
    p = BPF_CORE_READ(task, thread_pid);
    if (!p)
        return;
    level = BPF_CORE_READ(p, level);
    for (__u32 i = 0; i <= IORMESH_MAX_PID_NS_LEVEL; i++) {
        if (i > level)
            return;
        if (iormesh_upid_ns_inum(p, i) == IORMESH_PIDNS_INUM) {
            iormesh_pidns_level = i;
            return;
        }
    }
}

// iormesh_pid_nr is p's number in the tracer's namespace, 0 when p has none
// there (a task above or beside it). The level compare comes first, so a
// node process (level 0) beside a pod tracer costs no namespace read.
static __always_inline __u32 iormesh_pid_nr(struct pid *p) {
    __u32 level, at;
    __s32 nr = 0;

    if (!p)
        return 0;
    level = BPF_CORE_READ(p, level);
    if (level > IORMESH_MAX_PID_NS_LEVEL)
        return 0;
    at = iormesh_pidns_level;
    if (at == IORMESH_PIDNS_LEVEL_UNKNOWN) {
        // Not learnt yet: only a task whose innermost namespace is the
        // tracer's can say its level without a search.
        if (iormesh_upid_ns_inum(p, level) != IORMESH_PIDNS_INUM)
            return 0;
        iormesh_pidns_level = level;
        at = level;
    }
    if (at > level)
        return 0;
    if (iormesh_upid_ns_inum(p, at) != IORMESH_PIDNS_INUM)
        return 0;
    if (bpf_core_read(&nr, sizeof(nr), &p->numbers[at].nr))
        return 0;
    return nr > 0 ? (__u32)nr : 0;
}

// The namespace walk runs in global functions (non-static, __noinline):
// the verifier checks a global function once per program, apart from its
// callers, and hands each caller an unknown scalar back. Inlined, the walk's
// early returns split the verification of everything after each call - the
// object's load took ~6x the kernel CPU of host mode (12.4 s against 2.1 s,
// task 4f4). Few of them, each opening with the host-mode guard: host mode
// never calls them, yet the verifier checks every global function of every
// program all the same, so each one adds to host mode's load. A task pointer travels as a scalar:
// global functions take no kernel pointers.

// iormesh_ns_ids is task's pair in the tracer's namespace: the thread
// group's number (signal->pids[PIDTYPE_TGID], the struct pid behind
// task_tgid()) in the high half, the thread's in the low; 0 for a task
// outside the namespace. The tid is read only for a task inside it, so a
// node process beside a pod's tracer pays one walk.
__noinline __u64 iormesh_ns_ids(__u64 task_ptr) {
    struct task_struct *task = (struct task_struct *)task_ptr;
    struct signal_struct *signal;
    __u32 tgid;

    // Never true when called; it makes the verifier, which checks this
    // function in host mode too, prune the walk there.
    if (!IORMESH_PIDNS_INUM)
        return 0;
    signal = BPF_CORE_READ(task, signal);
    if (!signal)
        return 0;
    tgid = iormesh_pid_nr(BPF_CORE_READ(signal, pids[PIDTYPE_TGID]));
    if (!tgid)
        return 0;
    return ((__u64)tgid << 32) | iormesh_pid_nr(BPF_CORE_READ(task, thread_pid));
}

// iormesh_task_tgid is task's process id as the tracer names it: the host
// tgid in host mode, else its number in the tracer's namespace.
static __always_inline __u32 iormesh_task_tgid(struct task_struct *task) {
    if (!IORMESH_PIDNS_INUM)
        return (__u32)BPF_CORE_READ(task, tgid);
    return (__u32)(iormesh_ns_ids((__u64)task) >> 32);
}

// iormesh_task_tid is task's thread id as the tracer names it.
static __always_inline __u32 iormesh_task_tid(struct task_struct *task) {
    if (!IORMESH_PIDNS_INUM)
        return (__u32)BPF_CORE_READ(task, pid);
    return (__u32)iormesh_ns_ids((__u64)task);
}

// iormesh_ns_current_ids is the current task's pair. Most tasks a pod's
// tracer meets live exactly in its namespace - the pod's processes - and for
// them bpf_get_ns_current_pid_tgid answers in one helper call, without a
// probe read; the walk is left to tasks above, beside or nested below it.
// Measured on the S3 burst (task 4f4): with the walk alone, an untraced
// burst in the tracer's namespace paid 1.9x its CPU time against 1.2x on
// the host, every syscall paying the walk's eleven probe reads.
__noinline __u64 iormesh_ns_current_ids(void) {
    struct bpf_pidns_info ns = {};

    if (!IORMESH_PIDNS_INUM)
        return 0;
    if (!bpf_get_ns_current_pid_tgid(IORMESH_PIDNS_DEV, IORMESH_PIDNS_INUM, &ns, sizeof(ns)))
        return ((__u64)ns.tgid << 32) | ns.pid;
    return iormesh_ns_ids(bpf_get_current_task());
}

// struct iormesh_ns_ids_slot is ns_ids_cache's value: a task, its start
// time and its pair. The start time makes a reused task_struct another
// task, and it also changes when a non-leader exec takes the leader's pid
// (de_thread copies the leader's start_time), the one event that renumbers
// a live task; a task never changes its PID namespace.
struct iormesh_ns_ids_slot {
    __u64 task;
    __u64 start;
    __u64 ids;
};

// ns_ids_cache remembers, per CPU, the pair filter() last looked up. Why:
// filter() runs at every traced kind's sys_enter and sys_exit of every task
// on the host, mostly one task's calls back to back on a CPU, and even the
// helper fast path made such untraced calls cost twice the BPF time they
// cost a host tracer (close(-1) loop, task 4f4). A hit is two helper calls
// and three compares. Only filter() uses it: it runs in classic tracepoint
// programs, which never nest on a CPU and are never preempted (dispatch.c),
// so a slot is never half-written when read. Unused in host mode.
struct {
    __uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
    __uint(max_entries, 1);
    __type(key, __u32);
    __type(value, struct iormesh_ns_ids_slot);
} ns_ids_cache SEC(".maps");

// iormesh_current_start_time is the current task's start time, by a direct
// load where the kernel has bpf_get_current_task_btf (5.11) and by a probe
// read elsewhere; the enum relocation prunes the other branch at load.
static __always_inline __u64 iormesh_current_start_time(void) {
    if (bpf_core_enum_value_exists(enum bpf_func_id, BPF_FUNC_get_current_task_btf))
        return bpf_get_current_task_btf()->start_time;
    return BPF_CORE_READ((struct task_struct *)bpf_get_current_task(), start_time);
}

// iormesh_ns_cached_ids is iormesh_ns_current_ids through ns_ids_cache.
__noinline __u64 iormesh_ns_cached_ids(void) {
    struct iormesh_ns_ids_slot *slot;
    __u32 zero = 0;
    __u64 task, start, ids;

    if (!IORMESH_PIDNS_INUM)
        return 0;
    task = bpf_get_current_task();
    start = iormesh_current_start_time();
    slot = bpf_map_lookup_elem(&ns_ids_cache, &zero);
    if (slot && slot->task == task && slot->start == start)
        return slot->ids;
    ids = iormesh_ns_current_ids();
    // A 0 from before the level is learnt is no verdict on a task nested
    // below the namespace, so it is not remembered.
    if (slot && (ids || iormesh_pidns_level != IORMESH_PIDNS_LEVEL_UNKNOWN)) {
        slot->task = task;
        slot->start = start;
        slot->ids = ids;
    }
    return ids;
}

// iormesh_filter_pid_tgid is iormesh_current_pid_tgid for filter() alone,
// through the per-CPU cache (see ns_ids_cache for why only there).
static __always_inline __u64 iormesh_filter_pid_tgid(void) {
    if (!IORMESH_PIDNS_INUM)
        return bpf_get_current_pid_tgid();
    return iormesh_ns_cached_ids();
}

// iormesh_current_pid_tgid is bpf_get_current_pid_tgid() in the tracer's
// namespace: tgid in the high half, tid in the low, 0 for a task outside it.
// Everything that keys per-thread state or fills a record's pid/tid uses it,
// so the kernel side never mixes host and namespace numbers.
static __always_inline __u64 iormesh_current_pid_tgid(void) {
    if (!IORMESH_PIDNS_INUM)
        return bpf_get_current_pid_tgid();
    return iormesh_ns_current_ids();
}

// The execve and execveat syscall numbers, for iormesh_note_exec_tid.
#if defined(__TARGET_ARCH_arm64)
#define IORMESH_NR_EXECVE 221
#define IORMESH_NR_EXECVEAT 281
#else
#define IORMESH_NR_EXECVE 59
#define IORMESH_NR_EXECVEAT 322
#endif

// exec_tid_map carries a thread's namespace tid from its execve's sys_enter
// to sched_process_exec, keyed by its host tid. Why: when a non-leader thread
// execs, the tracepoint names the old tid only as a host number (old_pid),
// and by then the thread has taken the leader's struct pid - the old one is
// reachable from nowhere. ior_on_exec_tid_change (filter.c) needs the old
// tid in the namespace's numbers to move the call's enter state. LRU: an
// exec that fails leaves its entry behind for the LRU to reclaim. Unused in
// host mode.
struct {
    __uint(type, BPF_MAP_TYPE_LRU_HASH);
    __uint(max_entries, 1024);
    __type(key, __u32);
    __type(value, __u32);
} exec_tid_map SEC(".maps");

// iormesh_note_exec_tid records the namespace tid of a thread entering
// execve or execveat (called by the sys_enter dispatcher, dispatch.c, for
// every syscall: a compare in namespace mode, pruned in host mode).
static __always_inline void iormesh_note_exec_tid(long id) {
    __u64 ids;
    __u32 host_tid, tid;

    if (!IORMESH_PIDNS_INUM)
        return;
    if (id != IORMESH_NR_EXECVE && id != IORMESH_NR_EXECVEAT)
        return;
    ids = iormesh_current_pid_tgid();
    tid = (__u32)ids;
    if (!tid)
        return;
    host_tid = (__u32)bpf_get_current_pid_tgid();
    bpf_map_update_elem(&exec_tid_map, &host_tid, &tid, BPF_ANY);
}

// iormesh_exec_old_tid turns sched_process_exec's old_pid (a host tid) into
// the tracer's numbers; tid is the post-exec thread's. A leader's exec keeps
// its tid; a non-leader's old tid comes from exec_tid_map, 0 if it was lost
// (the call's enter state then is settled as an unpaired exit would be).
static __always_inline __u32 iormesh_exec_old_tid(__u32 host_old_tid, __u32 tid) {
    __u32 *old;
    __u32 nr;

    if (!IORMESH_PIDNS_INUM)
        return host_old_tid;
    if (host_old_tid == (__u32)bpf_get_current_pid_tgid())
        return tid;
    old = bpf_map_lookup_elem(&exec_tid_map, &host_old_tid);
    if (!old)
        return 0;
    nr = *old;
    bpf_map_delete_elem(&exec_tid_map, &host_old_tid);
    return nr;
}

// IORMESH_CLONE_THREAD is CLONE_THREAD (exec.c's IOR_CLONE_THREAD, defined
// after this file is included): the new task joins the creator's thread
// group.
#define IORMESH_CLONE_THREAD 0x00010000ULL
// IORMESH_CLONE_PARENT is CLONE_PARENT (include/uapi/linux/sched.h): the
// child's parent is the creator's parent.
#define IORMESH_CLONE_PARENT 0x00008000ULL
// IORMESH_NEWTASK_TRIES bounds the walk back along the list the child was
// appended to: a sibling thread's concurrent clone may have appended its own
// child after ours (copy_process links under tasklist_lock and fires
// task_newtask after dropping it).
#define IORMESH_NEWTASK_TRIES 4

// iormesh_newtask_child finds the task task_newtask announced (host tid
// host_tid) at the tail of the list copy_process() linked it into: the
// creator's thread list for a new thread, its parent's children for a new
// process. The classic tracepoint hands over no task pointer, and the child's
// number in the tracer's namespace (which may be a nested one's, for
// CLONE_NEWPID) lives only in its struct pid. NULL if not found.
static __always_inline struct task_struct *
iormesh_newtask_child(__u64 clone_flags, __u32 host_tid) {
    struct task_struct *cur = (struct task_struct *)bpf_get_current_task();
    struct task_struct *parent = cur;
    struct list_head *head, *node;
    __u64 off;

    if (clone_flags & IORMESH_CLONE_THREAD) {
        head = &BPF_CORE_READ(cur, signal)->thread_head;
        off = bpf_core_field_offset(struct task_struct, thread_node);
    } else {
        if (clone_flags & IORMESH_CLONE_PARENT)
            parent = BPF_CORE_READ(cur, real_parent);
        head = &parent->children;
        off = bpf_core_field_offset(struct task_struct, sibling);
    }
    node = BPF_CORE_READ(head, prev);
#pragma unroll
    for (int i = 0; i < IORMESH_NEWTASK_TRIES; i++) {
        struct task_struct *task;

        if (!node || node == head)
            return NULL;
        task = (struct task_struct *)((void *)node - off);
        if ((__u32)BPF_CORE_READ(task, pid) == host_tid)
            return task;
        node = BPF_CORE_READ(node, prev);
    }
    return NULL;
}

// iormesh_newtask_ids sets the new task's tgid and tid as the tracer names
// them. Host mode keeps the handler's derivation from the tracepoint's host
// tid; namespace mode reads the child's own struct pids. Both are 0 for a
// child the walk did not find or that has no number in the namespace.
static __always_inline void
iormesh_newtask_ids(__u64 clone_flags, __u32 host_tid, __u32 creator_pid,
                    __u32 *child_pid, __u32 *child_tid) {
    struct task_struct *child;

    if (!IORMESH_PIDNS_INUM) {
        *child_tid = host_tid;
        *child_pid = (clone_flags & IORMESH_CLONE_THREAD) ? creator_pid : host_tid;
        return;
    }
    *child_pid = 0;
    *child_tid = 0;
    child = iormesh_newtask_child(clone_flags, host_tid);
    if (!child)
        return;
    *child_tid = iormesh_task_tid(child);
    *child_pid = (clone_flags & IORMESH_CLONE_THREAD) ? creator_pid : *child_tid;
}
