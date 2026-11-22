//+build ignore

// TCP sequence numbers of single sends and receives (iormesh, PLAN.md M4
// "Clock reconciliation from the traffic itself", task xe4). Included by
// iormesh.bpf.c after fileident.c and sock.c, whose walk and constants it
// uses, and ahead of the generated handlers, which call the two hooks below
// (internal/generate/tcpseq.go).
//
// The controller's merge pairs every receive with the send that queued its
// last byte, by stream offset (internal/federate, clock.go), so each unfolded
// write, writev, sendto, sendfile64, sendmsg, sendmmsg, read, readv,
// recvfrom, recvmsg and recvmmsg on a TCP socket needs the sequence number
// of its first byte: args.tcp_seq. (The mmsg calls check their messages'
// byte sum, not their return, which counts messages: mmsg.c, task oh4.)
// Which field holds it:
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
// [start, end). One exception, a receive that also consumes the peer's
// FIN, moves copied_seq by ret + 1 and is accepted when the FIN is the
// last number received and the socket is still open (CLOSE_WAIT,
// LAST_ACK, CLOSING; iormesh_seq_moved_exactly). Such a read on a socket
// that had shut its own side first (FIN_WAIT2, so the FIN closes it) gets
// no tcp_seq. Anything else -
// another sender or reader interleaving, a call split around one, a
// MSG_PEEK or MSG_OOB read (copied_seq does not move, or skips the urgent
// byte), a kTLS socket (write_seq counts record framing), a TCP Fast Open
// send (the ISN is assigned inside the call) - fails the check and the
// call gets no tcp_seq: never a guess. The one residue is an assignment
// of the field inside the window (connect, disconnect, TCP_REPAIR) whose
// jump equals ret by chance, 1 in 2^32 (and a dup2 race, see
// iormesh_seq_on_exit).
// Not covered: a read or write the fold held at its enter (a hot streak,
// rwagg.c) parks nothing; if it then turns out not hot - it blocked over
// IORMESH_RW_HOT_NS, the read that waits on the peer after a burst - it
// is sent as a single event without tcp_seq. Parking held enters too would
// put the walk back on every folded call.
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

// iormesh_seq_on_enter is the enter hook of the data calls: it parks
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

// iormesh_tcp_fin_received tells whether state (skc_state) says the peer's
// FIN has arrived: the states tcp_fin moves a socket to that keep it alive
// - CLOSE_WAIT from ESTABLISHED, CLOSING from FIN_WAIT1 - and LAST_ACK,
// which follows CLOSE_WAIT once the reader shuts its own side down. Not
// CLOSE: a FIN in FIN_WAIT2 ends there (the time-wait handoff), but so
// does a reset that never carried a FIN, which would let a skipped urgent
// byte pass as the FIN; userspace drops tcp_seq after a TCP_CLOSE record
// anyway. The state rather than sk_shutdown's RCV_SHUTDOWN, which a local
// shutdown(SHUT_RD) sets too, without any FIN.
static __always_inline int iormesh_tcp_fin_received(__u8 state) {
    return state == TCP_CLOSE_WAIT || state == TCP_LAST_ACK || state == TCP_CLOSING;
}

