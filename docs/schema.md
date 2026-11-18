# Trace schema (v1 draft)

The field-level reference of the trace output. The contract's
goals and guarantees are in [output-contract.md](output-contract.md)
and PLAN.md ("Trace output contract"); this file says what is on
disk. The Go form — and the authority when this file lags — is the
cgo-free package `internal/schema`.

Version 1 is a **draft**: spikes S1 (Java ↔ syscall join) and S2 (cross-host
join) have reported and settled their fields; what M3 still owns (`http`) is
marked *provisional*. Fields are only ever appended, and appended fields are
optional, so an older reader never sees a changed meaning.

## Files

`iormesh run -out DIR` writes a trace directory:

| File | Format | Writer |
|------|--------|--------|
| `events.ndjson` | one event per line (NDJSON) | `schema.NDJSONSink`, as events complete |
| `graph.json` | one graph object, indented JSON | `schema.WriteGraphFile`, at exit |
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
| `comm` | string | process name |
| `event` | string | the event kind, below |
| `fd` | int | descriptor acted on — the argument, or the returned one for opens and accepts; `-1` when none |
| `fd_gen` | int | how often this fd number was (re)created in this incarnation; `0` = predates the trace or no fd. A forked child continues its parent's count (it inherits the parent's instances) |
| `ret` | int | the syscall's return value |
| `err` | string, optional | errno name (`ENOENT`), only when `ret < 0` |
| `latency_ns` | int | exit minus enter |
| `bytes` | int | bytes moved (read/write) |
| `args` | object | syscall-specific arguments, below |
| `conn_id` | string, optional | the connection a socket event belongs to; across hosts assigned by the merge's ISN-pair join (S2, decision #26) |
| `attribution` | string | `java` or `none` — missing attribution is data |
| `java` | object, optional | `{thread, stack}`, set exactly when attribution is `java`; `thread` is the Java thread name, `stack` method-table ids, innermost first (settled by S1) |
| `http` | object, optional | request fields on http events (*provisional until M3*) |
| `session` | object, optional | the host description, set exactly on `session` events (below) |

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
| `lost` | `drop` (required, > 0): records lost since the previous `drop` event |
| `drop_reason` | `drop`, optional: why the records were lost — `attach_gap` (M2); absent for ring-buffer and decoder loss |
| `end_reason` | `end` (required): why the trace ended, below |
| `argv` | `exec` only: the new program's argv, a list of strings, redacted (see Redaction); absent when unreadable |
| `fd2`, `fd2_gen` | a second fd instance (M2 step 7): the other end of `pipe`, `pipe2`, `socketpair`; the fd `epoll_ctl` registers |
| `signal` | the signal number: `kill`, `tgkill`, `tkill` (when the kernel generated a signal) and `signal` (required) |
| `target_pid` | `kill`, `tgkill`, `tkill`: the tgid signalled — raw, the target need not be traced |
| `sender_pid` | `signal` only: the tgid that sent it — raw, the sender need not be traced; absent for a kernel-raised signal |

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
| `drop` | loss — gaps are explicit: ring-buffer and decoder loss, or a pattern session's attach gap (`drop_reason`) |
| `session` | the per-trace header |
| `end` | why the trace ended (duration, disk limit, stop, targets exited, error) |

The set only grows; M2 appends the rest of the file and socket
surface and the opt-in families (below).

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
  undecodable — since the previous `drop`. `iormesh run` polls
  the counters every 200ms — also while it drains the targets'
  last records — and once more just before it detaches, so a
  `drop` follows its gap within a poll interval; it is never
  written with `lost` 0. No `drop` event means no loss was
  counted. Records lost after that last poll, while the probes
  detach, are counted nowhere (a known M1 limit).

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
graphs differ. An `app` id names nothing run-specific (project and
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
`session` or `signature` are recorded as `***`.

## Parquet mapping

To come with the M4 segment writer: one column per envelope field
and per `args` field (the `parquet:` tags in `internal/schema`),
zstd page compression, minute-rotated segments (PLAN.md decision
#16).
