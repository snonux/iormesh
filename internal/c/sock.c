//+build ignore

// TCP connections of the traced processes, read from the socket rather than
// the syscalls (iormesh; docs/milestones.md M1 "TCP lifecycle from the socket,
// not the syscall"). Included by iormesh.bpf.c after filter.c and ahead of the
// generated handlers, which call the two hooks below.
//
// Why not the syscalls: a non-blocking connect (every Java NIO connect)
// returns EINPROGRESS, the kernel picks the local port inside it and the
// handshake completes later in softirq, so no syscall argument or return ever
// carries the 4-tuple. sock:inet_sock_set_state does, on every state change.
// But it fires wherever the change happens - in softirq, "current" is
// whichever task was interrupted - so it can neither be filtered nor
// attributed by the current task. Instead:
//   - connect-enter and accept-exit, which do run in the owner's context and
//     pass filter(), register the socket's struct sock address with its
//     owner (pid, fd) in sock_owner_map (iormesh_sock_on_connect/_accept);
//   - the tracepoint handler reports only sockets found there, attributed to
//     the registered owner, and forgets a socket at TCP_CLOSE; a socket that
//     is TCP_CLOSE without a transition (a connect failing early, an accept
//     of a reset connection) is forgotten by the connect exit, or never
//     registered by the accept exit (see sock_owner_map);
//   - because a forgetting run can be lost, every record first checks that
//     the entry still belongs to the socket's current life
//     (iormesh_sock_owner_stale) and drops a stale entry instead.
// The struct sock address is the join key userspace uses to tie the state
// records to the fd's syscalls: an fd number is reused after close, a socket
// is the connection itself.
//
// Accept needs a second piece: the accepted socket went SYN_RECV ->
// ESTABLISHED in softirq before accept() returned, when no owner was known,
// so that transition was dropped. The accept exit therefore reads the tuple
// straight from the socket and emits it as a snapshot record (oldstate
// TCP_STATE_SNAPSHOT; types.h, struct tcp_state_event).
//
// The end of the session needs a third: a final snapshot of every socket
// still registered (oldstate TCP_STATE_FINAL), whose byte counters complete
// the merge's counter proof. Userspace runs iormesh_sock_final once per
// sock_owner_map entry; nothing attaches it (task sf4).
//
// Both kinds of record also carry four struct tcp_sock fields (snd_una,
// rcv_nxt, bytes_acked, bytes_received; M4, decision #26), from which the
// controller's merge rebuilds the TCP initial-sequence-number pair - the
// key that joins the two ends of one connection traced on two hosts.
//
// The fd -> struct sock walk uses BPF_CORE_READ probe reads, not the typed
// walk of fileident.c: connect and accept are rare next to read/write, so the
// helper calls cost nothing measurable, and the probe reads work on kernels
// without the bpf_rdonly_cast kfunc (before 6.2), which TCP tracing must not
// depend on. Every read is fault-safe; a socket closed by another thread
// mid-walk costs at worst that one registration.

// Constants vmlinux.h does not carry (they are macros in the kernel).
#define IORMESH_AF_INET 2
#define IORMESH_AF_INET6 10
#define IORMESH_SOCK_STREAM 1
#define IORMESH_S_IFMT 00170000
#define IORMESH_S_IFSOCK 0140000

// sock_owner is the traced process and descriptor a socket belongs to, and
// the inode number of the socket file the registration found behind that
// descriptor - the generation that tells this life of the struct sock from
// a later one at the same address (iormesh_sock_owner_stale). Socket inode
// numbers come from get_next_ino(), one per socket file, so a new socket
// reusing the struct sock has a new one even when the slab also hands it
// the old struct socket.
struct sock_owner {
    __u32 pid;
    __s32 fd;
    __u64 ino;
};

