# Event envelope

Part of [Trace schema (v1, frozen)](../schema.md).

Required fields are always present; a zero is a real zero.

| Key | Type | Meaning |
|-----|------|---------|
| `schema_version` | int | always `1` |
| `project` | string | the session's project label |
| `seq` | int | per-host sequence number from 1, gap-free except explicit `drop` events |
| `boot_ns` | int | boot-clock time of the syscall's start |
| `wall_ns` | int | the same instant on the wall clock (boot + offset measured once per session) |
| `host_id` | string | `/etc/machine-id` on a host; `pod-<uid>` (the pod's UID, from the downward API's `POD_UID`) for an in-pod tracer, which never reads a machine-id ([Pods](graph.md#pods-as-hosts-m7)) |
| `pid` | int | process id (tgid), as the tracer's PID namespace numbers it — the host's on a host, the pod's for an in-pod tracer (M7, below) |
| `pid_incarnation` | int | process start on the boot clock: a reused pid is another process |
| `tid` | int | thread id; `0` for records with no task context (`tcp_state`) |
| `comm` | string | process name at the event: the kernel's comm of the main thread (≤ 15 bytes), what `ps` shows; an exec renames it, so events before the exec carry the old name and the `exec` event and later ones the new, and so does the main thread's `prctl(PR_SET_NAME)` (the `task_rename` record); a worker thread naming itself does not rename the process |
| `event` | string | the event kind, below |
| `fd` | int | descriptor acted on — the argument, or the returned one for opens and accepts; `-1` when none, also for a negative argument (`fstat(-1)`, EBADF), which names no instance |
| `fd_gen` | int | how often this fd number was (re)created in this incarnation; `0` = predates the trace or no fd. A forked child continues its parent's count (it inherits the parent's instances) |
| `ret` | int | the syscall's return value |
| `err` | string, optional | errno name (`ENOENT`), only when `ret < 0` |
| `latency_ns` | int | exit minus enter; for a folded `read`/`write` the calls' sum |
| `bytes` | int | bytes moved (read/write); for a folded one the calls' sum |
| `args` | object | syscall-specific arguments, below |
| `conn_id` | string, optional | the connection a socket event belongs to; across hosts assigned by the merge's ISN-pair join (S2, decision #26) |
| `attribution` | string | `java` or `none` — missing attribution is data |
| `java` | object, optional | `{thread, stack, carrier}`, set exactly when attribution is `java`; `thread` is the Java thread name, `stack` method ids of `methods.ndjson`, innermost first (settled by S1); `carrier` (appended in M3) is `true` on a virtual thread's I/O, whose `thread` then names the carrier — see "Java attribution" |
| `http` | object, optional | the request, set exactly on `http` events — see "HTTP events" |
| `session` | object, optional | the host description, set exactly on `session` events (below) |
| `stack_id` | int, optional | the native user stack the thread entered the syscall with: an id of `stacks.ndjson` (≥ 1); only with `-native-stacks` (below) |
| `otel` | object, optional | `{trace_id, span_id}`, the OpenTelemetry span the Java code ran in; only with attribution `java`, absent outside a span (appended in M3) — see "OpenTelemetry span context" |
| `dns` | object, optional | `{host, addrs}`, set exactly on `dns` events (appended in M3, task wd4) — see "DNS events" |
| `pod` | object, optional | `{namespace, name, container_id}`, on every event of an in-pod tracer, absent outside a pod (appended in M7, task sh4) — see [Pod fields on events](graph.md#pod-fields-on-events-m7) |

`(pid, pid_incarnation, fd, fd_gen)` names one fd instance, even
when a library's open and a later socket land on the same number.

**fd lifecycle** (M2 step 6): a copy (`dup*`,
`fcntl(F_DUPFD*)`) is a new instance of the returned number that
names the old fd's file and connection; a forked child starts
with its parent's fds as they were (same `fd_gen`) and gets a
`forked` edge; an exec closes the close-on-exec fds — and those
whose state the trace never learned, except stdio — so a number
the new program reuses is a new instance. The state comes from
the open and `accept4` flags, `dup3`, `F_DUPFD_CLOEXEC` and
`fcntl(F_SETFD/F_GETFD)` — `F_SETFD`'s argument is the event's
`args.flags`, `F_GETFD`'s answer its `ret`. A fork is an event of
its own (`fork`, below), so a reader replaying the events knows
where a child's copy of the table starts. `close_range` is not
traced.

## Args

Every argument is its own optional field — one nullable parquet
column each, never a blob.

| Key | Set on |
|-----|--------|
| `path` | opens and `creat` (required); the M2 path kinds; the old name of the rename/link family (a symlink's text); the fd metadata kinds (the fd's current path); an AF_UNIX `bind`, `connect`, `sendto` or `recvfrom`: the socket path (`@name` for an abstract one) |
| `new_path` | rename/link family (M2): the new name |
| `flags` | opens: the raw open flags; the path kinds whose syscall has a flags argument (`statx`, `newfstatat`, `utimensat`, `fchownat`, `fchmodat2`, `linkat`); `dup3`; a successful `fcntl(F_SETFD)`: the descriptor flags it set (`FD_CLOEXEC` 1; appended in M4, task ze4) — on no other `fcntl` |
| `cmd` | `fcntl` (M2): the raw command (`F_DUPFD` 0, `F_GETFD` 1, `F_SETFD` 2, `F_DUPFD_CLOEXEC` 1030, …); `epoll_ctl`: the op (`EPOLL_CTL_ADD` 1, `DEL` 2, `MOD` 3) |
| `offset` | positional I/O (planned, not set yet) |
| `local_addr`, `local_port` | socket events, when known; a socket bound to the wildcard address (`0.0.0.0`, `::`) has its port but no `local_addr` |
| `remote_addr`, `remote_port` | socket events, when known; on `sendto`/`recvfrom` the datagram's peer |
| `sock_state` | `tcp_state` (required): `ESTABLISHED`, `CLOSE`, … |
| `marker` | `marker` (required): `<scenario>/<edge>` |
| `lost` | `drop` (required, > 0): records lost since the previous `drop` event of its kind; for `skipped_runs`, program runs skipped host-wide (an upper bound, not lost records) |
| `drop_reason` | `drop`, optional: why the records were lost — `attach_gap` (M2), `unattributed` (a folded read/write no fd instance explains, M2 step 11), `skipped_runs` (program runs the kernel skipped, possible loss); absent for ring-buffer and decoder loss |
| `end_reason` | `end` (required): why the trace ended, below |
| `argv` | `exec` and `attach` only: the new program's argv (`exec`), or the command line of a process already running at attach (`attach`, task nh4), a list of strings, redacted (see Redaction); absent when unreadable |
| `fd2`, `fd2_gen` | a second fd instance (M2 step 7): the other end of `pipe`, `pipe2`, `socketpair`; the fd `epoll_ctl` registers |
| `signal` | the signal number: `kill`, `tgkill`, `tkill` (when the kernel generated a signal) and `signal` (required) |
| `target_pid` | `kill`, `tgkill`, `tkill`: the tgid signalled — raw, the target need not be traced |
| `sender_pid` | `signal` only: the tgid that sent it — raw, the sender need not be traced; absent for a kernel-raised signal, and for a sender outside the tracer's PID namespace (it has no number there) |
| `count` | `read`, `write` only, when ≥ 2: how many calls a folded hot loop's event stands for (M2 step 11, below); absent for a single call |

IPv4 peers of dual-stack sockets are written unmapped
(`127.0.0.1`, not `::ffff:127.0.0.1`).

| `snd_una`, `rcv_nxt` | `tcp_state` only, optional (appended in M4): the socket's `tcp_sock` fields of those names at the transition, u32 |
| `bytes_acked`, `bytes_received` | `tcp_state` only, optional (appended in M4): the `tcp_sock` byte counters, u64 |
| `tcp_seq` | optional (appended in M4): the TCP sequence number of the first stream byte the call moved, u32; the call moved `[tcp_seq, tcp_seq + bytes)` mod 2³². Only on an unfolded `write`, `writev`, `sendto`, `sendfile64`, `sendmsg`, `sendmmsg`, `read`, `readv`, `recvfrom`, `recvmsg` or `recvmmsg` with a `conn_id` that moved bytes |
| `bytes_partial` | `sendmmsg` and `recvmmsg` only, optional (appended in M4, task oh4): `true` when `bytes` is a lower bound — the tracer could not read every message's length (a faulting read, more messages than its cap, a lost enter). Never `false`; such a record carries no `tcp_seq` |
| `peek` | `recvfrom`, `recvmsg` and `recvmmsg` only, optional (appended in M4, task ok4): `true` when the call passed `MSG_PEEK` — it copied `bytes` but consumed none, so the stream sums skip it. Never `false`; such a record carries no `tcp_seq` |
| `pattern` | `attach` only, optional (appended in M4, task ze4): the `-pattern` the session matched the process by; with the project it names the app (`app:<project>/<pattern>`). Absent for a `-pid` or port target. Never empty |
| `parent_pid`, `parent_incarnation` | `fork` only, both or none (appended in M4, task ze4): the process that forked the child, as its own events name it (raw pid, u32; incarnation, u64); absent when the kernel record could not say |
| `sock_snapshot` | `tcp_state` only, optional (appended in M4, task sf4): `final` on the tracer's snapshot of a socket still open when the session ended — no transition, `sock_state` is the socket's state then, and the four `tcp_sock` fields are always present; `initial_client` or `initial_server` (task cm4, decision #35) on its snapshot of a socket a target already held when the session started — no transition either, the end's role in the value, the four fields present when the state can rebuild the key; absent on every other record, the accept snapshot's included |

The four `tcp_sock` fields come together or not at all: the
controller's merge rebuilds the connection's ISN pair — the
cross-host join key (S2, decision #26) — from all four, so a
partial set is rejected rather than read into a wrong key. The
tracer fills them (task ve4) on every `tcp_state` record whose
handshake is complete: `ESTABLISHED` and every later state, the
accept snapshot (`CLOSE` included — a child reset in the accept
queue), and a `CLOSE` that ends an established connection. The
opening records (`SYN_SENT`, `SYN_RECV`), a `CLOSE` straight
from `SYN_SENT` (a refused connect), from `SYN_RECV` (an aborted
handshake) or from `LISTEN` (a closed listener), and an accept
snapshot whose reads never agreed carry none of the four: there
the fields hold no peer ISN yet, or were caught mid-update. The
accept snapshot reads a socket nobody locks, so the merge keys a
server end from its state changes and uses the snapshot only
when it has nothing else. The values derive from
the kernel's random initial sequence numbers, so the golden
normalizer drops all four.

When a session ends, the tracer adds one more `tcp_state`
record for every traced socket still open: the final snapshot
(`args.sock_snapshot: "final"`, task sf4), stamped after the
session's last recorded call on it, with the socket's state,
tuple and the four fields. A socket closed during the session
gets none — its `CLOSE` was its last record. It reports no
transition; a reader counting state changes skips it. Its
counters are the session's last reading of the socket's byte
counts, which the merge's clock bounds use to prove that a
send and a receive of one sequence offset are the same bytes
(milestones.md M4, "Clock reconciliation"); it never keys the
connection, since it reads an unlocked socket like the accept
snapshot.

When a session starts — and again when it takes on a process
that was already running — the tracer adds one `tcp_state`
record for every TCP socket a target already held, other than
a listener: the initial snapshot (`args.sock_snapshot:
"initial_client"` or `"initial_server"`, task cm4, decision
#35). Without it a connection opened before the session — a
keep-alive client, a connection pool — had no connect, no
accept and no state change in the trace, so its reads and
writes named no connection until it reconnected. The record
binds the socket to the descriptor it was found at (an fd that
predates the trace, `fd_gen` 0 unless the trace saw the number
before), gives the connection its `conn_id` and carries the
state, tuple and — from `ESTABLISHED` on, unless its reads
never agreed — the four fields. Its value is the end's role,
which no later record will say: `initial_server` when a
listening socket holds the socket's local port (on its address
or the wildcard), or the socket is in `SYN_RECV`;
`initial_client` otherwise. It reports no transition either.
Read from an unlocked socket like the accept snapshot, it keys
the connection only when no state change does; and since the
connection was established at its `wall_ns` or earlier, the
merge's fusion window bounds it from one side only.

`tcp_seq` gives a send or receive its TCP sequence range (start
sequence plus `bytes`). A byte has the same sequence number on
both hosts, so the merge pairs each receive with the send that
queued its last byte and bounds the two hosts' clock offset by
it (S2; milestones.md M4 "Clock reconciliation"). A folded record
cannot carry it — its `boot_ns` is the first call's and its
`latency_ns` a sum, so no instant of it belongs to the call that
moved a given byte. The tracer fills it (task xe4) from the
socket itself: `write_seq` (the end of the send queue — not
`snd_nxt`, which lags behind queued data) for a send,
`copied_seq` (the next byte the reader gets — not `rcv_nxt`)
for a receive, read at the call's enter and again at its exit.
It is given only when the two reads differ by exactly the
bytes the call moved, which proves no other call's bytes went
through the socket in between — or, for a receive that also
consumes the peer's FIN, by one more (the FIN's own sequence
number) when nothing was received after it (task wi4). So a
send racing another thread's send on the same socket, a
`MSG_PEEK` or `MSG_OOB` read (`copied_seq` does not move, or
skips the urgent byte), a kTLS socket (`write_seq` counts
record framing), a TCP Fast Open send (the ISN is chosen inside
the call) and a `sendmmsg`/`recvmmsg` whose message lengths
the tracer could not all read (`bytes_partial`) carry none: no
`tcp_seq` is ever a guess. Neither does a call on a kernel
without the `bpf_rdonly_cast` kfunc (before 6.2), where the
tracer cannot read the socket cheaply, nor one whose socket the
trace never bound (opened before the session), nor a `read` or
`write` the in-kernel fold held at its enter on a hot streak and
then sent alone because it turned out slow — typically the read
that waits for the peer's answer right after a burst of calls.
A `recvfrom`, `recvmsg` or `recvmmsg` with `MSG_PEEK` copies
bytes but consumes none: its `bytes` say what it copied, it is
marked `args.peek` and carries no `tcp_seq`, and the receiver's
stream sums (the merge's and the report's, its thread and
method bytes included) skip it, so the bytes count once, with
the receive that consumes them (tasks ok4, cl4).

## Event kinds (M1)

| Kind | Source |
|------|--------|
| `open`, `openat`, `openat2` | syscall pair |
| `read`, `write`, `close` | syscall pair |
| `connect`, `accept`, `accept4` | syscall pair; args carry the 4-tuple |
| `tcp_state` | `sock:inet_sock_set_state`, joined to the owning pid/fd; the accept snapshot at the accept exit; the initial (task cm4) and final (task sf4) snapshots at the session's start and end |
| `marker` | an open of `/iormesh-marker/<scenario>/<edge>` |
| `drop` | loss — gaps are explicit: ring-buffer and decoder loss, skipped program runs (possible loss), or a pattern session's attach gap (`drop_reason`) |
| `session` | the per-trace header |
| `end` | why the trace ended (duration, disk limit, stop, targets exited, error) |

The set only grows; M2 appends the rest of the file and socket
surface and the opt-in families (below), M3 `http` (see "HTTP
events").

## Event kinds (M2 file surface)

Default-on (decision #7), each named after its kernel tracepoint
(`internal/schema/kinds_file.go`, attach list
`internal/tracepoints/default_set.go`):

| Shape | Kinds | fd | args |
|-------|-------|----|------|
| path | `creat`, `mkdir`, `mkdirat`, `mknod`, `mknodat`, `rmdir`, `unlink`, `unlinkat`, `readlink`, `readlinkat`, `newstat`, `newlstat`, `newfstatat`, `statx`, `truncate`, `utime`, `utimes`, `futimesat`, `utimensat`, `chmod`, `fchmodat`, `fchmodat2`, `chown`, `lchown`, `fchownat`, `setxattr`, `lsetxattr`, `getxattr`, `lgetxattr`, `listxattr`, `llistxattr`, `removexattr`, `lremovexattr` | the dirfd; `-1` for `AT_FDCWD` | `path`, `flags` where the syscall has them |
| name | `rename`, `renameat`, `renameat2`, `link`, `linkat`, `symlink`, `symlinkat` | `-1` | `path` (old), `new_path` |
| fd | `newfstat`, `ftruncate`, `fallocate`, `fchmod`, `fchown`, `fsetxattr`, `fgetxattr`, `flistxattr`, `fremovexattr` | the fd | `path`: the fd's current path, except on `newfstat` |
| data | `pread64`, `pwrite64`, `readv`, `writev`, `preadv`, `pwritev`, `preadv2`, `pwritev2` | the fd | `bytes` as for `read`/`write`; no path |
| dup | `dup`, `dup2`, `dup3` | the old fd; `ret` is the new one | `flags` on `dup3` |
| fcntl | `fcntl` | the fd | `cmd`; for `F_DUPFD`/`F_DUPFD_CLOEXEC` `ret` is the new fd |

`creat` is an open: `fd` is the returned descriptor, `flags`
`O_CREAT|O_WRONLY|O_TRUNC`. A relative path is resolved against
its dirfd's path when the trace saw that fd opened; against
`AT_FDCWD` it stays as the call gave it (the working directory is
not tracked). An empty path with a dirfd (`AT_EMPTY_PATH`,
`utimensat`'s NULL form) names the fd's own file. A symlink's
`path` is the link's text, never resolved.

**Rename-aware paths** (M2 step 5): the path an fd's metadata
events report follows its file through `rename*` — an fd at or
below the old name (a directory rename moves everything under
it) names the same place under the new one, in every traced
process of the host — and its process gains an `opened` edge to
the new name. A name that was unlinked, or replaced by a rename
onto it, no longer moves the fds of the file it named. Matching is
by name: the rename record carries no inode, and a directory
rename moves files of many inodes. That narrows the name-reuse
hazard but does not close it: an unlink, recreate or rename by an
untraced process, a cwd-relative unlink or rename,
`renameat2(RENAME_EXCHANGE)` (its flags are not recorded, so the
fds at the new name are marked gone with a stale name instead of
swapped) and processes in different mount namespaces can still
leave an fd under a wrong name. `read` and `write` carry no
path at all (decision #25's per-event cost); their fd instance
joins to the open that named it.

## Folded reads and writes (M2 step 11)

A hot `read` or `write` loop — one thread, one fd, calls following
each other within 1 ms and each returning data within 1 ms — is
summed in the kernel (decision #25; `internal/c/rwagg.c`) instead
of sending each call: one event then stands for `args.count` calls
on that fd instance. Its `bytes` and `ret` are the calls' summed
byte counts, `latency_ns` the sum of their latencies (time spent
in the calls, not the span), `boot_ns` the first call's start; it
says nothing about the individual calls' sizes or gaps.

- The first four hot calls of each fd and direction are always
  single events, and so is every call that is not hot (end of
  file, an error, a call that blocked, one after a pause): what a
  scenario declares stays exact.
- Every other traced event of the thread — open, close, dup,
  connect, accept, a marker, exec, a fork, a signal it sends, and
  every `read` or `write` it sends as a single event — comes after
  the folded calls that preceded it. Only folded events of one
  thread's different fds are spans that may overlap each other.
- A folded event is sent at the latest when the loop has run
  100 ms — checked as a call folds, so if the key's next call
  blocks, when that call returns —, at the thread's next other
  traced call, its exit, or the end of the trace. A thread that
  folds and then blocks in an untraced call therefore delivers it
  late (`seq` order, not `boot_ns` order).
- The kernel keys a loop by the fd *number* (the file identity is
  not captured), so another thread's `close`, `dup2` or reopen of
  that number — while the loop ran, or before its late event was
  sent — is resolved by time: the event names the fd instance
  that was live from its first call's start to its last call's
  exit. One that no single instance covers (its calls straddle
  the change) is not written as a read or write but as a `drop`
  with `args.drop_reason` `unattributed`, `args.lost` its call
  count, the thread's `pid`/`tid` and the first call's `boot_ns`;
  the tracer's summary line counts these as `unattributed
  aggregates`. A change made by an untraced call (`close_range`,
  io_uring) is not seen and can still put a loop's calls on the
  number's next instance.
- A folded event lost to a full ring buffer is one lost record
  (`drop`), which hid `args.count` calls.
- `pread64`, `pwrite64` and the vectored calls are never folded.
- `iormesh run -aggregate=false` sends every call as its own
  event, for debugging; a busy loop then exceeds decision #25's
  loss budget.

## Event kinds (M2 process lifecycle)

| Kind | Source |
|------|--------|
| `exec` | a successful exec of a traced process (`sched_process_exec` plus the argv record): `args.path` is the executable as exec'd (empty when unreadable, then absent), `args.argv` the command line, redacted; `fd` is `-1`, `ret` `0` |

| `fork` | a new process of a traced one (`task_newtask` without `CLONE_THREAD`), on the **child**: `pid`, `pid_incarnation`, `tid` (= pid) and `comm` are the child's as it starts, `args.parent_pid`/`parent_incarnation` the forker; `fd` `-1`, `ret` `0`, `boot_ns` the record's (appended in M4, task ze4) |
| `attach` | a process the session took on as a target — a `-pid` or port target at session start, or a `-pattern` match (then with `args.pattern`) — with the identity read then; a process already running then (no exec of its own: a `-pid` or port target, a pattern's start-scan match) also carries `args.path`, its executable (`/proc/<pid>/exe`), and `args.argv`, its command line (`/proc/<pid>/cmdline`), redacted like an `exec`'s, each absent when `/proc` could not be read (task nh4); `fd` `-1`, `ret` `0`, `boot_ns` the tracer's clock at the attach — read when the event is numbered, so it can be later than the `boot_ns` of events with a higher `seq` (records the tracer handles later but stamped earlier); `seq` orders it (appended in M4, task ze4) |

`fork` and `attach` carry what the graph's `forked` edge, a
child's inherited fds, and the `app` node with its
`incarnation` edges rest on, so the controller's rebuild from the
events (decision #3) has them too. Each is one event per
process. A child's `fork` is its first event; a pattern
incarnation's `attach` comes before its `attach_gap` drop.

A followed child's exec is one, and so is the exec through which a
`-pattern` session found its app (after that process's
`attach_gap` drop). A process already running when the trace
attached has none; its `attach` carries the argv and executable
`/proc` showed then instead, redacted by the same policy, read only
while the process is still the incarnation seeded (a reused PID
yields neither) and absent when unreadable (the process gone,
`hidepid`, another pid namespace). Its `args.path` is the resolved
`/proc/<pid>/exe` link — a symlink-free path, with the kernel's
` (deleted)` suffix when the binary was replaced or removed since —
while an `exec`'s is the path as exec'd. `jspawnhelper`
and the program it execs are two `exec` events of one process.
BPF copies the first 1 KiB of the argument block; a longer one is
completed from `/proc/<pid>/cmdline` only when that still starts
with the copied bytes — the process may have exec'd again, or its
PID been reused, by the time userspace reads it. Otherwise the cut
argv is kept, its partial last element masked as `***` (a cut
`https://u:secr` is no longer recognizable as a URL password).
Every read of `/proc/<pid>/cmdline` for an event (an `attach`'s
argv, an `exec`'s completion) takes at most 64 KiB
(`target.MaxCmdline`); a longer command line (a JVM's class path
can be megabytes) is cut there and its partial last element masked
the same way, so an event line stays far below the 1 MiB the NDJSON
readers take.
The environment is never captured (decision #9).

## Event kinds (M2 step 7: IPC, signals, UDP, poll, zero-copy)

Each family is decision #7's (`internal/schema/kinds_ipc.go`;
families in `internal/tracepoints/trace_family.go`): `bind`,
`sendto`, `recvfrom` and the msghdr data calls `sendmsg`,
`recvmsg`, `sendmmsg` and `recvmmsg` (task oh4) belong to the
default-on socket family,
the rest to opt-in ones — `iormesh run -trace-families
ipc,signal,poll,zerocopy` (or `all`) adds them.

| Family | Kinds | fd | args |
|--------|-------|----|------|
| socket | `bind` | the socket | AF_UNIX: `path`; inet: `local_addr`/`local_port` (the bound address; the kernel's port for port 0) |
| socket | `connect` (M1) | the socket | TCP: the 4-tuple; AF_UNIX: `path` |
| socket | `sendto`, `recvfrom` | the socket | `bytes`; per datagram `local_*` and `remote_*` (the peer it went to or came from); AF_UNIX: `path`. On a stream socket (`send`/`recv` on a TCP or unix connection) only the connection's 4-tuple, as `read`/`write` |
| socket | `sendmsg`, `recvmsg` | the socket | `bytes` (the return); on a stream socket the connection's 4-tuple, as `read`/`write`, and `tcp_seq` on TCP; on a datagram socket `local_*` and `remote_*` as `sendto`/`recvfrom`, the peer from the msghdr's `msg_name` or, without one, the connected socket's (task nk4); AF_UNIX: `path` |
| socket | `sendmmsg`, `recvmmsg` | the socket | `ret`: the messages moved; `bytes`: the sum of their lengths (`msg_len`), `bytes_partial` when incomplete; otherwise as `sendmsg`, the peer being the first message's `msg_name` (one per call; a batch to several peers is named by its first, and its whole `bytes` go to that first peer's flow or connection — a QUIC server's `recvmmsg` from several clients adds them all to the first client's flow; task hl4) |
| socket | `recvfrom`, `recvmsg`, `recvmmsg` with `MSG_PEEK` | the socket | as without, plus `peek`: the bytes copied, none consumed; no `tcp_seq` |
| ipc | `pipe`, `pipe2`, `socketpair` | the first end (a pipe's read end) | `fd2`, `fd2_gen`: the second end |
| signal | `kill`, `tgkill`, `tkill` | `-1` | `target_pid`, `signal` |
| signal | `signal` | `-1` | `signal`, `sender_pid`; `pid`/`tid` are the receiver |
| poll | `epoll_create`, `epoll_create1` | the new epoll fd | — |
| poll | `epoll_ctl` | the epoll fd | `fd2`, `fd2_gen` (registered fd), `cmd` (op) |
| poll | `epoll_wait`, `epoll_pwait`, `epoll_pwait2` | the epoll fd | `ret`: ready count; `latency_ns`: the block |
| poll | `poll`, `ppoll` | `-1` | as the waits |
| zerocopy | `sendfile64`, `splice`, `tee`, `copy_file_range` | the first fd argument | `bytes` moved (the second fd is not captured yet) |

**Connections beyond TCP** (each is a `socket` node and draws
`connected` edges, and its events carry `conn_id`):

- a **pipe or socketpair** is one connection, `fused` at
  creation — its creator holds both ends; a process that inherits
  an end (fork, `dup2` onto stdio) joins the same node, so a
  parent and the child it talks to over stdio share it;
- a **unix stream connection**: the connecting socket and the
  accepted one are paired by the kernel (`unix_sk->peer`), so the
  two ends fuse when both are traced and the accept follows the
  connect within the fuse window (5 s); otherwise each stays
  `unresolved` — a later accept is a node of its own. Only an
  accepted end and a connecting one fuse: two accepts (or two
  connects) naming the same socket pair are two connections whose
  untraced peers' sockets the kernel reused — the tracer's own
  attach connections to two JVMs, say — each `unresolved`;
- a **UDP flow** — UDP has no connection, so the unit is one
  (local, remote) address pair seen in the datagrams: the two
  sides of a loopback flow mirror each other and fuse within the
  window, like TCP's ends. A socket bound to the wildcard sends
  from the address the route picks; towards a loopback peer that
  is the peer's own address, so such flows fuse too, while a flow
  to another host stays `unresolved`. A flow ends when the socket
  that carried its first datagram is closed; until then one tuple
  is one flow (a flow of a process that exits or execs without
  closing lasts until the session ends).

**Signals**: `signal` is a delivery, from `signal_generate`, on
the receiving process. Only a signal a task sent — `kill`,
`sigqueue`, `tkill`/`tgkill`, told by its siginfo — has a sender:
`sender_pid` is that task's tgid. A kernel-raised one (a timer's
`SIGALRM`, a child's `SIGCHLD`, `SIGIO`, a fault) fires in whatever
task happens to run, so it has no `sender_pid`, puts nothing on a
pending `kill` and draws no edge. The receiver is the target's
thread for `tgkill`, the process's tid for a process-directed
signal. An ignored signal
(a default-ignored `SIGCHLD`), or one the kernel could not queue,
is not a delivery and is not recorded. A sender's `kill` carries
`target_pid` and `signal` only when the kernel generated one
(`kill(pid, 0)`, `ESRCH` and `EPERM` carry neither). Both ends
traced, and a task sent it: a `signaled` edge.

## Host-level events: session, drop, end

`session`, `drop` and `end` describe the tracer, not a syscall:
`pid`, `pid_incarnation` and `tid` are `0`, `comm` is empty, `fd`
is `-1`, `attribution` is `none`, and `boot_ns` is the boot clock
when the tracer wrote them. PID 0 is what tells the normalizer to
keep them out of process renumbering and inside the marker
bracket. Like every event they take the next `seq`, so the stream
stays gap-free.

- **`session`** is the first event (`seq` 1) of every
  `iormesh run` trace. Its `session` object is required there and
  forbidden elsewhere; every key in it is required:

  | Key | Meaning |
  |-----|---------|
  | `boot_id` | `/proc/sys/kernel/random/boot_id` — `boot_ns` of two traces compare only when these match |
  | `fqdn` | the host's canonical name (`hostname -f`), or its hostname when it has none; in a pod `<pod name>.<namespace>.<first 8 hex digits of the pod UID>`, with no resolver lookup ([Pods](graph.md#pods-as-hosts-m7)) |
  | `kernel` | kernel release (`uname -r`) |
  | `arch` | machine hardware name (`uname -m`), e.g. `x86_64` |

- **`drop`** reports loss: `args.lost` records — kernel
  ring-buffer reservations that failed plus records that arrived
  undecodable — since the previous such `drop`. `iormesh run`
  polls the counters every 200ms — also while it drains the
  targets' last records — and once more just before it detaches,
  so a `drop` follows its gap within a poll interval; it is never
  written with `lost` 0. No `drop` event means no loss was
  counted. Records lost after that last poll, while the probes
  detach, are counted nowhere (a known M1 limit).

  **Skipped program runs** are possible loss and get drops of
  their own, polled alongside: `args.drop_reason`
  `skipped_runs`, host-level (`pid` 0), `args.lost` the runs
  skipped since the previous `skipped_runs` drop. The kernel's
  recursion guard skips a tracepoint program while its guard is
  raised on that CPU — since task 8e4 the host-wide one of the
  syscall dispatchers and other classic tracepoint programs,
  held by another BPF program or by a task preempted inside a
  `bpf()` map syscall (output-contract.md, "Syscall handlers run behind
  `raw_syscalls` dispatchers"); a skip of a target's call loses
  the one record, or the one folded call, that run would have
  written. But the skip comes before the program's target
  filter — a dispatcher's before it even looks up the syscall,
  so a skipped dispatcher run is any syscall of any process,
  traced kind or not — and the count includes untraced
  processes' calls (a two-record trace saw 691 on a loaded
  host) and is an upper bound on the target records lost, not
  a count of them
  (output-contract.md). A reader adds it to the
  lost records only for a worst case; the summary line reports
  it apart, as "skipped runs".

  One `drop` is not host-level: the **attach gap** of a
  pattern-bound session (`-pattern`, M2). Between a matching
  process's exec and the tracer attaching to it, the kernel counts
  the records the process produced but could not trace; the drop
  carries that count, `args.drop_reason` `attach_gap`, and the
  process's `pid`, `pid_incarnation`, `tid` (= pid) and `comm`. It
  is written at the attach, ahead of every traced event of that
  process; its `boot_ns` is the attach time. A JVM's start-up runs
  between exec and its first marker, so the drop lies before the
  scenario's bracket — one inside it means the attach came too
  late. The count is a lower bound (records counted between the
  tracer's read and its attach are not in it), and a gap of 0
  writes no drop.
- **`end`** is the last event; `args.end_reason` is one of:

  | Reason | Meaning |
  |--------|---------|
  | `duration` | the `-duration` bound was reached |
  | `stop` | SIGINT/SIGTERM — a user or controller stop, also one that cuts the final drain short |
  | `targets_exited` | every traced process exited and the stream was drained until quiet |
  | `disk_limit` | the trace disk budget ran out (M4) |
  | `error` | the tracer failed — an events-file write error, the ready file could not be created, the record stream closed, or the loss counters could not be read; the trace is incomplete |

  A trace **without** an `end` event was cut short: the tracer
  crashed, or a write error also lost the end record.
