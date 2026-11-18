//+build ignore

// Socket addresses of the traced processes' bind, connect, sendto, recvfrom
// and AF_UNIX accepts (iormesh, M2 step 7). Included by iormesh.bpf.c after
// sock.c (whose iormesh_file_of_fd it uses) and ahead of the generated
// handlers, which call the hooks below (internal/generate/sockowner.go).
//
// Why: the generated records of these calls carry the fd and the return
// value only. UDP is connectionless, so its peer is in each datagram's
// address argument, never in the socket; a unix socket's only name is the
// sun_path of its bind and connect; and the two ends of a unix connection
// can only be joined through the kernel's own pairing (unix_sk->peer). TCP
// needs none of this: sock.c reads its 4-tuple from the socket's state
// transitions, so a TCP connect sends no record here.
//
// How: the enter hook parks the address argument's user pointer and its
// length by tid (recvfrom fills both in only at exit, so they cannot be read
// at enter);
// the exit hook reads it together with the socket and sends one
// sock_addr_event right before the exit record, which userspace attaches to
// the thread's pending enter. Every read is fault-safe: an unreadable
// argument leaves arg_family 0 and a socket closed by another thread
// mid-walk leaves family 0, never a wrong value.

#define IORMESH_AF_UNIX 1

// Exit hook flags. They are arguments rather than trace-id compares because
// the SYS_EXIT_* ids are defined by generated_tracepoints.c, after this file.
// IORMESH_ADDR_FAILED_TOO: report a failed call too (bind, connect: the name
// it tried is the useful part; a failed sendto or recvfrom moved nothing).
// IORMESH_ADDR_NOT_TCP: send nothing for a TCP socket (connect: sock.c's
// state records carry its 4-tuple).
// IORMESH_ADDR_NOT_STREAM: send nothing for any stream socket (sendto,
// recvfrom): glibc's send(3) and recv(3) are these syscalls, so every TCP
// or connected unix send and receive would otherwise pay a record whose
// addresses the connection already has (decision #25).
#define IORMESH_ADDR_FAILED_TOO 1
#define IORMESH_ADDR_NOT_TCP 2
#define IORMESH_ADDR_NOT_STREAM 4

// addr_arg is a parked address argument: the user pointer, its length -
// the socklen_t value (bind, connect, sendto) or, with len_is_ptr, a
// pointer to the one the kernel writes back (recvfrom) - and the fd.
struct addr_arg {
    __u64 uaddr;
    __u64 len;
    __s32 fd;
    __u32 len_is_ptr;
};

// addr_arg_map carries an address argument from a call's enter to its exit,
// by tid (a thread runs one syscall at a time). LRU, because an exit that
// never runs (the tracer detaches mid-call) strands its entry.
struct {
    __uint(type, BPF_MAP_TYPE_LRU_HASH);
    __uint(max_entries, 8192);
    __type(key, __u32);
    __type(value, struct addr_arg);
} addr_arg_map SEC(".maps");

// iormesh_sock_of_any_fd returns the struct sock behind descriptor fd for
// any socket family, else NULL; the checks are iormesh_tcp_sock_of_fd's.
static __always_inline struct sock *iormesh_sock_of_any_fd(__s32 fd) {
    struct file *file = iormesh_file_of_fd(fd);
    struct socket *sock;

    if (!file)
        return 0;
    if ((BPF_CORE_READ(file, f_inode, i_mode) & IORMESH_S_IFMT) != IORMESH_S_IFSOCK)
        return 0;
    sock = BPF_CORE_READ(file, private_data);
    if (!sock)
        return 0;
    return BPF_CORE_READ(sock, sk);
}

// iormesh_addr_on_enter is the enter hook of bind, connect (args[1],
// length args[2]), sendto (args[4], args[5]) and recvfrom (args[4], length
// pointer args[5]): it parks the address argument. A NULL pointer is parked
// too, so the exit still reports the socket itself.
static __always_inline void iormesh_addr_on_enter(__u32 tid, __s32 fd, __u64 uaddr, __u64 len,
                                                  __u32 len_is_ptr) {
    struct addr_arg arg = {.uaddr = uaddr, .len = len, .fd = fd, .len_is_ptr = len_is_ptr};

    bpf_map_update_elem(&addr_arg_map, &tid, &arg, BPF_ANY);
}

