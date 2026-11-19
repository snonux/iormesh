package schema

// Event kinds. A kind is either the traced syscall's name or one of the
// synthetic kinds the correlator emits. The set only grows: M2 widens the
// traced surface by appending kinds (the file surface in kinds_file.go),
// and Validate rejects any kind not listed so a typo or a stale producer
// fails loudly instead of producing events no query matches.
const (
	KindOpen    = "open"
	KindOpenat  = "openat"
	KindOpenat2 = "openat2"
	KindRead    = "read"
	KindWrite   = "write"
	KindClose   = "close"
	KindConnect = "connect"
	KindAccept  = "accept"
	KindAccept4 = "accept4"

	// KindTCPState is a TCP state transition from the
	// sock:inet_sock_set_state tracepoint, joined back to pid/fd through
	// the socket; args.sock_state names the new state.
	KindTCPState = "tcp_state"
	// KindMarker is an openat of /iormesh-marker/<scenario>/<edge>; the
	// normalizer cuts traces to the bracket these delimit.
	KindMarker = "marker"
	// KindDrop records loss: gaps are explicit, never silent. args.lost
	// counts the records lost since the previous drop event; without
	// args.drop_reason it is ring-buffer or decoder loss of the host
	// (pid 0), with one it says why (DropAttachGap, DropUnattributed).
	KindDrop = "drop"
	// KindSession is the per-trace header record, the first event of a
	// trace; Event.Session describes the host.
	KindSession = "session"
	// KindEnd records why the trace ended (args.end_reason), so a reader
	// knows whether the trace is complete. It is the last event; a trace
	// without one was cut short (crash, or a write error that also lost
	// the end record).
	KindEnd = "end"
	// KindExec is a successful exec of a traced process (M2 step 8,
	// sched_process_exec): args.path is the executable as exec'd, and
	// args.argv the new program's argv, redacted by internal/redact
	// (decision #9; env is never captured). The exec through which a
	// pattern session found its target counts too; a process already
	// running when the trace attached has none.
	KindExec = "exec"
)

// End reasons, the values of args.end_reason. PLAN.md ("Explicit end")
// names duration, disk limit, controller stop and error; targets_exited
// is the foreground tracer's own natural end.
const (
	EndDuration      = "duration"       // the -duration bound was reached
	EndStop          = "stop"           // SIGINT/SIGTERM: a user or controller stop
	EndTargetsExited = "targets_exited" // every traced process exited
	EndDiskLimit     = "disk_limit"     // the trace disk budget ran out (M4)
	EndError         = "error"          // the tracer failed; the trace is incomplete
)

// Drop reasons, the values of args.drop_reason.
const (
	// DropAttachGap: the records a pattern-matched process produced
	// between its exec and the tracer attaching to it (M2 step 2). The
	// drop names that process (pid, pid_incarnation) and is emitted when
	// the tracer attached, before any of its traced events.
	DropAttachGap = "attach_gap"
	// DropUnattributed: a folded read or write (M2 step 11) whose calls no
	// single fd instance explains - another thread closed, dup2'd or
	// reopened the fd number while the loop ran or before its record was
	// sent. args.lost is the calls it stood for; the drop names the thread
	// (pid, pid_incarnation, tid) and its boot_ns is the first call's.
	DropUnattributed = "unattributed"
)

// Attribution values. Missing attribution is data, not a hole: every
// event says which one applies, and the empty string is invalid.
const (
	AttributionJava = "java"
	AttributionNone = "none"
)

// NoFD is the fd of an event that acts on no file descriptor: synthetic
// kinds, and an open or accept that failed.
const NoFD = -1