// iormesh_seq_moved_exactly is the check of the top of this file: the
// field moved from start to end by exactly the call's ret bytes. ret is at
// most MAX_RW_COUNT (INT_MAX rounded down to a page), so it fits the
// 32-bit distance. One case moves a receive's field one further: the read
// that drains the last data and then finds the peer's FIN behind it
// consumes the FIN too (tcp_recvmsg's found_fin_ok), and the FIN takes a
// sequence number of its own - the final read of a "write, then close"
// exchange (task wi4). end - start == ret + 1 is accepted for it only when
// the FIN has arrived and end is rcv_nxt, the end of everything received:
// the extra number is then the FIN, the last one there is, so the ret data
// bytes are exactly [start, start + ret) - whether this call or another
// reader consumed the FIN. Without those two, the extra one could be
// another reader's byte or a skipped urgent byte, and the call gets no
// tcp_seq as before.
// Order: the state is read before rcv_nxt. tcp_data_queue stores the
// FIN's rcv_nxt before tcp_fin sets the state, so a FIN state seen first
// implies the rcv_nxt read after it already counts the FIN; the other
// order could pair a FIN state with an rcv_nxt from before the FIN. The
// empty asm keeps the compiler from hoisting the rcv_nxt load above the
// state load (skc_state is volatile, rcv_nxt is not). There is no read
// barrier for the CPU: x86 keeps loads in order, a weakly ordered CPU
// (arm64) strictly may not - a residue there. The state is read
// through struct sock, which a tcp_sock starts with (the verifier walks
// the typed pointer to the same field), so the CO-RE relocation names
// sock.__sk_common.skc_state like sock.c's reads and the trimmed el9 BTF
// (internal/testdata/btf) needs no new types.
static __always_inline int iormesh_seq_moved_exactly(struct tcp_sock *tp, __u32 dir, __u32 start,
                                                     __u32 end, __s64 ret) {
    __u64 moved = (__u32)(end - start);
    __u8 state;

    if (moved == (__u64)ret)
        return 1;
    if (dir != IORMESH_SEQ_RECV || moved != (__u64)ret + 1)
        return 0;
    state = ((struct sock *)tp)->__sk_common.skc_state;
    asm volatile("" ::: "memory");
    return iormesh_tcp_fin_received(state) && end == tp->rcv_nxt;
}

// iormesh_seq_take is the exit half of the check, for every data call:
// it empties the thread's park and returns 1, with the parked socket in
// *sk and the call's first sequence number in *seq, when the call moved
// n > 0 bytes and the field moved by exactly that many since the park
// (iormesh_seq_moved_exactly); otherwise 0. n is the call's return for
// the single-buffer calls and sendmsg/recvmsg, the messages' byte sum
// for sendmmsg/recvmmsg (mmsg.c), and 0 - which only empties the park -
// when that sum is incomplete. A park of the other direction is another
// call's (a missed exit) and is only emptied. The socket is re-read
// through the parked address: the call's fd reference, or a
// single-threaded file table, kept the socket alive until the syscall
// returned; if another thread closed it right after, the read is
// fault-safe, and a freed or reused socket fails the check (or
// userspace's socket check). Residue besides the 1 in 2^32 above: another
// thread dup2s a second socket onto the fd between this enter's walk and
// the kernel's fd lookup while a third party moves exactly n bytes on
// the parked one - userspace catches it only if it traced the dup2.
static __always_inline int iormesh_seq_take(__u32 tid, __s64 n, __u32 dir, __u64 *sk, __u32 *seq) {
    struct seq_park *slot = bpf_map_lookup_elem(&seq_park_map, &tid);
    struct tcp_sock *tp;
    __u64 parked;
    __u32 start, end;

    if (!slot)
        return 0;
    if (slot->dir != dir) {
        slot->dir = 0;
        return 0;
    }
    parked = slot->sk;
    start = slot->seq;
    slot->dir = 0;
    if (n <= 0 || !ior_file_ident_supported())
        return 0;
    tp = bpf_rdonly_cast((void *)parked, bpf_core_type_id_kernel(struct tcp_sock));
    end = iormesh_tcp_seq_field(tp, dir);
    if (!iormesh_seq_moved_exactly(tp, dir, start, end, n))
        return 0;
    *sk = parked;
    *seq = start;
    return 1;
}

// iormesh_seq_on_exit is the exit hook of the single-buffer data calls
// and sendmsg/recvmsg, behind the hooks that decide the exit record is
// sent: when iormesh_seq_take proves the call's ret bytes are its own, it
// sends the exit record itself, with the call's first sequence number,
// and returns 1 - the handler then sends no ret_event. Otherwise it
// returns 0 and the handler sends its plain ret_event. sendmmsg and
// recvmmsg take the park through mmsg.c's exit hook instead: their ret
// counts messages, not bytes.
static __always_inline int iormesh_seq_on_exit(__u32 pid, __u32 tid, __u32 trace_id, __u64 now,
                                               __s64 ret, __u32 dir) {
    __u64 sk = 0;
    __u32 seq = 0;

    if (!iormesh_seq_take(tid, ret, dir, &sk, &seq))
        return 0;
    iormesh_emit_tcp_seq(pid, tid, trace_id, now, ret, sk, seq);
    return 1;
}
