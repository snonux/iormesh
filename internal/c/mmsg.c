//+build ignore

// Byte sums of sendmmsg and recvmmsg (iormesh, docs/milestones.md M4 "Clock
// reconciliation from the traffic itself", task oh4). Included by
// iormesh.bpf.c after tcpseq.c, whose park and check it shares, and ahead of
// the generated handlers, which call the two hooks below
// (internal/generate/tcpseq.go).
//
// Every other data call returns the bytes it moved; these two return how
// many messages they moved. The bytes are in the caller's array of struct
// mmsghdr: for each message it sent or received, the kernel writes its
// length into the entry's msg_len before the syscall returns (put_user in
// __sys_sendmmsg and do_recvmmsg). So the enter parks the array's address
// by tid, and the exit reads msg_len of the first ret entries and sends
// their sum in the call's exit record (struct mmsg_event in place of the
// ret_event), which userspace takes as the event's bytes - what the
// stream byte sums of the merge's clock proof add up (internal/federate).
//
// The loop. A call moves at most UIO_MAXIOV (1024) messages: the kernel
// clamps vlen to it. The exit reads at most IORMESH_MMSG_MAX entries, one
// 4-byte bpf_probe_read_user each, in a bounded loop (bounded loops exist
// since 5.3, bpf_loop only since 5.17). Unrolling is off and the bound
// constant: el9's 5.14 verifier accepts it, processing 42,078
// instructions for the exit handler against the 1M limit
// (TestMmsgVerifierHeadroom on iom1, 5.14.0-741.22.1.el9_9, task rj4;
// about the same on 7.2; docs/testing.md, "el9 runbook and runs"). The cap
// equals the kernel's
// clamp, so it truncates nothing a kernel returns today; it is still
// checked, and the harness test lowers it. A read that faults (the caller
// unmapped the array mid-call) ends the sum. Either way the record says so
// - MMSG_BYTES_PARTIAL, bytes then a lower bound - rather than passing a
// short sum off as the whole: the correlator marks the event
// args.bytes_partial, and the merge's stream check does not trust that
// direction's sums. So does an exit that finds no park (an LRU eviction, an
// enter whose park the map refused): bytes 0, partial.
//
// tcp_seq. On a TCP socket the messages' bytes go into (come out of) the
// stream back to back, so tcpseq.c's check applies to their sum: the enter
// parks write_seq (copied_seq) through iormesh_seq_on_enter like every data
// call, and the exit hands the sum to iormesh_seq_take. Only a complete sum
// is checked; a partial one only empties the park. A sum of 4 GiB or more
// cannot match the 32-bit distance and gets no tcp_seq.
//
// Cost (decision #25). Both calls are rare next to read and write; a JVM
// issues neither for TCP or UDP (the JDK sends through write, writev and
// sendto). The enter pays the park, one map write; the exit one lookup plus
// one probe read per message, so the cost follows the messages moved.

// IORMESH_MMSG_MAX is the most mmsghdr entries the exit reads: UIO_MAXIOV,
// the kernel's own clamp on vlen. The harness test (internal/generate)
// lowers it to reach the truncation.
#ifndef IORMESH_MMSG_MAX
#define IORMESH_MMSG_MAX 1024
#endif

// The 64-bit userspace ABI of struct mmsghdr: a 56-byte struct msghdr, then
// the unsigned int msg_len, padded to 64 bytes. Written out as recv.c does
// for msghdr: the ABI is stable, vmlinux.h carries the kernel's view.
#define IORMESH_MMSGHDR_SIZE 64
#define IORMESH_MMSG_LEN_OFF 56

// mmsg_park is one thread's parked mmsg call: the address of its mmsghdr
// array and the direction (tcpseq.c's IORMESH_SEQ_*), dir 0 once the exit
// used it.
struct mmsg_park {
    __u64 vec;
    __u32 dir;
    __u32 reserved;
};

// mmsg_park_map: tid -> parked call. Kept emptied after the exit and LRU
// for the reasons seq_park_map is (tcpseq.c). Every exit empties the park
// and every sent enter overwrites it, so an exit reads another call's
// array only when its own enter's park failed (the map refused the
// insert) right after a call whose exit was missed - a residue; an exit
// that finds an emptied park or none sends bytes 0, partial.
struct {
    __uint(type, BPF_MAP_TYPE_LRU_HASH);
    __uint(max_entries, 8192);
    __type(key, __u32);
    __type(value, struct mmsg_park);
} mmsg_park_map SEC(".maps");

