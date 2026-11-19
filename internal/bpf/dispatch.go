package bpf

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	bpf "github.com/aquasecurity/libbpfgo"
)

// Syscall probes are not attached to their own tracepoints but stored in
// the program arrays of internal/c/dispatch.c, whose two programs on the
// classic raw_syscalls tracepoints tail-call them by syscall number (task
// 8e4): Linux 7.2 runs a per-syscall tracepoint program preemptibly behind
// a per-program recursion guard, so a task preempted inside it made the
// kernel skip the same syscall of every other task on that CPU - target
// calls among them. dispatch.c has the kernel side of the story.

const (
	enterProgsMap = "syscall_enter_progs"
	exitProgsMap  = "syscall_exit_progs"
	// rawSyscalls is the classic tracepoint category of the dispatchers.
	rawSyscalls = "raw_syscalls"
)

// errNoSyscallNumber: the syscall has no number on this arch
// (syscallNumbers), so no dispatch slot can reach its handler. It wraps
// ENOENT, a probe this kernel cannot run, which attachGroup skips and
// Skipped reports like a missing tracepoint.
var errNoSyscallNumber = fmt.Errorf("no syscall number on this arch: %w", syscall.ENOENT)

// tracefsRoots are where libbpf looks for tracefs, in its order.
var tracefsRoots = []string{"/sys/kernel/tracing", "/sys/kernel/debug/tracing"}

// dispatchers are the two raw_syscalls programs, exit first: attached after
// the slots are filled, the exit side runs before the enter side does, so
// no enter is recorded whose exit could not be.
var dispatchers = []Probe{
	{Program: "handle_raw_sys_exit", Category: rawSyscalls, Event: "sys_exit"},
	{Program: "handle_raw_sys_enter", Category: rawSyscalls, Event: "sys_enter"},
}

// slotLink is one filled program-array slot; Destroy empties it again.
type slotLink struct {
	m  *bpf.BPFMap
	nr uint32
}

func (s slotLink) Destroy() error {
	nr := s.nr
	if err := s.m.DeleteKey(unsafe.Pointer(&nr)); err != nil {
		return fmt.Errorf("clear %s slot %d: %w", s.m.Name(), nr, err)
	}
	return nil
}

// attachSlot stores the handler of a syscalls:sys_{enter,exit}_<name>
// probe in its dispatch slot. A syscall this kernel lacks - no tracepoint
// of that name in tracefs, so the kernel has no such syscall - or one
// without a number on this arch is skipped (ENOENT), as attaching to the
// missing tracepoint used to be.
func (t *Tracer) attachSlot(p Probe) (link, error) {
	if !tracepointExists(p.Category, p.Event) {
		return nil, fmt.Errorf("tracepoint %s: %w", p, syscall.ENOENT)
	}
	mapName, name := enterProgsMap, p.Event[len("sys_enter_"):]
	if p.isExit() {
		mapName, name = exitProgsMap, p.Event[len("sys_exit_"):]
	}
	nr, ok := syscallNumbers[name]
	if !ok {
		return nil, fmt.Errorf("syscall %s: %w", name, errNoSyscallNumber)
	}
	prog, err := t.module.GetProgram(p.Program)
	if err != nil {
		// Our own object lacks the program: a build defect, never a skip.
		return nil, fmt.Errorf("program %s: %v", p.Program, err)
	}
	m, err := t.module.GetMap(mapName)
	if err != nil {
		return nil, fmt.Errorf("get %s: %v", mapName, err)
	}
	fd := uint32(prog.FileDescriptor())
	if err := m.Update(unsafe.Pointer(&nr), unsafe.Pointer(&fd)); err != nil {
		return nil, fmt.Errorf("fill %s slot %d with %s: %v", mapName, nr, p.Program, err)
	}
	t.slots++
	return slotLink{m: m, nr: nr}, nil
}

// attachDispatchers attaches the raw_syscalls programs once at least one
// slot is filled; with none, they would only cost every syscall on the
// host. Their skips are possible loss (Losses.Missed): the tail-called
// handlers run inside them and are never skipped on their own.
func (t *Tracer) attachDispatchers() error {
	if t.slots == 0 {
		return nil
	}
	for _, p := range dispatchers {
		l, err := t.attachTracepoint(p)
		if err != nil {
			return err
		}
		t.links = append(t.links, l)
	}
	return nil
}

// tracepointExists reports whether tracefs has the event, at the first
// tracefs root that has an events directory.
func tracepointExists(category, event string) bool {
	for _, root := range tracefsRoots {
		if _, err := os.Stat(filepath.Join(root, "events")); err != nil {
			continue
		}
		_, err := os.Stat(filepath.Join(root, "events", category, event))
		return err == nil || !errors.Is(err, os.ErrNotExist)
	}
	return false
}
