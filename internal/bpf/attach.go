package bpf

import (
	"errors"
	"fmt"
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

// DefaultProbes is M1's attach set: enter and exit of every syscall in
// tracepoints.DefaultSyscalls, plus sock:inet_sock_set_state, which carries
// the TCP 4-tuple and state transitions (M1 step 4). The generated object
// has handlers for every syscall; only this list decides what attaches.
func DefaultProbes() []Probe {
	var probes []Probe
	for _, sc := range tracepoints.DefaultSyscalls() {
		for _, dir := range []string{"enter", "exit"} {
			event := "sys_" + dir + "_" + sc
			probes = append(probes, Probe{Program: "handle_" + event, Category: "syscalls", Event: event})
		}
	}
	return append(probes, Probe{
		Program: "handle_inet_sock_set_state", Category: "sock", Event: "inet_sock_set_state",
	})
}

// attachAll attaches probes one at a time. libbpfgo appends each link to a
// module-wide slice without a lock, so attaches must never run in parallel
// (ior's libbpfAttachMu, internal/ior_bpflink.go @ b21afc50328c); a single
// sequential loop needs no mutex.
func (t *Tracer) attachAll(probes []Probe) error {
	for _, p := range probes {
		link, err := t.attach(p)
		switch {
		case errors.Is(err, syscall.ENOENT):
			// The tracepoint does not exist on this kernel (an arch without
			// open(2), say): skip it, as ior does, and report it.
			t.skipped = append(t.skipped, p)
		case err != nil:
			return err
		default:
			t.links = append(t.links, link)
		}
	}
	return nil
}

func (t *Tracer) attach(p Probe) (*bpf.BPFLink, error) {
	prog, err := t.module.GetProgram(p.Program)
	if err != nil {
		// Our own object lacks the program: a build defect, never a skip.
		return nil, fmt.Errorf("program %s: %v", p.Program, err)
	}
	link, err := prog.AttachTracepoint(p.Category, p.Event)
	if err != nil {
		return nil, fmt.Errorf("attach %s to %s: %w", p.Program, p, err)
	}
	return link, nil
}
