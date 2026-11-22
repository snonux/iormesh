// Adapted from ior internal/c/types.h @ b21afc50328c.
// Changes: TCP_STATE_EVENT and struct tcp_state_event added (sock.c);
// SOCK_ADDR_EVENT and SIGNAL_EVENT with their structs added (sockaddr.c,
// signal.c, M2 step 7); RW_AGGREGATE_EVENT and struct rw_aggregate_event
// added (rwagg.c, M2 step 11); STACK_EVENT and struct stack_event added
// (stack.c, M2 step 9); STACK_INLINE_EVENT, struct stack_inline_event and
// IORMESH_STACK_DEPTH (moved here from maps.h) added (stack.c, task kd4);
// EXEC_ARGV_EVENT, PROCESS_ARGV_EVENT and struct exec_argv_event added
// (exec.c, M2); JAVA_MARK_EVENT and struct java_mark_event added
// (javamark.c, task td4); TCP_SEQ_EVENT and struct tcp_seq_event added
// (tcpseq.c, task xe4); SYSCALL_RESTART_EVENT's comments say no program
// emits it (task pf4); TCP_STATE_FINAL added (sock.c, task sf4);
// task_rename_event's comment names its consumer (task 0g4); comments that
// cited ior's Go files name them as ior's (task gj4).

//+build ignore

#define MAX_FILENAME_LENGTH 256
#define MAX_PROGNAME_LENGTH 16

#define ENTER_OPEN_EVENT 1
#define EXIT_OPEN_EVENT 2
#define ENTER_NULL_EVENT 3
#define EXIT_NULL_EVENT 4
#define ENTER_FD_EVENT 5
#define EXIT_FD_EVENT 6
#define ENTER_RET_EVENT 7
#define EXIT_RET_EVENT 8
#define ENTER_NAME_EVENT 9
#define EXIT_NAME_EVENT 10
#define ENTER_PATH_EVENT 11
#define EXIT_PATH_EVENT 12
#define ENTER_FCNTL_EVENT 13
#define EXIT_FCNTL_EVENT 14
#define ENTER_DUP3_EVENT 15
#define EXIT_DUP3_EVENT 16
#define ENTER_OPEN_BY_HANDLE_AT_EVENT 17
#define EXIT_OPEN_BY_HANDLE_AT_EVENT 18
#define ENTER_SOCKET_EVENT 19
#define EXIT_SOCKET_EVENT 20
#define ENTER_SOCKETPAIR_EVENT 21
#define EXIT_SOCKETPAIR_EVENT 22
#define ENTER_ACCEPT_EVENT 23
#define EXIT_ACCEPT_EVENT 24
#define ENTER_PIPE_EVENT 25
#define EXIT_PIPE_EVENT 26
#define ENTER_EVENTFD_EVENT 27
#define EXIT_EVENTFD_EVENT 28
#define ENTER_EPOLL_CTL_EVENT 29
#define EXIT_EPOLL_CTL_EVENT 30
#define ENTER_POLL_EVENT 31
#define EXIT_POLL_EVENT 32
#define ENTER_MEM_EVENT 33
#define EXIT_MEM_EVENT 34
#define ENTER_SLEEP_EVENT 35
#define EXIT_SLEEP_EVENT 36
#define ENTER_TWO_FD_EVENT 37
#define EXIT_TWO_FD_EVENT 38
#define ENTER_KEYCTL_EVENT 39
#define EXIT_KEYCTL_EVENT 40
#define ENTER_PTRACE_EVENT 41
#define EXIT_PTRACE_EVENT 42
#define ENTER_PERF_OPEN_EVENT 43
#define EXIT_PERF_OPEN_EVENT 44
#define ENTER_EXEC_EVENT 45
#define EXIT_EXEC_EVENT 46
#define PROCESS_EXEC_EVENT 47
#define OPEN_NAME_FIXUP_EVENT 48
#define PROCESS_EXIT_EVENT 49
#define ENTER_MMAP_EVENT 50
#define EXIT_MMAP_EVENT 51
#define ENTER_BPF_EVENT 52
#define EXIT_BPF_EVENT 53
#define ENTER_FD_PATH_EVENT 54
#define EXIT_FD_PATH_EVENT 55
// The rare payloads of fd_event, two_fd_event and eventfd_event travel in
// their own records so that the hot kinds stay lean (task 89). Only the enter
// side exists: the exits of these syscalls are ret_event or the lean
// eventfd_event.
#define ENTER_FD_SIZE_EVENT 56
#define EXIT_FD_SIZE_EVENT 57
#define ENTER_TWO_FD_NAMES_EVENT 58
#define EXIT_TWO_FD_NAMES_EVENT 59
#define ENTER_EVENTFD_NAME_EVENT 60
#define EXIT_EVENTFD_NAME_EVENT 61
// Control record of the hand-written task:task_newtask handler (exec.c).
#define TASK_NEWTASK_EVENT 62
// Control record of the hand-written task:task_rename handler (exec.c).
#define TASK_RENAME_EVENT 63
// Control record of ior's restart-fold probes (ior internal/c/restart.c):
// what the kernel does with a call a signal interrupted. iormesh does not
// lift the fold; no program emits it (task pf4).
#define SYSCALL_RESTART_EVENT 64
// Control record carrying the file handle a successful name_to_handle_at
// returned (ior_emit_file_handle, handle.c).
#define FILE_HANDLE_EVENT 65
// close's enter record when the file it releases has a name to report:
// fd_event plus the last path component (ior_emit_fd_name_enter, fdname.c).
// Only the enter side exists; the exit of close is a ret_event.
#define ENTER_FD_NAME_EVENT 66
// Control record carrying the registered-ring table entries a successful
// io_uring_register(IORING_REGISTER_RING_FDS / IORING_UNREGISTER_RING_FDS)
// set or released (ior_emit_ring_fds, iouring.c). 66 is ENTER_FD_NAME_EVENT;
// the ids only have to be distinct, not dense.
#define RING_FDS_EVENT 67
// iormesh: a TCP socket's 4-tuple and state, from sock:inet_sock_set_state
// or from the accept exit (struct tcp_state_event, sock.c).
#define TCP_STATE_EVENT 68
// iormesh: an exec of an untraced process in a pattern session, carrying the
// executable path and argv the target pattern is matched against
// (struct exec_argv_event, exec.c).
#define EXEC_ARGV_EVENT 69
// iormesh: the exe and argv of an exec of a traced process, recorded
// redacted in the trace (decision #9; struct exec_argv_event, exec.c). The
// same struct as EXEC_ARGV_EVENT under its own type, so userspace never
// mistakes a traced exec for one awaiting the pattern's verdict.
#define PROCESS_ARGV_EVENT 70
// iormesh: the addresses of a bind, connect, sendto, recvfrom or AF_UNIX
// accept, sent by the call's exit hook right before its exit record
// (struct sock_addr_event, sockaddr.c, M2 step 7).
#define SOCK_ADDR_EVENT 71
// iormesh: a signal generated by or for a traced process, from
// signal:signal_generate (struct signal_event, signal.c, M2 step 7).
#define SIGNAL_EVENT 72
// iormesh: hot read or write calls of one thread on one fd folded into one
// record (struct rw_aggregate_event, rwagg.c, M2 step 11, decision #25).
#define RW_AGGREGATE_EVENT 73
// iormesh: the native user stack of a syscall enter, sent right before the
// enter record when native stacks are on (struct stack_event, stack.c, M2
// step 9).
#define STACK_EVENT 74
// iormesh: the same, with the stack's frames inline, for an enter whose
// stack stack_map could not take (a colliding bucket, a full map; struct
// stack_inline_event, stack.c).
#define STACK_INLINE_EVENT 75
// iormesh: the JDK native a traced thread was inside when it entered a
// syscall, sent right before the enter record when the Java mark is on
// (struct java_mark_event, javamark.c, M3 "Join to kernel events", task td4).
#define JAVA_MARK_EVENT 76
// iormesh: the exit record of a single TCP send or receive with the TCP
// sequence number of its first byte, sent by the call's exit hook in place
// of its ret_event (struct tcp_seq_event, tcpseq.c, M4, task xe4).
#define TCP_SEQ_EVENT 77

