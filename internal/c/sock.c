//+build ignore

// TCP connections of the traced processes, read from the socket rather than
// the syscalls (iormesh; PLAN.md M1 "TCP lifecycle from the socket, not the
// syscall"). Included by iormesh.bpf.c after filter.c and ahead of the
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
//     the registered owner, and forgets a socket at TCP_CLOSE.
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

// sock_owner is the traced process and descriptor a socket belongs to.
struct sock_owner {
    __u32 pid;
    __s32 fd;
};

// sock_owner_map: struct sock address -> owner. LRU, because a registered
// socket that never reaches TCP_CLOSE while traced (the trace ends first, or
// the close transition is lost) would otherwise hold its entry forever; the
// least recently used entries go first, which are the long-idle ones.
struct {
    __uint(type, BPF_MAP_TYPE_LRU_HASH);
    __uint(max_entries, 65536);
    __type(key, __u64);
    __type(value, struct sock_owner);
} sock_owner_map SEC(".maps");

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
// is a TCP socket, else NULL. A socket inode alone is not proof: an O_PATH
// descriptor of a bound AF_UNIX path has one too, with no struct socket
// behind private_data - hence the NULL and type checks before the sock.
static __always_inline struct sock *iormesh_tcp_sock_of_fd(__s32 fd) {
    struct file *file = iormesh_file_of_fd(fd);
    struct socket *sock;
    struct sock *sk;

    if (!file)
        return 0;
    if ((BPF_CORE_READ(file, f_inode, i_mode) & IORMESH_S_IFMT) != IORMESH_S_IFSOCK)
        return 0;
    sock = BPF_CORE_READ(file, private_data);
    if (!sock || BPF_CORE_READ(sock, type) != IORMESH_SOCK_STREAM)
        return 0;
    sk = BPF_CORE_READ(sock, sk);
    if (!sk || !iormesh_sock_is_tcp(sk))
        return 0;
    return sk;
}

// iormesh_sock_register records pid/fd as the owner of sk. A failed update
// (an LRU map evicts rather than fills, so practically never) loses only
// this socket's state records.
static __always_inline void iormesh_sock_register(struct sock *sk, __u32 pid, __s32 fd) {
    __u64 key = (__u64)sk;
    struct sock_owner owner = {.pid = pid, .fd = fd};

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

// iormesh_sock_on_connect is the connect-enter hook (generated handler,
// internal/generate/sockowner.go). Registering before the call, not after,
// is what makes the CLOSE -> SYN_SENT transition inside connect and the
// later SYN_SENT -> ESTABLISHED in softirq visible at all; a connect that
// fails ends in TCP_CLOSE, which unregisters it again.
static __always_inline void iormesh_sock_on_connect(__u32 pid, __s32 fd) {
    struct sock *sk = iormesh_tcp_sock_of_fd(fd);

    if (sk)
        iormesh_sock_register(sk, pid, fd);
}

// iormesh_sock_on_accept is the accept/accept4-exit hook: ret is the new
// descriptor. It registers the accepted socket and emits its snapshot.
static __always_inline void iormesh_sock_on_accept(__u32 pid, __s64 ret) {
    struct tcp_state_event *ev;
    struct sock *sk;

    if (ret < 0 || ret > 0x7fffffff)
        return;
    sk = iormesh_tcp_sock_of_fd((__s32)ret);
    if (!sk)
        return;
    iormesh_sock_register(sk, pid, (__s32)ret);
    ev = iormesh_reserve_tcp_state((__u64)sk, pid, (__s32)ret);
    if (!ev)
        return;
    ev->oldstate = TCP_STATE_SNAPSHOT;
    ev->newstate = BPF_CORE_READ(sk, __sk_common.skc_state);
    iormesh_read_sk_tuple(ev, sk);
    bpf_ringbuf_submit(ev, 0);
}

// iormesh_read_ctx_tuple fills ev's tuple from the tracepoint record, whose
// ports the kernel already converted to host order.
static __always_inline void iormesh_read_ctx_tuple(struct tcp_state_event *ev,
                                                   struct trace_event_raw_inet_sock_set_state *ctx) {
    ev->family = ctx->family;
    ev->sport = ctx->sport;
    ev->dport = ctx->dport;
    if (ev->family == IORMESH_AF_INET6) {
        __builtin_memcpy(ev->saddr, ctx->saddr_v6, TCP_ADDR_LEN);
        __builtin_memcpy(ev->daddr, ctx->daddr_v6, TCP_ADDR_LEN);
        return;
    }
    __builtin_memcpy(ev->saddr, ctx->saddr, 4);
    __builtin_memcpy(ev->daddr, ctx->daddr, 4);
}

// handle_inet_sock_set_state reports a state change of a registered socket.
// No filter() and no current task here (see the top of this file): scope and
// attribution come from sock_owner_map alone.
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
    ev = iormesh_reserve_tcp_state(sk, owner->pid, owner->fd);
    if (ev) {
        ev->oldstate = ctx->oldstate;
        ev->newstate = newstate;
        iormesh_read_ctx_tuple(ev, ctx);
        bpf_ringbuf_submit(ev, 0);
    }
    // TCP_CLOSE is final for this struct sock; its address may be reused by
    // the next socket, which must not inherit this owner.
    if (newstate == TCP_CLOSE)
        bpf_map_delete_elem(&sock_owner_map, &sk);
    return 0;
}
