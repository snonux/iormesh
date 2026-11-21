//+build ignore

// Adapted from ior internal/c/ior.bpf.c @ b21afc50328c.
// Changes: renamed to iormesh.bpf.c; rawsyscall.c not included; bpf_endian.h
// and sock.c added; sockaddr.c and signal.c added (M2 step 7); rwagg.c added
// (M2 step 11); stack.c added (M2 step 9); javamark.c added (task td4);
// tcpseq.c added (task xe4); pidns.c added (M7, task 4f4); targethint.c
// added (task 9f4); restart.c not included (task pf4).

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
// PID-namespace-aware pids and tids (M7, task 4f4): every pid the kernel
// side keys by or reports is the task's number in the tracer's namespace.
// Before filter.c, whose filter() is its first user.
#include "pidns.c"

// The target hint, the bitmap in front of target_map (task 9f4).
#include "targethint.c"

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

// TCP sequence numbers of single sends and receives (M4, task xe4), parked
// at the generated data calls' enters and sent at their exits; after sock.c
// and fileident.c, whose constants and typed walk it uses.
#include "tcpseq.c"

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

// Hand-written non-syscall tracepoints (sched:sched_process_exec, sched:sched_process_exit).
#include "exec.c"

// Syscall dispatch (task 8e4): two classic raw_syscalls programs tail-call
// the generated handlers by syscall number, because Linux 7.2 lets a
// per-syscall tracepoint program be preempted and then skips the other
// tasks' calls on that CPU. After exec.c, whose compat test it shares.
#include "dispatch.c"

// Auto-generated tracepoints, run through dispatch.c's program arrays. ior's
// raw-tracepoint dispatch prototype (rawsyscall.c, task 703) is not lifted:
// its raw tracepoints keep the preemptible recursion guard dispatch.c
// avoids.
#include "generated_tracepoints.c"

char LICENSE[] SEC("license") = "Dual BSD/GPL";