#define UNCLASSIFIED 0
#define READ_CLASSIFIED 1
#define WRITE_CLASSIFIED 2
#define TRANSFER_CLASSIFIED 3

// Status of one pathname pointer captured with bpf_probe_read_user_str.
// A valid empty string is PATH_READ_OK; PATH_READ_NULL records an actual NULL
// syscall argument, and PATH_READ_FAILED records a non-NULL pointer the nofault
// helper could not read. Userspace may attribute an empty OK/NULL value to a
// concrete dirfd only when that exact syscall, side, flags, and result permit
// descriptor semantics; it must never do so for FAILED.
#define PATH_READ_OK 0
#define PATH_READ_NULL 1
#define PATH_READ_FAILED 2

// Whether a successful path syscall necessarily validated its target. Most
// path syscalls always do. utimensat is exceptional: two UTIME_OMIT values
// return success before validating the path, dirfd, or flags. UNKNOWN records
// a non-NULL timespec array that the enter probe could not safely read.
#define PATH_TARGET_REQUIRED 0
#define PATH_TARGET_SKIPPED 1
#define PATH_TARGET_UNKNOWN 2
#define IOR_UTIME_OMIT 1073741822

// The first open_event schema ended after comm and occupied 300 bytes before
// C tail padding (304 bytes in the ring buffer). Keep that field prefix stable
// and append new fields so userspace can distinguish legacy payloads by size.
// dirfd alone would still collide with the legacy 304-byte kernel layout. The
// schema version, filename status and reserved word make the final v3 layout
// 320 bytes. path_event v4 appends requested-size metadata to its v3
// 300/304-byte layout. Intermediate development layouts were never released
// and are not part of the decoder compatibility contract.
//
// fd_size_event is the fd_event v1 layout (the legacy 28/32-byte fd_event plus
// requested-size metadata) under its own event type; fd_event itself is the
// legacy layout again. eventfd_name_event is the eventfd_event v2 layout and
// eventfd_event the name-less 48-byte layout that preceded it. Older BPF
// objects still send the wide layouts under ENTER_FD_EVENT/ENTER_EVENTFD_EVENT;
// userspace tells them apart by size.
#define OPEN_EVENT_SCHEMA_VERSION 3
#define FD_SIZE_EVENT_SCHEMA_VERSION 1
// Compatibility names for userspace decoders of records from older BPF objects.
#define FD_EVENT_SCHEMA_VERSION 1
#define PATH_EVENT_SCHEMA_VERSION 4
#define NAME_EVENT_SCHEMA_VERSION 2
#define EVENTFD_NAME_EVENT_SCHEMA_VERSION 2
#define EVENTFD_EVENT_SCHEMA_VERSION 2
// accept_event v1 appends flags and a discriminator after ret. Keeping the
// legacy prefix intact lets userspace decode old 36/40-byte payloads without
// mistaking the old kernel padding before ret for creation flags.
#define ACCEPT_EVENT_SCHEMA_VERSION 1
// Schema 2 predates the kcmp pid1/type packing used for safe file attribution.
// Schema 3 is shared by the lean two_fd_event (close_range, kcmp) and by
// two_fd_names_event (move_mount), which is the former 568-byte two_fd_event
// layout under its own event type.
#define TWO_FD_EVENT_PRE_KCMP_OWNER_SCHEMA_VERSION 2
#define TWO_FD_EVENT_SCHEMA_VERSION 3
#define FD_PATH_EVENT_SCHEMA_VERSION 1
// poll_event v1 appends the optional descriptor and a schema discriminator to
// the legacy nfds/timeout prefix. Timeout sentinels are part of the exported
// stream contract: -1 means an intentional infinite wait, while -2 means the
// timeout was unreadable, invalid, unrepresentable in nanoseconds, or has no
// capture recipe.
#define POLL_EVENT_SCHEMA_VERSION 1
// exec_event v1 appends the filename read status and a schema discriminator
// to the legacy 304-byte layout (task 9p2), for 312 bytes with no padding.
// Without the status an unreadable execveat name was indistinguishable from
// the "" of an AT_EMPTY_PATH fexecve, so userspace had to guess. Legacy
// 304-byte records still decode, with the status reported as PATH_READ_OK.
#define EXEC_EVENT_SCHEMA_VERSION 1
#define POLL_TIMEOUT_INFINITE_NS -1
#define POLL_TIMEOUT_UNKNOWN_NS -2

struct open_event {
    __u32 event_type;
    __u32 trace_id; 
    __u64 time;
    __u32 pid;
    __u32 tid;
    __s32 flags;
    char filename[MAX_FILENAME_LENGTH];
    char comm[MAX_PROGNAME_LENGTH];
    __s32 dirfd;
    __u32 schema_version;
    __u32 filename_status;
    __u32 schema_reserved;
};

