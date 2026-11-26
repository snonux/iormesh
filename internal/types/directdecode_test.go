package types

import (
	"bytes"
	"encoding/binary"
	"math/rand"
	"reflect"
	"testing"
)

// directDecodeCase is one generated record constructor that used to decode
// with reflective binary.Read (task am4). fresh returns a zero value for the
// oracle to fill; prepare, when set, fixes bytes a constructor validates
// before it decodes (FdPathEvent's schema word).
type directDecodeCase struct {
	name    string
	decode  func([]byte) any
	fresh   func() any
	prepare func([]byte)
	// lengths overrides the payload lengths tried; nil means the field sum,
	// the field sum plus 4 and plus 8 (the kernel's tail padding to the
	// struct's 8-byte alignment) and the field sum minus 1.
	lengths []int
}

func directDecodeCases() []directDecodeCase {
	c := func(name string, decode func([]byte) any, fresh func() any) directDecodeCase {
		return directDecodeCase{name: name, decode: decode, fresh: fresh}
	}
	fdPath := c("FdPathEvent", func(r []byte) any { return NewFdPathEvent(r) }, func() any { return &FdPathEvent{} })
	fdPath.prepare = func(raw []byte) {
		if len(raw) >= 300 {
			binary.LittleEndian.PutUint32(raw[296:300], FD_PATH_EVENT_SCHEMA_VERSION)
		}
	}
	fdPath.lengths = []int{300, 304, 299}
	return []directDecodeCase{
		c("OpenEvent", func(r []byte) any { return NewOpenEvent(r) }, func() any { return &OpenEvent{} }),
		c("OpenNameFixupEvent", func(r []byte) any { return NewOpenNameFixupEvent(r) }, func() any { return &OpenNameFixupEvent{} }),
		c("ExecEvent", func(r []byte) any { return NewExecEvent(r) }, func() any { return &ExecEvent{} }),
		c("NullEvent", func(r []byte) any { return NewNullEvent(r) }, func() any { return &NullEvent{} }),
		c("RetEvent", func(r []byte) any { return NewRetEvent(r) }, func() any { return &RetEvent{} }),
		c("NameEvent", func(r []byte) any { return NewNameEvent(r) }, func() any { return &NameEvent{} }),
		c("PathEvent", func(r []byte) any { return NewPathEvent(r) }, func() any { return &PathEvent{} }),
		fdPath,
		c("FcntlEvent", func(r []byte) any { return NewFcntlEvent(r) }, func() any { return &FcntlEvent{} }),
		c("Dup3Event", func(r []byte) any { return NewDup3Event(r) }, func() any { return &Dup3Event{} }),
		c("OpenByHandleAtEvent", func(r []byte) any { return NewOpenByHandleAtEvent(r) }, func() any { return &OpenByHandleAtEvent{} }),
		c("FileHandleEvent", func(r []byte) any { return NewFileHandleEvent(r) }, func() any { return &FileHandleEvent{} }),
		c("RingFdsEvent", func(r []byte) any { return NewRingFdsEvent(r) }, func() any { return &RingFdsEvent{} }),
		c("FdNameEvent", func(r []byte) any { return NewFdNameEvent(r) }, func() any { return &FdNameEvent{} }),
		c("SocketEvent", func(r []byte) any { return NewSocketEvent(r) }, func() any { return &SocketEvent{} }),
		c("SocketpairEvent", func(r []byte) any { return NewSocketpairEvent(r) }, func() any { return &SocketpairEvent{} }),
		c("AcceptEvent", func(r []byte) any { return NewAcceptEvent(r) }, func() any { return &AcceptEvent{} }),
		c("PipeEvent", func(r []byte) any { return NewPipeEvent(r) }, func() any { return &PipeEvent{} }),
		c("EpollCtlEvent", func(r []byte) any { return NewEpollCtlEvent(r) }, func() any { return &EpollCtlEvent{} }),
		c("PollEvent", func(r []byte) any { return NewPollEvent(r) }, func() any { return &PollEvent{} }),
		c("MemEvent", func(r []byte) any { return NewMemEvent(r) }, func() any { return &MemEvent{} }),
		c("MmapEvent", func(r []byte) any { return NewMmapEvent(r) }, func() any { return &MmapEvent{} }),
		c("SleepEvent", func(r []byte) any { return NewSleepEvent(r) }, func() any { return &SleepEvent{} }),
		c("BpfEvent", func(r []byte) any { return NewBpfEvent(r) }, func() any { return &BpfEvent{} }),
		c("KeyctlEvent", func(r []byte) any { return NewKeyctlEvent(r) }, func() any { return &KeyctlEvent{} }),
		c("PtraceEvent", func(r []byte) any { return NewPtraceEvent(r) }, func() any { return &PtraceEvent{} }),
		c("PerfOpenEvent", func(r []byte) any { return NewPerfOpenEvent(r) }, func() any { return &PerfOpenEvent{} }),
		c("ProcessExecEvent", func(r []byte) any { return NewProcessExecEvent(r) }, func() any { return &ProcessExecEvent{} }),
		c("ProcessExitEvent", func(r []byte) any { return NewProcessExitEvent(r) }, func() any { return &ProcessExitEvent{} }),
		c("TaskNewtaskEvent", func(r []byte) any { return NewTaskNewtaskEvent(r) }, func() any { return &TaskNewtaskEvent{} }),
		c("TaskRenameEvent", func(r []byte) any { return NewTaskRenameEvent(r) }, func() any { return &TaskRenameEvent{} }),
		c("SyscallRestartEvent", func(r []byte) any { return NewSyscallRestartEvent(r) }, func() any { return &SyscallRestartEvent{} }),
		c("TcpStateEvent", func(r []byte) any { return NewTcpStateEvent(r) }, func() any { return &TcpStateEvent{} }),
		c("ExecArgvEvent", func(r []byte) any { return NewExecArgvEvent(r) }, func() any { return &ExecArgvEvent{} }),
		c("SockAddrEvent", func(r []byte) any { return NewSockAddrEvent(r) }, func() any { return &SockAddrEvent{} }),
		c("SignalEvent", func(r []byte) any { return NewSignalEvent(r) }, func() any { return &SignalEvent{} }),
		c("RwAggregateEvent", func(r []byte) any { return NewRwAggregateEvent(r) }, func() any { return &RwAggregateEvent{} }),
		c("StackEvent", func(r []byte) any { return NewStackEvent(r) }, func() any { return &StackEvent{} }),
		c("StackInlineEvent", func(r []byte) any { return NewStackInlineEvent(r) }, func() any { return &StackInlineEvent{} }),
		c("JavaMarkEvent", func(r []byte) any { return NewJavaMarkEvent(r) }, func() any { return &JavaMarkEvent{} }),
		c("TcpSeqEvent", func(r []byte) any { return NewTcpSeqEvent(r) }, func() any { return &TcpSeqEvent{} }),
		c("MmsgEvent", func(r []byte) any { return NewMmsgEvent(r) }, func() any { return &MmsgEvent{} }),
	}
}

