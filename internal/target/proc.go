package target

import (
	"bytes"
	"errors"
	"fmt"
	"io"
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
	// Comm is field 2, the task's name between the first '(' and the last
	// ')': read in the same read as StartNS, so the two always describe
	// one incarnation (correlate.ReadProcInfo).
	Comm  string
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
	open := bytes.IndexByte(line, '(')
	i := bytes.LastIndexByte(line, ')')
	if open < 0 || i < open {
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
	st := Stat{Comm: string(line[open+1 : i]), State: fields[0][0], PPID: uint32(ppid), Flags: flags,
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

// MaxCmdline bounds a command line read from /proc for an event (an
// attach's argv, a cut exec record's completion): a process's argument
// block can be megabytes (a JVM's class path), and an event holding it
// whole would pass the 1 MiB line both NDJSON readers take
// (schema.ReadNDJSON, federate.NDJSONDir), losing the session. 64 KiB
// stays far below that even with JSON escaping and the exe path.
const MaxCmdline = 64 << 10

// CmdlineBounded returns pid's argv from at most MaxCmdline bytes of its
// argument block, and whether the block was longer: then the last
// element is a fragment the redaction policy cannot judge, and the
// caller masks it whole (correlate.ExecInfo.Cut, Identity.ArgvCut). A
// kernel thread's argv is empty.
func (p ProcFS) CmdlineBounded(pid uint32) (argv []string, cut bool, err error) {
	f, err := os.Open(p.path(pid, "cmdline"))
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, MaxCmdline+1))
	if err != nil {
		return nil, false, err
	}
	if len(b) > MaxCmdline {
		b, cut = b[:MaxCmdline], true
	}
	return SplitArgv(b), cut, nil
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

// Command returns the argv and executable of pid for the attach event of
// a process already running when the session took it on (task nh4),
// only while pid is still the incarnation that started at startNS: the
// two reads come after the caller's stat read, and a process that exited
// meanwhile and had its PID reused would otherwise lend the target a
// stranger's command line. The stat is re-read after both, so a reuse
// before or between them is caught; one after them is harmless. Any
// failure — the process gone, hidepid, another pid namespace, a kernel
// thread or zombie (empty cmdline, no exe link) — leaves that part
// empty, never an error: the attach goes on without it. Both reads only
// read (cmdline a file, exe a link that is read, not followed). The
// argv is bounded (CmdlineBounded): cut says its last element is a
// fragment to mask. exe is the resolved link — " (deleted)" appended by
// the kernel when the binary was replaced or removed since. The result
// is raw — a secret's carrier, redacted by the correlator and never
// logged.
func (p ProcFS) Command(pid uint32, startNS uint64) (argv []string, exe string, cut bool) {
	if startNS == 0 {
		return nil, "", false
	}
	argv, cut, err := p.CmdlineBounded(pid)
	if err != nil || len(argv) == 0 {
		argv, cut = nil, false
	}
	exe = p.Exe(pid)
	if st, err := p.Stat(pid); err != nil || st.StartNS != startNS {
		return nil, "", false
	}
	return argv, exe, cut
}

// Tgid returns the thread group pid belongs to. /proc/<tid> resolves for
// a thread id too, although no listing shows it, so a PID that is really
// some process's thread is told apart by its Tgid.
func (p ProcFS) Tgid(pid uint32) (uint32, error) {
	return p.statusField(pid, "Tgid:", 0)
}

// statusField returns the n-th whitespace-separated number of the line of
// /proc/<pid>/status that starts with key ("Tgid:", "Uid:").
func (p ProcFS) statusField(pid uint32, key string, n int) (uint32, error) {
	b, err := os.ReadFile(p.path(pid, "status"))
	if err != nil {
		return 0, err
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		rest, ok := strings.CutPrefix(line, key)
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) <= n {
			return 0, fmt.Errorf("target: /proc/%d/status %s %q: no field %d", pid, key, rest, n)
		}
		v, err := strconv.ParseUint(f[n], 10, 32)
		if err != nil {
			return 0, fmt.Errorf("target: /proc/%d/status %s %q: %w", pid, key, rest, err)
		}
		return uint32(v), nil
	}
	return 0, fmt.Errorf("target: /proc/%d/status has no %s line", pid, key)
}

// Owner is how a listening-port session recognises its app after a
// restart (decision #23, tasks mh4 and kk4): the executable, effective
// uid and cgroup of a process that owned the port. The same port alone
// is not the same app: anything may bind a port the app released.
// Compare owners with SameApp, not ==.
type Owner struct {
	Exe string // the resolved exe link, " (deleted)" dropped
	UID uint32 // the effective uid
	// Cgroup is the process's cgroup path (procCgroup): on systemd the
	// service's unit or the login session's scope. "" when unreadable or
	// the root cgroup, which tie nothing together.
	Cgroup string
}

// SameApp reports whether o, a new holder of the port, is the app prev
// held it as (decision #23): always the same effective uid, and either
// the same executable path (a plain restart, a package update in place)
// or — task kk4, a restart onto an upgraded path such as a JDK in a
// versioned directory — the same executable base name in the same
// known cgroup. The base name alone is not enough ("java" names every
// JVM of the user) and neither is the cgroup (a unit may run helpers).
func (o Owner) SameApp(prev Owner) bool {
	if o.UID != prev.UID {
		return false
	}
	if o.Exe == prev.Exe {
		return true
	}
	return prev.Cgroup != "" && o.Cgroup == prev.Cgroup && filepath.Base(o.Exe) == filepath.Base(prev.Exe)
}

// deletedSuffix is what the kernel appends to an exe link whose binary
// was replaced or removed since the exec — a package update under a
// running app, which the restarted app, exec'ing the new binary, lacks.
const deletedSuffix = " (deleted)"

// Owner returns pid's owner key. A process with no readable exe link (a
// kernel thread, a zombie, hidepid) or status has none: an error. An
// unreadable cgroup is no error, only an empty Cgroup: the key then
// matches the exact executable only (SameApp).
func (p ProcFS) Owner(pid uint32) (Owner, error) {
	exe := strings.TrimSuffix(p.Exe(pid), deletedSuffix)
	if exe == "" {
		return Owner{}, fmt.Errorf("target: pid %d: no readable executable", pid)
	}
	uid, err := p.statusField(pid, "Uid:", 1)
	if err != nil {
		return Owner{}, err
	}
	return Owner{Exe: exe, UID: uid, Cgroup: p.procCgroup(pid)}, nil
}

// procCgroup returns pid's cgroup path from /proc/<pid>/cgroup: the
// cgroup v2 line ("0::<path>"), else the v1 name=systemd line (a hybrid
// or legacy host), the hierarchy where systemd places units and scopes.
// "" when the file is unreadable or only the root cgroup "/" is named:
// every process outside a unit shares it, so it identifies nothing.
func (p ProcFS) procCgroup(pid uint32) string {
	b, err := os.ReadFile(p.path(pid, "cgroup"))
	if err != nil {
		return ""
	}
	var v2, systemd string
	for line := range strings.SplitSeq(string(b), "\n") {
		if path, ok := strings.CutPrefix(line, "0::"); ok {
			v2 = path
		} else if _, path, ok := strings.Cut(line, ":name=systemd:"); ok {
			systemd = path
		}
	}
	for _, path := range []string{v2, systemd} {
		if path != "" && path != "/" {
			return path
		}
	}
	return ""
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