// Control record carrying a pathname re-read at sys_exit (see "Recovering a
// path whose sys_enter read faulted" in filter.c). slot says which of the
// enter event's path fields it belongs to: OPEN_NAME_FIXUP_SLOT_FIRST for the
// only (or first) path - filename, pathname, oldname - and
// OPEN_NAME_FIXUP_SLOT_SECOND for the newname of the rename/link family and
// move_mount (whose from/to pathnames travel as oldname/newname). slot
// trails the string so the 268-byte prefix (tid at 8, filename at 12) stays
// what older readers decoded; a record of that size reads as slot FIRST.
#define OPEN_NAME_FIXUP_SLOT_FIRST 0
#define OPEN_NAME_FIXUP_SLOT_SECOND 1
struct open_name_fixup_event {
    __u32 event_type;
    __u32 trace_id;
    __u32 tid;
    char filename[MAX_FILENAME_LENGTH];
    __u32 slot;
};

struct exec_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __s32 dirfd;
    __s32 flags;
    char filename[MAX_FILENAME_LENGTH];
    char comm[MAX_PROGNAME_LENGTH];
    __u32 filename_status;
    __u32 schema_version;
};

struct null_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
};

// fd_event is the hot single-descriptor record (read, write, close, ...): 32
// bytes in the ring buffer. It has no schema field; its layout is the legacy
// one, which userspace has always decoded. A close of a file that has a last
// path component is sent as the wider fd_name_event instead (below).
//
// file_ident says which file fd named when the call entered: the low 32 bits
// of its inode number, or 0 for "unknown" (ior_file_ident in fileident.c,
// task 603). It occupies what was the tail padding of the 28-byte record, so
// the size did not change and cannot tell an older object's record apart:
// that one leaves stale ring-buffer bytes there. Userspace therefore reads
// the word only in a run whose object has the IOR_FILE_IDENT global and had
// it switched on (flags.h).
struct fd_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __s32 fd;
    __u32 file_ident;
};

// fd_size_event is fd_event plus the capacity of the caller's output buffer,
// captured for the syscalls whose return value is not by itself a count of
// bytes copied:
//   - the fd-based xattr reads (fgetxattr, flistxattr), whose zero-size call
//     is a size probe returning the required capacity;
//   - recvfrom and recvmsg, where MSG_TRUNC makes the return the datagram's
//     real length rather than the bytes copied into the buffer, and MSG_PEEK
//     copies without consuming.
// flags occupies what was the alignment padding after fd, so the record stays
// 48 bytes. It carries the recv flags argument for recvfrom/recvmsg and is 0
// for every other user; older BPF objects left those four bytes as padding, and
// userspace only interprets flags for the recv syscalls, which older objects
// never sent in this record (they used the size-less fd_event).
struct fd_size_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __s32 fd;
    __u32 flags;
    __u64 size;
    __u32 size_valid;
    __u32 schema_version;
};

// ret_event is the exit record of most syscalls. file_ident is, for the exits
// of the open kinds, the identity of the file behind the descriptor the call
// returned (ior_file_ident_of_ret in fileident.c; 0 = unknown, as in
// fd_event), so that the fd table entry userspace registers knows which file
// it stands for. Every other exit writes 0. Like fd_event's, the word took
// the place of the tail padding of the 36-byte record and is only read in a
// run that switched IOR_FILE_IDENT on.
struct ret_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __s64 ret;
    __u32 pid;
    __u32 tid;
    __u32 ret_type;
    __u32 file_ident;
};

struct name_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    char oldname[MAX_FILENAME_LENGTH];
    char newname[MAX_FILENAME_LENGTH];
    __s32 olddirfd;
    __s32 newdirfd;
    __u32 oldname_status;
    __u32 newname_status;
    __u32 flags;
    __u32 schema_version;
};

struct path_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    char pathname[MAX_FILENAME_LENGTH];
    __s32 dirfd;
    __u32 pathname_status;
    __u32 flags;
    __u32 schema_version;
    __u32 target_status;
    __u32 size_valid;
    __u64 size;
};

// Notification group fd and watched pathname are different identities. Keep
// this separate from path_event so older BPF objects retain their old layouts.
struct fd_path_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __s32 fd;
    __s32 dirfd;
    char pathname[MAX_FILENAME_LENGTH];
    __u32 pathname_status;
    __u32 flags;
    __u32 schema_version;
};

struct fcntl_event {
    __u32 event_type;
    __u32 trace_id; 
    __u64 time;
    __u32 pid;
    __u32 tid;
    __u32 fd;
    __u32 cmd;
    __u64 arg;
};

// dup and dup2 are just fd_events, but dup3 also has the additional flags.
//
// file_ident is fd_event's identity word for the old descriptor: which file
// fd named when the call entered (ior_file_ident in fileident.c, task d23),
// 0 = unknown. dup3 copies the fd table entry of fd to the new number, so
// userspace checks that entry against the word exactly as it does for dup
// and dup2. The 32-byte record had no padding to take, so it grew to 40
// (36 bytes of fields plus tail padding); an older object's 32-byte record
// decodes with identity 0. As for fd_event, userspace reads the word only
// in a run that switched IOR_FILE_IDENT on (flags.h).
struct dup3_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __s32 fd;
    __s32 flags;
    __u32 file_ident;
};

// A struct file_handle as the handle syscalls pass it: handle_bytes says how
// many bytes of f_handle are the handle, handle_type how the filesystem encoded
// them, and the two together are what the kernel resolves to a file.
// IOR_MAX_HANDLE_SZ is the kernel's MAX_HANDLE_SZ: name_to_handle_at never
// returns a longer handle and open_by_handle_at rejects one with EINVAL.
//
// handle_status says what the record's handle fields hold:
//   - FILE_HANDLE_NONE: nothing; the record predates the capture (the value an
//     older, shorter record decodes with).
//   - FILE_HANDLE_OK: handle_bytes (0..IOR_MAX_HANDLE_SZ) and handle_type are
//     the caller's, and the first handle_bytes bytes of f_handle are the
//     handle. A zero handle_bytes is reported as it is; no file has such a
//     handle and the kernel rejects it.
//   - FILE_HANDLE_NULL: the handle pointer was NULL.
//   - FILE_HANDLE_READ_FAILED: the nofault read of the header or of the bytes
//     failed (the page is not resident). handle_bytes and handle_type are the
//     header's when only the bytes could not be read, else 0.
//   - FILE_HANDLE_TOO_LARGE: handle_bytes (reported) exceeds
//     IOR_MAX_HANDLE_SZ, so no bytes were read.
//
// Only FILE_HANDLE_OK identifies a handle; userspace treats every other status
// as "handle unknown". f_handle is zero-filled before the read, so a record
// holds the handle and zeros and never stale ring-buffer bytes: these syscalls
// are rare, and the bytes are binary, so there is no terminator a reader could
// stop at (compare "String fields in ring-buffer records" in filter.c).
#define IOR_MAX_HANDLE_SZ 128
#define FILE_HANDLE_NONE 0
#define FILE_HANDLE_OK 1
#define FILE_HANDLE_NULL 2
#define FILE_HANDLE_READ_FAILED 3
#define FILE_HANDLE_TOO_LARGE 4