// Event is the envelope every trace record shares, in NDJSON and parquet
// alike. Fields without omitempty are required: DecodeEvent rejects an
// event that lacks any of them or sets one to null, so a zero value is
// always a real zero.
type Event struct {
	SchemaVersion uint32 `json:"schema_version" parquet:"schema_version"`
	Project       string `json:"project" parquet:"project,dict"`
	// Seq is the per-host sequence number, starting at 1 and gap-free
	// apart from explicit drop events. 0 means "never assigned".
	Seq    uint64 `json:"seq" parquet:"seq"`
	BootNS uint64 `json:"boot_ns" parquet:"boot_ns"`
	WallNS uint64 `json:"wall_ns" parquet:"wall_ns"`
	// HostID is /etc/machine-id; the session header adds boot id, FQDN,
	// kernel release and arch.
	HostID string `json:"host_id" parquet:"host_id,dict"`
	PID    uint32 `json:"pid" parquet:"pid"`
	// PIDInc is the task's start time on the boot clock
	// (task->start_boottime), so a reused PID is a different process.
	PIDInc uint64 `json:"pid_incarnation" parquet:"pid_incarnation"`
	TID    uint32 `json:"tid" parquet:"tid"`
	Comm   string `json:"comm" parquet:"comm,dict"`
	Event  string `json:"event" parquet:"event,dict"`
	// FD is the descriptor the event acts on: the argument, or for open*
	// and accept* the returned one. NoFD when there is none.
	FD int32 `json:"fd" parquet:"fd"`
	// FDGen counts how often this fd number was (re)created in this
	// process incarnation. Every fd-creating syscall bumps it — open*,
	// accept*, socket, dup*, pipe, … — not only the traced ones, so
	// (pid, pid_incarnation, fd, fd_gen) names one fd instance even when
	// a library's open and a later socket land on the same number. 0
	// means the fd predates the trace (or there is no fd).
	FDGen     uint32 `json:"fd_gen" parquet:"fd_gen"`
	Ret       int64  `json:"ret" parquet:"ret"`
	Err       string `json:"err,omitempty" parquet:"err,dict"` // errno name, only when ret < 0
	LatencyNS uint64 `json:"latency_ns" parquet:"latency_ns"`
	Bytes     uint64 `json:"bytes" parquet:"bytes"`
	Args      Args   `json:"args" parquet:"args"`
	// ConnID names the connection a socket event belongs to. M1 assigns
	// it locally for fused loopback pairs; across hosts the controller's
	// merge joins by the TCP ISN pair (decision #26) and may reassign it.
	ConnID      string `json:"conn_id,omitempty" parquet:"conn_id,optional"`
	Attribution string `json:"attribution" parquet:"attribution,dict"`
	// Java is set exactly when Attribution is AttributionJava (shape
	// settled by spike S1: nested thread + stack).
	Java *Java `json:"java,omitempty" parquet:"java,optional"`
	// HTTP is provisional until M3; present on http events.
	HTTP *HTTP `json:"http,omitempty" parquet:"http,optional"`
	// Session describes the traced host; set exactly on session events.
	// A nested object rather than envelope fields because only one event
	// per trace carries it.
	Session *Session `json:"session,omitempty" parquet:"session,optional"`
	// StackID names the native user stack the thread had when it entered
	// the syscall: a stack_id of the trace's stack table (stacks.ndjson,
	// Stack). Only with iormesh run -native-stacks (M2 step 9), and only on
	// syscall events whose stack the kernel could walk; absent otherwise,
	// and always ≥ 1 when set. Appended after Session: fields only append.
	StackID *uint32 `json:"stack_id,omitempty" parquet:"stack_id,optional"`
}