// sock_owner_map: struct sock address -> owner. An entry must not outlive
// its socket: once the struct sock is freed, the slab can hand its address
// to a new socket - possibly a non-target's - whose transitions would then be
// attributed to the old owner. Two removals keep it exact: the TCP_CLOSE
// transition (handle_inet_sock_set_state), and, for a socket that is
// already TCP_CLOSE and so never transitions again, its registering call's
// own exit (iormesh_sock_on_connect_exit, iormesh_sock_on_accept).
//
// Removal alone is not enough: a lost tracepoint run leaks the entry. The
// kernel skips a tracepoint program whenever another tracing BPF program
// already runs on that CPU (trace_call_bpf's bpf_prog_active guard, counted
// as a recursion miss - our skipped_runs drop), and the final LAST_ACK ->
// CLOSE or FIN_WAIT2 -> CLOSE fires in softirq, which readily interrupts a
// syscall handler of this or another tracer. On a busy host (Rocky 9.9, two
// vCPUs, eight tracers; task if4, M4-9 in lb4) inventory's CLOSE was lost,
// the freed struct sock came back as another process's socket, and its
// whole life was recorded under inventory's pid, fd and conn id. So every
// record also checks the entry against the socket's current life
// (iormesh_sock_owner_stale) and drops it when it belongs to an earlier
// one. What is left is a socket still open when the trace ends, or a stale
// entry whose address is never reused; LRU bounds those: the least
// recently used entries, the long-idle ones, go first.
struct {
    __uint(type, BPF_MAP_TYPE_LRU_HASH);
    __uint(max_entries, 65536);
    __type(key, __u64);
    __type(value, struct sock_owner);
} sock_owner_map SEC(".maps");

// connect_sock_map carries a connect's struct sock from its enter to its
// exit, by tid, so the exit can check the socket's state without walking a
// descriptor another thread may have closed or reused meanwhile (the
// connect itself holds the file, so the socket is alive until the exit).
// LRU, because an exit that never runs (the tracer detaches mid-call)
// strands its entry.
struct {
    __uint(type, BPF_MAP_TYPE_LRU_HASH);
    __uint(max_entries, 8192);
    __type(key, __u32);
    __type(value, __u64);
} connect_sock_map SEC(".maps");

// iormesh_file_of_fd returns the current task's file at descriptor fd, or
// NULL: current->files->fdt->fd[fd], bounds-checked against max_fds before
// the slot is read (past the table is some other object's memory).
static __always_inline struct file *iormesh_file_of_fd(__s32 fd) {
    struct task_struct *task = (struct task_struct *)bpf_get_current_task();
    struct fdtable *fdt = BPF_CORE_READ(task, files, fdt);
    struct file **fds;
    struct file *file = 0;

    if (!fdt || fd < 0 || (__u32)fd >= BPF_CORE_READ(fdt, max_fds))
        return 0;
    fds = BPF_CORE_READ(fdt, fd);
    if (!fds)
        return 0;
    if (bpf_probe_read_kernel(&file, sizeof(file), &fds[fd]))
        return 0;
    return file;
}

// iormesh_sock_is_tcp reports whether sk is an IPv4/IPv6 TCP socket.
// sk_protocol was a bitfield before Linux 5.6; the probed bitfield read is
// relocated for either layout.
static __always_inline int iormesh_sock_is_tcp(struct sock *sk) {
    __u16 family = BPF_CORE_READ(sk, __sk_common.skc_family);

    if (family != IORMESH_AF_INET && family != IORMESH_AF_INET6)
        return 0;
    return BPF_CORE_READ_BITFIELD_PROBED(sk, sk_protocol) == IPPROTO_TCP;
}

// iormesh_tcp_sock_of_fd returns the struct sock behind descriptor fd when it
// is a TCP socket, else NULL, and stores the socket file's inode number in
// *ino (the owner's generation, struct sock_owner). A socket inode alone is
// not proof: an O_PATH descriptor of a bound AF_UNIX path has one too, with
// no struct socket behind private_data - hence the NULL and type checks
// before the sock.
static __always_inline struct sock *iormesh_tcp_sock_of_fd(__s32 fd, __u64 *ino) {
    struct file *file = iormesh_file_of_fd(fd);
    struct socket *sock;
    struct sock *sk;

    if (!file)
        return 0;
    if ((BPF_CORE_READ(file, f_inode, i_mode) & IORMESH_S_IFMT) != IORMESH_S_IFSOCK)
        return 0;
    *ino = BPF_CORE_READ(file, f_inode, i_ino);
    sock = BPF_CORE_READ(file, private_data);
    if (!sock || BPF_CORE_READ(sock, type) != IORMESH_SOCK_STREAM)
        return 0;
    sk = BPF_CORE_READ(sock, sk);
    if (!sk || !iormesh_sock_is_tcp(sk))
        return 0;
    return sk;
}