// The handle fields were appended (task k03): handle_status sits in what was
// the implicit tail pad of the 32-byte record, which an older object leaves
// unwritten, so a record of that size is decoded by its length as
// FILE_HANDLE_NONE rather than by that word. The layout has no implicit
// padding: 168 bytes for the kernel record and a binary.Write payload alike.
struct open_by_handle_at_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __s32 flags;
    __u32 handle_status;
    __u32 handle_bytes;
    __s32 handle_type;
    __u8 f_handle[IOR_MAX_HANDLE_SZ];
};

// file_handle_event is a control record, not a syscall event: the exit handler
// of name_to_handle_at emits it after a successful return, with the handle the
// kernel wrote into the caller's buffer, and reserves it before the exit
// record of the same call (ior_emit_file_handle, handle.c). ior's userspace
// files the pathname of the still-pending enter under that handle, so that an
// open_by_handle_at carrying the same handle - on any thread or process - can
// be named (handleFileHandleEvent, ior internal/eventloop_handle.go); iormesh
// decodes the record but does not use it yet. It is never rendered as a row.
//
// trace_id is the ENTER trace ID of the call (SYS_ENTER_NAME_TO_HANDLE_AT).
// The record names both ends of its call by their clock reads, because
// userspace must not pair it by position:
//   - time is the exit handler's single clock read and therefore equals the
//     time of the exit record that follows, bit for bit; userspace accepts the
//     handle only for an exit with that time, so a record whose exit was lost
//     is not claimed by a later call of the tid.
//   - enter_time is the start_ns of the enter state the exit handler found,
//     i.e. the time of the call's own enter record (the enter handler stamps
//     both with one clock read). Userspace accepts the record only while the
//     enter it has pending for the tid carries that time: the pathname it
//     files is the pending enter's, and that enter is an EARLIER call's when
//     that call's exit record and this call's enter record were both lost (or
//     the enter was shed by the raw path filter).
// Both checks compare clock reads of one tid that have at least a syscall
// entry or exit between them; a clock too coarse to tell those apart could
// make two calls look alike, which is accepted.
//
// Only a handle that was read completely is submitted, so handle_status is
// always FILE_HANDLE_OK; the word is kept so that the handle fields sit at
// the offsets they have in open_by_handle_at_event (reserved is where that
// record has flags, and is 0). enter_time was appended behind them for the
// same reason. No implicit padding, 176 bytes.
struct file_handle_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __u32 reserved;
    __u32 handle_status;
    __u32 handle_bytes;
    __s32 handle_type;
    __u8 f_handle[IOR_MAX_HANDLE_SZ];
    __u64 enter_time;
};

// The two io_uring_register opcodes that change a thread's registered-ring
// table (<linux/io_uring.h>: IORING_REGISTER_RING_FDS and
// IORING_UNREGISTER_RING_FDS), the table's size (IO_RINGFD_REG_MAX, the most
// entries one call takes) and the size of one array element, struct
// io_uring_rsrc_update {__u32 offset; __u32 resv; __u64 data;}. All four are
// syscall ABI.
#define IOR_REGISTER_RING_FDS 20
#define IOR_UNREGISTER_RING_FDS 21
#define IOR_RING_FDS_MAX 16
#define IOR_RING_FD_UPDATE_SIZE 16
#define IOR_RING_FDS_BYTES 256

// Status of the array read of a ring_fds_event:
//   - RING_FDS_OK: updates holds count entries.
//   - RING_FDS_READ_FAILED: the nofault read of the array failed.
//   - RING_FDS_TOO_MANY: the call returned more entries than the table has,
//     which these opcodes cannot; nothing was read.
// For both failures count is 0, and userspace forgets the thread's table.
#define RING_FDS_OK 1
#define RING_FDS_READ_FAILED 2
#define RING_FDS_TOO_MANY 3

// ring_fds_event is a control record, not a syscall event: the exit handler
// of io_uring_register emits it for the two opcodes above after a return
// that processed at least one entry, ahead of the call's exit record and
// whether or not the sampling rate emits that one (ior_emit_ring_fds,
// iouring.c). It is never rendered as a row; ior's userspace applies it to
// the registered-ring table of the thread tid (handleRingFdsEvent,
// ior internal/eventloop_ringfds.go); iormesh decodes the record but does
// not use it yet.
//
// trace_id is the ENTER trace ID of the call (SYS_ENTER_IO_URING_REGISTER),
// opcode the io_uring_register opcode without the
// IORING_REGISTER_USE_REGISTERED_RING bit. time is the exit handler's single
// clock read and therefore equals the time of the exit record that follows,
// bit for bit: an exit of such a call without a record of that time tells
// userspace that the record was lost.
//
// updates holds the first count elements of the caller's array as the kernel
// left them, IOR_RING_FD_UPDATE_SIZE bytes each in host byte order: offset
// (the index, after IORING_REGISTER_RING_FDS the one the kernel allocated),
// resv, and data (the ring descriptor of a registration, 0 of a release).
// The bytes past them are zeros. No implicit padding, 296 bytes.
struct ring_fds_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __u32 opcode;
    __u32 status;
    __u32 count;
    __u32 reserved;
    __u8 updates[IOR_RING_FDS_BYTES];
};

// fd_name_event is fd_event plus the last path component of the file the
// descriptor names while the call enters. The enter handler of close sends
// it instead of an fd_event when that file has such a name
// (ior_emit_fd_name_enter in fdname.c, task xz2): userspace cannot ask procfs
// about a descriptor that is closed by the time its row is processed, so the
// close of a descriptor ior never saw opened had no name at all
// (ior internal/eventloop_procfs_close.go). iormesh leaves IOR_FILE_IDENT
// off (flags.h), so its runs send none. Every other close - a pipe, a
// socket, an anonymous-inode file, the root of a mount (the root directory
// of a filesystem, a mounted subvolume, a bind-mounted directory or single
// file: its dentry is named after its source, not after what was opened),
// any close on a kernel that cannot run the walk or in a run that switched
// IOR_FILE_IDENT off - is the plain 32-byte fd_event as before.
//
// The first 32 bytes are fd_event's, file_ident included, so userspace
// decodes the record into the same event and handles the pair as any close.
// name is the dentry's own name, NUL-terminated and cut to
// IOR_FD_NAME_LENGTH - 1 bytes (the bytes after the NUL are stale ring-buffer
// memory, see "String fields in ring-buffer records" in filter.c); name_len
// is its real length, so a value of IOR_FD_NAME_LENGTH or more says the name
// was cut - provided the text also fills the field. 0 says there is no name
// after all (the read failed). The name can hold an earlier NUL than
// name_len says when the close raced a rename of its file (fdname.c);
// userspace stops at the first one and then does not take the name for a cut
// one, whatever the length (FdEvent.LeafName). The
// length is a compromise: every close of a file below a directory pays for
// the record, a component may be 255 bytes long, and 67 hold a 64-digit
// content hash with a short suffix. The layout has no implicit padding, so
// the kernel record and a binary.Write payload share one size, 104 bytes.
#define IOR_FD_NAME_LENGTH 68
struct fd_name_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __s32 fd;
    __u32 file_ident;
    __u32 name_len;
    char name[IOR_FD_NAME_LENGTH];
};

