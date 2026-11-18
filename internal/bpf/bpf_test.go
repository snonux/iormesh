package bpf

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"regexp"
	"syscall"
	"testing"
	"unsafe"

	bpf "github.com/aquasecurity/libbpfgo"

	"iormesh/internal/tracepoints"
	"iormesh/internal/types"
)

func TestDecodeTypedRecord(t *testing.T) {
	want := types.TcpStateEvent{EventType: types.TCP_STATE_EVENT, Pid: 42, Fd: 7, Sport: 8080}
	raw, err := want.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	rec, why := decode(raw)
	if why != decodedOK {
		t.Fatalf("decode: %v", why)
	}
	got, ok := rec.(*types.TcpStateEvent)
	if !ok {
		t.Fatalf("decoded %T, want *types.TcpStateEvent", rec)
	}
	if got.Pid != 42 || got.Fd != 7 || got.Sport != 8080 {
		t.Fatalf("decoded %+v, want pid 42 fd 7 sport 8080", got)
	}
}

func TestDecodeRejects(t *testing.T) {
	typeOnly := func(et types.EventType) []byte {
		return binary.LittleEndian.AppendUint32(nil, uint32(et))
	}
	cases := []struct {
		name string
		raw  []byte
		want decodeError
	}{
		{"empty", nil, decodeShort},
		{"three bytes", []byte{1, 0, 0}, decodeShort},
		{"unknown type", typeOnly(9999), decodeUnknown},
		{"truncated tcp_state", typeOnly(types.TCP_STATE_EVENT), decodeMalformed},
		{"truncated open", typeOnly(types.ENTER_OPEN_EVENT), decodeMalformed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec, why := decode(c.raw)
			if why != c.want || rec != nil {
				t.Fatalf("decode = (%v, %v), want (nil, %v)", rec, why, c.want)
			}
		})
	}
}

// TestDecodersCoverEveryRecordType keeps the table complete: a record type
// the generator adds to types.h without a decoder here would be counted as
// unknown loss at run time. EXIT_* types are exempt: most exits reuse a
// shared record type (EXIT_RET_EVENT, EXIT_FD_EVENT, ...), and ior's table
// decodes exactly the ones the C emits. ENTER_RET_EVENT is defined in
// types.h but emitted by no handler (checked with grep over internal/c).
func TestDecodersCoverEveryRecordType(t *testing.T) {
	src, err := os.ReadFile("../types/generated_types.go")
	if err != nil {
		t.Fatal(err)
	}
	notEmitted := map[string]bool{"ENTER_RET_EVENT": true}
	for _, m := range regexp.MustCompile(`(?m)^const (EXIT_\w+_EVENT) = `).FindAllStringSubmatch(string(src), -1) {
		notEmitted[m[1]] = true
	}
	re := regexp.MustCompile(`(?m)^const (\w+_EVENT) = (\d+)$`)
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		var v uint32
		for _, c := range m[2] {
			v = v*10 + uint32(c-'0')
		}
		if _, ok := decoders[types.EventType(v)]; !ok && !notEmitted[m[1]] {
			t.Errorf("no decoder for %s (%d)", m[1], v)
		}
	}
}

func TestDefaultProbes(t *testing.T) {
	probes := DefaultProbes()
	want := 2*len(tracepoints.DefaultSyscalls()) + 1 + len(ControlProbes())
	if len(probes) != want {
		t.Fatalf("%d probes, want %d", len(probes), want)
	}
	seen := map[string]bool{}
	for _, p := range probes {
		if p.Program != "handle_"+p.Event {
			t.Errorf("program %s does not follow handle_<event> for %s", p.Program, p)
		}
		seen[p.String()] = true
	}
	for _, need := range []string{"syscalls:sys_enter_openat", "syscalls:sys_exit_accept4",
		"sock:inet_sock_set_state", "task:task_newtask", "sched:sched_process_exec",
		"sched:sched_process_exit"} {
		if !seen[need] {
			t.Errorf("default probes lack %s", need)
		}
	}
}