// reflectiveDecode is the oracle: the reflective binary.Read decode every
// case's constructor used before task am4, kept here so the direct field
// reads stay pinned to it.
func reflectiveDecode(raw []byte, into any) bool {
	return binary.Read(bytes.NewReader(raw), binary.LittleEndian, into) == nil
}

func isNilRecord(v any) bool {
	return v == nil || reflect.ValueOf(v).IsNil()
}

// TestGeneratedDecodersMatchReflectiveDecode decodes known byte records of
// every formerly reflective record type — every byte random, so each field,
// the string and address arrays and the tail all carry distinct values — and
// holds each constructor to the binary.Read oracle: the same value on the
// field-sum length and on a longer kernel payload (tail padding), nil on a
// short one. Each type decodes twice through its pool so a recycled value
// with stale fields is overwritten field by field.
func TestGeneratedDecodersMatchReflectiveDecode(t *testing.T) {
	rng := rand.New(rand.NewSource(0x5eed))
	for _, tc := range directDecodeCases() {
		size := binary.Size(tc.fresh())
		lengths := tc.lengths
		if lengths == nil {
			lengths = []int{size, size + 4, size + 8, size - 1}
		}
		for round := 0; round < 2; round++ {
			for _, n := range lengths {
				raw := make([]byte, n)
				rng.Read(raw)
				if tc.prepare != nil {
					tc.prepare(raw)
				}
				want := tc.fresh()
				ok := n >= size && reflectiveDecode(raw, want)
				got := tc.decode(raw)
				if !ok {
					if !isNilRecord(got) {
						t.Fatalf("%s len %d: decoded %+v, want nil", tc.name, n, got)
					}
					continue
				}
				if isNilRecord(got) {
					t.Fatalf("%s len %d: nil, want %+v", tc.name, n, want)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("%s len %d:\n got %+v\nwant %+v", tc.name, n, got, want)
				}
				got.(interface{ Recycle() }).Recycle()
			}
		}
	}
}

