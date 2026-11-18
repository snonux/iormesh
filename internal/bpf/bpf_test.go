package bpf

import (
	"encoding/binary"
	"os"
	"regexp"
	"testing"
	"unsafe"

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
	want := 2*len(tracepoints.DefaultSyscalls()) + 1
	if len(probes) != want {
		t.Fatalf("%d probes, want %d", len(probes), want)
	}
	seen := map[string]bool{}
	for _, p := range probes {
		if p.Program != "handle_"+p.Event && p.Program != "handle_inet_sock_set_state" {
			t.Errorf("program %s does not follow handle_<event> for %s", p.Program, p)
		}
		seen[p.String()] = true
	}
	for _, need := range []string{"syscalls:sys_enter_openat", "syscalls:sys_exit_accept4",
		"sock:inet_sock_set_state"} {
		if !seen[need] {
			t.Errorf("default probes lack %s", need)
		}
	}
}

// TestDefaultProbeProgramsExist checks every default program is defined in
// the C the object is built from, so a renamed handler fails here and not
// as an attach error on a host.
func TestDefaultProbeProgramsExist(t *testing.T) {
	var src []byte
	for _, f := range []string{"../c/generated_tracepoints.c", "../c/sock.c"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src = append(src, b...)
	}
	for _, p := range DefaultProbes() {
		def := regexp.MustCompile(`int\s+` + regexp.QuoteMeta(p.Program) + `\s*\(`)
		if !def.Match(src) {
			t.Errorf("program %s not defined in internal/c", p.Program)
		}
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
