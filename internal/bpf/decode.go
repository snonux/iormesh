// Adapted from ior internal/eventloop_kinds.go @ b21afc50328c
// (rawSyscallEvents, rawTaskControlEvents, rawCallControlEvents and the
// decodeFdSizeEvent/decodeEventfdNameEvent/decodeTwoFdNamesEvent helpers).
// Changes: only the event type → decoder mapping is kept (ior's enter
// filters, exit handlers and control handlers belong to its event loop, the
// correlator's job here); TCP_STATE_EVENT (M1 step 4), EXEC_ARGV_EVENT
// (M2 step 2), PROCESS_ARGV_EVENT (M2 step 8), SOCK_ADDR_EVENT and
// SIGNAL_EVENT (M2 step 7), RW_AGGREGATE_EVENT (M2 step 11),
// STACK_EVENT (M2 step 9), STACK_INLINE_EVENT (task kd4) and
// JAVA_MARK_EVENT (task td4; its fast decoder, task ud4), TCP_SEQ_EVENT
// (task xe4) and MMSG_EVENT (task oh4) added.

package bpf

import (
	"encoding/binary"

	"iormesh/internal/types"
)

// decoder turns one ring-buffer record into its typed internal/types value,
// or returns nil when the record is too short or otherwise malformed.
type decoder func(raw []byte) any

// typed adapts a typed constructor to decoder. The explicit nil check is
// needed: a nil *T stored in an `any` is not a nil interface.
func typed[T any](decode func([]byte) *T) decoder {
	return func(raw []byte) any {
		if ev := decode(raw); ev != nil {
			return ev
		}
		return nil
	}
}

// decoders maps every record type the BPF object can emit to its decoder.
// The full table is kept although the default attach set
// (tracepoints.DefaultSyscalls) uses only some record types: a record type
// missing here would be counted as unknown instead of silently mis-decoded
// once a selector widens the attach set.
var decoders = buildDecoders()

// decoderTable is decoders as a slice indexed by event type, which decode
// reads once per record: an index instead of a map lookup (task ud4,
// decision #25's budget). Event types are small integers.
var decoderTable = tableOf(decoders)

func tableOf(m map[types.EventType]decoder) []decoder {
	var n types.EventType
	for et := range m {
		n = max(n, et+1)
	}
	t := make([]decoder, n)
	for et, d := range m {
		t[et] = d
	}
	return t
}

func buildDecoders() map[types.EventType]decoder {
	d := map[types.EventType]decoder{}
	addSyscallDecoders(d)
	addSocketDecoders(d)
	addControlDecoders(d)
	return d
}

func addSyscallDecoders(d map[types.EventType]decoder) {
	d[types.ENTER_OPEN_EVENT] = typed(types.NewOpenEventFast)
	d[types.EXIT_OPEN_EVENT] = typed(types.NewRetEventFast)
	d[types.ENTER_FD_EVENT] = typed(types.NewFdEventFast)
	d[types.ENTER_FD_SIZE_EVENT] = decodeFdSizeEvent
	d[types.ENTER_FD_NAME_EVENT] = typed(types.NewFdNameEventFast)
	d[types.EXIT_FD_EVENT] = typed(types.NewFdEventFast)
	d[types.ENTER_NULL_EVENT] = typed(types.NewNullEventFast)
	d[types.EXIT_NULL_EVENT] = typed(types.NewNullEventFast)
	d[types.EXIT_RET_EVENT] = typed(types.NewRetEventFast)
	d[types.ENTER_NAME_EVENT] = typed(types.NewNameEventFast)
	d[types.ENTER_PATH_EVENT] = typed(types.NewPathEventFast)
	d[types.ENTER_FD_PATH_EVENT] = typed(types.NewFdPathEventFast)
	d[types.ENTER_FCNTL_EVENT] = typed(types.NewFcntlEventFast)
	d[types.ENTER_OPEN_BY_HANDLE_AT_EVENT] = typed(types.NewOpenByHandleAtEventFast)
	d[types.ENTER_DUP3_EVENT] = typed(types.NewDup3EventFast)
	d[types.ENTER_MEM_EVENT] = typed(types.NewMemEventFast)
	d[types.ENTER_MMAP_EVENT] = typed(types.NewMmapEventFast)
	d[types.ENTER_SLEEP_EVENT] = typed(types.NewSleepEventFast)
	d[types.ENTER_EXEC_EVENT] = typed(types.NewExecEventFast)
	d[types.ENTER_KEYCTL_EVENT] = typed(types.NewKeyctlEventFast)
	d[types.ENTER_PTRACE_EVENT] = typed(types.NewPtraceEventFast)
	d[types.ENTER_PERF_OPEN_EVENT] = typed(types.NewPerfOpenEventFast)
	d[types.ENTER_BPF_EVENT] = typed(types.NewBpfEvent)
}