// Args holds every syscall-specific argument as an optional field: one
// nullable parquet column per field, never a JSON blob, so queries can
// filter on any of them.
type Args struct {
	Path       *string `json:"path,omitempty" parquet:"path,optional"`
	NewPath    *string `json:"new_path,omitempty" parquet:"new_path,optional"`
	Flags      *uint32 `json:"flags,omitempty" parquet:"flags,optional"`
	Cmd        *uint32 `json:"cmd,omitempty" parquet:"cmd,optional"` // fcntl's raw command; epoll_ctl's op
	Offset     *int64  `json:"offset,omitempty" parquet:"offset,optional"`
	LocalAddr  *string `json:"local_addr,omitempty" parquet:"local_addr,optional"`
	LocalPort  *uint16 `json:"local_port,omitempty" parquet:"local_port,optional"`
	RemoteAddr *string `json:"remote_addr,omitempty" parquet:"remote_addr,optional"`
	RemotePort *uint16 `json:"remote_port,omitempty" parquet:"remote_port,optional"`
	SockState  *string `json:"sock_state,omitempty" parquet:"sock_state,optional"`
	Marker     *string `json:"marker,omitempty" parquet:"marker,optional"`
	// Lost is a drop event's count: records lost since the previous drop
	// event, kernel- and userspace-side together. Always > 0.
	Lost *uint64 `json:"lost,omitempty" parquet:"lost,optional"`
	// DropReason says why a drop event's records were lost, one of the
	// Drop* constants; absent for ring-buffer and decoder loss.
	DropReason *string `json:"drop_reason,omitempty" parquet:"drop_reason,optional"`
	// EndReason is an end event's reason, one of the End* constants.
	EndReason *string `json:"end_reason,omitempty" parquet:"end_reason,optional"`
	// Argv is an exec event's command line, redacted before it is set
	// (internal/redact: "-Dpassword=***", "--token ***"). Only exec
	// events carry it; an unreadable argv leaves it absent.
	Argv []string `json:"argv,omitempty" parquet:"argv,list"`
	// FD2 and FD2Gen name a second fd instance (M2 step 7): the other end
	// of a pipe or socketpair, or the fd epoll_ctl registers. The pair is
	// an fd instance like the envelope's (fd, fd_gen).
	FD2    *int32  `json:"fd2,omitempty" parquet:"fd2,optional"`
	FD2Gen *uint32 `json:"fd2_gen,omitempty" parquet:"fd2_gen,optional"`
	// Signal is the signal number: what kill, tgkill and tkill sent (when
	// the kernel generated it) and what a signal event delivered.
	Signal *int32 `json:"signal,omitempty" parquet:"signal,optional"`
	// TargetPID is the tgid a kill, tgkill or tkill signalled; SenderPID
	// the tgid that sent a signal event's signal. Raw pids: the sender
	// or target need not be traced, and then no process node names it.
	TargetPID *uint32 `json:"target_pid,omitempty" parquet:"target_pid,optional"`
	SenderPID *uint32 `json:"sender_pid,omitempty" parquet:"sender_pid,optional"`
	// Count is how many calls a read or write event stands for when the
	// tracer folded a hot loop into it (M2 step 11, decision #25): bytes
	// and ret are then the calls' sums, latency_ns the sum of their
	// latencies, boot_ns the first call's enter. Absent for a single call;
	// when set, always ≥ 2.
	Count *uint64 `json:"count,omitempty" parquet:"count,optional"`
}

// Session is the host description of the session header. Its fields are
// required inside the object: a header that cannot say which boot or
// kernel it traced would make a cross-run comparison guess.
type Session struct {
	// BootID is /proc/sys/kernel/random/boot_id: boot_ns values of two
	// traces are comparable only when their boot ids are equal.
	BootID string `json:"boot_id" parquet:"boot_id"`
	// FQDN is the host's canonical name, or its hostname when it has
	// none.
	FQDN string `json:"fqdn" parquet:"fqdn"`
	// Kernel is the kernel release (uname -r).
	Kernel string `json:"kernel" parquet:"kernel"`
	// Arch is the machine hardware name (uname -m), e.g. x86_64.
	Arch string `json:"arch" parquet:"arch"`
}

// Java is the Java-side attribution of one event: the Java thread and
// the stack captured at the I/O boundary. Spike S1 settled this nested
// shape (docs/spikes/s1-java-join.md); a syscall the uprobe marked but
// no agent bracket contains stays attribution none.
type Java struct {
	Thread string `json:"thread,omitempty" parquet:"thread,optional"`
	// Stack lists method-table ids, innermost frame first.
	Stack []uint64 `json:"stack,omitempty" parquet:"stack,list"`
}

// HTTP describes one request on an http event. Provisional until M3.
type HTTP struct {
	Method string `json:"method,omitempty" parquet:"method,optional"`
	Scheme string `json:"scheme,omitempty" parquet:"scheme,optional"`
	Host   string `json:"host,omitempty" parquet:"host,optional"`
	Path   string `json:"path,omitempty" parquet:"path,optional"`
	Status uint16 `json:"status,omitempty" parquet:"status,optional"`
	// ReqSeq is the request ordinal on its connection (1-based).
	ReqSeq uint32 `json:"req_seq,omitempty" parquet:"req_seq,optional"`
}

// Sink receives finished events in seq order. The NDJSON and parquet
// writers implement it; the correlator only ever sees this interface, so
// it never knows which serialization it feeds.
type Sink interface {
	Event(Event) error
	Close() error
}
