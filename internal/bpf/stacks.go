package bpf

import (
	"encoding/binary"
	"fmt"
	"unsafe"

	bpf "github.com/aquasecurity/libbpfgo"
)

// Native stacks (internal/c/stack.c, maps.h stack_map; M2 step 9,
// iormesh run -native-stacks).
const (
	stackMapName  = "stack_map"
	stackFlagName = "IORMESH_NATIVE_STACKS"
	// stackDepth is IORMESH_STACK_DEPTH (maps.h): the u64 frames of one
	// stack_map value.
	stackDepth = 64
)

// setNativeStacks sets the load-time switch and, when it is off, shrinks
// stack_map to one entry: the map is preallocated (about 4.3 MB at its
// full size), and a session without native stacks never writes it.
func (t *Tracer) setNativeStacks(on bool) error {
	flag := uint32(0)
	if on {
		flag = 1
	}
	if err := t.module.InitGlobalVariable(stackFlagName, flag); err != nil {
		return fmt.Errorf("set %s: %w", stackFlagName, err)
	}
	if on {
		return nil
	}
	m, err := t.module.GetMap(stackMapName)
	if err != nil {
		return fmt.Errorf("get %s: %w", stackMapName, err)
	}
	if err := m.SetMaxEntries(1); err != nil {
		return fmt.Errorf("shrink %s: %w", stackMapName, err)
	}
	return nil
}

// StackTrace returns the user stack behind a stack_event's id: its
// instruction pointers, innermost first, as the kernel walked them (raw
// addresses in the address space of the process that made the call). The
// map never overwrites an id (no BPF_F_REUSE_STACKID), so the result is
// the same however late it is read during the session; call it before
// Close, since the map dies with the tracer.
func (t *Tracer) StackTrace(id uint32) ([]uint64, error) {
	if t.closed() {
		return nil, errClosed
	}
	m, err := t.module.GetMap(stackMapName)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", stackMapName, err)
	}
	raw, err := m.GetValue(unsafe.Pointer(&id))
	if err != nil {
		return nil, fmt.Errorf("read stack %d: %w", id, err)
	}
	return parseStack(raw), nil
}

// parseStack decodes one stack_map value: up to stackDepth u64 frames,
// innermost first, ended by the first zero (the kernel zero-fills the rest
// of a shorter stack).
func parseStack(raw []byte) []uint64 {
	var ips []uint64
	for off := 0; off+8 <= len(raw) && len(ips) < stackDepth; off += 8 {
		ip := binary.LittleEndian.Uint64(raw[off:])
		if ip == 0 {
			break
		}
		ips = append(ips, ip)
	}
	return ips
}

// StackMapEntries returns stack_map's capacity as loaded: the full map
// with native stacks on, one entry with them off (setNativeStacks). For
// the root tests and diagnostics: it is how "off costs no memory" is
// checked against the kernel rather than the code.
func (t *Tracer) StackMapEntries() (uint32, error) {
	if t.closed() {
		return 0, errClosed
	}
	m, err := t.module.GetMap(stackMapName)
	if err != nil {
		return 0, fmt.Errorf("get %s: %w", stackMapName, err)
	}
	info, err := bpf.GetMapInfoByFD(m.FileDescriptor())
	if err != nil {
		return 0, fmt.Errorf("info of %s: %w", stackMapName, err)
	}
	return info.MaxEntries, nil
}