// iormesh_sock_register records pid/fd, with the socket file's inode
// number ino, as the owner of sk. A failed update (an LRU map evicts rather
// than fills, so practically never) loses only this socket's state records.
static __always_inline void iormesh_sock_register(struct sock *sk, __u32 pid, __s32 fd, __u64 ino) {
    __u64 key = (__u64)sk;
    struct sock_owner owner = {.pid = pid, .fd = fd, .ino = ino};

    bpf_map_update_elem(&sock_owner_map, &key, &owner, BPF_ANY);
}

// iormesh_reserve_tcp_state reserves a zeroed tcp_state_event with its
// header filled, or returns NULL (counted as a drop) when event_map is full.
// Zeroed, because ring-buffer memory holds earlier records' bytes and an
// IPv4 record leaves 12 address bytes unwritten.
static __always_inline struct tcp_state_event *
iormesh_reserve_tcp_state(__u64 sk, __u32 pid, __s32 fd) {
    struct tcp_state_event *ev = bpf_ringbuf_reserve(&event_map, sizeof(*ev), 0);

    if (!ev) {
        ior_count_ringbuf_drop();
        return 0;
    }
    __builtin_memset(ev, 0, sizeof(*ev));
    ev->event_type = TCP_STATE_EVENT;
    ev->schema_version = TCP_STATE_EVENT_SCHEMA_VERSION;
    ev->time = bpf_ktime_get_boot_ns();
    ev->sk = sk;
    ev->pid = pid;
    ev->fd = fd;
    return ev;
}

// iormesh_read_sk_tuple fills ev's tuple from the socket itself (the accept
// snapshot). skc_num is the local port in host order, skc_dport the remote
// one in network order.
static __always_inline void iormesh_read_sk_tuple(struct tcp_state_event *ev, struct sock *sk) {
    ev->family = BPF_CORE_READ(sk, __sk_common.skc_family);
    ev->sport = BPF_CORE_READ(sk, __sk_common.skc_num);
    ev->dport = bpf_ntohs(BPF_CORE_READ(sk, __sk_common.skc_dport));
    if (ev->family == IORMESH_AF_INET6) {
        bpf_core_read(ev->saddr, TCP_ADDR_LEN, &sk->__sk_common.skc_v6_rcv_saddr);
        bpf_core_read(ev->daddr, TCP_ADDR_LEN, &sk->__sk_common.skc_v6_daddr);
        return;
    }
    bpf_core_read(ev->saddr, 4, &sk->__sk_common.skc_rcv_saddr);
    bpf_core_read(ev->daddr, 4, &sk->__sk_common.skc_daddr);
}

// IORMESH_SEQ_TRIES bounds the accept snapshot's reads of the four
// tcp_sock fields (iormesh_read_tcp_seq_stable).
#define IORMESH_SEQ_TRIES 3

// iormesh_tcp_seq is one read of the four tcp_sock fields the merge
// rebuilds the ISN pair from (M4, decision #26).
struct iormesh_tcp_seq {
    __u32 snd_una;
    __u32 rcv_nxt;
    __u64 bytes_acked;
    __u64 bytes_received;
};

// iormesh_read_tcp_seq_once reads the four fields of sk. sk is a TCP
// socket at every call site (the tracepoint checks the protocol, the
// accept exit iormesh_sock_is_tcp), so the cast to struct tcp_sock is
// sound. Probe reads, like the rest of this file: the tracepoint's skaddr
// is no trusted BTF pointer. Four reads per tcp_state record, a few
// records per connection - negligible next to the syscall handlers.
// bytes_acked and bytes_received exist since Linux 4.1/4.2, older than any
// kernel the tracer's other CO-RE reads accept.
static __always_inline void iormesh_read_tcp_seq_once(struct iormesh_tcp_seq *seq, struct sock *sk) {
    struct tcp_sock *tp = (struct tcp_sock *)sk;

    seq->snd_una = BPF_CORE_READ(tp, snd_una);
    seq->rcv_nxt = BPF_CORE_READ(tp, rcv_nxt);
    seq->bytes_acked = BPF_CORE_READ(tp, bytes_acked);
    seq->bytes_received = BPF_CORE_READ(tp, bytes_received);
}

