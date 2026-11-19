//+build ignore

// Adapted from ior internal/c/ior.bpf.c @ b21afc50328c.
// Changes: renamed to iormesh.bpf.c; rawsyscall.c not included; bpf_endian.h
// and sock.c added; sockaddr.c and signal.c added (M2 step 7); rwagg.c added
// (M2 step 11); stack.c added (M2 step 9); javamark.c added (task td4).

#include "vmlinux.h"
#include <bpf/bpf_core_read.h>
#include <bpf/bpf_endian.h>
#include <bpf/bpf_helpers.h>
#include "types.h"
#include "maps.h"
#include "flags.h"

/**
 * Including .c files, as linking several .o files into one single .o file doesn't work
 * with shared BPF state such as ring buffers, maps and globals so well. Other BPF projects
 * come along with one huuuuughe .c file with all the BPF code in it. I am rather 
 * splitting the code up into several smaller files.
 */
#include "filter.c"

// In-kernel aggregation of hot read/write loops (M2 step 11, decision #25):
// filter.c's enter hooks flush through it, the generated read and write
// handlers fold through it.
#include "rwagg.c"

// Receive-side capture helpers used by the generated recvmsg handler.
#include "recv.c"

// select timeout normalisation used by the generated select handler.
#include "poll.c"

// File-handle capture used by the generated name_to_handle_at and
// open_by_handle_at handlers.
#include "handle.c"

// Registered-ring capture used by the generated io_uring_register handlers.
#include "iouring.c"

// File identity of a descriptor (the inode behind an fd number), used by the
// generated single-descriptor enter handlers and the exits of the open kinds.
#include "fileident.c"

// Name of the file a close releases, read through the same walk, used by the
// generated close handler.
#include "fdname.c"

// TCP 4-tuples and state transitions from sock:inet_sock_set_state, joined
// back to pid/fd through the socket registered at connect/accept (iormesh).
// The generated connect and accept handlers call its hooks.
#include "sock.c"

// Address arguments and socket addresses of bind, connect, sendto, recvfrom
// and AF_UNIX accepts (M2 step 7), sent by hooks of the generated handlers.
#include "sockaddr.c"

// Signals sent by or to traced processes, from signal_generate (M2 step 7).
#include "signal.c"

// Opt-in native user stacks of syscall enters (M2 step 9), sent by a hook
// of the generated enter handlers.
#include "stack.c"

// The Java mark (M3, task td4): uprobes on the JDK's hooked I/O natives mark
// the thread, and a hook of the generated enter handlers sends the mark of
// the native a syscall was issued from. Before exec.c, whose thread-exit
// handler resets the mark.
#include "javamark.c"

// Restart fold: the pending-restart state behind filter.c's enter/exit hooks
// and its two probes (signal:signal_deliver, a second sys_enter_rt_sigreturn).
#include "restart.c"

// Hand-written non-syscall tracepoints (sched:sched_process_exec, sched:sched_process_exit).
#include "exec.c"

// Auto-generated tracepoints. ior's raw-tracepoint dispatch prototype
// (rawsyscall.c, task 703) is not lifted: it is opt-in there and S3 decides
// whether iormesh needs cheaper dispatch.
#include "generated_tracepoints.c"

char LICENSE[] SEC("license") = "Dual BSD/GPL";