// TestSockAddrEventDecodesCLayout pins the struct sock_addr_event offsets
// (internal/c/types.h) the msghdr path depends on: the 232-byte record with
// the three 16-byte address arrays at 72, 88 and 104 and the 108-byte path at
// 120, built byte by byte instead of through binary.Write.
func TestSockAddrEventDecodesCLayout(t *testing.T) {
	raw := make([]byte, 232)
	binary.LittleEndian.PutUint32(raw[0:], 77)
	binary.LittleEndian.PutUint32(raw[4:], 5)
	binary.LittleEndian.PutUint64(raw[8:], 1<<40)
	binary.LittleEndian.PutUint32(raw[16:], 100)
	binary.LittleEndian.PutUint32(raw[20:], 101)
	binary.LittleEndian.PutUint32(raw[24:], 0xfffffffd) // fd -3
	binary.LittleEndian.PutUint32(raw[28:], 16)         // AF_NETLINK
	binary.LittleEndian.PutUint32(raw[32:], 3)
	binary.LittleEndian.PutUint32(raw[36:], 4000)
	binary.LittleEndian.PutUint32(raw[40:], 4001)
	binary.LittleEndian.PutUint32(raw[44:], 2)
	binary.LittleEndian.PutUint32(raw[48:], 4002)
	binary.LittleEndian.PutUint32(raw[52:], 1)
	binary.LittleEndian.PutUint64(raw[56:], 0xffff888800001000)
	binary.LittleEndian.PutUint64(raw[64:], 0xffff888800002000)
	raw[72], raw[88], raw[104] = 0xa1, 0xb2, 0xc3
	copy(raw[120:], "/run/x.sock")
	binary.LittleEndian.PutUint32(raw[228:], 9)

	got := NewSockAddrEvent(raw)
	if got == nil {
		t.Fatal("nil decode")
	}
	defer got.Recycle()
	if got.EventType != 77 || got.TraceId != 5 || got.Time != 1<<40 || got.Pid != 100 ||
		got.Tid != 101 || got.Fd != -3 || got.Family != 16 || got.SockType != 3 ||
		got.LocalPort != 4000 || got.RemotePort != 4001 || got.ArgFamily != 2 ||
		got.ArgPort != 4002 || got.PathKind != 1 || got.Sk != 0xffff888800001000 ||
		got.Peer != 0xffff888800002000 || got.LocalAddr[0] != 0xa1 ||
		got.RemoteAddr[0] != 0xb2 || got.ArgAddr[0] != 0xc3 ||
		StringValue(got.Path[:]) != "/run/x.sock" || got.Reserved != 9 {
		t.Fatalf("decoded %+v", got)
	}
	if NewSockAddrEvent(raw[:231]) != nil {
		t.Fatal("short record decoded")
	}
}

// BenchmarkNewSockAddrEvent measures the decode of the msghdr path's address
// record, 40 % of the tracer's CPU on a netlink-heavy load while it went
// through binary.Read (task ak4).
func BenchmarkNewSockAddrEvent(b *testing.B) {
	raw := make([]byte, 232)
	rand.New(rand.NewSource(1)).Read(raw)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		NewSockAddrEvent(raw).Recycle()
	}
}
