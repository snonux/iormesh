package javaattach

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ErrOtherPidNS: the JVM is in another PID namespace than the tracer.
// Its agent names its ring by its own pid there, which the tracer would
// resolve as a different process of its own namespace (a host tracer
// reading a container JVM's 1.ring as host pid 1), so no stage is made.
var ErrOtherPidNS = errors.New("the JVM is in another PID namespace than the tracer")

// procDir is one process's /proc/<pid>, held open as an O_PATH directory
// fd: every file staging reads of it is opened relative to the fd, so
// should the pid be reused meanwhile, the reads fail with ESRCH instead
// of describing the new process (task cf4 review, N2).
type procDir struct {
	fd   int
	self string // the tracer's own /proc/self, for its namespaces
}

// openProc opens pid's /proc directory.
func (a Attacher) openProc(pid uint32) (procDir, error) {
	fd, err := unix.Open(procPath(a.ProcRoot, pid, ""), dirFlags, 0)
	if err != nil {
		return procDir{fd: -1}, fmt.Errorf("/proc/%d: %w", pid, err)
	}
	return procDir{fd: fd, self: filepath.Join(a.ProcRoot, "self")}, nil
}

func (p procDir) close() { _ = unix.Close(p.fd) }

// read reads one file of the process.
func (p procDir) read(name string) ([]byte, error) {
	fd, err := unix.Openat(p.fd, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	f := os.NewFile(uintptr(fd), name)
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

// readlink reads one of the process's links (ns/pid).
func (p procDir) readlink(name string) (string, error) {
	buf := make([]byte, 256)
	n, err := unix.Readlinkat(p.fd, name, buf)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return string(buf[:n]), nil
}

// openRoot opens the process's root directory (the magic link followed
// on purpose: it names the JVM's root, in its own mount namespace).
func (p procDir) openRoot() (int, error) {
	root, err := unix.Openat(p.fd, "root", dirFlags, 0)
	if err != nil {
		return -1, fmt.Errorf("JVM root: %w", err)
	}
	return root, nil
}

// checkStageable is what staging needs of the target: a JVM (else
// ErrNotJVM), in the tracer's PID namespace (else ErrOtherPidNS), and
// its euid.
func (p procDir) checkStageable() (procInfo, error) {
	maps, err := p.read("maps")
	if err != nil {
		return procInfo{}, err
	}
	if jvm, err := parseMaps(bytes.NewReader(maps)); err != nil || !jvm {
		return procInfo{}, errors.Join(ErrNotJVM, err)
	}
	if err := p.samePidNS(); err != nil {
		return procInfo{}, err
	}
	status, err := p.read("status")
	if err != nil {
		return procInfo{}, err
	}
	return parseStatus(status)
}

// samePidNS refuses a process whose PID namespace is not the tracer's.
func (p procDir) samePidNS() error {
	theirs, err := p.readlink("ns/pid")
	if err != nil {
		return err
	}
	ours, err := os.Readlink(filepath.Join(p.self, "ns", "pid"))
	if err != nil {
		return fmt.Errorf("own ns/pid: %w", err)
	}
	if theirs != ours {
		return fmt.Errorf("%w (%s, the tracer's %s)", ErrOtherPidNS, theirs, ours)
	}
	return nil
}
