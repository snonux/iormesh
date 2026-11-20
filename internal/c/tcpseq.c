//+build ignore

// TCP sequence numbers of single sends and receives (iormesh, PLAN.md M4
// "Clock reconciliation from the traffic itself", task xe4). Included by
// iormesh.bpf.c after fileident.c and sock.c, whose walk and constants it
// uses, and ahead of the generated handlers, which call the two hooks below
// (internal/generate/tcpseq.go).
//
// The controller's merge pairs every receive with the send that queued its
// last byte, by stream offset (internal/federate, clock.go), so each unfolded
// write, writev, sendto, sendfile64, read, readv and recvfrom on a TCP socket
// needs the sequence number of its first byte: args.tcp_seq. Which field
// holds it:
//   - a send appends at tcp_sock.write_seq (the end of the send queue), not
//     at snd_nxt, which lags behind whatever is still queued;
//   - a receive copies from tcp_sock.copied_seq (the next byte the reader
//     gets), not rcv_nxt, which runs ahead by whatever is still unread.
// Both are read in the syscall hooks, not in tcp_sendmsg/tcp_recvmsg
// kprobes: the syscall hooks already run (dispatch.c), while a kprobe would
// add a program run to every TCP send and receive on the host, traced or
// not, and share bpf_prog_active with the dispatchers.
//
// The race. The hooks run without the socket lock, and another thread may
// send (or read) on the same socket at the same time; a single read at the
// enter could then name another call's first byte. So the field is read
// twice: at the enter (start), parked by tid, and again at the exit (end).
// The field only grows while bytes are queued (or consumed), and the call
// moved ret bytes between the two reads, so end - start == ret proves that
// no other bytes went through in between and the call's bytes are exactly
// [start, end). Anything else - another sender or reader interleaving, a
// call split around one, a MSG_PEEK or MSG_OOB read (copied_seq does not
// move, or skips the urgent byte), a kTLS socket (write_seq counts record
// framing), a TCP Fast Open send (the ISN is assigned inside the call) -
// fails the check and the call gets no tcp_seq: never a guess. The one
// residue is an assignment of the field inside the window (connect,
// disconnect, TCP_REPAIR) whose jump equals ret by chance, 1 in 2^32.
// A start read long before the call (a parked entry whose exit was missed)
// is still exact when the check holds; userspace makes sure the record's
// socket is the call's (struct tcp_seq_event).
//
// Cost (decision #25). The fd -> tcp_sock walk is fileident.c's typed walk
// (bpf_get_current_task_btf and the bpf_rdonly_cast kfunc, no helper calls):
// probe reads would cost ~650 instructions per call on every traced read and
// write. A kernel without the kfunc (before 6.2) captures nothing, exactly as
// fileident.c reports no identity there. Only calls whose records are sent
// pay: the hooks sit behind the sampling and fold decisions, so a folded
// read or write costs nothing here. A call on anything but a TCP socket pays
// the walk to the inode's mode; a TCP one adds a per-tid map write at the
// enter, and every hooked exit one map lookup. The sequence number travels
// in the exit record itself (struct tcp_seq_event replaces the ret_event),
// not in a record of its own: the tracer's cost is per ring record (one
// callback each), and a third record per call measured +0.6 us per event.

// Directions, the dir argument of both hooks. 0 marks an empty park.
#define IORMESH_SEQ_SEND 1
#define IORMESH_SEQ_RECV 2

// seq_park is one thread's parked start: the socket and the field's value
// at the enter of the call in flight, dir 0 once the exit used it.
struct seq_park {
    __u64 sk;
    __u32 seq;
    __u32 dir;
};

// seq_park_map: tid -> parked start. An entry is kept, emptied, after its
// exit - an update in place is cheaper than an insert and a delete per call
// (java_mark_map does the same) - so a thread pays the insert once. LRU,
// because entries are never deleted: a thread that exited, or one whose
// exit never ran, leaves an entry the least recently used order removes.
// Neither can produce a wrong record: a stale start fails the exit's check
// unless it is exact anyway (see above), and an evicted one costs a single
// call its tcp_seq.
struct {
    __uint(type, BPF_MAP_TYPE_LRU_HASH);
    __uint(max_entries, 8192);
    __type(key, __u32);
    __type(value, struct seq_park);
} seq_park_map SEC(".maps");

