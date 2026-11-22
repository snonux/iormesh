// Adapted from ior internal/c/maps.h @ b21afc50328c.
// Changes: target_map added (replaces ior's rodata PID_FILTER), with
// struct iormesh_target values for tree following and pattern sessions (M2);
// rw_agg_map, rw_agg_stop and struct iormesh_rw_state added (rwagg.c, M2
// step 11); stack_map added (stack.c, M2 step 9); java_mark_map and struct
// iormesh_java_mark added (javamark.c, task td4); restart_pending_map
// removed with restart.c (task pf4); event_map's and the sampling map's
// comments say what iormesh's userspace does with them (task gj4).

//+build ignore

// struct iormesh_target is one target_map value. A TRACED entry is a process
// whose syscalls are traced: a session target userspace inserted, or a child
// the task_newtask handler inserted because its creator was TRACED (M2
// process-tree following, exec.c). A PENDING entry exists only in pattern
// sessions (IORMESH_EXEC_WATCH): the sched_process_exec handler inserts it for
// every exec of an untraced process, before it emits the exec_argv_event
// userspace matches the target pattern against. Until userspace decides,
// filter() counts every record the process would have produced in gap - the
// attach gap (docs/implementation.md M2 step 2) - and userspace either turns
// the entry TRACED and reports gap as an attach_gap drop, or deletes it. since
// is the exec's boot-clock time, also the record's time, so userspace deletes
// only the pending entry of the exec it judged and never a newer one (a
// posix_spawn child exec's jspawnhelper and then the app, two execs of one
// pid in quick succession). The value is 16 bytes with no implicit padding;
// internal/bpf writes it with the same layout.
#define IORMESH_TARGET_TRACED 1
#define IORMESH_TARGET_PENDING 2
struct iormesh_target {
    __u32 state;
    __u32 gap;
    __u64 since;
};

// target_map is the set of TGIDs being traced or, in a pattern session,
// waiting for userspace's verdict (struct iormesh_target above). The
// sched_process_exit handler removes a process's entry once its whole thread
// group is dead, so a reused PID never inherits it. 16384 entries leave room
// for process trees and for the pending entries of a burst of host-wide execs
// that userspace has not judged yet; a full map makes a userspace insert fail
// loudly and a kernel-side insert count a drop (exec.c), never silently.
struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 16384);
    __type(key, __u32);
    __type(value, struct iormesh_target);
} target_map SEC(".maps");

// event_map is the ring buffer carrying every event to userspace. max_entries
// is its size in BYTES, and iormesh loads it as written here: its loader
// (internal/bpf/loader.go) does not resize the map and has no flag for it,
// so this line is the one place the size is set. (ior overrides it at load
// time from -mapSize and pins its default to this line with a test; neither
// was lifted.) It is 16 MiB because a smaller buffer drops events whenever
// the consumer pauses for more than a fraction of a millisecond under load;
// such drops are counted (ringbuf_drop_map below).
struct {
    __uint(type, BPF_MAP_TYPE_RINGBUF);
    __uint(max_entries, 1 << 24);
} event_map SEC(".maps");

// pending_filename carries a user-space string pointer from sys_enter to
// sys_exit: the path argument of a syscall whose sys_enter
// bpf_probe_read_user_str faulted (open, stat, access, unlink, rename, ...), so
// the sys_exit handler can read the string once the kernel has faulted the page
// in, and the output buffer of getcwd (outputPathSyscalls in
// internal/generate/classify.go), whose path the kernel only writes during the
// call. pending_filename2 is the same for the second path of the rename/link
// family (newname) and of move_mount (to_pathname); either of the two reads
// can fault, both or neither. For name_to_handle_at, a single-path kind, the
// second slot instead carries the output struct file_handle pointer its exit
// handler reads the returned handle through (ior_stash_pending_handle in
// handle.c). io_uring_register, which captures no path, uses both slots for
// its two ring-fds opcodes: the pointer to the io_uring_rsrc_update array in
// the first and the opcode in the second (ior_stash_ring_fds in iouring.c).
// 0 means "nothing to read" and is the state
// ior_on_syscall_enter leaves behind for every other syscall (the struct is
// zero-initialised there).
struct syscall_enter_state {
    __u64 start_ns;
    __u64 pending_filename;
    __u64 pending_filename2;
    __u32 enter_trace_id;
    __u8 emit_event;
};

// count must stay the first field: userspace may read a slot while the
// kernel updates it, the copy runs in ascending address order, and
// ior_update_syscall_aggregate (filter.c) stores count last so that a torn
// read sees count no newer than any other field. Moving count down would let
// a torn read see it ahead of the histogram and book a timed invocation as
// untimed. internal/generate/enterstate_fallback_test.go pins this.
struct syscall_aggregate {
    __u64 count;
    __u64 errors;
    __u64 total_duration_ns;
    __u64 min_duration_ns;
    __u64 max_duration_ns;
    __u64 duration_histogram[8];
};

