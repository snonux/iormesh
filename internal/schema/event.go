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
	// (pid 0), with one it says why (DropAttachGap, DropUnattributed,
	// DropSkippedRuns).
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
	// KindFork is a new process of a traced one (task_newtask without
	// CLONE_THREAD, M2 step 1), written on the child: pid,
	// pid_incarnation, tid and comm are the child's as it starts,
	// args.parent_pid and args.parent_incarnation the process that forked
	// it — absent when the record could not name it. It carries what the
	// graph's forked edge and the child's inherited fd table rest on, so
	// a graph rebuilt from the events (decision #3) has them too. Appended
	// in M4 (task ze4).
	KindFork = "fork"
	// KindAttach is a process the session took on as its target: a -pid
	// or port target at session start, or a process a -pattern session
	// matched (then args.pattern is the session's pattern, and the
	// process an incarnation of the app it names, AppNodeID). Its
	// identity is the one read when the session took it on. Appended in
	// M4 (task ze4).
	KindAttach = "attach"
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
	// DropSkippedRuns: program runs the kernel skipped under its recursion
	// guard (another BPF program running on that CPU) since the previous
	// such drop. Host-level (pid 0) and possible loss only: the skip comes
	// before the target filter, so args.lost counts untraced processes'
	// calls too and bounds the target records lost from above (task vd4).
	DropSkippedRuns = "skipped_runs"
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
	// HTTP is set exactly on http events (KindHTTP); frozen with M3's
	// acceptance.
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
	// Otel names the OpenTelemetry span the I/O ran in (decision #20, M3
	// step 8): the span context current on the Java thread when the
	// agent's bracket opened, or when an http event's request was
	// recorded. Only with attribution java, and only inside a span: an
	// event outside any span, or of an app without OTEL, has none.
	// Appended after StackID (task 4b4).
	Otel *Otel `json:"otel,omitempty" parquet:"otel,optional"`
	// DNS is set exactly on dns events (KindDNS): a host name the JVM
	// resolved and its addresses (decision #8). Appended after Otel (task
	// wd4); frozen with M3's acceptance.
	DNS *DNS `json:"dns,omitempty" parquet:"dns,optional"`
}

// Otel is an OpenTelemetry span context in W3C form, so an event joins
// the app's distributed trace in any OTEL backend by these ids.
type Otel struct {
	// TraceID is 32 lowercase hex digits, never all zero.
	TraceID string `json:"trace_id" parquet:"trace_id"`
	// SpanID is 16 lowercase hex digits, never all zero.
	SpanID string `json:"span_id" parquet:"span_id"`
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
	// SndUna, RcvNxt, BytesAcked and BytesReceived are the four tcp_sock
	// fields a tcp_state record carries from M4 on (spike S2, decision
	// #26): the controller's merge rebuilds the connection's TCP
	// initial-sequence-number pair from them — the cross-host join key,
	// which NAT never rewrites. tcp_state only, all four or none: a
	// partial set would rebuild a wrong key. Appended in M4 (task fb4).
	SndUna        *uint32 `json:"snd_una,omitempty" parquet:"snd_una,optional"`
	RcvNxt        *uint32 `json:"rcv_nxt,omitempty" parquet:"rcv_nxt,optional"`
	BytesAcked    *uint64 `json:"bytes_acked,omitempty" parquet:"bytes_acked,optional"`
	BytesReceived *uint64 `json:"bytes_received,omitempty" parquet:"bytes_received,optional"`
	// TCPSeq is the TCP sequence number of the first stream byte a send
	// queued or a receive returned (spike S2): the call moved bytes
	// [tcp_seq, tcp_seq+bytes) mod 2^32. A byte has the same sequence
	// number on both hosts, so the merge pairs each receive with the send
	// that queued its last byte and bounds the two hosts' clock offset by
	// the pair. Only on an unfolded data record of a TCP socket (write,
	// writev, sendto, sendfile64, read, readv, recvfrom) with a conn_id that moved bytes:
	// a folded record has lost its calls' instants. Appended in M4 (task
	// gb4); the tracer's capture (tcp_sendmsg/tcp_recvmsg hooks) is open.
	TCPSeq *uint32 `json:"tcp_seq,omitempty" parquet:"tcp_seq,optional"`
	// Pattern is the target pattern (decision #23) through which a
	// pattern-bound session took a process on: attach events only. With
	// the project it names the app (AppNodeID). Appended in M4 (task ze4).
	Pattern *string `json:"pattern,omitempty" parquet:"pattern,optional"`
	// ParentPID and ParentInc name the process that forked a fork
	// event's child: fork only, both or none. Appended in M4 (task ze4).
	ParentPID *uint32 `json:"parent_pid,omitempty" parquet:"parent_pid,optional"`
	ParentInc *uint64 `json:"parent_incarnation,omitempty" parquet:"parent_incarnation,optional"`
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
	// Thread is the Java thread's name (the agent's THREAD record); for
	// I/O of a virtual thread, the carrier's name (Carrier), or absent
	// when the carrier never named itself to the agent.
	Thread string `json:"thread,omitempty" parquet:"thread,optional"`
	// Stack lists method-table ids (methods.ndjson, Method), innermost
	// frame first.
	Stack []uint64 `json:"stack,omitempty" parquet:"stack,list"`
	// Carrier flags I/O a virtual thread issued (PLAN.md M3, "Platform
	// threads only in v1"): the stack is the virtual thread's, but v1
	// names only the carrier thread it ran on. Appended in M3 (task
	// 2b4); absent means a platform thread.
	Carrier bool `json:"carrier,omitempty" parquet:"carrier,optional"`
}