struct socket_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __s32 family;
    __s32 type;
    __s32 protocol;
};

struct socketpair_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __s32 family;
    __s32 type;
    __s32 protocol;
    __s32 sv0;
    __s32 sv1;
    __s64 ret;
};

struct accept_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __s32 fd;
    __s64 ret;
    __s32 flags;
    __u32 schema_version;
};

struct pipe_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __s32 flags;
    __s32 fd0;
    __s32 fd1;
    __s64 ret;
};

// eventfd_event serves the descriptor-creating calls without an identifying
// name (eventfd2, epoll_create1, signalfd4, timerfd_create, pidfd_open, ...)
// and every exit of the family: 48 bytes in the ring buffer.
struct eventfd_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __s32 flags;
    __s64 ret;
    __s32 fd;
};

// eventfd_name_event is the enter record of memfd_create and fsopen, the two
// calls whose descriptor is identified by a user-supplied name.
struct eventfd_name_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __s32 flags;
    __s64 ret;
    __s32 fd;
    char filename[MAX_FILENAME_LENGTH];
    __u32 filename_status;
    __u32 schema_version;
};

struct epoll_ctl_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __s32 epfd;
    __s32 op;
    __s32 fd;
    __u32 events;
};

struct poll_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __s32 nfds;
    __s64 timeout_ns;
    __s32 fd;
    __u32 schema_version;
};

struct mem_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __u64 addr;
    __u64 length;
    __u64 length2;
    __u64 flags;
};

struct mmap_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __u64 addr;
    __u64 length;
    __u64 prot;
    __u64 flags;
    __s32 fd;
};

struct sleep_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __s64 requested_ns;
};

// two_fd_event carries two descriptor arguments and a third word (close_range
// first/last/flags, kcmp's KCMP_FILE indices and packed owner/type): 48 bytes
// in the ring buffer.
struct two_fd_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __s32 fd_a;
    __s32 fd_b;
    __u64 extra;
    __u32 schema_version;
};

// two_fd_names_event is two_fd_event plus the two pathnames only move_mount
// passes alongside its descriptors.
struct two_fd_names_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __s32 fd_a;
    __s32 fd_b;
    __u64 extra;
    char oldname[MAX_FILENAME_LENGTH];
    char newname[MAX_FILENAME_LENGTH];
    __u32 oldname_status;
    __u32 newname_status;
    __u32 schema_version;
};

struct bpf_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __u32 cmd;
};

struct keyctl_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __s32 option;
    __s32 key_serial;
    __u64 value;
};

struct ptrace_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __s64 request;
    __s32 target_pid;
    __s32 _pad;
    __u64 data;
};

struct perf_open_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __u32 attr_type;
    __u32 attr_size;
    __u64 config;
    __s32 target_pid;
    __s32 cpu;
    __s32 group_fd;
    __u32 flags;
};

// process_exec_event is not a syscall tracepoint event: it is emitted by the
// hand-written sched:sched_process_exec handler in exec.c, which fires after
// the kernel has already installed the new program's name in task->comm but
// before the new program executes its first syscall. Userspace consumes it as
// a control event (no enter/exit pair, never rendered as a row) that refreshes
// the pid->comm cache, so the first post-exec syscalls are labelled with the
// post-exec comm instead of the pre-exec one.
//
// old_tid is the tid the exec'ing thread had before the exec (the
// tracepoint's old_pid). It differs from tid when a non-leader thread
// exec'd: de_thread() hands it the leader's tid (== pid), so the execve that
// entered under old_tid returns under tid. Userspace re-keys the parked
// execve enter from old_tid to tid on this record so the exit still pairs
// (eventLoop.rekeyExecCaller); the BPF side moves the syscall_enter_state_map
// entry the same way (ior_on_exec_tid_change in filter.c).
//
// exit_untraced (0 or 1; a __u32 because the Go type generator maps only
// 32/64-bit integers) is set when the execve's sys_exit record will never
// arrive: only the pre-exec caller was in scope (-tid traced a non-leader
// thread, whose post-exec (leader) tid the filter rejects), or the exec
// loaded a 32-bit program, whose syscalls the classic tracepoints ignore
// (ior_exec_into_compat in exec.c). Userspace then
// completes the parked execve enter from this record
// (eventLoop.completeUntracedExec). The word occupies what used to be an
// explicit tail pad, so the layout stays 48 bytes with no implicit padding
// and kernel and binary.Write payloads share one size.
struct process_exec_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    char comm[MAX_PROGNAME_LENGTH];
    __u32 old_tid;
    __u32 exit_untraced;
};

// process_exit_event is not a syscall tracepoint event: it is emitted by the
// hand-written sched:sched_process_exit handler in exec.c, which fires when a
// task (thread) exits. Userspace consumes it as a control event (no enter/exit
// pair, never rendered as a row): tid-keyed state is dropped on every record,
// while the process's (tgid) fdTracker entries are evicted only when
// group_dead is set, i.e. when the last thread of the group has exited - a
// sibling thread's exit must not discard descriptors the process still holds.
// group_dead is a __u32 (0 or 1) because the Go type generator maps only
// 32/64-bit integers; the explicit exit_flags word keeps the layout at 32 bytes
// with no implicit padding, so kernel and binary.Write payloads share one size.
// exit_flags bit 0 (IOR_EXIT_TID_INHERITED, exec.c) marks the exit of a
// thread-group leader that another thread's execve killed in de_thread(): the
// exec'ing thread takes over the leader's tid (and start time), so the tid
// lives on in the new program although this task died. The word was the
// always-zero "reserved" before, so an older object reads as "tid gone".
// Like process_exec_event's siblings it carries no comm: the only payload
// userspace needs is the identity of the task and whether its process died.
struct process_exit_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __u32 group_dead;
    __u32 exit_flags;
};

