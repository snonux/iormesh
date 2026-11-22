package bpf

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	bpf "github.com/aquasecurity/libbpfgo"
)

const targetMapName = "target_map"

// Target states of a target_map value (maps.h, IORMESH_TARGET_*).
const (
	targetTraced  = 1
	targetPending = 2
)

// errENOTSUPP is the kernel's internal ENOTSUPP (524), which the bpf
// syscall leaks to userspace for a command a map type lacks; package
// syscall has no name for it.
const errENOTSUPP = syscall.Errno(524)

// targetValue mirrors struct iormesh_target (maps.h): 16 bytes, no
// padding, so its memory is the kernel's value byte for byte.
type targetValue struct {
	State uint32
	Gap   uint32
	Since uint64
}

// AddTarget starts tracing pid. Its children follow it into the trace by
// themselves (exec.c, iormesh_follow_child), and the kernel drops it from
// the set once it exited (iormesh_forget_target). pid's target hint bit is
// set first (setUserHint): filter() looks an entry up only behind a set bit.
func (t *Tracer) AddTarget(pid uint32) error {
	m, err := t.targetMap()
	if err != nil {
		return err
	}
	if err := t.setUserHint(pid); err != nil {
		return err
	}
	return updateTarget(m, pid)
}

// hintUserMapName is userspace's target hint bitmap (targethint.c), and
// hintUserWords its size in 64-bit words: IORMESH_HINT_USER_WORDS.
const (
	hintUserMapName = "target_hint_user"
	hintUserWords   = 1 << 10
)

// setUserHint sets pid's bit in target_hint_user, folded to the bitmap's
// 2^16 bits. Userspace is that map's only writer and hintMu orders its
// read-modify-writes; the kernel's own bitmap is never touched from here,
// since a word written by both could lose a bit (targethint.c). Bits are
// never cleared: a stale one costs a hash lookup, a missing one a target.
func (t *Tracer) setUserHint(pid uint32) error {
	m, err := t.module.GetMap(hintUserMapName)
	if err != nil {
		return fmt.Errorf("get %s: %w", hintUserMapName, err)
	}
	t.hintMu.Lock()
	defer t.hintMu.Unlock()
	word := (pid >> 6) % hintUserWords
	raw, err := m.GetValue(unsafe.Pointer(&word))
	if err != nil {
		return fmt.Errorf("read target hint of %d: %w", pid, err)
	}
	if len(raw) != 8 {
		return fmt.Errorf("target hint word of %d bytes, want 8", len(raw))
	}
	bits := *(*uint64)(unsafe.Pointer(&raw[0])) | 1<<(pid&63)
	if err := m.Update(unsafe.Pointer(&word), unsafe.Pointer(&bits)); err != nil {
		return fmt.Errorf("set target hint of %d: %w", pid, err)
	}
	return nil
}

// RemoveTarget stops tracing pid. Records already in the ring buffer are
// still delivered. A pid the kernel already dropped (its process exited)
// is not an error: the goal, pid untraced, holds.
func (t *Tracer) RemoveTarget(pid uint32) error {
	m, err := t.targetMap()
	if err != nil {
		return err
	}
	if err := m.DeleteKey(unsafe.Pointer(&pid)); err != nil && !errors.Is(err, syscall.ENOENT) {
		return fmt.Errorf("remove target %d: %w", pid, err)
	}
	return nil
}

// AdoptExec turns pid's pending exec (reported by an ExecArgvEvent) into
// a traced process and returns its attach gap: how many records the
// process produced since its pending entry was made, which no trace will
// ever show (maps.h). The entry is updated only if it still exists
// (BPF_EXIST): once the process exited, the kernel removed it
// (iormesh_forget_target), and re-inserting it would leave an entry no
// exit ever removes — tracing whatever process reuses the PID. adopted is
// false then, and the caller must not record an incarnation. Any pending
// entry's gap counts, even one of a later exec of pid (a wrapper that
// execs the app before the verdict on the wrapper): those are the
// process's lost records all the same. Records counted between the read
// and the update are not included, so the gap is a lower bound.
func (t *Tracer) AdoptExec(pid uint32) (gap uint64, adopted bool, err error) {
	m, err := t.targetMap()
	if err != nil {
		return 0, false, err
	}
	return adoptExec(m, pid)
}

