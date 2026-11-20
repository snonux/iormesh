package bpf

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// PID-namespace-aware targeting (internal/c/pidns.c, M7 step 1, decision
// #13, task 4f4): a tracer in a non-initial PID namespace - a pod's - keys
// targets and reports pids as that namespace numbers them, so -pid, /proc
// and the JVMs' agent rings all agree, and nothing outside the namespace
// can be named or traced.

const (
	pidNSFlagName = "IORMESH_PIDNS_INUM"
	pidNSDevName  = "IORMESH_PIDNS_DEV"
	// initPidNSInode is the initial PID namespace's fixed inode
	// (PROC_PID_INIT_INO, include/linux/proc_ns.h): a tracer whose
	// /proc/self/ns/pid has it runs on the host.
	initPidNSInode = 0xEFFFFFFC
)

// errForeignProc: the tracer's /proc belongs to another PID namespace, so
// every /proc/<pid> it would read names a different process than the
// kernel side reports.
var errForeignProc = errors.New("bpf: /proc is not mounted for the tracer's PID namespace " +
	"(mount a procfs inside it, e.g. unshare --pid --fork --mount-proc)")

// tracerPidNS returns the inode and device of the tracer's PID namespace
// file, inode 0 for the initial namespace (host mode: the kernel side then
// reads host pids exactly as before). In another namespace it also insists
// that /proc is that namespace's: /proc/self must name the tracer by its own
// pid.
func tracerPidNS(proc string) (inum uint32, dev uint64, err error) {
	var st unix.Stat_t
	if err := unix.Stat(proc+"/self/ns/pid", &st); err != nil {
		return 0, 0, fmt.Errorf("stat the tracer's PID namespace: %w", err)
	}
	if st.Ino == initPidNSInode {
		return 0, 0, nil
	}
	self, err := os.Readlink(proc + "/self")
	if err != nil {
		return 0, 0, fmt.Errorf("read %s/self: %w", proc, err)
	}
	if self != strconv.Itoa(os.Getpid()) {
		return 0, 0, fmt.Errorf("%w: %s/self is %q, the tracer is pid %d", errForeignProc, proc, self, os.Getpid())
	}
	// Namespace inodes are unsigned int in the kernel (proc_alloc_inum).
	return uint32(st.Ino), st.Dev, nil
}

// setPidNS hands the kernel side the tracer's PID namespace (pidns.c).
func (t *Tracer) setPidNS() error {
	inum, dev, err := tracerPidNS("/proc")
	if err != nil {
		return err
	}
	if err := t.module.InitGlobalVariable(pidNSFlagName, inum); err != nil {
		return fmt.Errorf("set %s: %w", pidNSFlagName, err)
	}
	// bpf_get_ns_current_pid_tgid wants the device as stat reports it.
	if err := t.module.InitGlobalVariable(pidNSDevName, dev); err != nil {
		return fmt.Errorf("set %s: %w", pidNSDevName, err)
	}
	t.pidNS = inum
	return nil
}

// PidNS is the inode of the PID namespace the session's pids are numbered
// in, 0 for the host's (the initial namespace).
func (t *Tracer) PidNS() uint32 {
	return t.pidNS
}
