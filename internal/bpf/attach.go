package bpf

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"syscall"

	bpf "github.com/aquasecurity/libbpfgo"

	"iormesh/internal/tracepoints"
)

// Probe is one BPF program and the tracepoint it runs on: attached there
// itself, or - a per-syscall probe - reached through the raw_syscalls
// dispatch (dispatch.go), which runs it for that syscall's number.
type Probe struct {
	Program  string // e.g. "handle_sys_enter_openat"
	Category string // e.g. "syscalls"
	Event    string // e.g. "sys_enter_openat"
}

func (p Probe) String() string {
	return p.Category + ":" + p.Event
}

// isSyscall reports whether p is a per-syscall probe, which is reached
// through dispatch.c's program arrays rather than attached itself.
func (p Probe) isSyscall() bool {
	return p.Category == "syscalls"
}

// isExit reports whether a syscall probe is the exit side.
func (p Probe) isExit() bool {
	return strings.HasPrefix(p.Event, "sys_exit_")
}

// pairKey groups a syscall's enter and exit probe: they attach together or
// not at all, since an enter without its exit (or the reverse) yields
// records the correlator could never pair. Other probes stand alone.
func (p Probe) pairKey() string {
	if !p.isSyscall() {
		return p.String()
	}
	name := strings.TrimPrefix(strings.TrimPrefix(p.Event, "sys_enter_"), "sys_exit_")
	return "syscalls:" + name
}

// rawCategory marks a Probe attached as a raw tracepoint (SEC
// "raw_tracepoint/<event>"), whose handler gets the tracepoint's arguments
// - task_struct pointers included - instead of the formatted record.
const rawCategory = "raw_tracepoint"

// DefaultProbes is the attach set of the default families
// (tracepoints.DefaultFamilies, decision #7): ProbesFor them.
func DefaultProbes() []Probe {
	return ProbesFor(tracepoints.DefaultFamilies())
}

// ProbesFor is the attach set of the given families: enter and exit of
// each of their syscalls (tracepoints.SyscallsFor), the socket family's
// sock:inet_sock_set_state, which carries the TCP 4-tuple and state
// transitions (M1 step 4), the signal family's signal_generate (the
// delivery with its sender, M2 step 7), and always ControlProbes. The
// generated object has handlers for every syscall; only this list decides
// what attaches.
func ProbesFor(families []tracepoints.TraceFamily) []Probe {
	var probes []Probe
	for _, sc := range tracepoints.SyscallsFor(families) {
		for _, dir := range []string{"enter", "exit"} {
			event := "sys_" + dir + "_" + sc
			probes = append(probes, Probe{Program: "handle_" + event, Category: "syscalls", Event: event})
		}
	}
	if slices.Contains(families, tracepoints.FamilySocket) {
		probes = append(probes, Probe{
			Program: "handle_inet_sock_set_state", Category: "sock", Event: "inet_sock_set_state",
		})
	}
	if slices.Contains(families, tracepoints.FamilySignal) {
		probes = append(probes, Probe{
			Program: "handle_signal_generate", Category: rawCategory, Event: "signal_generate",
		})
	}
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

// link is one attached probe: a BPF link, or a filled dispatch slot
// (slotLink, dispatch.go).
type link interface {
	Destroy() error
}

// attachFunc attaches one probe; destroyFunc detaches one link. They are
// parameters so attachProbes can be tested without a kernel.
type (
	attachFunc  func(Probe) (link, error)
	destroyFunc func(link) error
)

// destroyLink is the real destroyFunc.
func destroyLink(l link) error {
	return l.Destroy()
}

// attachProbes attaches probes one pair group at a time, in order. A group
// with a tracepoint this kernel lacks (ENOENT) is detached again and
// skipped as a whole; any other error aborts. Attaches run sequentially
// because libbpfgo appends each link to a module-wide slice without a lock
// (ior's libbpfAttachMu, internal/ior_bpflink.go @ b21afc50328c).
func attachProbes(probes []Probe, attach attachFunc, destroy destroyFunc) (
	links []link, skipped []Probe, err error) {
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
	[]link, bool, error) {
	var links []link
	for _, p := range group {
		l, err := attach(p)
		if err == nil {
			links = append(links, l)
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

// attachProbe is the real attachFunc of a loaded module: a syscall probe
// fills its dispatch slot (attachSlot), every other probe attaches to its
// own tracepoint.
func (t *Tracer) attachProbe(p Probe) (link, error) {
	if p.isSyscall() {
		return t.attachSlot(p)
	}
	return t.attachTracepoint(p)
}

// attachTracepoint attaches p's program to its tracepoint, classic or raw.
func (t *Tracer) attachTracepoint(p Probe) (link, error) {
	prog, err := t.module.GetProgram(p.Program)
	if err != nil {
		// Our own object lacks the program: a build defect, never a skip,
		// so the error is not wrapped (it may carry ENOENT).
		return nil, fmt.Errorf("program %s: %v", p.Program, err)
	}
	var bl *bpf.BPFLink
	if p.Category == rawCategory {
		bl, err = prog.AttachRawTracepoint(p.Event)
	} else {
		bl, err = prog.AttachTracepoint(p.Category, p.Event)
	}
	if err != nil {
		return nil, fmt.Errorf("attach %s to %s: %w", p.Program, p, err)
	}
	// Programs the kernel runs itself only - these and the dispatchers, not
	// the handlers they tail-call: their skips are possible loss
	// (Losses.Missed).
	t.progFDs = append(t.progFDs, prog.FileDescriptor())
	return bl, nil
}
