package schema

// Event kinds. A kind is either the traced syscall's name or one of the
// synthetic kinds the correlator emits. The set only grows: M2 widens the
// traced surface by appending kinds here, and Validate rejects any kind not
// listed so a typo or a stale producer fails loudly instead of producing
// events no query matches.
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
	// KindDrop records ring-buffer loss: gaps are explicit, never silent.
	// args.lost counts the records lost since the previous drop event.
	KindDrop = "drop"
	// KindSession is the per-trace header record, the first event of a
	// trace; Event.Session describes the host.
	KindSession = "session"
	// KindEnd records why the trace ended (args.end_reason), so a reader
	// knows whether the trace is complete. It is the last event; a trace
	// without one was cut short (crash, or a write error that also lost
	// the end record).
	KindEnd = "end"
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
	// ConnID names the connection a socket event belongs to. Provisional
	// until S2: M1 assigns it locally for fused loopback pairs; the
	// cross-host join key the controller's merge assigns may differ.
	ConnID      string `json:"conn_id,omitempty" parquet:"conn_id,optional"`
	Attribution string `json:"attribution" parquet:"attribution,dict"`
	// Java is provisional until S1 reports; set exactly when
	// Attribution is AttributionJava.
	Java *Java `json:"java,omitempty" parquet:"java,optional"`
	// HTTP is provisional until S1 reports; present on http events (M3).
	HTTP *HTTP `json:"http,omitempty" parquet:"http,optional"`
	// Session describes the traced host; set exactly on session events.
	// A nested object rather than envelope fields because only one event
	// per trace carries it.
	Session *Session `json:"session,omitempty" parquet:"session,optional"`
}

// Args holds every syscall-specific argument as an optional field: one
// nullable parquet column per field, never a JSON blob, so queries can
// filter on any of them.
type Args struct {
	Path       *string `json:"path,omitempty" parquet:"path,optional"`
	NewPath    *string `json:"new_path,omitempty" parquet:"new_path,optional"`
	Flags      *uint32 `json:"flags,omitempty" parquet:"flags,optional"`
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
	// EndReason is an end event's reason, one of the End* constants.
	EndReason *string `json:"end_reason,omitempty" parquet:"end_reason,optional"`
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

// Java is the Java-side attribution of one event. Provisional: its shape
// is a placeholder until spike S1 settles how stacks join to syscalls.
// PLAN.md's sketch names the fields flat (java_thread, java_stack); they
// are nested here until S1 decides.
type Java struct {
	Thread string `json:"thread,omitempty" parquet:"thread,optional"`
	// Stack lists method-table ids, innermost frame first.
	Stack []uint64 `json:"stack,omitempty" parquet:"stack,list"`
}

// HTTP describes one request on an http event. Provisional until S1/M3.
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
