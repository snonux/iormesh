# Trace schema (v1, frozen outside http)

The field-level reference of the trace output. The contract's
goals and guarantees are in [output-contract.md](output-contract.md)
and PLAN.md ("Trace output contract"); this file says what is on
disk. The Go form — and the authority when this file lags — is the
cgo-free package `internal/schema`.

Version 1 is frozen outside `http` since M2's acceptance (2026-11-19):
spikes S1 (Java ↔ syscall join) and S2 (cross-host join) settled their
fields, and M2 the rest of the traced surface. What M3 still owns
(`http`, an event kind since task 3b4) is marked *provisional*. Fields
are only ever appended, and appended fields are optional, so an older
reader never sees a changed meaning. Enum values (`drop_reason`,
`end_reason`, …) are appended the same way as event kinds, so a reader
should pass an unknown value through. One meaning did narrow:
task vd4 moved skipped program runs out of a plain `drop`'s `args.lost`
into their own `skipped_runs` drops (see `drop` below). M3
appended the optional `otel` object (task 4b4, "OpenTelemetry span
context" below).

## Files

`iormesh run -out DIR` writes a trace directory:

| File | Format | Writer |
|------|--------|--------|
| `events.ndjson` | one event per line (NDJSON) | `schema.NDJSONSink`, as events complete |
| `graph.json` | one graph object, indented JSON | `schema.WriteGraphFile`, at exit |
| `stacks.ndjson` | the native stack table, one stack per line; only with `-native-stacks` | `schema.StackWriter`, one line per new stack |
| `methods.ndjson` | the Java method table; only with `-spool` (M3) | `schema.TableWriter[Method]`, one line per new frame |
| `threads.ndjson` | the Java thread table; only with `-spool` (M3) | `schema.TableWriter[Thread]`, one line per agent THREAD record |
| `*.parquet` | the same events, columnar | M4 segment writer (to come) |

`iormesh normalize -out DIR` writes the same two files for the
normalized trace.

**NDJSON framing**: UTF-8, one JSON object per line, `\n`
terminated, no blank lines, keys in the order of the table below
(byte-stable for the same event). Events appear in `seq` order.
The reader also accepts a final line without `\n` and a `\r\n`
ending, and rejects a line of 1 MiB or more. Linux paths are raw
bytes: a path that is not valid UTF-8 is written with U+FFFD in
place of the invalid bytes — lossy, and a known v1 limit.

**The graph export** is written atomically by `WriteGraphFile`
(synced temp file, mode 0644, renamed into place) with
nodes sorted by `(type, id)` and edges by `(type, from, to)`, so
the same graph is the same bytes.

**Readers are strict** (`schema.DecodeEvent`, `ReadNDJSON`,
`DecodeGraph`): an unknown or mis-cased key, a missing or `null`
required field, another `schema_version`, an unknown event kind or
trailing data rejects the record — never half-read. Writers
validate before writing, so a producer bug fails loudly.

## Event envelope

Required fields are always present; a zero is a real zero.

| Key | Type | Meaning |
|-----|------|---------|
| `schema_version` | int | always `1` |
| `project` | string | the session's project label |
| `seq` | int | per-host sequence number from 1, gap-free except explicit `drop` events |
| `boot_ns` | int | boot-clock time of the syscall's start |
| `wall_ns` | int | the same instant on the wall clock (boot + offset measured once per session) |
| `host_id` | string | `/etc/machine-id` |
| `pid` | int | process id (tgid) |
| `pid_incarnation` | int | process start on the boot clock: a reused pid is another process |
| `tid` | int | thread id; `0` for records with no task context (`tcp_state`) |
| `comm` | string | process name at the event: the kernel's comm (≤ 15 bytes); an exec renames it, so events before the exec carry the old name and the `exec` event and later ones the new |
| `event` | string | the event kind, below |
| `fd` | int | descriptor acted on — the argument, or the returned one for opens and accepts; `-1` when none |
| `fd_gen` | int | how often this fd number was (re)created in this incarnation; `0` = predates the trace or no fd. A forked child continues its parent's count (it inherits the parent's instances) |
| `ret` | int | the syscall's return value |
| `err` | string, optional | errno name (`ENOENT`), only when `ret < 0` |
| `latency_ns` | int | exit minus enter; for a folded `read`/`write` the calls' sum |
| `bytes` | int | bytes moved (read/write); for a folded one the calls' sum |
| `args` | object | syscall-specific arguments, below |
| `conn_id` | string, optional | the connection a socket event belongs to; across hosts assigned by the merge's ISN-pair join (S2, decision #26) |
| `attribution` | string | `java` or `none` — missing attribution is data |
| `java` | object, optional | `{thread, stack, carrier}`, set exactly when attribution is `java`; `thread` is the Java thread name, `stack` method ids of `methods.ndjson`, innermost first (settled by S1); `carrier` (appended in M3) is `true` on a virtual thread's I/O, whose `thread` then names the carrier — see "Java attribution" |
| `http` | object, optional | the request, set exactly on `http` events — see "HTTP events" (*provisional until M3 closes*) |
| `session` | object, optional | the host description, set exactly on `session` events (below) |
| `stack_id` | int, optional | the native user stack the thread entered the syscall with: an id of `stacks.ndjson` (≥ 1); only with `-native-stacks` (below) |
| `otel` | object, optional | `{trace_id, span_id}`, the OpenTelemetry span the Java code ran in; only with attribution `java`, absent outside a span (appended in M3) — see "OpenTelemetry span context" |
| `dns` | object, optional | `{host, addrs}`, set exactly on `dns` events (appended in M3, task wd4; *provisional until M3 closes*) — see "DNS events" |

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
`fcntl(F_SETFD/F_GETFD)`. `close_range` is not traced.