// iormesh_read_inet_arg fills the arg_* fields from a sockaddr_in or
// sockaddr_in6 at uaddr, if len holds one. Only the family's own size is
// read: the caller's buffer may be exactly a sockaddr_in. A buffer that ends
// at an unmapped page within that size fails the read, and the argument is
// then silently absent (arg_family 0) rather than guessed.
static __always_inline void iormesh_read_inet_arg(struct sock_addr_event *ev, __u64 uaddr,
                                                  __u16 family, __u32 len) {
    if (family == IORMESH_AF_INET) {
        struct sockaddr_in sin;

        if (len < sizeof(sin) || bpf_probe_read_user(&sin, sizeof(sin), (void *)uaddr))
            return;
        ev->arg_port = bpf_ntohs(sin.sin_port);
        __builtin_memcpy(ev->arg_addr, &sin.sin_addr, 4);
    } else {
        struct sockaddr_in6 sin6;

        if (len < sizeof(sin6) || bpf_probe_read_user(&sin6, sizeof(sin6), (void *)uaddr))
            return;
        ev->arg_port = bpf_ntohs(sin6.sin6_port);
        __builtin_memcpy(ev->arg_addr, &sin6.sin6_addr, TCP_ADDR_LEN);
    }
    ev->arg_family = family;
}

// iormesh_cut_path terminates path after n bytes when n is shorter than the
// buffer: a sun_path need not be NUL-terminated within its address length,
// and the bytes after it are the caller's, not the name's.
static __always_inline void iormesh_cut_path(struct sock_addr_event *ev, __u32 n) {
    n &= 0x7f;
    if (n < SOCK_ADDR_PATH_LENGTH)
        ev->path[n] = 0;
}

// iormesh_read_unix_arg fills path from a sockaddr_un of len bytes at uaddr:
// a filesystem name, or an abstract one (leading NUL) read from the byte
// after it, each cut at the address length (abstract names are not
// NUL-terminated at all). A failed read, or an address with no name (an
// unnamed peer), leaves path "" and path_kind NONE.
static __always_inline void iormesh_read_unix_arg(struct sock_addr_event *ev, __u64 uaddr,
                                                  __u32 len) {
    __u64 sun_path = uaddr + sizeof(__u16);
    __u32 n = len - sizeof(__u16);
    char first = 0;

    ev->path[0] = 0;
    ev->arg_family = IORMESH_AF_UNIX;
    if (len <= sizeof(__u16) || bpf_probe_read_user(&first, 1, (void *)sun_path))
        return;
    if (first) {
        if (bpf_probe_read_user_str(ev->path, sizeof(ev->path), (void *)sun_path) > 0) {
            ev->path_kind = SOCK_ADDR_PATH_NAMED;
            iormesh_cut_path(ev, n);
        }
    } else if (n > 1 &&
               bpf_probe_read_user_str(ev->path, sizeof(ev->path) - 1, (void *)(sun_path + 1)) > 0) {
        ev->path_kind = SOCK_ADDR_PATH_ABSTRACT;
        iormesh_cut_path(ev, n - 1);
    }
    if (ev->path_kind == SOCK_ADDR_PATH_NONE)
        ev->path[0] = 0;
}

// iormesh_addr_len resolves a parked length: the value, or the one the
// kernel wrote back through the pointer (0 when unreadable).
static __always_inline __u32 iormesh_addr_len(struct addr_arg *arg) {
    __u32 len = 0;

    if (!arg->len_is_ptr)
        return (__u32)arg->len;
    if (!arg->len || bpf_probe_read_user(&len, sizeof(len), (void *)arg->len))
        return 0;
    return len;
}

// iormesh_read_addr_arg reads the address argument by its own family, only
// as far as its length says: a recvfrom whose kernel wrote no address (len
// 0) has none, whatever its buffer holds.
static __always_inline void iormesh_read_addr_arg(struct sock_addr_event *ev, __u64 uaddr,
                                                  __u32 len) {
    __u16 family = 0;

    ev->path[0] = 0;
    if (!uaddr || len < sizeof(family) ||
        bpf_probe_read_user(&family, sizeof(family), (void *)uaddr))
        return;
    if (family == IORMESH_AF_INET || family == IORMESH_AF_INET6)
        iormesh_read_inet_arg(ev, uaddr, family, len);
    else if (family == IORMESH_AF_UNIX)
        iormesh_read_unix_arg(ev, uaddr, len);
}