// iormesh_tcp_sock_typed returns the current task's descriptor fd as a
// typed tcp_sock when it is an IPv4/IPv6 TCP socket, else NULL, by
// fileident.c's walk: ior_file_of_table, then one bpf_rdonly_cast each to
// type private_data (a void *) as the struct socket and the sock as the
// struct tcp_sock. A socket inode is no proof alone (an O_PATH descriptor
// of a bound AF_UNIX path has one; sock.c), hence the type checks. The
// pointer is valid only within this program run; every load through it is
// fault-safe.
static __always_inline struct tcp_sock *iormesh_tcp_sock_typed(__s32 fd) {
    struct task_struct *task;
    struct file *file;
    struct socket *sock;
    struct sock *sk;
    __u16 family;

    if (!ior_file_ident_supported())
        return 0;
    task = bpf_get_current_task_btf();
    file = ior_file_of_table(task->files, fd);
    if (!file || !file->f_inode)
        return 0;
    if ((file->f_inode->i_mode & IORMESH_S_IFMT) != IORMESH_S_IFSOCK)
        return 0;
    sock = bpf_rdonly_cast(file->private_data, bpf_core_type_id_kernel(struct socket));
    if (!sock || sock->type != IORMESH_SOCK_STREAM)
        return 0;
    sk = sock->sk;
    if (!sk)
        return 0;
    family = sk->__sk_common.skc_family;
    if ((family != IORMESH_AF_INET && family != IORMESH_AF_INET6) || sk->sk_protocol != IPPROTO_TCP)
        return 0;
    return bpf_rdonly_cast(sk, bpf_core_type_id_kernel(struct tcp_sock));
}

// iormesh_tcp_seq_field reads the field dir names: write_seq for a send,
// copied_seq for a receive. Each is one aligned 32-bit load, so a read
// racing the kernel's update sees the old or the new value, never a mix.
static __always_inline __u32 iormesh_tcp_seq_field(struct tcp_sock *tp, __u32 dir) {
    if (dir == IORMESH_SEQ_SEND)
        return tp->write_seq;
    return tp->copied_seq;
}

// iormesh_seq_on_enter is the enter hook of the seven data calls: it parks
// the start of a call on a TCP socket. It runs only for an enter whose
// record is sent (behind the sampling and fold hooks). A call on anything
// else parks nothing and leaves the thread's entry as it was: its exit then
// finds an empty or stale entry, and either sends nothing.
static __always_inline void iormesh_seq_on_enter(__u32 tid, __s32 fd, __u32 dir) {
    struct tcp_sock *tp = iormesh_tcp_sock_typed(fd);
    struct seq_park park;
    struct seq_park *slot;

    if (!tp)
        return;
    park.sk = (__u64)tp;
    park.seq = iormesh_tcp_seq_field(tp, dir);
    park.dir = dir;
    slot = bpf_map_lookup_elem(&seq_park_map, &tid);
    if (slot) {
        *slot = park;
        return;
    }
    bpf_map_update_elem(&seq_park_map, &tid, &park, BPF_ANY);
}

// iormesh_emit_tcp_seq sends the call's exit record with its sequence
// number: a tcp_seq_event in place of the ret_event. A full ring loses it
// like any record (counted as a drop) - the exit record is then lost, as a
// failed ret_event reserve would have lost it.
static __always_inline void iormesh_emit_tcp_seq(__u32 pid, __u32 tid, __u32 trace_id, __u64 now,
                                                 __s64 ret, __u64 sk, __u32 seq) {
    struct tcp_seq_event *ev = bpf_ringbuf_reserve(&event_map, sizeof(*ev), 0);

    if (!ev) {
        ior_count_ringbuf_drop();
        return;
    }
    ev->event_type = TCP_SEQ_EVENT;
    ev->trace_id = trace_id;
    ev->time = now;
    ev->ret = ret;
    ev->pid = pid;
    ev->tid = tid;
    ev->sk = sk;
    ev->seq = seq;
    ev->reserved = 0;
    bpf_ringbuf_submit(ev, 0);
}

// iormesh_seq_on_exit is the exit hook of the seven data calls, behind
// the hooks that decide the exit record is sent: it empties the thread's
// park and, when the call moved bytes and the field moved by exactly that
// many since the park (see the top of this file), sends the exit record
// itself, with the call's first sequence number, and returns 1 - the
// handler then sends no ret_event. Otherwise it returns 0 and the handler
// sends its plain ret_event. A park of the other direction is another
// call's (a missed exit) and is only emptied. The socket is re-read
// through the parked address: the call held the file, so the socket
// outlived it; if another thread closed it right after, the read is
// fault-safe, and a freed or reused socket fails the check (or
// userspace's socket check).
static __always_inline int iormesh_seq_on_exit(__u32 pid, __u32 tid, __u32 trace_id, __u64 now,
                                               __s64 ret, __u32 dir) {
    struct seq_park *slot = bpf_map_lookup_elem(&seq_park_map, &tid);
    struct tcp_sock *tp;
    __u64 sk;
    __u32 start, end;

    if (!slot)
        return 0;
    if (slot->dir != dir) {
        slot->dir = 0;
        return 0;
    }
    sk = slot->sk;
    start = slot->seq;
    slot->dir = 0;
    if (ret <= 0 || !ior_file_ident_supported())
        return 0;
    tp = bpf_rdonly_cast((void *)sk, bpf_core_type_id_kernel(struct tcp_sock));
    end = iormesh_tcp_seq_field(tp, dir);
    // ret is at most MAX_RW_COUNT (INT_MAX rounded down to a page), so it
    // fits the 32-bit distance.
    if ((__u64)(__u32)(end - start) != (__u64)ret)
        return 0;
    iormesh_emit_tcp_seq(pid, tid, trace_id, now, ret, sk, start);
    return 1;
}