// iormesh_put_tcp_seq copies one read into the record.
static __always_inline void iormesh_put_tcp_seq(struct tcp_state_event *ev,
                                                const struct iormesh_tcp_seq *seq) {
    ev->snd_una = seq->snd_una;
    ev->rcv_nxt = seq->rcv_nxt;
    ev->bytes_acked = seq->bytes_acked;
    ev->bytes_received = seq->bytes_received;
}

// iormesh_read_tcp_seq fills ev's four fields with one read: for the
// state-change records, whose tcp_set_state runs with the socket locked,
// so no update of the four can interleave with the read; and for the final
// snapshot, which reads unlocked but needs no consistent four
// (iormesh_sock_final).
static __always_inline void iormesh_read_tcp_seq(struct tcp_state_event *ev, struct sock *sk) {
    struct iormesh_tcp_seq seq;

    iormesh_read_tcp_seq_once(&seq, sk);
    iormesh_put_tcp_seq(ev, &seq);
}

// iormesh_read_tcp_seq_stable fills ev's four fields for the accept
// snapshot, which reads a socket nobody locks: inet_accept released the
// child, and softirq on another CPU may be processing the client's first
// segment - tcp_rcv_nxt_update adds to bytes_received, then sets rcv_nxt
// (tcp_snd_una_update likewise for bytes_acked and snd_una), so one read
// can pair an old value with a new counter and rebuild a key off by the
// segment's length. It re-reads, up to IORMESH_SEQ_TRIES times, until two
// consecutive reads agree; if none do it marks the record TCP_SEQ_TORN
// and leaves the four zero (types.h). Two agreeing reads can still both
// fall inside one slow update - the residual race the merge covers by
// preferring state-change records (internal/federate/half.go).
static __always_inline void iormesh_read_tcp_seq_stable(struct tcp_state_event *ev, struct sock *sk) {
    struct iormesh_tcp_seq prev, cur;

    iormesh_read_tcp_seq_once(&prev, sk);
#pragma unroll
    for (int i = 1; i < IORMESH_SEQ_TRIES; i++) {
        iormesh_read_tcp_seq_once(&cur, sk);
        if (cur.snd_una == prev.snd_una && cur.rcv_nxt == prev.rcv_nxt &&
            cur.bytes_acked == prev.bytes_acked && cur.bytes_received == prev.bytes_received) {
            iormesh_put_tcp_seq(ev, &cur);
            return;
        }
        prev = cur;
    }
    ev->seq_status = TCP_SEQ_TORN;
}

// iormesh_sock_state returns sk's TCP state (TCP_ESTABLISHED ... ).
static __always_inline int iormesh_sock_state(struct sock *sk) {
    return BPF_CORE_READ(sk, __sk_common.skc_state);
}

// iormesh_sock_on_connect is the connect-enter hook (generated handler,
// internal/generate/sockowner.go). Registering before the call, not after,
// is what makes the CLOSE -> SYN_SENT transition inside connect and the
// later SYN_SENT -> ESTABLISHED in softirq visible at all. The socket is
// also parked for the exit hook, by tid.
static __always_inline void iormesh_sock_on_connect(__u32 pid, __u32 tid, __s32 fd) {
    __u64 ino = 0;
    struct sock *sk = iormesh_tcp_sock_of_fd(fd, &ino);
    __u64 key = (__u64)sk;

    if (!sk)
        return;
    iormesh_sock_register(sk, pid, fd, ino);
    bpf_map_update_elem(&connect_sock_map, &tid, &key, BPF_ANY);
}

// iormesh_sock_on_connect_exit is the connect-exit hook. A connect that
// fails before tcp_v4_connect/tcp_v6_connect set SYN_SENT (ENETUNREACH,
// EINVAL, EAFNOSUPPORT, a cgroup BPF EACCES, ...) leaves the socket in
// TCP_CLOSE with no transition at all, and its later close() is silent too
// (__tcp_close goes straight to the orphan path when the state is already
// TCP_CLOSE). Nothing would ever unregister it, so the exit does: a socket
// still in TCP_CLOSE here is dropped. Every other outcome keeps the entry -
// EINPROGRESS and EALREADY leave SYN_SENT, success ESTABLISHED, and a
// refused connect already unregistered itself through its SYN_SENT ->
// CLOSE transition (the delete here is then a harmless miss).
static __always_inline void iormesh_sock_on_connect_exit(__u32 tid) {
    __u64 *parked = bpf_map_lookup_elem(&connect_sock_map, &tid);
    __u64 key;

    if (!parked)
        return;
    key = *parked;
    bpf_map_delete_elem(&connect_sock_map, &tid);
    if (iormesh_sock_state((struct sock *)key) == TCP_CLOSE)
        bpf_map_delete_elem(&sock_owner_map, &key);
}