struct socketpair_ctx {
    __u64 usockvec;
    __s32 family;
    __s32 type;
    __s32 protocol;
};

struct pipe_ctx {
    __u64 upipefd;
    __s32 flags;
};

struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 8192);
    __type(key, __u32);
    __type(value, struct socketpair_ctx);
} socketpair_ctx_map SEC(".maps");

struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 8192);
    __type(key, __u32);
    __type(value, struct pipe_ctx);
} pipe_ctx_map SEC(".maps");

struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 8192);
    __type(key, __u32);
    __type(value, __s32);
} eventfd_flags_map SEC(".maps");

// syscall_enter_state_map carries each tid's in-flight syscall from sys_enter
// to sys_exit. It is bounded, so a write can fail on a host with more threads
// parked in traced syscalls than entries; ior_on_syscall_enter and
// ior_on_syscall_exit (internal/c/filter.c, "Enter state and its two
// fallbacks") keep the aggregate/ring-buffer partition exact when it does.
struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 32768);
    __type(key, __u32);
    __type(value, struct syscall_enter_state);
} syscall_enter_state_map SEC(".maps");

struct {
    __uint(type, BPF_MAP_TYPE_PERCPU_HASH);
    __uint(max_entries, 4096);
    __type(key, __u32);
    __type(value, struct syscall_aggregate);
} syscall_aggregate_map SEC(".maps");

// syscall_sampling_rate_map holds the sampling rate of each enter trace ID
// (task 2s2). It is an ARRAY indexed by the trace ID, not a HASH: every
// traced syscall reads it at sys_enter and again at sys_exit, and an array
// lookup is inlined by the verifier where a hash lookup is a helper call
// (~280 ns per traced syscall in total was measured, a good part of it these
// helper calls). Trace IDs top out near 1900, far below max_entries; an ID
// beyond it looks up NULL and gets the default rate, like an unconfigured one.
//
// An array has no "absent" state - an untouched slot reads 0, which as a
// rate would mean aggregate-only - so a slot stores rate + 1 and 0 means
// "not configured, use the default rate 1" (ior_sampling_rate decodes it;
// ior encodes it in applySyscallSamplingRates,
// ior internal/syscall_aggregate_consumer.go). iormesh's userspace never
// writes this map, so every slot reads 0 and every traced syscall runs at
// the default rate 1.
struct {
    __uint(type, BPF_MAP_TYPE_ARRAY);
    __uint(max_entries, 4096);
    __type(key, __u32);
    __type(value, __u32);
} syscall_sampling_rate_map SEC(".maps");

// ringbuf_drop_map counts events lost because bpf_ringbuf_reserve() found
// event_map full. A full ring buffer is the kernel-side symptom of userspace
// backpressure (the libbpfgo ring-buffer callback blocks on a full Go channel),
// and without this counter those losses are completely silent.
//
// PERCPU_ARRAY with a single slot: the increment sits on the hot drop path, so
// each CPU bumps its own private u64 without atomics or contention; userspace
// sums the per-CPU values when reporting.
struct {
    __uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
    __uint(max_entries, 1);
    __type(key, __u32);
    __type(value, __u64);
} ringbuf_drop_map SEC(".maps");

// struct iormesh_rw_slot is one aggregation key of a thread: the read or
// write calls (trace_id, the enter id; 0 marks a free slot) on one fd
// instance (fd plus its file identity). streak counts the key's consecutive
// hot calls sent individually so far; count, bytes, latency_ns and first_ns
// describe the folded calls not flushed yet (count 0: nothing pending).
// last_ns is the exit time of the key's latest call, folded or not, which
// tells whether the next call continues a hot loop (rwagg.c).
struct iormesh_rw_slot {
    __u64 first_ns;
    __u64 last_ns;
    __u64 bytes;
    __u64 latency_ns;
    __s32 fd;
    __u32 file_ident;
    __u32 trace_id;
    __u32 streak;
    __u32 count;
    __u32 reserved;
};

// IORMESH_RW_SLOTS is how many keys one thread aggregates at once: a copy
// loop needs two (read one fd, write another), the syscall burst too (a
// write fd and a read fd), a two-way proxy four. A fifth key evicts the
// least recently used one, flushing it first.
#define IORMESH_RW_SLOTS 4