// iormesh_mmsg_on_enter is the enter hook of sendmmsg and recvmmsg, behind
// the hooks that decide the enter record is sent (as tcpseq.c's): it parks
// the mmsghdr array the exit sums.
static __always_inline void iormesh_mmsg_on_enter(__u32 tid, __u64 vec, __u32 dir) {
    struct mmsg_park park = {.vec = vec, .dir = dir, .reserved = 0};
    struct mmsg_park *slot = bpf_map_lookup_elem(&mmsg_park_map, &tid);

    if (slot) {
        *slot = park;
        return;
    }
    bpf_map_update_elem(&mmsg_park_map, &tid, &park, BPF_ANY);
}

// iormesh_mmsg_sum adds up msg_len of the first n entries of the array at
// vec into *bytes and returns MMSG_BYTES_PARTIAL when it could not read
// them all (n past the cap, a faulting read), else 0.
static __always_inline __u32 iormesh_mmsg_sum(__u64 vec, __s64 n, __u64 *bytes) {
    __u64 total = 0;
    __u32 flags = 0;

    if (n > IORMESH_MMSG_MAX) {
        n = IORMESH_MMSG_MAX;
        flags = MMSG_BYTES_PARTIAL;
    }
#pragma clang loop unroll(disable)
    for (__u32 i = 0; i < IORMESH_MMSG_MAX; i++) {
        __u32 len = 0;

        if (i >= n)
            break;
        if (bpf_probe_read_user(&len, sizeof(len),
                                (void *)(vec + (__u64)i * IORMESH_MMSGHDR_SIZE + IORMESH_MMSG_LEN_OFF)) != 0) {
            flags = MMSG_BYTES_PARTIAL;
            break;
        }
        total += len;
    }
    *bytes = total;
    return flags;
}

// iormesh_mmsg_emit sends the call's exit record. A full ring loses it
// like any record (counted as a drop).
static __always_inline void iormesh_mmsg_emit(__u32 pid, __u32 tid, __u32 trace_id, __u64 now, __s64 ret,
                                              __u64 bytes, __u64 sk, __u32 seq, __u32 flags) {
    struct mmsg_event *ev = bpf_ringbuf_reserve(&event_map, sizeof(*ev), 0);

    if (!ev) {
        ior_count_ringbuf_drop();
        return;
    }
    ev->event_type = MMSG_EVENT;
    ev->trace_id = trace_id;
    ev->time = now;
    ev->ret = ret;
    ev->pid = pid;
    ev->tid = tid;
    ev->total_bytes = bytes;
    ev->sk = sk;
    ev->seq = seq;
    ev->flags = flags;
    bpf_ringbuf_submit(ev, 0);
}

// iormesh_mmsg_on_exit is the exit hook of sendmmsg and recvmmsg, behind
// the hooks that decide the exit record is sent: it empties both parks
// and, for a call that moved messages (ret > 0), sends the mmsg_event -
// the messages' byte sum and, when tcpseq.c's check holds for it, the
// first sequence number - and returns 1, so the handler sends no
// ret_event. A call that moved nothing returns 0: the handler's plain
// ret_event says all there is.
static __always_inline int iormesh_mmsg_on_exit(__u32 pid, __u32 tid, __u32 trace_id, __u64 now,
                                                __s64 ret, __u32 dir) {
    struct mmsg_park *slot = bpf_map_lookup_elem(&mmsg_park_map, &tid);
    __u64 vec = 0, bytes = 0, sk = 0;
    __u32 seq = 0, flags = MMSG_BYTES_PARTIAL;

    if (slot && slot->dir == dir) {
        vec = slot->vec;
        flags = 0;
    }
    if (slot)
        slot->dir = 0;
    if (ret > 0 && !flags)
        flags = iormesh_mmsg_sum(vec, ret, &bytes);
    // sk and seq stay 0 unless the check holds; a partial sum only
    // empties the park.
    iormesh_seq_take(tid, flags ? 0 : (__s64)bytes, dir, &sk, &seq);
    if (ret <= 0)
        return 0;
    iormesh_mmsg_emit(pid, tid, trace_id, now, ret, bytes, sk, seq, flags);
    return 1;
}