// task_newtask_event is not a syscall tracepoint event: it is emitted by the
// hand-written task:task_newtask handler in exec.c when the kernel creates a
// task (fork, vfork, clone or clone3; a new thread is a task too). Userspace
// consumes it as a control event (no enter/exit pair, never rendered as a
// row) that seeds the tid->comm cache with the name the child inherited from
// its parent, before the child's first syscall. The seed is provisional: a
// thread that renames itself is corrected by one later /proc read.
//
// Why: a task's comm was otherwise resolved lazily and asynchronously from
// /proc/<tid>/comm, which loses the race against short-lived tasks (the
// thread has exited before the read, so its rows carry an empty comm) and,
// under -comm, gave the first rows of every new tid whose name was not cached
// yet an empty comm, which the exit-side comm check drops.
//
// pid is the *child's* thread-group id and tid the child's task id: the
// record is emitted from the parent's context, but describes the child.
// clone_flags is the raw flag word of the creating clone (CLONE_THREAD tells
// a new thread from a new process; CLONE_VM/CLONE_FILES tell which resources
// are shared - the basis for fd-table inheritance and shared-table tracking).
// creator_pid is the tgid of the task that called clone (the handler's own
// context): the process whose descriptor table a fork()ed child starts from,
// which the child's pid/tid alone cannot name. A record of a pre-creator_pid
// IOR_BPF_OBJECT is 48 bytes long and decodes with creator_pid 0 ("unknown").
// The layout has no implicit padding (clone_flags starts at offset 40 and the
// explicit scope_flags word completes the trailing 8-byte alignment), so the
// kernel record and a binary.Write payload share one size, 56 bytes.
// scope_flags bit 0 (IOR_NEWTASK_CHILD_OUT_OF_SCOPE) marks the one record
// emitted for a child the PID/TID filter excludes: a CLONE_FILES process child
// of an in-scope creator, whose invisible descriptor-table writes change the
// creator's table. The word was the always-zero "reserved" before, so an older
// object reads as "child in scope".
struct task_newtask_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    char comm[MAX_PROGNAME_LENGTH];
    __u64 clone_flags;
    __u32 creator_pid;
    __u32 scope_flags;
};

// task_rename_event is a control record, not a syscall event: the hand-written
// task:task_rename handler in exec.c emits it whenever the kernel changes a
// task's comm (prctl(PR_SET_NAME), pthread_setname_np, a /proc/<tid>/comm
// write, and the exec's own rename). The correlator renames the process when
// tid is the pid (renameTask, task 0g4); it is never an event of its own.
//
// pid is the renamed task's thread-group id and tid its task id - the
// *renamed* task, which a /proc/<tid>/comm write makes different from the
// task that emitted the record. comm is the new name, NUL-terminated by the
// handler (bytes after the NUL are stale ring-buffer memory, see "String
// fields in ring-buffer records" in filter.c). The layout has no implicit
// padding, so the kernel record and a binary.Write payload share one size,
// 40 bytes.
struct task_rename_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    char comm[MAX_PROGNAME_LENGTH];
};

// Phase of a syscall_restart_event (see there).
#define RESTART_PHASE_HANDLER 1
#define RESTART_PHASE_RESUME 2

// syscall_restart_event is kept from ior so the generated types and the
// decoder tables stay ior's, but no iormesh program emits it: the restart
// fold was not lifted (task pf4, docs/implementation.md, M1 step 6). In ior
// it is a control record, not a syscall event: restart.c emits it for a
// task whose last traced syscall exit carried -ERESTARTSYS, -ERESTARTNOINTR, -ERESTARTNOHAND or -ERESTART_RESTARTBLOCK
// (-512/-513/-514/-516), so userspace can fold the interrupted row and the
// kernel's continuation of the call - its re-execution, or for -516 its
// restart_syscall - into one row (tasks 103 and t13,
// ior internal/eventloop_restart.go). It is never rendered as a row.
//
// phase says which of two things happened:
//   - RESTART_PHASE_HANDLER: the signal:signal_deliver probe saw the first
//     user handler being run for the interrupted call. sa_restart is 1 when
//     that handler was installed with SA_RESTART, else 0; with the return code
//     userspace holds it decides, by the kernel's handle_signal rules, whether
//     the call still restarts once the handler returns or the program gets
//     EINTR.
//   - RESTART_PHASE_RESUME: the task's syscall enter that follows this record
//     is the kernel's continuation of the interrupted call: the same syscall
//     again, or restart_syscall after -516 (when restart_syscall is traced;
//     otherwise the record precedes some later enter, which userspace tells
//     by its syscall). It is the only record that licenses a fold; sa_restart
//     is 0.
//
// time is the boot clock, as in every record. For RESUME it is more than a
// timestamp: it equals the time field of the enter record the RESUME
// announces, bit for bit (both are the enter handler's single clock read).
// That enter may be sampled out or lost after RESUME was emitted, so
// userspace accepts as the continuation only an enter with this exact time;
// the task's next call of the same syscall has a later one. For HANDLER it is
// the time of the delivery.
//
// pid/tid name the interrupted task (every such record is emitted in its own
// context). sa_restart is a __u32 (0 or 1) because the Go type generator maps
// only 32/64-bit integers; with phase it completes the 8-byte alignment, so
// the layout has no implicit padding and the kernel record and a binary.Write
// payload share one size, 32 bytes.
struct syscall_restart_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __u32 phase;
    __u32 sa_restart;
};