// iormesh_sock_on_accept is the accept/accept4-exit hook: ret is the new
// descriptor. It emits the accepted socket's snapshot and registers the
// socket - unless it is already TCP_CLOSE (the peer reset it while it sat
// in the accept queue): such a socket never transitions again, so its entry
// could only outlive it.
static __always_inline void iormesh_sock_on_accept(__u32 pid, __s64 ret) {
    struct tcp_state_event *ev;
    struct sock *sk;
    __u64 ino = 0;
    int state;

    if (ret < 0 || ret > 0x7fffffff)
        return;
    sk = iormesh_tcp_sock_of_fd((__s32)ret, &ino);
    if (!sk)
        return;
    state = iormesh_sock_state(sk);
    // Reserved - and so timestamped - before the socket is registered:
    // every state-change record of it is then later than its snapshot,
    // which is how the merge tells the snapshot apart (half.go).
    ev = iormesh_reserve_tcp_state((__u64)sk, pid, (__s32)ret);
    if (state != TCP_CLOSE)
        iormesh_sock_register(sk, pid, (__s32)ret, ino);
    if (!ev)
        return;
    ev->oldstate = TCP_STATE_SNAPSHOT;
    ev->newstate = state;
    iormesh_read_sk_tuple(ev, sk);
    iormesh_read_tcp_seq_stable(ev, sk);
    bpf_ringbuf_submit(ev, 0);
}

// iormesh_read_ctx_tuple fills ev's tuple from the tracepoint record, whose
// ports the kernel already converted to host order.
//
// The address arrays are copied with bpf_probe_read_kernel, not
// __builtin_memcpy: clang lowers the 16-byte copy to loads off a ctx
// pointer it advanced first (r3 = ctx + 48; *(u64 *)(r3 + 8)), and the
// el9 verifier (Rocky 9.9, 5.14.0-741.22.1.el9_9) refuses that with
// "dereference of modified ctx ptr R3 off=48 disallowed" - the whole
// object then fails to load (task lb4). Linux 7.2 accepts either; the
// helper call costs nothing measurable on a state change.
static __always_inline void iormesh_read_ctx_tuple(struct tcp_state_event *ev,
                                                   struct trace_event_raw_inet_sock_set_state *ctx) {
    ev->family = ctx->family;
    ev->sport = ctx->sport;
    ev->dport = ctx->dport;
    if (ev->family == IORMESH_AF_INET6) {
        bpf_probe_read_kernel(ev->saddr, TCP_ADDR_LEN, ctx->saddr_v6);
        bpf_probe_read_kernel(ev->daddr, TCP_ADDR_LEN, ctx->daddr_v6);
        return;
    }
    bpf_probe_read_kernel(ev->saddr, 4, ctx->saddr);
    bpf_probe_read_kernel(ev->daddr, 4, ctx->daddr);
}