// HTTP describes one request on an http event (KindHTTP): what the Java
// agent's HTTP hooks saw at the library's API boundary (decision #22, M3
// step 7), so HTTPS is included without touching the wire. Frozen with
// M3's acceptance: fields are only appended.
type HTTP struct {
	Method string `json:"method,omitempty" parquet:"method,optional"`
	Scheme string `json:"scheme,omitempty" parquet:"scheme,optional"`
	// Host is the request URI's host, client side only: a server knows
	// the name it was asked for only from the Host header.
	Host string `json:"host,omitempty" parquet:"host,optional"`
	// Path is the request target, path and query, with every query value
	// masked ("/login?user=***"); path segments are verbatim (#22).
	Path   string `json:"path,omitempty" parquet:"path,optional"`
	Status uint16 `json:"status,omitempty" parquet:"status,optional"`
	// ReqSeq is the request ordinal on its connection (1-based): the
	// client's and the server's event of one request carry the same, so
	// the hops pair by (conn_id, req_seq). Only over HTTP/1 and only when
	// the connection is known and no request of the JVM was lost; absent
	// otherwise (HTTP/2 hops are time-ordered, decision #22).
	ReqSeq uint32 `json:"req_seq,omitempty" parquet:"req_seq,optional"`
	// Version is the protocol, "HTTP/1.1" or "HTTP/2". Appended in M3
	// (task 3b4), as are the headers.
	Version string `json:"version,omitempty" parquet:"version,optional"`
	// ReqHeaders and RespHeaders are the request's and the response's
	// headers as "name: value", names lower-cased, sorted by name, at
	// most 32 each; a denied name's value is masked ("authorization:
	// ***") and URLs in the others are redacted (decision #9).
	ReqHeaders  []string `json:"req_headers,omitempty" parquet:"req_headers,list"`
	RespHeaders []string `json:"resp_headers,omitempty" parquet:"resp_headers,list"`
	// ReqBytes and RespBytes are the request's and the response's body
	// sizes in bytes, as the library knew them (a declared Content-Length
	// it enforces, a publisher's length, or the bytes the app wrote);
	// absent when it did not (a chunked or streamed body) — never an
	// estimate. Appended in M3 (task wd4).
	ReqBytes  *uint64 `json:"req_bytes,omitempty" parquet:"req_bytes,optional"`
	RespBytes *uint64 `json:"resp_bytes,omitempty" parquet:"resp_bytes,optional"`
	// Held says the recording thread worked on this request alone from
	// its start to its completion: a synchronous library (the JDK's
	// HttpServer, a servlet's service, HttpURLConnection,
	// HttpClient.send, Apache's classic client, OkHttp's call chain).
	// Absent for Netty, whose event loop serves other channels inside a
	// request's span, and for an async completion. Only on a held span
	// may a view place the thread's other calls and file work (task mb4
	// review). Appended in M5.
	Held bool `json:"held,omitempty" parquet:"held,optional"`
}

// Sink receives finished events in seq order. The NDJSON and parquet
// writers implement it; the correlator only ever sees this interface, so
// it never knows which serialization it feeds.
//
// Event borrows ev for the call only: a sink that keeps the event copies
// it — what it points to too: the caller may change or reuse ev and the
// objects it points to (the Java join reuses one java and one otel
// object for every joined event, task ee4) once Event returns. A pointer,
// not a value, because an Event is some 400 bytes and each hop of the
// tracer's pipeline (correlator → Java join → writer) copied it once per
// event by value (decision #25's budget, task ud4).
type Sink interface {
	Event(ev *Event) error
	Close() error
}