func addSocketDecoders(d map[types.EventType]decoder) {
	d[types.ENTER_SOCKET_EVENT] = typed(types.NewSocketEventFast)
	d[types.ENTER_SOCKETPAIR_EVENT] = typed(types.NewSocketpairEventFast)
	d[types.EXIT_SOCKETPAIR_EVENT] = typed(types.NewSocketpairEventFast)
	d[types.ENTER_ACCEPT_EVENT] = typed(types.NewAcceptEventFast)
	d[types.EXIT_ACCEPT_EVENT] = typed(types.NewAcceptEventFast)
	d[types.ENTER_PIPE_EVENT] = typed(types.NewPipeEventFast)
	d[types.EXIT_PIPE_EVENT] = typed(types.NewPipeEventFast)
	d[types.ENTER_EVENTFD_EVENT] = typed(types.NewEventfdEventFast)
	d[types.ENTER_EVENTFD_NAME_EVENT] = decodeEventfdNameEvent
	d[types.EXIT_EVENTFD_EVENT] = typed(types.NewEventfdEventFast)
	d[types.ENTER_EPOLL_CTL_EVENT] = typed(types.NewEpollCtlEventFast)
	d[types.ENTER_POLL_EVENT] = typed(types.NewPollEventFast)
	d[types.ENTER_TWO_FD_EVENT] = typed(types.NewTwoFdEventFast)
	d[types.ENTER_TWO_FD_NAMES_EVENT] = decodeTwoFdNamesEvent
	d[types.TCP_STATE_EVENT] = typed(types.NewTcpStateEvent)
}

func addControlDecoders(d map[types.EventType]decoder) {
	d[types.PROCESS_EXEC_EVENT] = typed(types.NewProcessExecEventFast)
	d[types.PROCESS_EXIT_EVENT] = typed(types.NewProcessExitEventFast)
	d[types.TASK_NEWTASK_EVENT] = typed(types.NewTaskNewtaskEventFast)
	d[types.TASK_RENAME_EVENT] = typed(types.NewTaskRenameEventFast)
	// No iormesh program emits it: ior's restart fold was not lifted
	// (task pf4). The entry stays so the table matches ior's.
	d[types.SYSCALL_RESTART_EVENT] = typed(types.NewSyscallRestartEventFast)
	d[types.OPEN_NAME_FIXUP_EVENT] = typed(types.NewOpenNameFixupEventFast)
	d[types.FILE_HANDLE_EVENT] = typed(types.NewFileHandleEventFast)
	d[types.RING_FDS_EVENT] = typed(types.NewRingFdsEventFast)
	d[types.EXEC_ARGV_EVENT] = typed(types.NewExecArgvEvent)
	// The same record shape, for a traced process's exec: its EventType
	// tells the caller it is to be recorded, not judged.
	d[types.PROCESS_ARGV_EVENT] = typed(types.NewExecArgvEvent)
	// M2 step 7: a socket call's addresses (sockaddr.c) and a signal
	// sent by or to a traced process (signal.c).
	d[types.SOCK_ADDR_EVENT] = typed(types.NewSockAddrEvent)
	d[types.SIGNAL_EVENT] = typed(types.NewSignalEvent)
	// M2 step 11: a hot loop's folded reads or writes (rwagg.c).
	d[types.RW_AGGREGATE_EVENT] = typed(types.NewRwAggregateEvent)
	// M2 step 9: a syscall enter's native user stack id (stack.c), only
	// sent with Config.NativeStacks.
	d[types.STACK_EVENT] = typed(types.NewStackEvent)
	// The same with the frames inline, for a stack the stack map refused
	// (task kd4).
	d[types.STACK_INLINE_EVENT] = typed(types.NewStackInlineEvent)
	// M3 (task td4): the JDK native a syscall enter was issued from
	// (javamark.c), only sent with Config.JavaMarks — then once per hooked
	// call, so decoded field by field, not by reflection (task ud4).
	d[types.JAVA_MARK_EVENT] = typed(types.NewJavaMarkEventFast)
	// M4 (task xe4): the exit record of a single TCP send or receive with
	// its first sequence number (tcpseq.c), one per such call, hence the
	// fast decoder.
	d[types.TCP_SEQ_EVENT] = typed(types.NewTcpSeqEventFast)
	// M4 (task oh4): the exit record of a sendmmsg or recvmmsg with its
	// messages' byte sum (mmsg.c). Rare next to read and write, so the
	// reflective decoder does.
	d[types.MMSG_EVENT] = typed(types.NewMmsgEvent)
}