// iormesh_sock_owner_stale reports whether owner, sk's sock_owner_map
// entry, belongs to an earlier life of the struct sock at that address: one
// whose forgetting TCP_CLOSE run the kernel skipped (sock_owner_map), after
// which the slab handed the address to a new socket. Two proofs, each
// enough on its own:
//
//   - A transition out of TCP_LISTEN or TCP_CLOSE starts a new life. A
//     socket leaves TCP_CLOSE by connect (CLOSE -> SYN_SENT) or listen
//     (CLOSE -> LISTEN), and an accepted child is born LISTEN -> SYN_RECV
//     (inet_csk_clone_lock copies the listener's state). The only such
//     transition a live entry can see is the CLOSE -> SYN_SENT inside the
//     very connect that registered it, and that connect parked sk under
//     its tid in connect_sock_map - the transition runs in the connecting
//     task's context, so the tid is looked up as filter() stored it: in
//     the tracer's PID namespace (iormesh_current_pid_tgid; pod mode, M7),
//     not the host tid. Children are registered only after accept()
//     returns. A listener is registered only by a connect() on it, which
//     fails with EISCONN and leaves it LISTEN; its one later transition,
//     LISTEN -> CLOSE, ends it, so dropping that record and the entry
//     loses nothing. Every other such transition means the entry is
//     stale.
//   - The socket file's inode number is the generation (struct sock_owner).
//     While the socket has a file (sk_socket is set from socket() or
//     accept() until close() orphans it) its inode must be the registered
//     one; a kernel socket without a file never is. An orphaned socket
//     (sk_socket NULL: the FIN_WAIT2/LAST_ACK -> CLOSE tail after close(),
//     or a child not yet accepted) cannot be checked this way and is
//     believed - the first proof covers the next life's start.
//
// Accepted residual risk: two skipped runs in a row on one address - the
// old life's CLOSE, then the new child's LISTEN -> SYN_RECV - leave the
// stale entry in place for the child's SYN_RECV -> ESTABLISHED, which runs
// before accept() gives the child a file, so neither proof catches it and
// that one record goes out under the old owner (accept() then re-registers
// the child, or its next transition with a file drops the entry). The
// correlator's userspace backstop (internal/correlate, sockTable.observe)
// starts a new socket when a record's tuple contradicts the socket's —
// both complete: addresses specified and ports set — so that record and
// the accepted child's later ones get a conn of their own, the stale
// record naming no process; it also catches the lone lost CLOSE whose
// next child at the address the tracer registers by its accept, and, like
// the first proof above, treats a transition out of CLOSE or LISTEN as a
// new life (task qf4).
//
// The reads are probe reads: sk_socket may be cleared by close() on another
// CPU meanwhile, which at worst makes one record unverifiable.
static __always_inline int iormesh_sock_owner_stale(struct trace_event_raw_inet_sock_set_state *ctx,
                                                    const struct sock_owner *owner, __u64 sk) {
    int oldstate = ctx->oldstate;
    struct socket *sock;

    if (oldstate == TCP_LISTEN)
        return 1;
    if (oldstate == TCP_CLOSE) {
        __u32 tid = (__u32)iormesh_current_pid_tgid();
        __u64 *parked = bpf_map_lookup_elem(&connect_sock_map, &tid);

        if (!parked || *parked != sk)
            return 1;
    }
    sock = BPF_CORE_READ((struct sock *)sk, sk_socket);
    if (!sock)
        return 0;
    return BPF_CORE_READ(sock, file, f_inode, i_ino) != owner->ino;
}

// handle_inet_sock_set_state reports a state change of a registered socket.
// No filter() and no current task here (see the top of this file): scope and
// attribution come from sock_owner_map alone, after the entry has been
// checked to belong to this life of the socket (iormesh_sock_owner_stale) -
// a stale one is dropped, and with it the record, which is another
// process's socket or one the tracer never registered.
SEC("tracepoint/sock/inet_sock_set_state")
int handle_inet_sock_set_state(struct trace_event_raw_inet_sock_set_state *ctx) {
    __u64 sk = (__u64)ctx->skaddr;
    struct sock_owner *owner;
    struct tcp_state_event *ev;
    int newstate = ctx->newstate;

    if (ctx->protocol != IPPROTO_TCP)
        return 0;
    owner = bpf_map_lookup_elem(&sock_owner_map, &sk);
    if (!owner)
        return 0;
    if (iormesh_sock_owner_stale(ctx, owner, sk)) {
        bpf_map_delete_elem(&sock_owner_map, &sk);
        return 0;
    }
    ev = iormesh_reserve_tcp_state(sk, owner->pid, owner->fd);
    if (ev) {
        ev->oldstate = ctx->oldstate;
        ev->newstate = newstate;
        iormesh_read_ctx_tuple(ev, ctx);
        // The tracepoint fires from tcp_set_state before the new state is
        // stored, but after the segment that caused it was processed: at
        // SYN_SENT -> ESTABLISHED the SYN-ACK already set rcv_nxt and
        // advanced snd_una/bytes_acked (S2 read them at this very point).
        // The socket is locked there, so one read is consistent.
        iormesh_read_tcp_seq(ev, (struct sock *)ctx->skaddr);
        bpf_ringbuf_submit(ev, 0);
    }
    // TCP_CLOSE is final for this struct sock; its address may be reused by
    // the next socket, which must not inherit this owner.
    if (newstate == TCP_CLOSE)
        bpf_map_delete_elem(&sock_owner_map, &sk);
    return 0;
}

