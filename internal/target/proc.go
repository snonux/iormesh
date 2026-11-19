package target

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// clockTicks is USER_HZ, the unit of /proc/<pid>/stat's starttime: 100
// on every Linux architecture iormesh targets.
const clockTicks = 100

// ProcFS reads process facts from a /proc tree. Root is "/proc" on a
// host; tests point it at a fake tree.
type ProcFS struct {
	Root string
}

// Stat is what a session needs of /proc/<pid>/stat.
type Stat struct {
	State byte   // field 3: R, S, D, Z (zombie), X (dead), …
	PPID  uint32 // field 4
	// Flags is field 9, the task's PF_* flags; PF_EXITING marks a task
	// already in do_exit.
	Flags uint64
	// StartNS is field 22, starttime, on the boot clock in ns: the pid
	// incarnation, which tells a reused PID from its predecessor.
	StartNS uint64
	// ArgStart is field 48, the address space's mm->arg_start: a new value
	// with every exec (the stack is randomized), so it tells two images of
	// one process apart (internal/stacks, M2 step 9). 0 when the kernel
	// hides it (no ptrace access), the task has no mm (a zombie) or the
	// line is shorter.
	ArgStart uint64
}

// ErrStat: a stat line that does not parse.
var ErrStat = errors.New("target: malformed /proc stat line")

// HostProcFS is the host's /proc.
func HostProcFS() ProcFS { return ProcFS{Root: "/proc"} }

// ParseStat parses a /proc/<pid>/stat line. The comm field may contain
// spaces and parentheses, so fields are counted after the last ')'.
func ParseStat(line []byte) (Stat, error) {
	i := bytes.LastIndexByte(line, ')')
	if i < 0 {
		return Stat{}, ErrStat
	}
	// fields[0] is field 3 (state), so ppid (4) is fields[1], flags (9)
	// is fields[6], starttime (22) is fields[19] and arg_start (48)
	// fields[45].
	fields := strings.Fields(string(line[i+1:]))
	if len(fields) < 20 || len(fields[0]) != 1 {
		return Stat{}, ErrStat
	}
	ppid, err1 := strconv.ParseUint(fields[1], 10, 32)
	flags, err2 := strconv.ParseUint(fields[6], 10, 64)
	ticks, err3 := strconv.ParseUint(fields[19], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return Stat{}, ErrStat
	}
	st := Stat{State: fields[0][0], PPID: uint32(ppid), Flags: flags,
		StartNS: ticks * (1_000_000_000 / clockTicks)}
	if len(fields) > 45 {
		st.ArgStart, _ = strconv.ParseUint(fields[45], 10, 64)
	}
	return st, nil
}

// Stat reads and parses /proc/<pid>/stat.
func (p ProcFS) Stat(pid uint32) (Stat, error) {
	b, err := os.ReadFile(p.path(pid, "stat"))
	if err != nil {
		return Stat{}, err
	}
	return ParseStat(b)
}

// Cmdline returns pid's argv. A kernel thread's is empty.
func (p ProcFS) Cmdline(pid uint32) ([]string, error) {
	b, err := os.ReadFile(p.path(pid, "cmdline"))
	if err != nil {
		return nil, err
	}
	return SplitArgv(b), nil
}

// Exe returns the path of pid's executable ("" when it has none, as for
// a kernel thread, or when reading it is not permitted).
func (p ProcFS) Exe(pid uint32) string {
	exe, err := os.Readlink(p.path(pid, "exe"))
	if err != nil {
		return ""
	}
	return exe
}

// Tgid returns the thread group pid belongs to. /proc/<tid> resolves for
// a thread id too, although no listing shows it, so a PID that is really
// some process's thread is told apart by its Tgid.
func (p ProcFS) Tgid(pid uint32) (uint32, error) {
	b, err := os.ReadFile(p.path(pid, "status"))
	if err != nil {
		return 0, err
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "Tgid:"); ok {
			n, err := strconv.ParseUint(strings.TrimSpace(rest), 10, 32)
			if err != nil {
				return 0, fmt.Errorf("target: /proc/%d/status Tgid %q: %w", pid, rest, err)
			}
			return uint32(n), nil
		}
	}
	return 0, fmt.Errorf("target: /proc/%d/status has no Tgid line", pid)
}

// pfExiting is the kernel's PF_EXITING task flag (include/linux/sched.h).
const pfExiting = 0x4

// Alive reports whether pid is still the process a session started
// tracing: it exists, is a thread group's leader rather than another
// process's thread, is neither a zombie, dead nor exiting, and — when
// startNS is
// not 0 — has the start time recorded then, so a reused PID is not taken
// for the process that exited. A bare existence check kept a run alive
// for a recycled PID, a zombie or a thread id.
//
// Exiting matters for the start scan's re-check after AddTarget: the BPF
// exit handler (sched_process_exit) runs inside do_exit before the task
// turns zombie, and in between — file and mm teardown, milliseconds for
// a big JVM — its state still reads R/S/D. PF_EXITING is set before
// that tracepoint fires, so a process whose handler already ran never
// passes as alive and its re-inserted entry is removed.
func (p ProcFS) Alive(pid uint32, startNS uint64) bool {
	st, err := p.Stat(pid)
	if err != nil || st.State == 'Z' || st.State == 'X' || st.State == 'x' || st.Flags&pfExiting != 0 {
		return false
	}
	if startNS != 0 && st.StartNS != startNS {
		return false
	}
	tgid, err := p.Tgid(pid)
	return err == nil && tgid == pid
}

// PIDs lists the processes in the tree (the numeric entries of Root).
func (p ProcFS) PIDs() ([]uint32, error) {
	entries, err := os.ReadDir(p.Root)
	if err != nil {
		return nil, err
	}
	var pids []uint32
	for _, e := range entries {
		if n, err := strconv.ParseUint(e.Name(), 10, 32); err == nil && n > 0 {
			pids = append(pids, uint32(n))
		}
	}
	return pids, nil
}

// Ancestors returns pid and every process above it up to init: the
// processes a pattern scan must skip, because the tracer's own command
// line — and that of the sudo or shell that started it — names the
// pattern and so matches it.
func (p ProcFS) Ancestors(pid uint32) map[uint32]bool {
	seen := map[uint32]bool{}
	for pid > 0 && !seen[pid] {
		seen[pid] = true
		st, err := p.Stat(pid)
		if err != nil {
			break
		}
		pid = st.PPID
	}
	return seen
}

func (p ProcFS) path(pid uint32, file string) string {
	return filepath.Join(p.Root, strconv.FormatUint(uint64(pid), 10), file)
}