// adoptExec is AdoptExec on m.
func adoptExec(m targetStore, pid uint32) (gap uint64, adopted bool, err error) {
	v, ok, err := lookupTarget(m, pid)
	if err != nil || !ok {
		return 0, false, err
	}
	if v.State == targetPending {
		gap = uint64(v.Gap)
	}
	traced := targetValue{State: targetTraced}
	err = m.UpdateValueFlags(unsafe.Pointer(&pid), unsafe.Pointer(&traced), bpf.MapFlagUpdateExist)
	if errors.Is(err, syscall.ENOENT) {
		return 0, false, nil // exited between the read and the update
	}
	if err != nil {
		return 0, false, fmt.Errorf("adopt target %d: %w", pid, err)
	}
	return gap, true, nil
}

// IgnoreExec dismisses the exec reported by an ExecArgvEvent, made at
// since: its pending entry is removed so the process costs nothing more.
// An entry that is traced, or pending for a later exec of pid, must stay
// — the process was adopted already, or a newer exec (jspawnhelper's
// JVM, sh -c 'exec java …') still awaits its own verdict. A read, a
// compare and a delete would race that newer exec: the kernel can replace
// the entry between the read and the delete, which then removes the new
// one, and the matching app would go untraced without a word. So only an
// entry the plain read shows as the dismissed exec's is removed, and it
// is taken out atomically (BPF_MAP_LOOKUP_AND_DELETE_ELEM, hash maps
// since 5.14) and put back if the kernel replaced it after the read
// (putBackTarget). A traced or later entry is never taken out: records
// made while an entry is out are lost uncounted. alive reports whether
// the process still runs; it is asked only on the put-back path.
func (t *Tracer) IgnoreExec(pid uint32, since uint64, alive func() bool) error {
	m, err := t.targetMap()
	if err != nil {
		return err
	}
	return ignoreExec(m, pid, since, alive)
}

// ignoreExec is IgnoreExec on m.
func ignoreExec(m targetStore, pid uint32, since uint64, alive func() bool) error {
	v, ok, err := lookupTarget(m, pid)
	if err != nil || !ok || v.State != targetPending || v.Since != since {
		return err
	}
	raw, err := m.GetValueAndDeleteKey(unsafe.Pointer(&pid))
	switch {
	case errors.Is(err, syscall.ENOENT):
		return nil // exited since the read
	case errors.Is(err, errENOTSUPP) || errors.Is(err, syscall.EINVAL):
		return deleteTarget(m, pid)
	case err != nil:
		return fmt.Errorf("dismiss exec of %d: %w", pid, err)
	}
	v, _, err = decodeTargetValue(raw)
	if err != nil || (v.State == targetPending && v.Since == since) {
		return err
	}
	return putBackTarget(m, pid, v, alive)
}

// deleteTarget is the dismissal on a kernel before 5.14, whose hash maps
// lack lookup-and-delete (ENOTSUPP or EINVAL): a plain delete after
// ignoreExec's compare. The race stays there — an exec landing between
// the read and the delete loses its entry, and that app incarnation goes
// untraced — but the session goes on: failing would end it at the first
// exec on the host that does not match.
func deleteTarget(m targetStore, pid uint32) error {
	if err := m.DeleteKey(unsafe.Pointer(&pid)); err != nil && !errors.Is(err, syscall.ENOENT) {
		return fmt.Errorf("dismiss exec of %d: %w", pid, err)
	}
	return nil
}