### Args

Every argument is its own optional field — one nullable parquet
column each, never a blob.

| Key | Set on |
|-----|--------|
| `path` | opens and `creat` (required); the M2 path kinds; the old name of the rename/link family (a symlink's text); the fd metadata kinds (the fd's current path); an AF_UNIX `bind`, `connect`, `sendto` or `recvfrom`: the socket path (`@name` for an abstract one) |
| `new_path` | rename/link family (M2): the new name |
| `flags` | opens: the raw open flags; the path kinds whose syscall has a flags argument (`statx`, `newfstatat`, `utimensat`, `fchownat`, `fchmodat2`, `linkat`); `dup3` |
| `cmd` | `fcntl` (M2): the raw command (`F_DUPFD` 0, `F_GETFD` 1, `F_SETFD` 2, `F_DUPFD_CLOEXEC` 1030, …); `epoll_ctl`: the op (`EPOLL_CTL_ADD` 1, `DEL` 2, `MOD` 3) |
| `offset` | positional I/O (planned, not set yet) |
| `local_addr`, `local_port` | socket events, when known; a socket bound to the wildcard address (`0.0.0.0`, `::`) has its port but no `local_addr` |
| `remote_addr`, `remote_port` | socket events, when known; on `sendto`/`recvfrom` the datagram's peer |
| `sock_state` | `tcp_state` (required): `ESTABLISHED`, `CLOSE`, … |
| `marker` | `marker` (required): `<scenario>/<edge>` |
| `lost` | `drop` (required, > 0): records lost since the previous `drop` event of its kind; for `skipped_runs`, program runs skipped host-wide (an upper bound, not lost records) |
| `drop_reason` | `drop`, optional: why the records were lost — `attach_gap` (M2), `unattributed` (a folded read/write no fd instance explains, M2 step 11), `skipped_runs` (program runs the kernel skipped, possible loss); absent for ring-buffer and decoder loss |
| `end_reason` | `end` (required): why the trace ended, below |
| `argv` | `exec` only: the new program's argv, a list of strings, redacted (see Redaction); absent when unreadable |
| `fd2`, `fd2_gen` | a second fd instance (M2 step 7): the other end of `pipe`, `pipe2`, `socketpair`; the fd `epoll_ctl` registers |
| `signal` | the signal number: `kill`, `tgkill`, `tkill` (when the kernel generated a signal) and `signal` (required) |
| `target_pid` | `kill`, `tgkill`, `tkill`: the tgid signalled — raw, the target need not be traced |
| `sender_pid` | `signal` only: the tgid that sent it — raw, the sender need not be traced; absent for a kernel-raised signal |
| `count` | `read`, `write` only, when ≥ 2: how many calls a folded hot loop's event stands for (M2 step 11, below); absent for a single call |

IPv4 peers of dual-stack sockets are written unmapped
(`127.0.0.1`, not `::ffff:127.0.0.1`).