// TestProbesForFamilies: the opt-in families add their syscalls and the
// signal family its raw signal_generate probe; without the socket family
// there is no inet_sock_set_state.
func TestProbesForFamilies(t *testing.T) {
	all := ProbesFor(tracepoints.AllFamilies())
	want := 2*len(tracepoints.SyscallsFor(tracepoints.AllFamilies())) + 2 + len(ControlProbes())
	if len(all) != want {
		t.Fatalf("%d probes for every family, want %d", len(all), want)
	}
	seen := map[string]bool{}
	for _, p := range all {
		seen[p.String()] = true
	}
	for _, need := range []string{"syscalls:sys_enter_pipe2", "syscalls:sys_exit_kill",
		"raw_tracepoint:signal_generate", "syscalls:sys_enter_epoll_wait",
		"syscalls:sys_exit_sendfile64"} {
		if !seen[need] {
			t.Errorf("probes for every family lack %s", need)
		}
	}
	for _, p := range ProbesFor([]tracepoints.TraceFamily{tracepoints.FamilyFile}) {
		if p.Category == "sock" || p.Category == rawCategory {
			t.Errorf("file family alone attaches %s", p)
		}
	}
}

// TestDefaultProbeProgramsExist checks every program of every family is defined in
// the C the object is built from, so a renamed handler fails here and not
// as an attach error on a host.
func TestDefaultProbeProgramsExist(t *testing.T) {
	var src []byte
	for _, f := range []string{"../c/generated_tracepoints.c", "../c/sock.c", "../c/exec.c",
		"../c/signal.c"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src = append(src, b...)
	}
	for _, p := range ProbesFor(tracepoints.AllFamilies()) {
		def := regexp.MustCompile(`int\s+` + regexp.QuoteMeta(p.Program) + `\s*\(`)
		if !def.Match(src) {
			t.Errorf("program %s not defined in internal/c", p.Program)
		}
	}
}

// TestDecodeTargetValue: a target_map value reads back field by field in
// struct iormesh_target's layout, and a value of another size (a map
// declared differently from maps.h) is an error, not garbage.
func TestDecodeTargetValue(t *testing.T) {
	raw := binary.NativeEndian.AppendUint32(nil, targetPending)
	raw = binary.NativeEndian.AppendUint32(raw, 7)
	raw = binary.NativeEndian.AppendUint64(raw, 123456789)
	v, ok, err := decodeTargetValue(raw)
	if err != nil || !ok || v != (targetValue{State: targetPending, Gap: 7, Since: 123456789}) {
		t.Fatalf("decodeTargetValue = %+v, %v, %v", v, ok, err)
	}
	if _, _, err := decodeTargetValue(raw[:8]); err == nil {
		t.Fatal("an 8-byte value decoded")
	}
}

func TestSumPerCPUCounters(t *testing.T) {
	raw := binary.LittleEndian.AppendUint64(nil, 3)
	raw = binary.LittleEndian.AppendUint64(raw, 0)
	raw = binary.LittleEndian.AppendUint64(raw, 39)
	if got, err := sumPerCPUCounters(raw); err != nil || got != 42 {
		t.Fatalf("sum = %d, %v; want 42", got, err)
	}
	for _, bad := range [][]byte{nil, make([]byte, 7), make([]byte, 12)} {
		if _, err := sumPerCPUCounters(bad); err == nil {
			t.Errorf("sumPerCPUCounters(%d bytes) accepted", len(bad))
		}
	}
}

type fakeMap struct {
	raw []byte
	err error
}

func (f fakeMap) GetValue(unsafe.Pointer) ([]byte, error) { return f.raw, f.err }

func TestReadKernelDrops(t *testing.T) {
	if got, err := readKernelDrops(fakeMap{raw: binary.LittleEndian.AppendUint64(nil, 5)}); err != nil || got != 5 {
		t.Fatalf("readKernelDrops = %d, %v; want 5", got, err)
	}
	if _, err := readKernelDrops(fakeMap{err: os.ErrPermission}); err == nil {
		t.Fatal("readKernelDrops swallowed the map error")
	}
}

