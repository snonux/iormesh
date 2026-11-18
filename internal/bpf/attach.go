package bpf

import (
	"errors"
	"fmt"
	"strings"
	"syscall"

	bpf "github.com/aquasecurity/libbpfgo"

	"iormesh/internal/tracepoints"
)

// Probe is one BPF program and the classic tracepoint it attaches to.
type Probe struct {
	Program  string // e.g. "handle_sys_enter_openat"
	Category string // e.g. "syscalls"
	Event    string // e.g. "sys_enter_openat"
}

func (p Probe) String() string {
	return p.Category + ":" + p.Event
}

// pairKey groups a syscall's enter and exit probe: they attach together or
// not at all, since an enter without its exit (or the reverse) yields
// records the correlator could never pair. Other probes stand alone.
func (p Probe) pairKey() string {
	if p.Category != "syscalls" {
		return p.String()
	}
	name := strings.TrimPrefix(strings.TrimPrefix(p.Event, "sys_enter_"), "sys_exit_")
	return "syscalls:" + name
}

// DefaultProbes is the attach set: enter and exit of every syscall in
// tracepoints.DefaultSyscalls, sock:inet_sock_set_state, which carries the
// TCP 4-tuple and state transitions (M1 step 4), and ControlProbes. The
// generated object has handlers for every syscall; only this list decides
// what attaches.
func DefaultProbes() []Probe {
	var probes []Probe
	for _, sc := range tracepoints.DefaultSyscalls() {
		for _, dir := range []string{"enter", "exit"} {
			event := "sys_" + dir + "_" + sc
			probes = append(probes, Probe{Program: "handle_" + event, Category: "syscalls", Event: event})
		}
	}
	probes = append(probes, Probe{
		Program: "handle_inet_sock_set_state", Category: "sock", Event: "inet_sock_set_state",
	})
	return append(probes, ControlProbes()...)
}

// ControlProbes are the process-lifecycle handlers of exec.c (M2 step 1):
// task_newtask inserts a traced process's children into target_map before
// their first syscall, sched_process_exit removes a dead process from it
// (so a reused PID is not traced), and sched_process_exec reports execs —
// of traced processes, and in a pattern session (Config.ExecWatch) of every
// untraced one, with its argv. Each stands alone: a kernel lacking one
// skips just that one, which Skipped reports.
func ControlProbes() []Probe {
	return []Probe{
		{Program: "handle_task_newtask", Category: "task", Event: "task_newtask"},
		{Program: "handle_sched_process_exec", Category: "sched", Event: "sched_process_exec"},
		{Program: "handle_sched_process_exit", Category: "sched", Event: "sched_process_exit"},
	}
}

// errNothingAttached: every probe was skipped. A missing tracepoint and an
// unmounted tracefs both surface as ENOENT, so without this check a host
// without tracefs would run a session that silently traces nothing (ior's
// noProbesError, internal/ior_bpfsetup.go @ b21afc50328c).
var errNothingAttached = errors.New("bpf: no probe attached (every tracepoint missing — is tracefs mounted?)")

// attachFunc attaches one probe; destroyFunc detaches one link. They are
// parameters so attachProbes can be tested without a kernel.
type (
	attachFunc  func(Probe) (*bpf.BPFLink, error)
	destroyFunc func(*bpf.BPFLink) error
)

// attachProbes attaches probes one pair group at a time, in order. A group
// with a tracepoint this kernel lacks (ENOENT) is detached again and
// skipped as a whole; any other error aborts. Attaches run sequentially
// because libbpfgo appends each link to a module-wide slice without a lock
// (ior's libbpfAttachMu, internal/ior_bpflink.go @ b21afc50328c).
func attachProbes(probes []Probe, attach attachFunc, destroy destroyFunc) (
	links []*bpf.BPFLink, skipped []Probe, err error) {
	for _, group := range pairGroups(probes) {
		got, missing, err := attachGroup(group, attach, destroy)
		if err != nil {
			return links, skipped, err
		}
		if missing {
			skipped = append(skipped, group...)
			continue
		}
		links = append(links, got...)
	}
	if len(links) == 0 {
		return nil, skipped, errNothingAttached
	}
	return links, skipped, nil
}

// pairGroups splits probes into groups by pairKey, keeping first-seen order.
func pairGroups(probes []Probe) [][]Probe {
	index := map[string]int{}
	var groups [][]Probe
	for _, p := range probes {
		k := p.pairKey()
		i, ok := index[k]
		if !ok {
			i = len(groups)
			index[k] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], p)
	}
	return groups
}

// attachGroup attaches every probe of one group. On ENOENT it detaches what
// it attached and reports the group missing.
func attachGroup(group []Probe, attach attachFunc, destroy destroyFunc) (
	[]*bpf.BPFLink, bool, error) {
	var links []*bpf.BPFLink
	for _, p := range group {
		link, err := attach(p)
		if err == nil {
			links = append(links, link)
			continue
		}
		var derr error
		for _, l := range links {
			derr = errors.Join(derr, destroy(l))
		}
		if errors.Is(err, syscall.ENOENT) && derr == nil {
			return nil, true, nil
		}
		return nil, false, errors.Join(err, derr)
	}
	return links, false, nil
}

// attachTracepoint is the real attachFunc of a loaded module.
func (t *Tracer) attachTracepoint(p Probe) (*bpf.BPFLink, error) {
	prog, err := t.module.GetProgram(p.Program)
	if err != nil {
		// Our own object lacks the program: a build defect, never a skip,
		// so the error is not wrapped (it may carry ENOENT).
		return nil, fmt.Errorf("program %s: %v", p.Program, err)
	}
	link, err := prog.AttachTracepoint(p.Category, p.Event)
	if err != nil {
		return nil, fmt.Errorf("attach %s to %s: %w", p.Program, p, err)
	}
	return link, nil
}