Planned for M4 (S2, decision #26): `tcp_state` gains the four
`tcp_sock` fields the merge rebuilds the ISN-pair key from
(`snd_una`, `rcv_nxt`, `bytes_acked`, `bytes_received`), and
socket send/receive events gain their TCP sequence range (start
sequence plus `bytes`, from new `tcp_sendmsg`/`tcp_recvmsg`
hooks), which the merge's clock bounds match across hosts. Not
in the M1 schema yet.

### Event kinds (M1)

| Kind | Source |
|------|--------|
| `open`, `openat`, `openat2` | syscall pair |
| `read`, `write`, `close` | syscall pair |
| `connect`, `accept`, `accept4` | syscall pair; args carry the 4-tuple |
| `tcp_state` | `sock:inet_sock_set_state`, joined to the owning pid/fd |
| `marker` | an open of `/iormesh-marker/<scenario>/<edge>` |
| `drop` | loss — gaps are explicit: ring-buffer and decoder loss, skipped program runs (possible loss), or a pattern session's attach gap (`drop_reason`) |
| `session` | the per-trace header |
| `end` | why the trace ended (duration, disk limit, stop, targets exited, error) |

The set only grows; M2 appends the rest of the file and socket
surface and the opt-in families (below), M3 `http` (see "HTTP
events").

### Event kinds (M2 file surface)

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

### Folded reads and writes (M2 step 11)

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

### Event kinds (M2 process lifecycle)

| Kind | Source |
|------|--------|
| `exec` | a successful exec of a traced process (`sched_process_exec` plus the argv record): `args.path` is the executable as exec'd (empty when unreadable, then absent), `args.argv` the command line, redacted; `fd` is `-1`, `ret` `0` |

A followed child's exec is one, and so is the exec through which a
`-pattern` session found its app (after that process's
`attach_gap` drop). A process already running when the trace
attached has none, so its argv is not in the trace. `jspawnhelper`
and the program it execs are two `exec` events of one process.
BPF copies the first 1 KiB of the argument block; a longer one is
completed from `/proc/<pid>/cmdline` only when that still starts
with the copied bytes — the process may have exec'd again, or its
PID been reused, by the time userspace reads it. Otherwise the cut
argv is kept, its partial last element masked as `***` (a cut
`https://u:secr` is no longer recognizable as a URL password).
The environment is never captured (decision #9).

### Event kinds (M2 step 7: IPC, signals, UDP, poll, zero-copy)

Each family is decision #7's (`internal/schema/kinds_ipc.go`;
families in `internal/tracepoints/trace_family.go`): `bind`,
`sendto` and `recvfrom` belong to the default-on socket family,
the rest to opt-in ones — `iormesh run -trace-families
ipc,signal,poll,zerocopy` (or `all`) adds them.

| Family | Kinds | fd | args |
|--------|-------|----|------|
| socket | `bind` | the socket | AF_UNIX: `path`; inet: `local_addr`/`local_port` (the bound address; the kernel's port for port 0) |
| socket | `connect` (M1) | the socket | TCP: the 4-tuple; AF_UNIX: `path` |
| socket | `sendto`, `recvfrom` | the socket | `bytes`; per datagram `local_*` and `remote_*` (the peer it went to or came from); AF_UNIX: `path`. On a stream socket (`send`/`recv` on a TCP or unix connection) only the connection's 4-tuple, as `read`/`write` |
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
  `unresolved` — a later accept is a node of its own;
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

### Host-level events: session, drop, end

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
  | `fqdn` | the host's canonical name (`hostname -f`), or its hostname when it has none |
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
  `bpf()` map syscall (PLAN.md, "Syscall handlers run behind
  `raw_syscalls` dispatchers"); a skip of a target's call loses
  the one record, or the one folded call, that run would have
  written. But the skip comes before the program's target
  filter — a dispatcher's before it even looks up the syscall,
  so a skipped dispatcher run is any syscall of any process,
  traced kind or not — and the count includes untraced
  processes' calls (a two-record trace saw 691 on a loaded
  host) and is an upper bound on the target records lost, not
  a count of them
  (PLAN.md, "Trace output contract"). A reader adds it to the
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

## Native stacks (M2 step 9)

`iormesh run -native-stacks` records the user stack each traced
thread had when it entered a syscall; without the flag nothing
below exists and no event has `stack_id` (decision #25: it is
not free). An event's `stack_id` names one line of
`stacks.ndjson` in the same trace directory. A worker writes
each stack as it first appears and the tracer waits for it
before it exits, so a trace that ended normally has every
stack its events name; a crash can lose the stacks still
queued:

```json
{"schema_version":1,"stack_id":3,"pid":4711,"pid_incarnation":893115330000000,
 "frames":[{"addr":"0x408a8e","module":"/usr/bin/echopeer","offset":35470,
            "build_id":"f77d64fd…","symbol":"internal/runtime/syscall/linux.Syscall6+0xe"},
           {"addr":"0x4968e6","module":"/usr/bin/echopeer","offset":616678,
            "build_id":"f77d64fd…","symbol":"syscall.Syscall+0x45"},
           {"addr":"0x7f3c2d41b2a0"}]}
```

| Key | Type | Meaning |
|-----|------|---------|
| `schema_version` | int | always `1` |
| `stack_id` | int | the id events use, from 1, unique in the trace |
| `pid`, `pid_incarnation` | int | whose address space the addresses are in |
| `frames` | array | innermost first: the frame that made the syscall, then its callers (at most 64) |
| `frames[].addr` | string | the instruction address, hex (`0x…`): JSON numbers cannot hold 64 bits |
| `frames[].module` | string, optional | the mapping's path as `/proc/<pid>/maps` names it, or a pseudo mapping (`[vdso]`); absent for anonymous memory — JIT code |
| `frames[].offset` | int, optional | the address's offset in `module`'s file; set exactly for file mappings |
| `frames[].build_id` | string, optional | the module's GNU build id, hex |
| `frames[].symbol` | string, optional | the enclosing function from the module's ELF symbol tables, `name+0xN` (raw, not demangled) |

How it is captured and what it can and cannot say:

- **Kernel**: every emitted syscall enter first sends a small
  record with `bpf_get_stackid(BPF_F_USER_STACK)`'s id into a
  stack map of 8192 buckets (`internal/c/stack.c`). User stacks
  only: at a `sys_enter` tracepoint the kernel stack is the
  syscall entry path. The map deduplicates, so each distinct
  stack is read back once. It never overwrites a bucket, so an
  id stays valid for the session. A stack the map refuses — its
  bucket holds another stack, or the map is full — is sent with
  its frames instead (`bpf_get_stack`, a 552-byte record) and
  deduplicated by them in the tracer, so it gets its `stack_id`
  all the same; collisions grow with the square of the distinct
  stacks, a few per couple of hundred. Only a walk that unwinds
  nothing leaves the event without `stack_id`. The run's
  summary line counts both: `native stacks N (inline I;
  uncaptured M: collisions C, faults F, other O)`.
- **Not every event has one**: calls folded into a `read`/`write`
  with `args.count` (M2 step 11) and a held-back call's enter the
  fold sends later carry none, nor do synthetic events (`drop`,
  `tcp_state`, `signal`, `exec`).
- **Ids are per process image**: the same kernel stack in two
  processes, or before and after an exec, gets two ids, since
  the addresses mean different code. The stack record carries
  the image's `mm->arg_start`, which every exec draws anew and
  `/proc/<pid>/stat` shows as field 48.
- **Symbolization** happens in the tracer. At an image's first
  stack it reads `/proc/<pid>/maps` and believes it only when
  `/proc/<pid>/stat` shows the record's incarnation and image
  tag before and after — so a process that exec'd, exited or
  whose pid was reused before the tracer got to the record
  keeps bare addresses, never another image's names. The maps
  are re-read when a later address lies outside every mapping
  (a `dlopen`); an address that stays uncovered is remembered,
  not re-read for. The mapped files are opened right then
  through `/proc/<pid>/map_files/<range>` — the very file the
  mapping holds, no path lookup the traced process could steer
  — with `O_NONBLOCK` (which stops a FIFO or device from
  blocking, though not a regular file on a hung FUSE or NFS
  mount: such a target can still stall the tracer while
  `-native-stacks` is on, visible as loss), and only regular
  files are kept; a worker
  goroutine parses them (`.symtab`, `.dynsym`, build id) with a
  panic guard, since the files are the traced process's. Parsed
  files are cached by device, inode, size and mtime for the
  session, so a reused inode is another file. A stripped binary
  keeps module, offset and build id, which is what an offline
  symbolizer needs (`addr2line -e FILE OFFSET` after mapping the
  offset through the file's segments). The table forgets a
  process at its exec and exit, so a long session holds only the
  live ones.
- **Walks follow frame pointers**. Go binaries and C built with
  frame pointers (Fedora's default) unwind to `main`; the JVM's
  own C++ frames (libjvm) do too, but interpreted and
  JIT-compiled Java frames lie in anonymous memory, stay raw
  addresses, and the walk through them is unreliable. Java
  attribution comes from the agent (M3, `java.stack`), never
  from here.

## Java attribution (M3)

`iormesh run -spool DIR` joins the I/O of JVMs started with
`-javaagent:iormesh-agent.jar=spool=DIR` to the Java code that
caused it (`internal/correlate`, `JavaJoin`). The tracer creates
`DIR` (mode `1733`: every user may create its ring, none may list
or delete another's); each agent maps one ring there,
`<pid>.ring` (decision #27, `internal/agentspool`; the agent
creates it `O_EXCL`, without following a symlink, mode `0600`). The
tracer opens a ring without following a symlink and checks on the
descriptor, before mapping anything, that it is a regular file with
one link owned by the uid of the process its name names; a file
that fails is reported on stderr and never mapped. After mapping,
the header must name the same pid. A JVM already gone at the
first scan after its ring appeared cannot be checked, so a JVM
that lives less than a scan interval (50 ms) after its agent
installed stays `none`. A ring truncated under the tracer's
mapping (its owner can) faults on access; the reader turns that
into a corrupt ring instead of a crash. The JVM must share the
tracer's pid namespace (containers are M7's).

How an event gets `attribution: java` — never by guessing:

- The agent's hooked JDK methods write a *bracket* (hook, kernel
  tid, enter and exit `System.nanoTime()`, stack id) when they
  return. The agent writes its hook table into the ring at install
  (HOOK records): each hook's native and that native's syscall
  allowlist (`stat`, `lstat` and `fstat` match the kinds
  `newstat`, `newlstat`, `newfstat`).
- The kernel marks the syscall (task td4): once a JVM's ring has
  delivered its hook table, the tracer attaches uprobes to the
  hooks' natives in that JVM's `libjava`/`libnio`/`libnet`
  (scoped to its pid, each native's BPF cookie its id), which
  push and pop a per-thread mark. A syscall a thread enters from
  inside a hooked native carries that native and the native's
  entry time, stamped in CLOCK_MONOTONIC — `System.nanoTime()`'s
  clock — by BPF.
- The event joins the innermost bracket on its tid whose window
  contains the native's entry, no clock conversion; the bracket's
  hook must reach that very native and allow the event's kind,
  and the syscall's start (converted from `boot_ns` with a boot −
  monotonic offset sampled in user space as the midpoint of two
  monotonic reads around a boot read, S1 experiment 2) must not
  lie after the bracket's exit, which defuses a stale mark.
- So JVM work inside a bracket stays `none` whatever its kind: a
  syscall without a mark was issued outside every hooked native
  (class loading reading the runtime image or a JAR's bytes
  through no hooked native, the JVM's own `/proc` reads), and one
  marked with another native was issued by JVM work that called
  it. The allowlist stays as a second guard: another kind under
  the hook's own native is that native's JVM work (a class loaded
  inside `open0`).
- `none` also for: a syscall no bracket contains (JVM startup
  before the agent, JIT/GC threads, JNI, JVM-internal I/O), two
  brackets that overlap without nesting, a folded read/write
  (`args.count`: many calls, which one bracket cannot vouch for),
  a bracket whose stack the agent did not deliver (dropped STACK
  record, or a JVM run without stack walks) — `java` needs a
  stack — the syscalls a JVM issued before its natives were
  probed (a JVM that starts after the tracer: up to one spool scan,
  50 ms, past its agent's install; JVMs already running are probed
  before the tracer's ready file) — and every syscall of a JVM
  whose natives could not be probed (no uprobe_multi links, Linux
  < 6.6; a JDK whose libraries define none of the natives — no
  `Java_*` symbols; a JVM gone before the probe): reported on
  stderr and counted as an unmarked JVM. The mark needs Linux ≥
  6.10, or a 6.6–6.9 stable kernel carrying commit 46ba0e49 ("bpf:
  fix multi-uprobe PID filtering logic"): before it, a pid-scoped
  uprobe_multi link fired only for the thread whose tid equals the
  pid — in a JVM the launcher thread, not even Java's `main` — so
  every other thread's I/O would stay `none`. A JVM whose threads close 64
  brackets after the probe without one mark is reported on stderr
  and counted as an unmarked JVM too.
- **Virtual threads** (JDK 21+): the agent records the carrier tid
  at a bracket's enter and exit, 0 when they differ (such
  brackets are not indexed, so their own syscalls stay `none`).
  Equal tids do not prove the thread stayed: a hook that runs JDK
  code around a native of another class (`NioSocketImpl.accept` →
  `Net.accept`, `ServerSocketChannelImpl.implAccept`: the hook
  table's qualified natives) can park in the poller, let the
  carrier run other virtual threads and resume on the same
  carrier. Such a bracket's window may then hold another virtual
  thread's syscall whose own bracket was lost, so a parking
  hook's bracket of a virtual thread — or of a thread whose
  THREAD record is missing — joins nothing (counted ambiguous);
  hooks whose native is their own class's are pinned to the
  carrier for the native call and join as usual. A joined event
  gets the virtual thread's stack, `carrier: true`, and as
  `thread` the name of the platform thread with that tid — v1
  does not name virtual threads (PLAN.md M3).

Events of a JVM with a ring wait (in seq order, holding the events
behind them) until the ring's WATERMARK is 5 ms past the
syscall's end — a bracket is written when its method returns —
or 2 s after it if the agent stops writing watermarks; an event
of a process without a ring leaves once a spool scan (every 50 ms
and on the run loop's tick) after its syscall found none; a
syscall that ended before its JVM's ring existed is decided at
once. At the end of the trace everything is decided with what has
been read. The summary line counts the rings (and the JVMs whose
natives could not be probed), brackets, joined events (and
carrier ones), why a JVM's other events stayed `none` (unmarked,
other native, unbracketed, other kind, ambiguous, no stack), the events released by the timeout rather than a
watermark, and the agents' own loss counters. A suspend between
a syscall and its join shifts the boot − monotonic offset by the
time suspended; held events are then converted with the new
offset and most likely stay `none` (not handled specially).

`methods.ndjson` — one line per distinct frame, written before the
first event naming it:

```json
{"schema_version":1,"method_id":3,
 "frame":"iormesh.workload.scenarios.MethodFlow$Journal#append(MethodFlow.java:80)",
 "class":"iormesh.workload.scenarios.MethodFlow$Journal","method":"append",
 "file":"MethodFlow.java","line":80}
```

| Key | Type | Meaning |
|-----|------|---------|
| `method_id` | int | the id `java.stack` uses, from 1, unique in the trace |
| `frame` | string | the agent's frame text, `class#method(file:line)` |
| `class`, `method` | string | the frame's binary class name and method name |
| `file` | string, optional | the source file; absent when the class has no source attribute |
| `line` | int, optional | the line; absent when unknown (no line table, a native method) |

A frame is a method *and* a line, so two call sites in one method
are two ids. Ids are keyed by the frame text alone, across JVMs;
JAR and class loader come with the class-load map (a later M3
step), appended as optional fields.

`threads.ndjson` — one line per agent THREAD record, the agent's
own `iormesh-agent-watermark` thread included:

| Key | Type | Meaning |
|-----|------|---------|
| `pid`, `pid_incarnation` | int | the JVM |
| `tid` | int | the kernel tid; `0` if the agent could not look it up; for a virtual thread the carrier of its first bracket only |
| `java_thread_id` | int | `Thread.getId()` |
| `name` | string | the thread's name at its first bracket |
| `virtual` | bool, optional | `true` for a virtual thread |

Both tables carry `parquet:` tags for the M4 segment writer
(`methods` and `threads` files, PLAN.md M3); until it exists they
are NDJSON only.

## HTTP events (M3)

With `-spool`, the agent's HTTP hooks (decision #22, M3 step 7)
add one `http` event per request a JVM made or answered through
the JDK's HttpServer, HttpClient (`send`) or HttpURLConnection.
It is not a syscall: it is stamped when the request completed —
the server's exchange was closed by the handler (or the handler
returned without closing it), the client's `send` returned, the
connection's response was read — on the thread that did it, with
its stack as `java` (the handler's or the caller's method; the
JDK's dispatch when the handler never closed the exchange). `fd`
is `-1`; `ret` and `bytes` are 0.

Since task wd4 the event spans its request like a syscall: `boot_ns`
is when the library began it and `latency_ns` the time to its
completion, so `boot_ns + latency_ns` is the completion stamp. The
start is the entry of the call that began the request on the
recording thread — HttpClient's `send`; HttpURLConnection's
response read (`getInputStream0`, outermost), so a request body the
app uploaded earlier through `getOutputStream` is not part of the
latency; for the server the start of the exchange's task
(`ServerImpl$Exchange.run`, when an executor thread took it up — the
time the task waited in the executor's queue is not included) — and
only when the hook can tie that entry to this very request;
otherwise `boot_ns` is the completion and `latency_ns` 0 (a server
exchange closed on another thread than its dispatch, and every
`sendAsync` request, below). The body sizes go into the `http`
object.

```json
{"event":"http","fd":-1,"conn_id":"cb061683df650ac2","attribution":"java",
 "java":{"thread":"main","stack":[220,221]},
 "http":{"method":"GET","scheme":"http","host":"127.0.0.1",
  "path":"/redact/login?user=***&token=***","status":200,"req_seq":1,
  "version":"HTTP/1.1","req_headers":["authorization: ***","x-api-token: ***"],
  "resp_headers":["content-length: 7","set-cookie: ***"]}}
```

| `http` field | Type | Meaning |
|---|---|---|
| `method`, `scheme` | string | as the library had them; `scheme` `https` over TLS (seen at the API boundary, never on the wire) |
| `host` | string, optional | the request URI's host — client side only |
| `path` | string, optional | path and query, every query (and `;`/`#` parameter) value masked; path segments verbatim (#22's default) |
| `status` | int, optional | the response's status; absent when none came |
| `req_seq` | int, optional | the request's ordinal on its connection, 1-based, the same on both ends — see below |
| `version` | string, optional | `HTTP/1.1` or `HTTP/2` (appended in M3) |
| `req_headers`, `resp_headers` | list of string, optional | `name: value`, names lower-cased, sorted, at most 32 each, redacted (see "Redaction"); HttpURLConnection's request line is no header and never recorded (appended in M3) |
| `req_bytes`, `resp_bytes` | int, optional | the request's and the response's body size in bytes as the library knew it — absent when it did not, never estimated (appended in M3, task wd4; see below) |

Body sizes come from what the library itself knows and enforces:
HttpClient's request publisher length (absent for a streamed body)
and the response's `Content-Length`; HttpURLConnection's count of
the bytes the app wrote (its buffered or streaming output) and the
response's `Content-Length`; HttpServer's request `Content-Length`
as the client declared it (absent when chunked) and the length the
handler passed to
`sendResponseHeaders` (absent for a chunked reply). A reply without
a body by protocol (HEAD, 1xx, 204, 304) is 0; a response without
`Content-Length` (chunked, or read to the close) has no size.

The event goes into the stream after the events that completed
before it: it waits until an event that completed 5 ms after it
has reached the joiner, or 2 s; the trace's `end` stays last.
`conn_id` is the connection whose socket ends match the ones the
library still held (both, else the remote end — a server's local
end is unreadable once the close shut the connection — else the
local one; a client knows the remote end only for an IP-literal
host), and only when exactly one connection of the process is
known on those ends: a client's two connections to one server
share the remote end, so such a request keeps no `conn_id`.

The tracer numbers a connection's requests per process and end
(client or server — a same-process loopback call has both on one
`conn_id`) in completion order (`req_seq`), so the client's and the
server's event of one HTTP/1 request pair by (`conn_id`,
`req_seq`). It leaves `req_seq` out

- over HTTP/2 (multiplexed, decision #22), and without a `conn_id`;
- for every request of a JVM that may have lost an HTTP record —
  dropped by the ring, abandoned, undecodable, or a hook call that
  failed or was skipped (the agent counts those as dropped);
- for every java.net.http (HttpClient) client request of a JVM that
  completes after the JVM's first `sendAsync` call: an async request's
  HTTP/1.1 connection goes back to the pool once its body is read,
  while its record waits for the agent's completion (which runs after
  the app's own callbacks), so another request reusing that connection
  can be recorded first and would take its ordinal. The agent writes a
  marker at the `sendAsync` call, before the request can reach a
  connection; from its stamp on the tracer numbers no java.net.http
  client end of that JVM, on any connection (requests completed before
  it keep their numbers; HttpURLConnection and server ends are
  unaffected). A java.net.http request is numbered only once the JVM's
  watermark passed its stamp — proof that no earlier marker is still
  unread — so one released by the timeout gets none;
- from the first request on a connection that took several HTTP
  exchanges on: HttpClient following a redirect or answering a
  401/407 through its authenticator, HttpURLConnection doing the
  same (an interim `100 Continue` counts too) — the server saw
  each exchange as a request, the caller one, and the ends' counts
  no longer agree.

The limit: completion order is the agent's stamp at the hook, so two
requests of one connection that complete within the stamps'
resolution on different threads (pipelining clients answered out of
the scheduler's order) could swap ordinals. HTTP/1.1 clients send the
next request on a connection only after the previous response, which
rules that out for the JDK's synchronous calls (`send`,
HttpURLConnection, the server's exchanges) — but not for `sendAsync`,
whose record is not stamped at its response: hence the rule above
that stops a JVM's java.net.http client numbering at its first
`sendAsync`.

The summary line counts the http events and those without a
connection. `HttpClient.sendAsync` (task wd4) is recorded when the
agent's completion of its future runs — on the client's executor —
but under the sending thread: its `tid`, `java.thread` and stack
(the method that called `sendAsync`) and the OTEL span current at
the call; a failed or cancelled future records nothing, like a
`send` that throws. A future runs its callbacks last-registered
first, so the agent's (registered first) runs after the app's own
callbacks and `get()`'s waiter: its stamp is not when the response
arrived. Such an event therefore has no latency and no `req_seq`,
and its connection's client end numbers no more requests from it on
(the server end still does). Not recorded in v1: the other libraries of #22's
set.

The agent writes each request as an HTTP record of the ring
(format version 6: exit stamp, stack id, Java thread id, kernel
tid, status, the string and request-header counts, flags — server
or client, unordered (a `sendAsync` completion), java.net.http, or
a `sendAsync` marker that is no request — the exchange count, the
OTEL span ids, the start stamp and the two body sizes, then
method, scheme, host, path, version, local and remote socket
address, and the headers), redacted before it is written; a record
that would pass 16 KiB keeps the request and drops its headers.

## DNS events (M3)

With `-spool`, the agent's DNS hook (decision #8's default, task
wd4) adds a `dns` event when a JVM resolves a host name to a set
of addresses it had not mapped that name to before: the first
lookup of a name, and again whenever its address set changes
(compared as a set, so a resolver rotating the order is no
change). The agent remembers 4096 names per JVM; past that it
starts afresh, and a mapping it had recorded before is recorded
once more. The hook is `InetAddress.getAllByName(String)`, through
which `getByName`, `InetSocketAddress(host, port)` and so the
JDK's sockets, HttpClient and HttpURLConnection resolve. IP
literals (parsed, never looked up), the empty host and failed
lookups are not recorded; reverse lookups are not hooked.

```json
{"event":"dns","fd":-1,"attribution":"java",
 "java":{"thread":"HttpClient-1-Worker-0","stack":[139,140]},
 "dns":{"host":"localhost","addrs":["127.0.0.1","0:0:0:0:0:0:0:1"]}}
```

| `dns` field | Type | Meaning |
|---|---|---|
| `host` | string | the name as the app passed it |
| `addrs` | list of string | the addresses, in the resolver's order (the JDK connects to the first), textual — IPv6 in the JDK's full form; at most 32 |

The event is stamped when the lookup returned, on the thread and
stack that asked (`latency_ns` 0), and goes into the stream like
an `http` event, after the events that completed before it. It
names no connection: a view pairs a connection's remote address
with the latest mapping of the same process that contains it. In
the ring a mapping is a DNS record (format 5 on: exit stamp, stack
id, Java thread id, kernel tid, string count, OTEL span ids, then
host and addresses); one the ring dropped is counted (`agent
lost`) and not sent again. The summary line counts the dns
events.

## OpenTelemetry span context (M3)

When a JVM traced with `-spool` runs the OpenTelemetry API (with
or without an SDK, decision #20's default), each event joined to
the agent gets the span that was current on its Java thread:

```json
{"event":"write","fd":6,"attribution":"java",
 "java":{"thread":"main","stack":[31,32]},
 "otel":{"trace_id":"a3ce929d0e0e47364bf92f3577b34d01",
  "span_id":"c11e0000000000a1"}}
```

| `otel` field | Type | Meaning |
|---|---|---|
| `trace_id` | string | the W3C trace id, 32 lowercase hex digits, never all zero |
| `span_id` | string | the W3C span id, 16 lowercase hex digits, never all zero |

- A syscall gets the span current when its bracket opened (the
  hooked JDK method's entry); an `http` event the span current
  when the request was recorded (the server's exchange closed,
  the client's response read). Events outside any span, events
  that stay `none`, and every event of an app without OTEL carry
  no `otel`.
- The ids are what the app's `Span.current().getSpanContext()`
  returns, read reflectively — the agent has no OTEL dependency —
  so iormesh events join the app's distributed traces in any OTEL
  backend by these two ids. They are span metadata, not payload;
  spans themselves are not ingested (#20's optional half).
- The agent starts reading a copy of OTEL (one per class loader)
  only after the app initialized OTEL's context storage, and does
  not initialize it itself (one exception: a live attach that
  catches a copy between loading and initializing it initializes
  it on the attaching thread); I/O before the app's first use of an
  OTEL context carries no ids, which is right — no span can be
  current then.
- In the ring (format 4, and 5 since) a bracket inside a span is 72 bytes
  instead of 48 (trace id as two u64 halves, span id as one u64);
  the HTTP record always carries the three words, zero for none.

## Graph

```json
{"schema_version": 1,
 "nodes": [{"type": "process", "id": "proc:<host>/<pid>@<inc>",
            "attrs": {"comm": "java"}}],
 "edges": [{"type": "connected", "from": "proc:…", "to": "conn:<id>",
            "state": "fused"}]}
```

| Node | Id |
|------|----|
| `host` | `host:<host_id>` |
| `process` | `proc:<host_id>/<pid>@<pid_incarnation>` |
| `file` | `file:<host_id>:<path>` — the path exactly as the open event reports it, or the new name of a rename that moved an open fd |
| `socket` | `conn:<conn_id>` — one node per connection, both ends: a TCP or unix stream connection, a UDP flow, a pipe or socketpair (M2 step 7) |
| `app` | `app:<project>/<pattern>` — the identity of a pattern-bound session's app (M2): every process incarnation the pattern matched links to it, so a restarted app is a new process node under the same app node |

| Edge | From → to | State |
|------|-----------|-------|
| `runs_on` | process → host | — |
| `opened` | process → file: opened under that name, or held open while a rename moved it there | — |
| `connected` | process → socket | `fused` (both ends traced and joined) or `unresolved` |
| `incarnation` | app → process | — |
| `forked` | process → process: a traced process forked the child (M2) | — |
| `signaled` | process → process: a traced process sent the other a signal (M2 step 7, signal family) | — |

Node attributes stay minimal (`comm` on processes): raw pids or
ports in attributes would survive normalization and make two runs'
graphs differ. A process node's `comm` is the process's newest
name: an exec does not make a new incarnation, so a forked child
that execs keeps its node, and the node is named after the program
it runs, not after its parent; the events keep each moment's
name. An `app` id names nothing run-specific (project and
pattern), so the normalizer passes it through unchanged and two
runs — or two hosts — name the same app the same way; goldens bind
it with `$a1`. A `-pid` or `-listen-port` session has no app node.

## Redaction

One policy, `internal/redact` (`redact.Default()`), masks secrets
before an event exists — no queue, sink or later stage holds the
raw value (PLAN.md decisions #9 and #22). A masked value is `***`
and the structure stays:

| Input | Recorded |
|-------|----------|
| `-Dpassword=s`, `--client-secret=s`, `DB_PASSWORD=s` | `-Dpassword=***`, `--client-secret=***`, `DB_PASSWORD=***` |
| `--token s`, `-secret s` (option without `=`) | `--token ***` — the next element is masked, whatever it is |
| `mysql -ps`, `curl -u bob:s`, `redis-cli -a s` | `-p***`, `-u ***`, `-a ***` — only for those programs |
| `https://bob:s@h/p?k=s` (anywhere in an argument) | `https://bob:***@h/p?k=***` |
| `jdbc:sqlserver://h;user=u;password=s` | `jdbc:sqlserver://h;user=***;password=***` |
| `https://h/cb#access_token=s` | `https://h/cb#access_token=***` — fragment parameters too |
| `--token s` in one element; `sh -c "mysql -ps"` | `--token ***`; `mysql -p***` — several words are redacted word by word, separators and quoting kept |
| `curl -H "Authorization: Bearer s"`, `--header=Cookie: s`, `--api_key:s`, `pass:s` | `Authorization: ***`, `--header=Cookie: ***`, `--api_key:***`, `pass:***` |
| `sudo -u root mysql -ps`, `env X=1 mysql -p s`, `docker login -p s` | `-p***`, `-p ***`, `-p ***` — a flag rule applies after any word naming its program |

Keys match case-insensitively with `-`, `_` and `.` ignored, so
one glob covers every spelling. The argv denylist names
`password`, `passwd`, `passphrase`, `pwd` and `pass` (suffixes),
`passin` and `passout` (openssl), `secret`,
`token`, `apikey`, `accesskey`, `privatekey`, `credential`,
`authorization`, `bearer` and `cookie` anywhere in a key; URL
userinfo is always masked, query values all (decision #22's
default). It is best effort: a secret under an innocuous name or
as a bare positional argument is not recognized, a quoted
value with spaces inside a shell string is masked only up to the
quote's end as a shell would split it, and the
defaults over-mask rather than leak (`--password-stdin` masks the
argument after it). HTTP headers (M3) use the same package:
`authorization`, `proxy-authorization`, `cookie`, `set-cookie`
and names containing `token`, `apikey`, `secret`, `password`,
`session` or `signature` are recorded as `***`, URLs in the other
headers' values and every query value of the path are masked;
`location`, `content-location`, `referer` and `origin` are masked
as a URL even when relative (`/next?token=***`).
The Java agent applies a copy of these header and query rules
(`iormesh.agent.http.Redaction`) before a request reaches the
spool, so a secret never lands on disk, and the tracer applies
`internal/redact` again to every http event it writes.

## Parquet mapping

To come with the M4 segment writer: one column per envelope field
and per `args` field (the `parquet:` tags in `internal/schema`),
zstd page compression, minute-rotated segments (PLAN.md decision
#16).
