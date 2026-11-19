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

// Program runs the kernel skipped (task vc4). A tracepoint program is not
// run when another BPF program is already running on that CPU - the
// kernel's recursion guard (bpf_prog_active, trace_call_bpf) - and the
// skip is visible only as the program's recursion_misses counter. Under
// concurrent load on Linux 7.2 the read/write handlers of a syscall burst
// were skipped thousands of times per run: a skip of a target's call
// loses the enter or exit record it would have written (or, for a held
// read/write, the call itself), and the trace showed them only as
// unpaired exits. So the misses are reported, as skipped_runs drops.
//
// They are not lost records, though (task vd4): the guard skips the
// program before its target filter runs, so the count includes every
// untraced process's syscalls that hit the guard - on a host with other
// BPF tracers, hundreds during a two-record trace. Nothing runs that
// could tell whose call it was, so the count is only an upper bound on
// the target records lost and stays out of Losses.Total.

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