func TestDecodeLossesCount(t *testing.T) {
	var d decodeLosses
	for _, e := range []decodeError{decodeUnknown, decodeMalformed, decodeMalformed, decodeShort, decodedOK} {
		d.count(e)
	}
	l := Losses{Kernel: 1, Unknown: d.unknown.Load(), Malformed: d.malformed.Load(), Short: d.short.Load()}
	if l.Unknown != 1 || l.Malformed != 2 || l.Short != 1 || l.Total() != 5 {
		t.Fatalf("losses %+v total %d; want unknown 1 malformed 2 short 1 total 5", l, l.Total())
	}
}

// fakeAttach attaches every probe except those whose event is in missing
// (ENOENT) or broken (EPERM), and records attach/destroy calls.
type fakeAttach struct {
	missing, broken map[string]bool
	attached        []string
	destroyed       int
}

func (f *fakeAttach) attach(p Probe) (*bpf.BPFLink, error) {
	switch {
	case f.missing[p.Event]:
		return nil, fmt.Errorf("attach %s: %w", p, syscall.ENOENT)
	case f.broken[p.Event]:
		return nil, fmt.Errorf("attach %s: %w", p, syscall.EPERM)
	}
	f.attached = append(f.attached, p.Event)
	return &bpf.BPFLink{}, nil
}

func (f *fakeAttach) destroy(*bpf.BPFLink) error {
	f.destroyed++
	return nil
}

func TestAttachProbesAllPresent(t *testing.T) {
	f := &fakeAttach{}
	links, skipped, err := attachProbes(DefaultProbes(), f.attach, f.destroy)
	if err != nil || len(skipped) != 0 || len(links) != len(DefaultProbes()) {
		t.Fatalf("links %d skipped %v err %v; want all %d attached", len(links), skipped, err, len(DefaultProbes()))
	}
}

// TestAttachProbesSkipsWholePair: a missing exit takes its already attached
// enter down with it, so no half pair ever runs.
func TestAttachProbesSkipsWholePair(t *testing.T) {
	f := &fakeAttach{missing: map[string]bool{"sys_exit_open": true}}
	links, skipped, err := attachProbes(DefaultProbes(), f.attach, f.destroy)
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 2 || skipped[0].Event != "sys_enter_open" || skipped[1].Event != "sys_exit_open" {
		t.Fatalf("skipped %v, want the open pair", skipped)
	}
	if f.destroyed != 1 || len(links) != len(DefaultProbes())-2 {
		t.Fatalf("destroyed %d links %d; want the enter detached and the rest attached", f.destroyed, len(links))
	}
}

func TestAttachProbesNothingAttached(t *testing.T) {
	f := &fakeAttach{missing: map[string]bool{}}
	for _, p := range DefaultProbes() {
		f.missing[p.Event] = true
	}
	links, _, err := attachProbes(DefaultProbes(), f.attach, f.destroy)
	if !errors.Is(err, errNothingAttached) || links != nil {
		t.Fatalf("links %d err %v; want errNothingAttached", len(links), err)
	}
}

// TestAttachProbesRealErrorAborts: an error other than ENOENT is never
// mistaken for a missing tracepoint.
func TestAttachProbesRealErrorAborts(t *testing.T) {
	f := &fakeAttach{broken: map[string]bool{"sys_exit_read": true}}
	_, skipped, err := attachProbes(DefaultProbes(), f.attach, f.destroy)
	if !errors.Is(err, syscall.EPERM) || len(skipped) != 0 {
		t.Fatalf("err %v skipped %v; want EPERM and no skip", err, skipped)
	}
	if f.destroyed != 1 {
		t.Fatalf("destroyed %d; want the half-attached read pair's enter detached", f.destroyed)
	}
}