// decodeFdSizeEvent widens an fd+size record to the FdEvent shape the
// correlator handles for every fd-only syscall.
func decodeFdSizeEvent(raw []byte) any {
	ev := types.NewFdSizeEventFast(raw)
	if ev == nil {
		return nil
	}
	out := ev.AsFdEvent()
	ev.Recycle()
	return out
}

// decodeEventfdNameEvent folds the named eventfd variant into EventfdEvent.
func decodeEventfdNameEvent(raw []byte) any {
	ev := types.NewEventfdNameEventFast(raw)
	if ev == nil {
		return nil
	}
	out := &types.EventfdEvent{EventType: ev.EventType, TraceId: ev.TraceId, Time: ev.Time,
		Pid: ev.Pid, Tid: ev.Tid, Flags: ev.Flags, Ret: ev.Ret, Fd: ev.Fd,
		Filename: ev.Filename, FilenameStatus: ev.FilenameStatus, SchemaVersion: ev.SchemaVersion}
	ev.Recycle()
	return out
}

// decodeTwoFdNamesEvent folds the named two-fd variant into TwoFdEvent.
func decodeTwoFdNamesEvent(raw []byte) any {
	ev := types.NewTwoFdNamesEventFast(raw)
	if ev == nil {
		return nil
	}
	out := &types.TwoFdEvent{EventType: ev.EventType, TraceId: ev.TraceId, Time: ev.Time,
		Pid: ev.Pid, Tid: ev.Tid, FdA: ev.FdA, FdB: ev.FdB, Extra: ev.Extra,
		Oldname: ev.Oldname, Newname: ev.Newname, OldnameStatus: ev.OldnameStatus,
		NewnameStatus: ev.NewnameStatus, SchemaVersion: ev.SchemaVersion}
	ev.Recycle()
	return out
}

// decodeError says why a record could not be decoded.
type decodeError int

const (
	decodedOK decodeError = iota
	// decodeShort: fewer than the 4 bytes of the event type.
	decodeShort
	// decodeUnknown: no decoder for the record's event type.
	decodeUnknown
	// decodeMalformed: the decoder rejected the record (wrong length).
	decodeMalformed
)

// decode returns the typed record for raw, or the reason it has none.
func decode(raw []byte) (any, decodeError) {
	if len(raw) < 4 {
		return nil, decodeShort
	}
	et := binary.LittleEndian.Uint32(raw)
	if uint64(et) >= uint64(len(decoderTable)) || decoderTable[et] == nil {
		return nil, decodeUnknown
	}
	d := decoderTable[et]
	if rec := d(raw); rec != nil {
		return rec, decodedOK
	}
	return nil, decodeMalformed
}