// iormesh_read_sock_addrs fills family, sock_type, the inet socket's local
// and remote address and, for AF_UNIX, the peer socket. skc_num is the
// local port in host order, skc_dport the remote one in network order.
static __always_inline void iormesh_read_sock_addrs(struct sock_addr_event *ev, struct sock *sk) {
    ev->sk = (__u64)sk;
    ev->family = BPF_CORE_READ(sk, __sk_common.skc_family);
    ev->sock_type = BPF_CORE_READ(sk, sk_type);
    if (ev->family == IORMESH_AF_UNIX) {
        ev->peer = (__u64)BPF_CORE_READ((struct unix_sock *)sk, peer);
        return;
    }
    ev->local_port = BPF_CORE_READ(sk, __sk_common.skc_num);
    ev->remote_port = bpf_ntohs(BPF_CORE_READ(sk, __sk_common.skc_dport));
    if (ev->family == IORMESH_AF_INET6) {
        bpf_core_read(ev->local_addr, TCP_ADDR_LEN, &sk->__sk_common.skc_v6_rcv_saddr);
        bpf_core_read(ev->remote_addr, TCP_ADDR_LEN, &sk->__sk_common.skc_v6_daddr);
    } else if (ev->family == IORMESH_AF_INET) {
        bpf_core_read(ev->local_addr, 4, &sk->__sk_common.skc_rcv_saddr);
        bpf_core_read(ev->remote_addr, 4, &sk->__sk_common.skc_daddr);
    }
}

// iormesh_emit_sock_addr sends one zeroed sock_addr_event for fd (and the
// address argument at uaddr, 0 for none). Zeroed, because ring-buffer
// memory holds earlier records' bytes and an IPv4 record leaves address
// bytes unwritten. With IORMESH_ADDR_NOT_TCP a TCP socket sends nothing,
// with IORMESH_ADDR_NOT_STREAM no stream socket does.
static __always_inline void iormesh_emit_sock_addr(__u32 pid, __u32 tid, __u32 trace_id,
                                                   __s32 fd, __u64 uaddr, __u32 len,
                                                   __u32 flags) {
    struct sock *sk = iormesh_sock_of_any_fd(fd);
    struct sock_addr_event *ev;

    if ((flags & IORMESH_ADDR_NOT_TCP) && sk && iormesh_sock_is_tcp(sk))
        return;
    if ((flags & IORMESH_ADDR_NOT_STREAM) && sk &&
        BPF_CORE_READ(sk, sk_type) == IORMESH_SOCK_STREAM)
        return;
    ev = bpf_ringbuf_reserve(&event_map, sizeof(*ev), 0);
    if (!ev) {
        ior_count_ringbuf_drop();
        return;
    }
    __builtin_memset(ev, 0, sizeof(*ev));
    ev->event_type = SOCK_ADDR_EVENT;
    ev->trace_id = trace_id;
    ev->time = bpf_ktime_get_boot_ns();
    ev->pid = pid;
    ev->tid = tid;
    ev->fd = fd;
    if (sk)
        iormesh_read_sock_addrs(ev, sk);
    iormesh_read_addr_arg(ev, uaddr, len);
    bpf_ringbuf_submit(ev, 0);
}

// iormesh_addr_on_exit is the exit hook of the four address calls; flags
// are the IORMESH_ADDR_* above.
static __always_inline void iormesh_addr_on_exit(__u32 pid, __u32 tid, __u32 trace_id, __s64 ret,
                                                 __u32 flags) {
    struct addr_arg *parked = bpf_map_lookup_elem(&addr_arg_map, &tid);
    struct addr_arg arg;

    if (!parked)
        return;
    arg = *parked;
    bpf_map_delete_elem(&addr_arg_map, &tid);
    if (ret < 0 && !(flags & IORMESH_ADDR_FAILED_TOO))
        return;
    iormesh_emit_sock_addr(pid, tid, trace_id, arg.fd, arg.uaddr, iormesh_addr_len(&arg), flags);
}

// iormesh_addr_on_accept is the accept/accept4 exit hook: an accepted
// AF_UNIX socket is reported with its peer, the connecting client's socket,
// which joins the two ends. TCP accepts are sock.c's snapshot instead.
static __always_inline void iormesh_addr_on_accept(__u32 pid, __u32 tid, __u32 trace_id, __s64 ret) {
    struct sock *sk;

    if (ret < 0 || ret > 0x7fffffff)
        return;
    sk = iormesh_sock_of_any_fd((__s32)ret);
    if (!sk || BPF_CORE_READ(sk, __sk_common.skc_family) != IORMESH_AF_UNIX)
        return;
    iormesh_emit_sock_addr(pid, tid, trace_id, (__s32)ret, 0, 0, 0);
}