// struct iormesh_rw_state is one thread's rw_agg_map value. cur_* describe
// the read or write in flight: its slot + 1 (0 = none), its enter time, fd
// and file identity, and whether its enter record was held back
// (cur_held), in which case its exit either folds it or sends the enter it
// owes. pid is the thread's tgid, which every record flushed from here
// carries. internal/bpf parses this layout at session end (aggregate.go);
// 256 bytes, no implicit padding.
struct iormesh_rw_state {
    struct iormesh_rw_slot slot[IORMESH_RW_SLOTS];
    __u64 cur_enter_ns;
    __u32 cur_slot;
    __u32 cur_held;
    __s32 cur_fd;
    __u32 cur_ident;
    __u32 pid;
    __u32 reserved;
};

// rw_agg_map holds the aggregation state of every traced thread that made a
// read or write (rwagg.c, decision #25). Modelled on ior's
// syscall_aggregate_map above, which sums a syscall's calls in the kernel so
// userspace need not see each one; here the sums are per thread and fd and
// are flushed into event_map as rw_aggregate_event records instead of being
// polled, so every folded call still reaches the trace, in its thread's
// order. Entries are removed when their thread exits or execs. A full map
// (8192 threads with live entries) only stops aggregating for the threads
// that found no entry: their calls are sent one by one, as without
// aggregation.
struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 8192);
    __type(key, __u32);
    __type(value, struct iormesh_rw_state);
} rw_agg_map SEC(".maps");

// rw_agg_stop is the session end's switch for rw_agg_map (rwagg.c): userspace
// writes 1 into its one slot before it handles the last records in flight
// and reads the map, and from then on no hook folds or flushes, so the sums
// it reads can no longer also reach the ring buffer (or change under the
// read). An ARRAY, so the per-call check is an inlined lookup.
struct {
    __uint(type, BPF_MAP_TYPE_ARRAY);
    __uint(max_entries, 1);
    __type(key, __u32);
    __type(value, __u32);
} rw_agg_stop SEC(".maps");

// stack_map holds the distinct user stacks of syscall enters (stack.c, M2
// step 9; iormesh run -native-stacks). bpf_get_stackid hashes a stack into
// one of max_entries buckets and returns the bucket as its id; a stack
// already there gets the same id (so the map deduplicates and userspace
// reads each stack once), another stack hashing to an occupied bucket gets
// -EEXIST. No BPF_F_REUSE_STACKID: overwriting a bucket would change what
// an id already in the trace stands for, so an id stays valid for the whole
// session; a colliding stack is sent inline instead (struct
// stack_inline_event, stack.c). IORMESH_STACK_DEPTH is in types.h.
// Preallocated: about 4.3 MB here, which is why userspace shrinks it to one
// entry when native stacks are off (internal/bpf, Config.NativeStacks).
struct {
    __uint(type, BPF_MAP_TYPE_STACK_TRACE);
    __uint(max_entries, 8192);
    __uint(key_size, sizeof(__u32));
    __uint(value_size, IORMESH_STACK_DEPTH * sizeof(__u64));
} stack_map SEC(".maps");

// IORMESH_JAVA_MARK_DEPTH is how many nested hooked natives one thread's mark
// keeps (javamark.c). Hooked natives are leaves (S1 rule 1): they nest only
// when a native's JVM work runs Java code that calls another (an exception
// class loaded from a JAR), so 4 is room to spare; deeper, the mark says
// "unknown" until the thread is out of every native again.
#define IORMESH_JAVA_MARK_DEPTH 4

// struct iormesh_java_frame is one hooked native a thread is inside: the
// attach cookie of its uprobe (the native's id, set by userspace), its entry
// time on CLOCK_MONOTONIC and the user stack pointer at the entry, which
// tells the return probe which frames it ends (javamark.c).
struct iormesh_java_frame {
    __u64 cookie;
    __u64 entry;
    __u64 sp;
};

// struct iormesh_java_mark is one thread's stack of hooked natives, innermost
// at depth - 1. lost is set when a native was entered with the stack full:
// its frame is not kept, so the innermost native is unknown until the stack
// empties again.
struct iormesh_java_mark {
    __u32 depth;
    __u32 lost;
    struct iormesh_java_frame frames[IORMESH_JAVA_MARK_DEPTH];
};

// java_mark_map holds the Java mark per thread id (javamark.c, M3, task td4).
// An entry is made by a thread's first hooked native and kept, empty, after
// the native returns - an update in place is cheaper than an insert and a
// delete per call - until the thread exits (exec.c). LRU: should the threads
// that ever called a native outgrow it, the least recently used entry goes;
// a thread that loses its entry inside a native loses its mark, and its
// syscalls stay unattributed (never misattributed). 104 bytes a value, about
// 1.7 MB preallocated, which is why userspace shrinks it to one entry when
// the mark is off (internal/bpf, Config.JavaMarks).
struct {
    __uint(type, BPF_MAP_TYPE_LRU_HASH);
    __uint(max_entries, 16384);
    __type(key, __u32);
    __type(value, struct iormesh_java_mark);
} java_mark_map SEC(".maps");
