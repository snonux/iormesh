# Trace scope — what we trace and why

The mental-model inventory: an app needs more than file and
socket events to be understood. Companion to [PLAN.md](../PLAN.md)
("What we trace") — the plan is the source of truth.

Metadata only — nothing below is payload. Not all of it is on
by default (decision #7): files, sockets (TCP, unix, UDP) and
the lifecycle are; IPC, signals, poll/epoll and zero-copy are
event families `iormesh run -trace-families` adds (see
[schema.md](schema.md)).

- **Identity & lifecycle** — exec (exe, argv — redacted by
  policy), fork/clone, thread
  create/exit, thread renames; PID incarnation ids so a reused
  PID is never confused with the old one. Sessions bound to a
  process-name pattern auto-detect restarts and follow the app
  to its new PID. *Who exists, spawned by
  whom, for how long.*
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
  NAT-tolerant join key; UDP per-packet
  peers (sendto/recvfrom; sendmsg and its kin are planned, task
  oh4), poll/epoll wait registrations, DNS hostname↔IP maps,
  and ingress route maps for proxy hops that hide the peer. TLS
  peer identity is later (decision #8). *Who talks to whom,
  under what name — and which threads wait on what.*
- **HTTP/HTTPS** — method, scheme, host, path/URI, status,
  sizes, latency per request — captured at the Java HTTP
  client/server API boundary by the agent (so HTTPS included,
  nothing sniffed off the wire), sensitive headers and query
  params redacted by policy, path segments verbatim (decision
  #22; templating is planned, task xh4). *What is actually
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

## The end-goal picture

One level up from the inventory: a complete picture of how every
service talks to every other service — every edge, in the order
it happened, attributed to the Java threads that drove it, and
interleaved with those threads' local file I/O. The graph is the
source; the sequence view and the controller's
service-interaction database are the renderings of it (see
PLAN.md, M4/M5 and "Trace output contract").
