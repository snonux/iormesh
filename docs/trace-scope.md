# Trace scope — what we trace and why

The mental-model inventory: an app needs more than file and
socket events to be understood. The decisions it cites are in
[decisions.md](decisions.md); the events it produces are described
in [output-contract.md](output-contract.md) and
[schema.md](schema.md).

Metadata only — nothing below is payload. Not all of it is on
by default (decision #7): files, sockets (TCP, unix, UDP) and
the lifecycle are; IPC, signals, poll/epoll and zero-copy are
event families `iormesh run -trace-families` adds (see
[schema.md](schema.md)).

- **Identity & lifecycle** — exec (exe, argv — redacted by
  policy), fork/clone, thread create/exit, comm/thread renames;
  PID incarnation ids so a reused PID is never confused with the
  old one. Sessions bound to a
  process-name pattern auto-detect restarts and follow the app
  to its new PID. Sessions bound to a listening port
  (`-listen-port`) follow the port to a restarted owner, within
  `-listen-grace`, if it runs the original owner's executable
  under a uid one of the original holders had, and still is the
  process the port listing saw (decision #23). A
  privilege-separated server — a root master and workers of the
  same binary under their own uid — is followed whole; another
  program that only inherited the listener fd is not. A restart
  onto an upgraded path (a JDK in a versioned directory,
  `.../25.0.5/bin/java` to `.../25.0.6/bin/java`) is followed
  if the executable keeps its base name, the uid is the same and
  it runs in the original's cgroup — its systemd unit or login
  session scope (no escalation: the uid must match); without a
  known cgroup only the exact path is followed (task kk4). A
  socket-activated port that only systemd holds — the service is
  idle, not running — starts a session with no target, also
  under `expand -apply` (task lk4): systemd itself is never a
  target, since tracing it would follow every service it spawns.
  The session pins the listening socket systemd holds and waits
  up to `-listen-grace` for the service systemd activates: a
  process that holds that very socket and runs in a `.service`
  unit of that manager (pid 1's units, or below a user manager's
  `user@UID.service` and running as that user, task el4) is
  adopted, and from then on followed like a start owner. A shell,
  another user's process or another unit's process holding a copy
  of the fd is refused and named on stderr. No activation within
  the grace ends the session; `-listen-grace 0` refuses the port
  at start as before. Not covered: `Accept=yes` sockets (each
  instance gets a connection, never the listener) and a socket
  unit restarted while the session waits. *Who exists, spawned
  by whom, for how long.*
- **Filesystem** — the full syscall surface, not just data
  movement: open/close, read/write/pread, stat family, create
  (open O_CREAT, creat, mkdir), delete (unlink/rmdir), rename &
  move (renameat2, cross-directory and cross-device),
  touch-style timestamps (utimensat/futimens), symlinks
  (symlink/readlink, lstat's no-follow distinction), hard links,
  truncate, chmod/chown, xattrs, zero-copy paths
  (sendfile/splice/copy_file_range). File mmaps (which JARs,
  libs, data files are mapped) are not traced yet.
  *What data is touched — and what happened to it structurally:
  files created, deleted, renamed, relinked, touched.*
- **Descriptor lifecycle** — dup/dup2/fcntl(F_DUPFD), fd
  inheritance across fork/exec, per-fd generation numbers.
  *Which fd is really which file/socket after aliasing or reuse.*
- **Network** — TCP lifecycle with full 4-tuples, read from the
  socket's state transitions rather than syscall arguments (so
  Java's non-blocking connects are covered), plus a
  NAT-tolerant join key (decision #26); UDP per-packet
  peers (sendto/recvfrom), the msghdr data calls
  sendmsg/recvmsg/sendmmsg/recvmmsg with their bytes (the mmsg
  calls' summed over their messages) and TCP sequence ranges
  (task oh4) and, on a datagram socket, their peer from the
  msghdr's `msg_name` — the mmsg calls' first message's, one
  per call (task nk4); their 8 handlers are on by default, and a
  sendmmsg/recvmmsg exit pays up to 1024 user-memory reads per
  call, one per message — heavy for UDP workloads that batch
  with them — poll/epoll wait registrations, DNS hostname↔IP
  maps (from the agent's hooks, decision #8), and ingress route
  maps for proxy hops that hide the peer. TLS
  peer identity is later (decision #8). *Who talks to whom,
  under what name — and which threads wait on what.*
- **HTTP/HTTPS** — method, scheme, host, path/URI, status,
  sizes, latency per request — captured at the Java HTTP
  client/server API boundary by the agent (so HTTPS included,
  nothing sniffed off the wire), sensitive headers and query
  params redacted by policy, path segments verbatim (decision
  #22; the controller's per-path counts fold ids into
  templates, task xh4). *What is actually
  said, at the level the mesh routes on.*
- **Local IPC** — pipes and socketpairs (both ends), unix stream
  connections (paired by the kernel), loopback TCP pairs and
  UDP flows joined locally. eventfd and shared memory are not
  traced yet. *Single-host edges a "mesh" would otherwise
  miss.*
- **Signals** — delivery with sender PID. *Who touched the
  process — e.g. which supervisor sent the SIGTERM.*
- **Java** — class↔JAR map, thread names (kernel TID ↔ Java
  thread), the Java stack at every I/O boundary as
  `class#method(file:line)` frames (line numbers when debug
  info is present), and the active OTEL span (trace/span ids) when the app is
  OpenTelemetry-instrumented. The agent instruments only the
  points where Java hands I/O to the kernel — never every
  method — so its cost follows the I/O rate. *The
  source-level truth every event above is annotated with.*
- **Non-Java processes** — traced in a degraded but explicit
  shape: files, sockets, IPC, signals and lifecycle, optional
  native stacks; no Java stacks, no http events, no
  agent-sourced hostnames. Their events are marked
  unattributed, so "not a JVM" never reads as "nothing
  happened". *Real meshes end at nginx, a database or a
  sidecar — the graph says so honestly.*

## Local edges

How the local IPC above becomes edges in the graph (M2 step 7): a
pipe or socketpair is one connection node, fused at creation,
joined by whoever inherits an end; a unix stream connection fuses
through the kernel's own pairing of its two sockets; UDP has no
connection, so its unit is the flow — one local/remote address
pair — whose two loopback sides fuse like TCP's ends, while a flow
to another host stays unresolved. A signal one traced process sends
another is a `signaled` edge; a kernel-raised one (timer, SIGCHLD)
has no sender.

## Not traced as built

The inventory is the target. Not traced as built (2026-11-22;
`internal/tracepoints`, default_set.go and trace_family.go): file
mmaps, eventfd and shared memory (no probes); TLS peer identity
(later, decision #8). The peer address inside a sendmsg/recvmsg
msghdr is traced since task nk4 (see the network family above).

## The end-goal picture

One level up from the inventory: a complete picture of how every
service talks to every other service — every edge, in the order
it happened, attributed to the Java threads that drove it, and
interleaved with those threads' local file I/O. The graph is the
source; the sequence view and the controller's
service-interaction database are the renderings of it (see
[milestones.md](milestones.md), M4/M5, and
[output-contract.md](output-contract.md)).
