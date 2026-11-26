package bpf

import (
	"debug/elf"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strings"
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

// fakeRing is a ring buffer's data area in plain memory, mapped "twice"
// by writing every byte to both halves, as the kernel's double mapping
// shows it.
type fakeRing struct {
	data               []byte
	size               uint64
	consumer, producer uint64
}

func newFakeRing(size uint64) *fakeRing {
	return &fakeRing{data: make([]byte, 2*size), size: size}
}

// put commits a record of payload at the producer position with the
// given header flags.
func (f *fakeRing) put(payload []byte, flags uint32) {
	hdr := binary.LittleEndian.AppendUint32(nil, uint32(len(payload))|flags)
	rec := append(append(hdr, 0, 0, 0, 0), payload...)
	for i, b := range rec {
		off := (f.producer + uint64(i)) % f.size
		f.data[off], f.data[off+f.size] = b, b
	}
	f.producer += (uint64(len(payload)) + ringHdrSize + 7) &^ 7
}

func (f *fakeRing) consume(limit int) []string {
	var got []string
	consume(f.data, f.size-1, &f.consumer, &f.producer, limit, func(raw []byte) {
		got = append(got, string(raw))
	})
	return got
}

// TestConsumeRing: committed records come out in order, a discarded one
// is skipped, the limit is honoured, a record wrapping the end of the
// ring is whole, and a busy record stops the read until it is committed.
func TestConsumeRing(t *testing.T) {
	f := newFakeRing(64)
	f.put([]byte("one"), 0)
	f.put([]byte("dropped"), ringDiscardBit)
	f.put([]byte("two"), 0)
	if got := f.consume(1); len(got) != 1 || got[0] != "one" {
		t.Fatalf("limit 1: %q", got)
	}
	if got := f.consume(10); len(got) != 1 || got[0] != "two" || f.consumer != f.producer {
		t.Fatalf("rest: %q, consumer %d producer %d", got, f.consumer, f.producer)
	}
	f.put([]byte("wraps-around-the-end"), 0) // starts at 48, ends past 64
	f.put([]byte("busy"), ringBusyBit)
	busyAt := f.producer - 16
	if got := f.consume(10); len(got) != 1 || got[0] != "wraps-around-the-end" || f.consumer != busyAt {
		t.Fatalf("wrap: %q, consumer %d, want %d (stopped at the busy record)", got, f.consumer, busyAt)
	}
	off := busyAt % f.size
	f.data[off+3], f.data[off+f.size+3] = 0, 0 // commit: clear the busy bit
	if got := f.consume(10); len(got) != 1 || got[0] != "busy" {
		t.Fatalf("after commit: %q", got)
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
		"sched:sched_process_exit", "raw_tracepoint:task_rename"} {
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
	// The control probes attach with every family; task_rename is one of
	// them and a raw tracepoint too, so only the family's own count.
	fileOnly := ProbesFor([]tracepoints.TraceFamily{tracepoints.FamilyFile})
	for _, p := range fileOnly[:len(fileOnly)-len(ControlProbes())] {
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

// bpfObject is the object `mage bpfBuild` compiles and internal embeds;
// `mage test` builds it first.
const bpfObject = "../c/iormesh.bpf.o"

// objectPrograms lists the BPF programs of the compiled object: its global
// functions in an executable section other than .text (each program's
// section is its SEC name; .text holds the shared subprograms). Asking
// the object rather than matching the C sources cannot miss a spelling of
// a definition (task bg4).
func objectPrograms(t *testing.T) map[string]string {
	t.Helper()
	f, err := elf.Open(bpfObject)
	if err != nil {
		t.Fatalf("%v (run mage bpfBuild, or mage test, which builds it)", err)
	}
	defer func() { _ = f.Close() }()
	syms, err := f.Symbols()
	if err != nil {
		t.Fatal(err)
	}
	progs := map[string]string{}
	for _, s := range syms {
		if elf.ST_TYPE(s.Info) != elf.STT_FUNC || elf.ST_BIND(s.Info) != elf.STB_GLOBAL ||
			int(s.Section) >= len(f.Sections) {
			continue
		}
		sec := f.Sections[s.Section]
		if sec.Flags&elf.SHF_EXECINSTR == 0 || sec.Name == ".text" {
			continue
		}
		progs[s.Name] = sec.Name
	}
	return progs
}

// TestEveryHandWrittenProgramIsAttached: every hand-written program of
// the compiled object is one the Go side attaches somewhere - a probe of
// some family, a dispatcher, the PID-namespace pick of the task_newtask
// program or the Java mark's uprobes - or one it runs instead: the final
// socket snapshot, which FinalSockets runs through BPF_PROG_TEST_RUN (task
// sf4), and the session-start seeding's two iterators, which SeedSockets
// attaches and reads once (task cm4). restart.c defined two programs no Go code attached, so the state
// they were to keep was never kept (task pf4), and exec.c's task_rename was compiled and loaded for nothing until
// task 0g4 attached it. The generated handle_sys_* handlers are left out:
// they cover the whole syscall table, and only the families pick which
// run. The reverse holds too: every program the Go side attaches is in
// the object, so a renamed handler fails here, not on a host.
func TestEveryHandWrittenProgramIsAttached(t *testing.T) {
	attached := map[string]bool{newtaskProg: true, newtaskNSProg: true,
		javaMarkEnterProg: true, javaMarkExitProg: true, sockFinalProg: true,
		seedListenProg: true, seedSockProg: true}
	for _, p := range append(ProbesFor(tracepoints.AllFamilies()), dispatchers...) {
		attached[p.Program] = true
	}
	progs := objectPrograms(t)
	handWritten := 0
	for name, sec := range progs {
		if strings.HasPrefix(name, "handle_sys_") {
			continue
		}
		handWritten++
		if !attached[name] {
			t.Errorf("the object's program %s (SEC %q) is one no Go code attaches", name, sec)
		}
	}
	if handWritten == 0 {
		t.Fatalf("no hand-written program in %s", bpfObject)
	}
	for name := range attached {
		if _, ok := progs[name]; !ok {
			t.Errorf("Go attaches program %s, which %s does not have", name, bpfObject)
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

func (f *fakeAttach) attach(p Probe) (link, error) {
	switch {
	case f.missing[p.Event]:
		return nil, fmt.Errorf("attach %s: %w", p, syscall.ENOENT)
	case f.broken[p.Event]:
		return nil, fmt.Errorf("attach %s: %w", p, syscall.EPERM)
	}
	f.attached = append(f.attached, p.Event)
	return &bpf.BPFLink{}, nil
}

func (f *fakeAttach) destroy(link) error {
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

// TestEverySyscallProbeHasANumber: every syscall probe of every family has
// a dispatch slot on the arches the tracer builds for (dispatch.go), so
// none is silently reported missing on a kernel that has it. arm64 lacks
// the legacy x86 syscalls; there they are absent from its kernel too.
func TestEverySyscallProbeHasANumber(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skipf("syscall tables are checked on amd64, the generation arch (GOARCH %s)", runtime.GOARCH)
	}
	for _, p := range ProbesFor(tracepoints.AllFamilies()) {
		if !p.isSyscall() {
			continue
		}
		name := strings.TrimPrefix(strings.TrimPrefix(p.Event, "sys_enter_"), "sys_exit_")
		if _, ok := syscallNumbers[name]; !ok {
			t.Errorf("%s: syscall %s has no number", p, name)
		}
	}
	if syscallNumbers["read"] != 0 || syscallNumbers["newuname"] != 63 || syscallNumbers["sendfile64"] != 40 {
		t.Errorf("read %d newuname %d sendfile64 %d, want x86_64's 0, 63, 40",
			syscallNumbers["read"], syscallNumbers["newuname"], syscallNumbers["sendfile64"])
	}
}

// TestDispatchersExist: the two raw_syscalls dispatchers are defined in
// dispatch.c, exit side first, so the exit side runs before any enter is
// recorded.
func TestDispatchersExist(t *testing.T) {
	src, err := os.ReadFile("../c/dispatch.c")
	if err != nil {
		t.Fatal(err)
	}
	if dispatchers[0].Event != "sys_exit" || dispatchers[1].Event != "sys_enter" {
		t.Errorf("dispatchers %v, want the exit side first", dispatchers)
	}
	for _, p := range dispatchers {
		sec := `SEC("tracepoint/` + p.Category + `/` + p.Event + `")` + "\nint " + p.Program + "("
		if !strings.Contains(string(src), sec) {
			t.Errorf("dispatch.c lacks %s", sec)
		}
	}
}

// TestSlotOf: a syscall probe's side picks its program array and its
// syscall's number the slot; a syscall without a number on this arch is
// ENOENT, which attachGroup skips as a pair like a missing tracepoint.
func TestSlotOf(t *testing.T) {
	numbers := map[string]uint32{"read": 0, "newuname": 63}
	tests := []struct {
		event   string
		wantMap string
		wantNr  uint32
		missing bool
	}{
		{event: "sys_enter_read", wantMap: enterProgsMap, wantNr: 0},
		{event: "sys_exit_newuname", wantMap: exitProgsMap, wantNr: 63},
		{event: "sys_enter_file_getattr", missing: true},
	}
	for _, tt := range tests {
		m, nr, err := slotOf(Probe{Program: "handle_" + tt.event, Category: "syscalls", Event: tt.event}, numbers)
		if tt.missing {
			if !errors.Is(err, syscall.ENOENT) || !errors.Is(err, errNoSyscallNumber) {
				t.Errorf("%s: err %v, want errNoSyscallNumber (ENOENT)", tt.event, err)
			}
			continue
		}
		if err != nil || m != tt.wantMap || nr != tt.wantNr {
			t.Errorf("%s: %s slot %d err %v, want %s slot %d", tt.event, m, nr, err, tt.wantMap, tt.wantNr)
		}
	}
}

// TestReleaseRecyclesOnReader: a released batch comes back to the reader
// as an empty batch to fill, its records cleared from it (they went back
// to their pools), and a release the channel has no room for is
// recycled in place instead of blocking the consumer (task ud4).
func TestReleaseRecyclesOnReader(t *testing.T) {
	tr := &Tracer{spent: make(chan []any, 1)}
	batch := append(tr.emptyBatch(), types.NewNullEventFast(make([]byte, 24)))
	if cap(batch) != maxBatch {
		t.Fatalf("new batch cap %d, want %d", cap(batch), maxBatch)
	}
	tr.Release(batch)
	overflow := []any{types.NewNullEventFast(make([]byte, 24))}
	tr.Release(overflow) // channel full: recycled here
	if overflow[0] != nil {
		t.Errorf("overflowing batch not cleared: %v", overflow)
	}
	tr.reclaim()
	if len(tr.free) != 1 || len(tr.free[0]) != 0 || batch[0] != nil {
		t.Fatalf("free %v, released batch %v", tr.free, batch)
	}
	if again := tr.emptyBatch(); &again[:1][0] != &batch[0] {
		t.Error("released batch not reused")
	}
}

// TestTargetHintMatchesC: userspace's half of the target hint
// (setUserHint) folds pids to as many words as targethint.c's
// IORMESH_HINT_USER_WORDS, into the map it names; a mismatch would set
// bits the kernel never reads, and AddTarget's processes would go
// untraced.
func TestTargetHintMatchesC(t *testing.T) {
	src, err := os.ReadFile("../c/targethint.c")
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("#define IORMESH_HINT_USER_WORDS (1U << %d)", bitsOf(hintUserWords))
	if 1<<bitsOf(hintUserWords) != hintUserWords || !strings.Contains(string(src), want) {
		t.Errorf("targethint.c lacks %q (hintUserWords %d)", want, hintUserWords)
	}
	if !strings.Contains(string(src), "} "+hintUserMapName+" SEC(\".maps\");") {
		t.Errorf("targethint.c defines no map %s", hintUserMapName)
	}
}

// bitsOf is the exponent of the largest power of two in n.
func bitsOf(n int) int {
	b := 0
	for n > 1 {
		n >>= 1
		b++
	}
	return b
}

// TestTracedMapsMatchC: every program array has its mirror in dispatch.c,
// which the dispatcher asks before its scope test; a mirror missing there
// would fail attachSlot, one never filled would trace nothing.
func TestTracedMapsMatchC(t *testing.T) {
	src, err := os.ReadFile("../c/dispatch.c")
	if err != nil {
		t.Fatal(err)
	}
	for progs, mirror := range tracedMaps {
		for _, name := range []string{progs, mirror} {
			if !strings.Contains(string(src), "} "+name+" SEC(\".maps\");") {
				t.Errorf("dispatch.c defines no map %s", name)
			}
		}
	}
}
