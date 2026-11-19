package main

// The PID reuse roles. The kernel hands out PIDs cyclically, so a freed
// PID normally comes back only after pid_max allocations — a fork loop
// would need minutes of CPU and still race the rest of the host. clone3
// with set_tid (Linux 5.5, CAP_SYS_ADMIN in the init user namespace, i.e.
// root) asks for one exact PID instead: the child gets it, or the call
// fails with EEXIST. One attempt, no loop, no race.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// cloneArgs is struct clone_args (include/uapi/linux/sched.h), VER2 (88
// bytes, with cgroup); every field is a __u64.
type cloneArgs struct {
	flags      uint64
	pidfd      uint64
	childTID   uint64
	parentTID  uint64
	exitSignal uint64
	stack      uint64
	stackSize  uint64
	tls        uint64
	setTID     uint64
	setTIDSize uint64
	cgroup     uint64
}

// pidTarget is the traced process whose PID is reused once it exited.
func pidTarget(dir string) error {
	return bracketed("pid-reuse", func() error {
		return writeFile(filepath.Join(dir, "pid-reuse-target.dat"), []byte("target\n"))
	})
}

// pidKeeper keeps a PID session alive until the test closes its stdin,
// so the reused PID's new process runs while the tracer still runs.
func pidKeeper() error {
	return bracketed("pid-reuse-keeper", func() error { return readToEOF(0) })
}

// pidSquat runs in the process that took over the freed PID: file work
// the tracer must not see, then it holds the PID until stdin's EOF, so
// the test can check the session ends while the PID is in use again.
func pidSquat(dir string) error {
	err := bracketed("pid-reuse-squatter", func() error {
		return writeFile(filepath.Join(dir, "pid-reuse-squatter.dat"), []byte("squatter\n"))
	})
	if err != nil {
		return err
	}
	if err := fact("squatting", "pid", os.Getpid()); err != nil {
		return err
	}
	return readToEOF(0)
}

// pidReuse lands `edgepeer pid-squat -dir D` on pid, says so, and waits
// for it; the child's exit status becomes the launcher's.
func pidReuse(pid int, dir string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	child, err := spawnAt(pid, []string{self, "pid-squat", "-dir", dir})
	if err != nil {
		return fmt.Errorf("clone3 onto pid %d: %w", pid, err)
	}
	if err := fact("reused", "pid", child); err != nil {
		return err
	}
	var ws syscall.WaitStatus
	if _, err := syscall.Wait4(child, &ws, 0, nil); err != nil {
		return fmt.Errorf("wait for pid %d: %w", child, err)
	}
	if !ws.Exited() || ws.ExitStatus() != 0 {
		return fmt.Errorf("pid-squat (pid %d) ended with status %v", child, ws)
	}
	return nil
}

// spawnAt forks a child on exactly pid with clone3(set_tid) and execs
// argv in it. The child is a fork of a Go process: only the calling
// thread survives in it, so between the clone and the exec it must not
// allocate, grow its stack or meet the scheduler. It runs nothing but
// RawSyscall (nosplit) on pointers built beforehand — the same rule
// syscall.forkAndExecInChild follows.
func spawnAt(pid int, argv []string) (int, error) {
	argv0, err := syscall.BytePtrFromString(argv[0])
	if err != nil {
		return 0, err
	}
	argvp, err := syscall.SlicePtrFromStrings(argv)
	if err != nil {
		return 0, err
	}
	envp, err := syscall.SlicePtrFromStrings(os.Environ())
	if err != nil {
		return 0, err
	}
	tids := [1]int32{int32(pid)}
	// ForkLock keeps fds opened meanwhile from leaking into the child,
	// as os/exec does; taken before the addresses are computed, so no
	// call between them and the clone could move this goroutine's stack.
	syscall.ForkLock.Lock()
	args := cloneArgs{exitSignal: uint64(syscall.SIGCHLD),
		setTID: uint64(uintptr(unsafe.Pointer(&tids[0]))), setTIDSize: 1}
	r1, _, errno := syscall.RawSyscall(unix.SYS_CLONE3, uintptr(unsafe.Pointer(&args)), unsafe.Sizeof(args), 0)
	if errno == 0 && r1 == 0 {
		// The child: exec, or die without touching the Go runtime.
		_, _, _ = syscall.RawSyscall(syscall.SYS_EXECVE, uintptr(unsafe.Pointer(argv0)),
			uintptr(unsafe.Pointer(&argvp[0])), uintptr(unsafe.Pointer(&envp[0])))
		_, _, _ = syscall.RawSyscall(syscall.SYS_EXIT_GROUP, 127, 0, 0)
	}
	syscall.ForkLock.Unlock()
	runtime.KeepAlive(&tids)
	runtime.KeepAlive(argv0)
	runtime.KeepAlive(argvp)
	runtime.KeepAlive(envp)
	if errno != 0 {
		return 0, errno
	}
	return int(r1), nil
}