// sock_final_map is iormesh_sock_final's output slot: one record, read by
// userspace after each run (internal/bpf/finalsock.go). A map, not the
// ring buffer: the session is ending, nobody drains the ring any more,
// and a run whose record does not fit there would be lost.
struct {
    __uint(type, BPF_MAP_TYPE_ARRAY);
    __uint(max_entries, 1);
    __type(key, __u32);
    __type(value, struct tcp_state_event);
} sock_final_map SEC(".maps");

// iormesh_sock_final writes the final snapshot of one registered socket
// into sock_final_map and returns 1, or returns 0 when there is none to
// take (task sf4). It is never attached: at the end of a session userspace
// walks sock_owner_map and runs it once per entry through
// BPF_PROG_TEST_RUN, with the struct sock address as the raw tracepoint's
// first argument (internal/bpf/finalsock.go). Linux 5.10 added test runs
// of raw tracepoint programs; el9's 5.14 has them. Walking the owners
// instead of the kernel's socket tables (a bpf_iter over tcp, or
// INET_DIAG) is what reaches sockets in every network namespace - a
// pod's included - and only the traced ones.
//
// Why at all: the merge pairs a send and a receive by sequence offset mod
// 2^32, and its counter proof needs a counter record of each end taken
// after the message (internal/federate/clockproof.go). A socket closed
// during the session has its CLOSE record, taken after every call on it;
// one still open at the end had none after its last state change, and
// its messages fell back on the stream check, which trusts the trace to
// hold every byte. This record is that last counter reading.
//
// The entry may be stale - its TCP_CLOSE run lost and the address reused
// (sock_owner_map) - so the socket's file must still carry the registered
// inode, as iormesh_sock_owner_stale checks; an orphaned socket (closed,
// still finishing its FIN exchange) has no file and is believed, like a
// state change of it would be. A socket in TCP_CLOSE is skipped: it
// reported its last state already, or its run was lost and the address
// now holds a socket nobody owns. The socket is not locked here, but the
// four fields are read once (iormesh_read_tcp_seq), not re-read like the
// accept snapshot's: each counter is one aligned load of a value that only
// grows, so it proves what it proves torn or not, and a torn read would
// only drop the record's say - the merge uses a final snapshot's counters
// only when its fields rebuild the connection's key (internal/federate,
// counterRec), which a torn read fails. The timestamp is taken before
// the read: the counters hold at least what they held then.
SEC("raw_tp")
int iormesh_sock_final(struct bpf_raw_tracepoint_args *ctx) {
    __u64 key = ctx->args[0];
    struct sock *sk = (struct sock *)key;
    struct sock_owner *owner;
    struct tcp_state_event *ev;
    struct socket *sock;
    __u32 zero = 0;
    int state;

    ev = bpf_map_lookup_elem(&sock_final_map, &zero);
    owner = bpf_map_lookup_elem(&sock_owner_map, &key);
    if (!ev || !owner || !iormesh_sock_is_tcp(sk))
        return 0;
    state = iormesh_sock_state(sk);
    if (state == TCP_CLOSE)
        return 0;
    sock = BPF_CORE_READ(sk, sk_socket);
    if (sock && BPF_CORE_READ(sock, file, f_inode, i_ino) != owner->ino)
        return 0;
    __builtin_memset(ev, 0, sizeof(*ev));
    ev->event_type = TCP_STATE_EVENT;
    ev->schema_version = TCP_STATE_EVENT_SCHEMA_VERSION;
    ev->time = bpf_ktime_get_boot_ns();
    ev->sk = key;
    ev->pid = owner->pid;
    ev->fd = owner->fd;
    ev->oldstate = TCP_STATE_FINAL;
    ev->newstate = state;
    iormesh_read_sk_tuple(ev, sk);
    iormesh_read_tcp_seq(ev, sk);
    return 1;
}
