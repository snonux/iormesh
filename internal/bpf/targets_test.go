package bpf

import (
	"syscall"
	"testing"
	"unsafe"

	bpf "github.com/aquasecurity/libbpfgo"
)

// fakeTargets is a target_map in memory. after, when set, runs once after
// the next read (GetValue or GetValueAndDeleteKey): the kernel acting
// between userspace's read and its next call. takeOutErr, when set, is
// what GetValueAndDeleteKey fails with (a kernel before 5.14 has no
// lookup-and-delete on hash maps); takeOuts counts its calls.
type fakeTargets struct {
	m          map[uint32]targetValue
	after      func(*fakeTargets)
	takeOutErr error
	takeOuts   int
}

func newFakeTargets() *fakeTargets { return &fakeTargets{m: map[uint32]targetValue{}} }

func (f *fakeTargets) kernel() {
	if hook := f.after; hook != nil {
		f.after = nil
		hook(f)
	}
}

func (f *fakeTargets) GetValue(key unsafe.Pointer) ([]byte, error) {
	defer f.kernel()
	v, ok := f.m[*(*uint32)(key)]
	if !ok {
		return nil, syscall.ENOENT
	}
	return encodeTarget(v), nil
}

func (f *fakeTargets) GetValueAndDeleteKey(key unsafe.Pointer) ([]byte, error) {
	defer f.kernel()
	f.takeOuts++
	if f.takeOutErr != nil {
		return nil, f.takeOutErr
	}
	pid := *(*uint32)(key)
	v, ok := f.m[pid]
	if !ok {
		return nil, syscall.ENOENT
	}
	delete(f.m, pid)
	return encodeTarget(v), nil
}

func (f *fakeTargets) Update(key, value unsafe.Pointer) error {
	return f.UpdateValueFlags(key, value, bpf.MapFlagUpdateAny)
}

func (f *fakeTargets) UpdateValueFlags(key, value unsafe.Pointer, flags bpf.MapFlag) error {
	pid := *(*uint32)(key)
	_, ok := f.m[pid]
	switch {
	case flags == bpf.MapFlagUpdateExist && !ok:
		return syscall.ENOENT
	case flags == bpf.MapFlagUpdateNoExist && ok:
		return syscall.EEXIST
	}
	f.m[pid] = *(*targetValue)(value)
	return nil
}

func (f *fakeTargets) DeleteKey(key unsafe.Pointer) error {
	pid := *(*uint32)(key)
	if _, ok := f.m[pid]; !ok {
		return syscall.ENOENT
	}
	delete(f.m, pid)
	return nil
}

func encodeTarget(v targetValue) []byte {
	return append([]byte(nil), unsafe.Slice((*byte)(unsafe.Pointer(&v)), unsafe.Sizeof(v))...)
}

func pending(since uint64, gap uint32) targetValue {
	return targetValue{State: targetPending, Gap: gap, Since: since}
}

const execPid = 4242

func alwaysAlive() bool { return true }

// TestIgnoreExecRacingNextExec: the process execs again (jspawnhelper,
// then the JVM) right after the dismissal of its first exec read the
// entry. The second exec's pending entry must survive, so the verdict on
// the second exec adopts it, with the gap that entry counted.
func TestIgnoreExecRacingNextExec(t *testing.T) {
	f := newFakeTargets()
	f.m[execPid] = pending(1, 0)
	f.after = func(f *fakeTargets) { f.m[execPid] = pending(2, 7) }
	if err := ignoreExec(f, execPid, 1, alwaysAlive); err != nil {
		t.Fatal(err)
	}
	gap, adopted, err := adoptExec(f, execPid)
	if err != nil || !adopted || gap != 7 {
		t.Fatalf("adoptExec after a racing exec = %d, %v, %v: want adopted with gap 7", gap, adopted, err)
	}
}

// TestIgnoreExecKeepsLaterExec: the entry already belongs to a later exec
// when the first one is dismissed; it stays, gap and all, and is never
// taken out (records made while it was out would miss the gap).
func TestIgnoreExecKeepsLaterExec(t *testing.T) {
	f := newFakeTargets()
	f.m[execPid] = pending(2, 3)
	if err := ignoreExec(f, execPid, 1, alwaysAlive); err != nil {
		t.Fatal(err)
	}
	if got, ok := f.m[execPid]; !ok || got != pending(2, 3) || f.takeOuts != 0 {
		t.Fatalf("entry after dismissing an older exec = %+v, %v (%d take-outs): want %+v, none taken out",
			got, ok, f.takeOuts, pending(2, 3))
	}
}

// TestIgnoreExecKeepsTraced: an adopted process is not dismissed by a
// stale record of an earlier exec, and its entry is never taken out —
// its syscalls in that window would be lost without a count.
func TestIgnoreExecKeepsTraced(t *testing.T) {
	f := newFakeTargets()
	f.m[execPid] = targetValue{State: targetTraced}
	if err := ignoreExec(f, execPid, 1, alwaysAlive); err != nil {
		t.Fatal(err)
	}
	if got := f.m[execPid]; got.State != targetTraced || f.takeOuts != 0 {
		t.Fatalf("traced entry after a dismissal = %+v (%d take-outs): want traced, never taken out",
			got, f.takeOuts)
	}
}

// TestIgnoreExecWithoutLookupAndDelete: a kernel before 5.14 has no
// lookup-and-delete on hash maps (ENOTSUPP, 524, or EINVAL). The
// dismissal still removes its own pending entry and never fails — an
// error would end the session at the first exec that does not match.
func TestIgnoreExecWithoutLookupAndDelete(t *testing.T) {
	for _, errno := range []syscall.Errno{errENOTSUPP, syscall.EINVAL} {
		f := newFakeTargets()
		f.takeOutErr = errno
		f.m[execPid] = pending(1, 5)
		if err := ignoreExec(f, execPid, 1, alwaysAlive); err != nil {
			t.Fatalf("errno %d: dismissal failed: %v", errno, err)
		}
		if _, ok := f.m[execPid]; ok {
			t.Fatalf("errno %d: dismissed entry still there: %+v", errno, f.m[execPid])
		}
	}
}

// TestIgnoreExecRemovesOwnEntry: the dismissed exec's own entry goes.
func TestIgnoreExecRemovesOwnEntry(t *testing.T) {
	f := newFakeTargets()
	f.m[execPid] = pending(1, 5)
	if err := ignoreExec(f, execPid, 1, alwaysAlive); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.m[execPid]; ok {
		t.Fatalf("dismissed entry still there: %+v", f.m[execPid])
	}
	if err := ignoreExec(f, execPid, 1, alwaysAlive); err != nil {
		t.Fatalf("dismissing a missing entry: %v", err)
	}
}

// TestIgnoreExecExitedWhilePutBack: a later exec's entry was taken out
// and the process exited before it was put back — its exit found nothing
// to remove, so the put-back entry must go again, or a process reusing
// the PID would be adopted on the later exec's verdict.
func TestIgnoreExecExitedWhilePutBack(t *testing.T) {
	f := newFakeTargets()
	f.m[execPid] = pending(1, 0)
	f.after = func(f *fakeTargets) { f.m[execPid] = pending(2, 3) }
	if err := ignoreExec(f, execPid, 1, func() bool { return false }); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.m[execPid]; ok {
		t.Fatalf("entry of an exited process left behind: %+v", f.m[execPid])
	}
	if _, adopted, err := adoptExec(f, execPid); err != nil || adopted {
		t.Fatalf("adoptExec of an exited process = %v, %v: want not adopted", adopted, err)
	}
}