// tcp_state_event (iormesh, sock.c) reports a TCP socket of a traced process:
// its 4-tuple and a state transition. The syscalls cannot supply this - a
// non-blocking (Java NIO) connect returns EINPROGRESS before the local port
// is even chosen - so the record comes from the socket itself:
//   - sock:inet_sock_set_state, for every transition of a socket registered
//     at connect or accept. oldstate/newstate are the kernel's TCP states
//     (TCP_ESTABLISHED = 1 ... TCP_CLOSE = 7, TCP_SYN_SENT = 2). The
//     tracepoint also fires in softirq context, so pid and fd are not the
//     current task's but the socket's registered owner;
//   - the accept exit, as a snapshot: the accepted socket reached
//     ESTABLISHED in softirq before anyone could own it, so its transition
//     was not reported. oldstate is TCP_STATE_SNAPSHOT (0, no TCP state has
//     it) and newstate the socket's state at accept time;
//   - the end of the session, as a final snapshot of every socket still
//     registered (iormesh_sock_final, run by userspace through
//     BPF_PROG_TEST_RUN, never attached; task sf4): oldstate is
//     TCP_STATE_FINAL (256, beyond every TCP state) and newstate the
//     socket's state then. Its byte counters, taken after every call the
//     session recorded, complete the merge's counter proof
//     (internal/federate/clockproof.go).
// sk is the struct sock address, the join key back to the owner: an fd
// number can be reused, the socket is the connection. family is AF_INET (2)
// or AF_INET6 (10); sport/dport are host byte order (sport is the local
// port, 0 in the CLOSE -> SYN_SENT transition, which precedes the port
// choice); saddr/daddr are network byte order, an IPv4 address in the first
// 4 bytes and zeroes after. All scalars are 32/64-bit because the Go type
// generator maps no 16-bit integers.
// snd_una, rcv_nxt, bytes_acked and bytes_received (M4, decision #26) are
// struct tcp_sock's, read at the moment of the record, in every record:
// the controller's merge rebuilds the connection's TCP initial-sequence-
// number pair from them, the cross-host join key NAT never rewrites
// (docs/spikes/s2-cross-host.md). They hold no peer ISN yet in the
// opening transitions (CLOSE -> SYN_SENT, and a refused connect's
// SYN_SENT -> CLOSE): BPF reports them anyway and the correlator decides
// which states carry them into the trace (internal/correlate/sockets.go),
// which keeps the record shape and the handler free of state logic.
// Appended after the addresses so the M1 offsets stay; schema version 2.
// seq_status (M1's reserved word, always 0 before) says whether they were
// read consistently: a state change runs with the socket locked, but the
// accept snapshot reads an unlocked child, which softirq on another CPU
// may be updating (tcp_rcv_nxt_update bumps bytes_received, then
// rcv_nxt) - a torn read would rebuild a wrong ISN pair. The snapshot
// re-reads until two consecutive reads agree; when they never do it sets
// TCP_SEQ_TORN and leaves the four zero, and the correlator then passes
// none of them on. Two agreeing reads still leave a residual race (both
// may fall inside one update on a slow writer); the merge therefore
// prefers the locked state-change records and uses a snapshot only when
// a half has nothing else (internal/federate/half.go). The final snapshot
// reads unlocked too, but once, never TCP_SEQ_TORN: the merge never keys
// from it, uses its counters only when its fields rebuild the half's key
// (so a torn read costs only that record), and each counter, one aligned
// load of a value that only grows, proves what it proves torn or not.
// No implicit padding, 112 bytes.
#define TCP_STATE_SNAPSHOT 0
#define TCP_STATE_FINAL 256
// seq_status values.
#define TCP_SEQ_OK 0
#define TCP_SEQ_TORN 1
// An address field holds an IPv6 address; IPv4 uses its first 4 bytes.
#define TCP_ADDR_LEN 16
#define TCP_STATE_EVENT_SCHEMA_VERSION 2
struct tcp_state_event {
    __u32 event_type;
    __u32 schema_version;
    __u64 time;
    __u64 sk;
    __u32 pid;
    __s32 fd;
    __s32 oldstate;
    __s32 newstate;
    __u32 family;
    __u32 sport;
    __u32 dport;
    __u32 seq_status;
    __u8 saddr[TCP_ADDR_LEN];
    __u8 daddr[TCP_ADDR_LEN];
    __u32 snd_una;
    __u32 rcv_nxt;
    __u64 bytes_acked;
    __u64 bytes_received;
};

// exec_argv_event (iormesh, exec.c) reports an exec of a process that is not
// traced, emitted only in a pattern session (IORMESH_EXEC_WATCH, flags.h):
// userspace matches the target pattern (decision #23: a glob over argv and the
// executable path, never the 15-character comm, which cannot tell two JVMs
// apart) and adopts or dismisses the PENDING target_map entry the handler
// inserted (maps.h). time is the exec's boot-clock time and equals that
// entry's since. exe is the path the exec was asked for (the tracepoint's
// filename, NUL-terminated, empty if unreadable). argv is the head of the new
// program's argument block, NUL-separated as in /proc/<pid>/cmdline:
// argv_len bytes were copied (0 when the read failed), argv_total is the
// block's full length, so argv_len < argv_total means truncated and userspace
// re-reads /proc/<pid>/cmdline. A Java command line puts the main class after
// a class path that can be long, which is why the block is 1 KiB and the
// fallback exists. A traced process's exec needs no verdict; it is sent
// as PROCESS_ARGV_EVENT instead, in every session, so the trace can record
// its argv (redacted in userspace, decision #9). No implicit padding.
#define MAX_ARGV_LENGTH 1024
struct exec_argv_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __u32 argv_len;
    __u32 argv_total;
    char exe[MAX_FILENAME_LENGTH];
    char argv[MAX_ARGV_LENGTH];
};

// sock_addr_event (iormesh, sockaddr.c, M2 step 7) carries what the syscall
// records of bind, connect, sendto, recvfrom and accept cannot: the address
// argument and the socket's own addresses. The call's exit hook sends it
// right before the exit record, so userspace attaches it to the thread's
// pending enter (trace_id is the exit's). Why a side record: the generated
// fd_event and ret_event stay lean for every other syscall, and only these
// five pay for the 232 bytes.
//   - fd is the call's descriptor; for accept the returned one.
//   - family and sock_type are the socket's (AF_UNIX 1, AF_INET 2, AF_INET6
//     10; SOCK_STREAM 1, SOCK_DGRAM 2), 0 when the fd names no socket.
//   - local_* and remote_* are read from the socket itself (inet sockets
//     only): the local address after the call (a sendto autobinds the
//     port), the remote one when the socket is connected, else zero.
//   - arg_* is the address argument (bind's local name, connect's and
//     sendto's destination, the source recvfrom wrote back), arg_family 0
//     when there was none or it was unreadable. Ports are host order,
//     addresses network order with IPv4 in the first 4 bytes.
//   - path is an AF_UNIX address argument's sun_path, NUL-terminated;
//     path_kind says whether it is a filesystem name (SOCK_ADDR_PATH_NAMED),
//     an abstract one (SOCK_ADDR_PATH_ABSTRACT, the name after the leading
//     NUL, cut at its next NUL) or absent (SOCK_ADDR_PATH_NONE, path "").
//   - sk is the struct sock address and peer, for AF_UNIX stream sockets,
//     unix_sk(sk)->peer: a connected pair points at each other, which is
//     how userspace joins a unix connection's two ends (the client's peer
//     is the socket accept returns, and that one's peer the client).
// All scalars are 32/64-bit (the Go type generator maps no 16-bit
// integers); reserved completes the 8-byte alignment, so there is no
// implicit padding.
#define SOCK_ADDR_PATH_NONE 0
#define SOCK_ADDR_PATH_NAMED 1
#define SOCK_ADDR_PATH_ABSTRACT 2
#define SOCK_ADDR_PATH_LENGTH 108
struct sock_addr_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __s32 fd;
    __u32 family;
    __u32 sock_type;
    __u32 local_port;
    __u32 remote_port;
    __u32 arg_family;
    __u32 arg_port;
    __u32 path_kind;
    __u64 sk;
    __u64 peer;
    __u8 local_addr[TCP_ADDR_LEN];
    __u8 remote_addr[TCP_ADDR_LEN];
    __u8 arg_addr[TCP_ADDR_LEN];
    char path[SOCK_ADDR_PATH_LENGTH];
    __u32 reserved;
};