// putBackTarget re-inserts v, pid's entry that ignoreExec took out but
// was not the dismissed exec's. Records pid made while the entry was out
// are not counted in its gap, so the gap stays a lower bound (AdoptExec).
// The insert is BPF_NOEXIST: an entry there now is a still newer exec's,
// made by the kernel meanwhile, and it wins. If pid exited while the
// entry was out, its exit found nothing to remove (iormesh_forget_target)
// and nothing ever would, so a process reusing the PID could be adopted
// on the newer exec's verdict: alive is asked after the insert, as
// adoptRunning does in the CLI, and a dead process's entry is removed
// again. An exit after the insert removes the entry by itself. A window
// of microseconds remains: if the PID is reused and the new process
// execs between the insert and that removal, the kernel overwrites the
// put-back entry with the newcomer's pending one, and the unconditional
// delete then removes the newcomer's entry instead.
func putBackTarget(m targetStore, pid uint32, v targetValue, alive func() bool) error {
	err := m.UpdateValueFlags(unsafe.Pointer(&pid), unsafe.Pointer(&v), bpf.MapFlagUpdateNoExist)
	if errors.Is(err, syscall.EEXIST) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("put back target %d: %w", pid, err)
	}
	if alive() {
		return nil
	}
	if err := m.DeleteKey(unsafe.Pointer(&pid)); err != nil && !errors.Is(err, syscall.ENOENT) {
		return fmt.Errorf("remove exited target %d: %w", pid, err)
	}
	return nil
}

// Targeted reports whether pid is traced right now (a pending exec
// awaiting its verdict is not). For tests and diagnostics: the kernel
// changes the set by itself (children join, exited processes leave), so
// the answer can be stale by the time the caller acts on it.
func (t *Tracer) Targeted(pid uint32) (bool, error) {
	m, err := t.targetMap()
	if err != nil {
		return false, err
	}
	v, ok, err := lookupTarget(m, pid)
	return ok && v.State == targetTraced, err
}

// targetStore is what this file needs of target_map: *bpf.BPFMap in the
// tracer, a fake in unit tests, which can land a kernel update between
// two of userspace's calls.
type targetStore interface {
	GetValue(key unsafe.Pointer) ([]byte, error)
	GetValueAndDeleteKey(key unsafe.Pointer) ([]byte, error)
	Update(key, value unsafe.Pointer) error
	UpdateValueFlags(key, value unsafe.Pointer, flags bpf.MapFlag) error
	DeleteKey(key unsafe.Pointer) error
}

var _ targetStore = (*bpf.BPFMap)(nil)

// targetMap returns target_map, or errClosed once Close has started.
func (t *Tracer) targetMap() (targetStore, error) {
	if t.closed() {
		return nil, errClosed
	}
	m, err := t.module.GetMap(targetMapName)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", targetMapName, err)
	}
	return m, nil
}

// updateTarget makes pid a traced entry, replacing a pending one.
func updateTarget(m targetStore, pid uint32) error {
	v := targetValue{State: targetTraced}
	if err := m.Update(unsafe.Pointer(&pid), unsafe.Pointer(&v)); err != nil {
		return fmt.Errorf("add target %d: %w", pid, err)
	}
	return nil
}

// lookupTarget reads pid's entry; ok is false when there is none.
func lookupTarget(m targetStore, pid uint32) (targetValue, bool, error) {
	raw, err := m.GetValue(unsafe.Pointer(&pid))
	if errors.Is(err, syscall.ENOENT) {
		return targetValue{}, false, nil
	}
	if err != nil {
		return targetValue{}, false, fmt.Errorf("look up target %d: %w", pid, err)
	}
	return decodeTargetValue(raw)
}

// decodeTargetValue reads a struct iormesh_target in host byte order,
// the order the kernel wrote it in.
func decodeTargetValue(raw []byte) (targetValue, bool, error) {
	if len(raw) != int(unsafe.Sizeof(targetValue{})) {
		return targetValue{}, false, fmt.Errorf("target value of %d bytes, want %d",
			len(raw), unsafe.Sizeof(targetValue{}))
	}
	return *(*targetValue)(unsafe.Pointer(&raw[0])), true, nil
}
