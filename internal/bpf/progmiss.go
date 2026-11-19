package bpf

/*
#include <bpf/bpf.h>
#include <linux/bpf.h>

// iormesh_prog_misses returns the recursion_misses counter of the BPF
// program behind fd, or -1 when its info cannot be read. A kernel whose
// bpf_prog_info has no such field leaves it 0.
static long long iormesh_prog_misses(int fd) {
    struct bpf_prog_info info = {};
    __u32 len = sizeof(info);

    if (bpf_prog_get_info_by_fd(fd, &info, &len))
        return -1;
    return (long long)info.recursion_misses;
}
*/
import "C"

import "fmt"

// Program runs the kernel skipped (task vc4). The kernel does not run a
// tracepoint program while its recursion guard is raised, and the skip is
// visible only as the program's recursion_misses counter: a skip of a
// target's call loses the enter or exit record it would have written (or,
// for a held read/write, the call itself). So the misses are reported, as
// skipped_runs drops.
//
// They are not lost records, though (task vd4): the guard skips the
// program before its target filter runs, so the count includes every
// untraced process's syscalls that hit the guard. Nothing runs that could
// tell whose call it was, so the count is only an upper bound on the
// target records lost and stays out of Losses.Total.
//
// Which guard (task 8e4): on Linux 7.2 a per-syscall tracepoint program
// ran preemptibly behind a per-program guard, so a task preempted inside
// it skipped the same syscall of every other task on that CPU - up to a
// time slice of calls per preemption, the target's among them. Syscall
// handlers therefore run behind the raw_syscalls dispatchers
// (internal/c/dispatch.c), which run with preemption disabled; what can
// still skip them, and the other classic tracepoint programs, is the
// host-wide bpf_prog_active guard, held by another BPF program on that
// CPU or by a task preempted inside a bpf() map syscall. Those are the
// programs whose misses are read here (Tracer.progFDs): the tail-called
// handlers are never skipped on their own.

// readProgMisses sums the recursion misses of the programs behind fds.
func readProgMisses(fds []int, read func(fd int) (uint64, error)) (uint64, error) {
	var total uint64
	for _, fd := range fds {
		n, err := read(fd)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// progMisses reads one program's recursion_misses through
// BPF_OBJ_GET_INFO_BY_FD.
func progMisses(fd int) (uint64, error) {
	n := C.iormesh_prog_misses(C.int(fd))
	if n < 0 {
		return 0, fmt.Errorf("read info of BPF program fd %d", fd)
	}
	return uint64(n), nil
}