// signal_event (iormesh, signal.c, M2 step 7) is one signal:signal_generate:
// a signal queued for target_pid (tgid) / target_tid by the current task
// pid / tid - the sender, which for kill(2) and tgkill(2) is the caller,
// since the tracepoint fires inside the call. scope says whether a task sent
// it at all (SIGNAL_SCOPE_USER: kill, sigqueue, tkill/tgkill, by siginfo;
// not for a kernel-raised SIGALRM, SIGCHLD, SIGIO or fault, whose pid/tid
// are just the task that ran), whether that sender is traced
// (SIGNAL_SCOPE_SENDER, only with USER) and whether the target is
// (SIGNAL_SCOPE_TARGET). It is emitted when SENDER or TARGET is set.
// result is the tracepoint's (TRACE_SIGNAL_DELIVERED 0, _IGNORED 1,
// _ALREADY_PENDING 2, _OVERFLOW_FAIL 3, _LOSE_INFO 4); group is 1 for a
// process-directed signal. No implicit padding, 48 bytes.
#define SIGNAL_SCOPE_SENDER 1
#define SIGNAL_SCOPE_TARGET 2
#define SIGNAL_SCOPE_USER 4
struct signal_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __u32 target_pid;
    __u32 target_tid;
    __s32 sig;
    __s32 result;
    __u32 group;
    __u32 scope;
};

// rw_aggregate_event (iormesh, rwagg.c, M2 step 11) stands for count read or
// write calls (trace_id is the enter id, SYS_ENTER_READ or SYS_ENTER_WRITE)
// one thread made on one fd in a hot loop, whose own enter and exit records
// were never sent. time is the first call's enter, end_time the last call's
// exit; total_bytes and latency_ns are the sums of the calls' returns and of
// their exit-minus-enter durations (only successful calls, ret > 0, are
// folded; the field is not called bytes, which the generated Go type has as
// a method). file_ident is the fd's file identity as fd_event carries it
// (fileident.c, 0 when off). 64 bytes, no implicit padding.
struct rw_aggregate_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u64 end_time;
    __u64 total_bytes;
    __u64 latency_ns;
    __u32 pid;
    __u32 tid;
    __s32 fd;
    __u32 file_ident;
    __u32 count;
    __u32 reserved;
};

// stack_event (iormesh, stack.c, M2 step 9) names the user stack a traced
// thread had when it entered a syscall: stack_id is bpf_get_stackid's id in
// stack_map (maps.h), or its negative errno when the walk failed (-EFAULT:
// nothing unwound; -EEXIST: the id's bucket holds another stack). time is
// the enter handler's clock read, the very value its enter record carries,
// which is how userspace pairs the two: the stack record is sent right
// before the enter record (or not at all), so a stack whose enter never
// arrived - a full ring - pairs with nothing. trace_id is 0: the enter
// record names the syscall. image is the address space's mm->arg_start,
// which /proc/<pid>/stat shows as field 48: an exec draws a new one, so
// userspace reads /proc/<pid>/maps for these addresses only while the
// process still runs the image they were taken in, however late it gets to
// the record. 40 bytes, no implicit padding.
struct stack_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __s32 stack_id;
    __u32 reserved;
    __u64 image;
};

// IORMESH_STACK_DEPTH is how many user frames a stack keeps (stack_map's
// values in maps.h, stack_inline_event's ips); deeper stacks are cut to
// their innermost frames. 64 covers the call chains of the servers iormesh
// targets with room to spare, at half the memory of the kernel's 127-frame
// maximum. Defined here, not in maps.h, so the Go types get it too.
#define IORMESH_STACK_DEPTH 64

// stack_inline_event (iormesh, stack.c, task kd4) is a stack_event whose
// stack stack_map refused - bpf_get_stackid's -EEXIST when the stack's
// bucket holds another stack, -ENOMEM when the map is full - so it carries
// the frames itself, from bpf_get_stack: nr instruction pointers in ips,
// innermost first. stack_id keeps bpf_get_stackid's error, for diagnosis
// only. Userspace deduplicates these stacks by their frames. Without it a
// collision would cost every enter of that stack its stack for the whole
// session, and collisions grow with the square of the distinct stacks
// (about two in a 200-stack test run of 8192 buckets). 552 bytes, no
// implicit padding.
struct stack_inline_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __s32 stack_id;
    __u32 nr;
    __u64 image;
    __u64 ips[IORMESH_STACK_DEPTH];
};

// java_mark_event (iormesh, javamark.c, task td4) says which hooked JDK
// native (libjava/libnio/libnet) a thread was inside when it entered a
// syscall: cookie is the BPF attach cookie userspace gave that native's
// uprobe (internal/bpf, AttachJavaMarks), 0 when the thread was nested
// deeper than the mark keeps (the innermost native is then unknown), and
// entry the native's entry on CLOCK_MONOTONIC (bpf_ktime_get_ns), the clock
// of the agent's brackets (System.nanoTime). time is the enter handler's
// clock read, the very value its enter record carries, which is how
// userspace pairs the two, as with stack_event: the mark record is sent
// right before the enter record (or not at all). trace_id is 0: the enter
// record names the syscall. A syscall outside every hooked native sends no
// mark. 40 bytes, no implicit padding.
struct java_mark_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __u32 pid;
    __u32 tid;
    __u64 cookie;
    __u64 entry;
};

// tcp_seq_event (iormesh, tcpseq.c, M4 "Clock reconciliation", task xe4)
// is the exit record of one unfolded TCP send or receive that has a stream
// position, sent in place of its ret_event (so the call still costs two
// ring records): ret_event's fields plus seq, the TCP sequence number of
// the call's first byte - write_seq for a send, copied_seq for a receive,
// read at the call's enter - and sk, the struct sock address, the same
// value tcp_state_event carries, so userspace can check the record names
// the socket the call's fd is bound to. The call moved [seq, seq + ret)
// mod 2^32. It is sent only when the same field read at the exit is
// exactly ret further on, which proves that every byte the socket took (or
// gave) in between was this call's own (tcpseq.c); every other exit of
// these calls is a ret_event. 48 bytes, no implicit padding.
struct tcp_seq_event {
    __u32 event_type;
    __u32 trace_id;
    __u64 time;
    __s64 ret;
    __u32 pid;
    __u32 tid;
    __u64 sk;
    __u32 seq;
    __u32 reserved;
};
