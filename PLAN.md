# iormesh — project plan

Working plan for iormesh, the BPF-based process observability mesh.
This file tracks scope, milestones, and open decisions. Code lands
only once a milestone is picked up; nothing here is implemented yet.

## Purpose

Map what a PID touches (files, sockets), follow each connection
across the wire to its other side, and trace Java-level code flow
inside the process — correlated into one queryable graph:

hosts ↔ PIDs ↔ sockets ↔ remote PIDs ↔ files ↔ Java method stacks.

The end-goal reads one level up: a complete picture of how
every service talks to every other service — every edge,
in the order it happened, attributed to the Java threads
that drove it — and interleaved with the local file I/O
those same threads performed: which files they opened, what
was read from or written to them. The graph is the source;
the service-level sequence view (M5) and the interaction
database (M4) are the renderings of it.

## Guiding principles

1. **The test workload comes first — per milestone.** We build a
   purpose-built Java test app to be traced, and every tracer
   feature is validated against it *before* iormesh ever attaches
   to a live application. No live app is the first test subject —
   not in spirit, not ever. M0 is the framework, not the whole
   catalogue: each milestone lands its own scenarios before its
   tracer code.
2. **Read-only tracing.** eBPF and the Java agent observe; they
   never change what the target does or the data it handles.
   Observation is not free, though: probes cost time and the agent
   rewrites bytecode at I/O boundaries, so the overhead the target
   pays is budgeted and measured (decision #25). "Read-only" is
   about function — never an excuse to ignore cost.
3. **Expansion is opt-in, bounded and controller-driven.** The
   mesh grows along observed connections only from the controller,
   with the operator's SSH key, against an explicit allowlist,
   with a hop limit and a node cap checked before anything is
   deployed. Tracers hold no credentials and open no connections.
4. **One graph, many vantage points.** Every milestone must end with
   events landing in the same correlated dataset — never a
   per-component output format that needs re-joining later.
5. **Reuse ior's machinery — BPF/C and tests.** The
   code-generation pattern — generated tracepoint handlers,
   generated Go tables, diff-gated results, CO-RE embed, Mage +
   Docker generation host — and the integration-test suite are
   ior's, proven and pinned by its tests. iormesh copies/adapts
   them instead of inventing its own (details below; whoever
   implements this should look at `../ior` for inspiration
   first — see "BPF/C code generation" and "Integration tests
   — follow ior"). Reuse has an edge, though: what ior does
   *not* provide is listed there too, and is planned as new
   work.
6. **The trace is a document, not a dump.** Outputs follow a
   versioned, self-describing schema with stable ids and explicit
   loss accounting — precise enough for an LLM to reconstruct
   behavior and check it against source code, and, once
   normalized, deterministic enough to diff two runs. A trace is
   too big to read whole, so it is sliceable by query; and the
   LLM claim is tested by an eval (M5), not asserted.
7. **Risk first.** The mechanisms the whole project stands on —
   the Java↔syscall join, the cross-host join, the overhead
   budget — are proven by spikes before milestones depend on
   them, and a walking skeleton (M1) fuses one edge end to end
   before the traced surface widens.

## Milestones

Milestones are numbered in execution order. (Renumbered on
2026-11-17 — for readers of older commits: old M1 is now M1 + M2,
old M2 is M3, old M6 is M4, old M3 + M4 merged into M6; M0, M5
and M7 kept their numbers.)

| Milestone | What lands                                  | Gated by |
|-----------|---------------------------------------------|----------|
| M0        | Test workload framework                     | —        |
| M1        | Walking skeleton: one fused edge end to end | —        |
| M2        | Trace a PID: the full surface               | S3       |
| M3        | Java agent: code-flow attribution           | S1       |
| M4        | Controller: push, collect, merge            | S2       |
| M5        | Views, query and the LLM eval               | —        |
| M6        | Mesh expansion, controller-driven           | —        |
| M7        | Kubernetes                                  | —        |

How to build each of these — files, order of work, code
sketches, tests — is in the
[implementation guide](docs/implementation.md). The plan decides;
the guide shows how.

### Spikes — feasibility before commitment

Three mechanisms carry the project and none of them exists in ior.
Each gets a time-boxed spike that ends in a written result (a
short doc under `docs/spikes/`) — either "works, here are the
numbers" or "needs this other design". A spike's prototype code is
throwaway; its findings update this plan.

- **S1 — Java↔syscall join** (gates M3, and the `java_stack`
  fields of the envelope, which stay provisional until then).
  Questions:
  - Hook set: which JDK I/O boundary methods (file streams and
    channels, `Files` operations, socket and channel
    implementations, the NIO dispatchers) bracket the syscalls of
    the M0 scenarios, and which I/O stays unbracketed (JNI
    libraries, JVM-internal I/O).
  - Join key. Front-runner, shown to work in a first
    experiment (2026-11-18, JDK 25, bpftrace): uprobes on the
    JDK's native I/O functions (`libjava`/`libnio`/`libnet`)
    mark the thread id on entry and clear it on return, and
    the syscall handler reads the mark — an exact, in-kernel
    join by TID with no clocks involved, on platform and
    virtual threads alike. It says *which native I/O call*
    issued a syscall, not which Java method or virtual thread:
    that still needs the agent, or an eBPF HotSpot unwinder —
    how the two meet is the open question. Fallbacks: fd plus a
    time window; kernel TID (`/proc/thread-self`) plus a time
    window.
  - Clocks: the agent's clock is monotonic, ior's BPF records
    use the boot clock — they differ by time spent suspended.
    Verify the conversion's error is far below a syscall's
    duration.
  - Transport: how agent events reach the tracer without a
    socket (decision #27), and how the agent's own I/O is kept
    out of the target's story.
  - Attach: `-javaagent` vs live attach under JDK 21's
    dynamic-loading rules (decision #2).
  - Cost: overhead per I/O operation on the M0 workload.

  Exit: a prototype attributes every declared I/O op of one M0
  file scenario and one socket scenario to its declared method,
  on platform threads, with the overhead measured. (Virtual
  threads are deferred — see "Deferred — not in v1".)
- **S2 — Cross-host join** (gates M4's merge). Questions:
  - The NAT-tolerant join key (decision #26): conntrack state
    read on the NAT host vs the TCP initial-sequence-number pair
    captured at both ends, which NAT leaves untouched.
  - Clock offsets from the traffic itself: every fused
    connection yields send-before-receive constraints in both
    directions, which bound the offset between the two hosts —
    how tight are the bounds in practice?

  Exit: two tracers in separate network namespaces with DNAT
  between them and an injected clock skew (test-only flag)
  produce one fused edge and recover the skew within the bound.
- **S3 — Overhead and volume budget** (gates M2; pins decision
  #25). Measured on the M1 skeleton with ior's `perf/` +
  bench-profiles pattern: tracer CPU, the cost untraced
  processes on the host pay, ring-buffer loss rate, and trace
  bytes per minute.

  Exit: the budget's numbers are written into decision #25 and
  become acceptance criteria from M2 on.

### M0 — Traceable test workload (Java): framework first

A deterministic Java 21 app whose scenario behavior we can state
in advance. It is the Java sibling of ior's `cmd/ioworkload`,
which exists to make specific syscalls fire on demand.

M0 delivers the framework and the scenarios M1 needs. The rest of
the catalogue lands with the milestone that needs it — always
before that milestone's tracer code (principle #1).

- **Framework**: a scenario runner; client and server modes, so
  socket scenarios run between two processes on loopback or two
  test hosts; packaged as a plain JVM app (the container image
  arrives with M7).
- **Scenario markers**: every scenario brackets itself with a
  begin and an end marker — a syscall with a reserved,
  recognizable argument (an open of a reserved, non-existent
  path). Everything a JVM does outside the bracket — startup,
  class loading, JIT and GC threads — is not part of the
  scenario and is never asserted on.
- **Declared expectations**: each scenario ships the events and
  graph edges it must produce, written in the trace output
  schema with symbolic ids (see "Golden semantics" below). The
  declarations double as few-shot examples for LLM
  introspection later.
- **Golden semantics — subset, not equality**: a JVM issues
  thousands of syscalls nobody declared, and PIDs, TIDs, ports
  and inodes differ per run. So validation is:
  1. cut the collected trace to the marker bracket;
  2. *normalize* it — renumber PIDs, TIDs, fds, ports, inodes
     and timestamps to symbolic ids in order of first
     appearance inside the bracket, so JVM startup noise never
     shifts them (see "Trace output contract");
  3. assert *declared ⊆ collected*: every declared event and
     edge is present, in the declared per-thread order;
  4. assert the scenario's *must-not-appear* list (e.g. the
     planted `Authorization` header).

  This matches ior's two expectation layers (aggregate and
  row-exact) instead of contradicting them.

Scenario catalogue, by the milestone it lands with:

| Lands with | Scenarios |
|------------|-----------|
| M1 | File data ops (open/read/write/close) — `file-basic`; TCP client ↔ server on loopback — `tcp-loopback`; markers (all landed with M0) |
| S3 | Syscall load: tight 1-byte read/write loops, N ops over T threads — `syscall-burst` |
| M2 | Full file surface — `file-lifecycle` (create/delete, rename while open, move, truncate, touch), `file-links` (symlink/readlink/lstat, hardlink), `file-attrs` (chmod/chown by path and fd, xattr); fd lifecycle (dup, inheritance across fork/exec, reuse) — `fd-lifecycle` (these four landed ahead of the M2 tracer work); local IPC; signals; UDP; IPv4 + IPv6; restart; non-Java peer |
| M3 | Method flows (deep stacks, async I/O); HTTP plain + TLS; redaction; OTEL flavor |
| M4 | Client and server on two hosts as two projects; microservices topology, one service per host; NAT'ed pair |
| M5 | (renders M4's microservices scenario) |
| M6 | Microservices topology with only the gateway enrolled |
| M7 | The same scenarios as pods in kind; ClusterIP; ingress |

Scenario notes:

- **HTTP scenarios** (M3): client and server exchange declared
  HTTP requests (plain and over TLS) with known method, path and
  headers — expectations assert the captured http events and the
  redaction: a planted `Authorization` header, a planted secret
  in a query parameter and a planted secret in argv must never
  appear in the trace.
- **Microservices end-to-end scenario** (M4): several instances
  of the app playing declared service roles (gateway, orders,
  inventory, billing…), talking to each other over REST — call
  chains (A→B→C), fan-out (A→B and A→C), fan-in (B→A←C) — so
  tracing, detection and cross-host graph fusion are proven
  the way real microservices talk, not just one client↔server
  pair. The link mix is deliberately varied: plain HTTP and
  TLS (self-signed certs pinned per service), loopback and
  cross-host, one service per host/pod — and each service
  also does its own file work (config read, data write via
  the create-tmp/rename pattern), so the graph has file
  edges too. The expectation declares the complete service
  graph — every hop, which links are TLS — so the mesh must
  discover and fuse every hop itself. Runs on plain hosts
  (M4/M6) and as pods (M7), one project per service when
  cross-project fusion is under test.
- **Restart scenario** (M2): the app exits mid-session and
  comes back with a new PID; expectations assert both
  incarnations were traced under one app identity, with
  nothing mis-attributed across the gap.
- **File-surface and fd scenarios** (M2): `file-lifecycle`,
  `file-links`, `file-attrs` and `fd-lifecycle` declare the
  syscalls strace shows on the host JDK, as JDK-only Java can
  issue them (no `fchmod`, and `dup` only via a directory
  stream). `fd-lifecycle`'s child JVM brackets nothing itself:
  a process forked inside a bracket belongs to it, so the
  normalizer's per-process cut must keep such children.
- **Non-Java peer** (M2): one scenario talks to a non-JVM
  process (ior's `ioworkload`, or a minimal C/Go echo peer),
  proving the degraded node shape — files and sockets only,
  no Java stacks, no agent-sourced hostnames or http events,
  every event marked unattributed rather than left ambiguous.
- **NAT'ed pair** (M4): client and server with a DNAT hop
  between them (docker port-mapped), so the two ends observe
  different 4-tuples.
- **Ingress scenario** (M7): gateway → Traefik → backend
  service in kind (the microservices scenario's front door),
  with the route map declared alongside the expected graph.
- **Load scenarios** (S3): `syscall-burst` exists to be
  measured, not golden-checked — its event count scales with
  `--ops` and its threads interleave as the scheduler decides,
  so its expect.json declares only the marker bracket; S3
  asserts counts (syscalls, drops) instead.
- **OpenTelemetry-instrumented flavor** (M3): a separate module
  — the core workload stays JDK-only per decision #5 — that
  emits spans with declared trace ids, so OTEL attribution has
  expectations too.

Acceptance: the framework runs the M1 scenarios standalone
(no tracer) and prints their markers and declared
expectations; the catalogue table above stays current — every
milestone cites the scenarios that prove it.

### M1 — Walking skeleton: one fused edge, end to end

The thinnest slice that exercises every layer once: BPF →
correlator → schema → normalizer → golden → integration harness.
It proves the central thesis — two processes fused into one edge
by their connection — before the traced surface widens.

- **Probes**: open/read/write/close and TCP connect/accept only,
  built on the ior-pattern generation pipeline (see "BPF/C code
  generation — follow ior").
- **TCP lifecycle from the socket, not the syscall**: Java NIO
  connects are non-blocking, so `connect()` returns
  `EINPROGRESS` and never carries the local port. The 4-tuple
  and the state transitions come from the
  `sock:inet_sock_set_state` tracepoint; because that tracepoint
  can fire outside the process context, events join back to
  pid/fd through the sock pointer captured at connect/accept.
- **Correlator**: pid→fd→path and pid→fd→4-tuple; the loopback
  pair joined locally into one connection edge.
- **Target**: one or more PIDs — no patterns, no restarts yet.
- **Output**: trace schema v1 *draft* — NDJSON event stream +
  graph export. The envelope is not frozen until S1 and S2 have
  reported.
- **Normalizer + golden check** as defined in M0, as a library
  and a subcommand.
- **Integration harness** lifted from ior, with one test.
- Acceptance: the M1 scenarios (client and server processes on
  loopback, each doing file data ops) yield a graph with both
  PIDs, their file ops and exactly one fused connection edge;
  the normalized trace passes the golden check on repeated
  runs; S3's measurements are taken on this build.

### M2 — Trace a PID: the full surface

Gated by S3 — the overhead and volume budget is an acceptance
criterion here.

- eBPF probes for the full file-syscall surface and the socket
  lifecycle; userspace correlator building pid→fd→path
  (rename-aware: an open fd's path follows its inode through
  renames and moves) and pid→fd→(remote ip,port).
- **Target spec — PID, process-name pattern or listening
  port**: a session is bound to a PID, to a process-name
  pattern matched on exec (argv and exe path — kernel comms are
  15 chars and cannot tell two JVMs apart), or to whichever
  process owns the listening socket on a port (what expansion
  uses, M6). Mechanics in decision #23. A pattern-bound session
  auto-detects restarts: when the traced app exits and a new
  process matching the pattern appears, the tracer re-attaches,
  the Java agent re-attaches to the new JVM, and both PID
  incarnations link under one app node in the graph. This is
  also what keeps a project's target stable across restarts.
- Probes reuse ior's fd/path resolution helpers where the
  problems are the same; peer addresses, process-tree following
  and exec matching are new work (see "What ior does not give
  us").
- Full mental-model scope (see "What we trace" below): process &
  thread lifecycle (exec/fork/exit, renames), fd lifecycle (dup,
  inheritance, generation numbers), signals, local IPC (pipes,
  unix sockets, loopback TCP joined locally), zero-copy I/O paths,
  poll/epoll wait events.
- **Native stacks are opt-in** (`stack_id`): BPF stack walks do
  not unwind JIT-compiled Java frames reliably, so they serve
  non-JVM targets; Java attribution comes from the agent (M3).
- Argv is recorded redacted (decision #9).
- Output: trace schema v1 — NDJSON event stream + graph export.
- Acceptance: M2's scenarios pass the golden check, including
  edge cases (fd reuse and aliasing, exec, PID reuse, pattern
  restarts (new PID, same app), threaded I/O, loopback pairs,
  socket races) and the non-Java peer, proven by the
  integration suite ("Integration tests — follow ior"); the
  run stays inside the overhead and volume budget (decision
  #25).

### M3 — Java agent: code-flow attribution

Gated by S1 — the design below is the working hypothesis the
spike confirms or corrects.

- **Instrument I/O boundaries, not every method.** The agent
  rewrites only the JDK methods where Java hands I/O to the
  kernel (hook set from S1) and captures the Java stack there.
  Cost scales with the I/O rate, not the call rate; blanket
  method entry/exit tracing is a non-goal.
- **Join to kernel events**: the front-runner is an in-kernel
  join by thread id — uprobes on the JDK's native I/O functions
  bracket each call, and the syscall handler reads the bracket
  (see S1). The agent's boundary hook supplies what the kernel
  cannot see: the Java stack id and thread. How the two records
  meet, or whether the agent joins by fd or TID plus a time
  window instead, is S1's pick. Events no hook
  brackets (JNI libraries, JVM-internal I/O) and ambiguous
  matches stay in the trace marked `attribution: none` — never
  guessed.
- **Platform threads only in v1.** Virtual-thread attribution
  is deferred (see "Deferred — not in v1"). I/O issued by a
  virtual thread is still traced and still joined to its native
  I/O call, but it is attributed to the carrier thread and
  flagged `carrier: true` — visible as a known gap, never
  passed off as the carrier's own work.
- **Agent → tracer transport**: socket-free like everything
  else — a spool the agent appends to in a tracer-owned
  directory, read by the correlator (decision #27). The agent's
  own I/O is tagged and kept out of the target's story.
- **Attach**: `-javaagent` at launch is the primary path. Live
  attach stays supported for the "trace an existing PID" story,
  with its cost stated: JDK 21 warns on dynamically loaded
  agents (JEP 451) and a later release disables them by
  default, after which the app must opt in with
  `-XX:+EnableDynamicAgentLoading` (decision #2).
- Supporting mappings the model needs: class-load map
  (class↔JAR/classloader), thread names (kernel TID ↔ Java
  thread), DNS hostname↔IP (InetAddress hooks), `file:line` on
  frames when debug info is present; TLS peer identity if adopted
  (decision #8).
- **OpenTelemetry introspection**: when the app runs the OTEL
  SDK, the agent reads the active span context and tags every
  I/O event with its OTEL trace/span ids — iormesh events join
  1:1 with the app's distributed traces in any OTEL backend.
  Optionally, spans can also be ingested (an OTLP receiver the
  app dual-exports to, or the collector's file export —
  decision #20) to overlay the service graph on the mesh.
  Socket-free either way; span ids are metadata, and ingested
  spans are the app's own deliberately-exported telemetry,
  not captured payload.
- **HTTP/HTTPS request events**: hooks at known HTTP client and
  server API boundaries (JDK HttpClient, HttpURLConnection,
  Servlet, … — the v1 set is decision #22) record method,
  scheme, host, path/URI, status, sizes and latency, joined to
  the socket 4-tuple, the Java stack and the OTEL span.
  Captured at the API boundary, so HTTPS is included without
  ever touching the wire; sensitive headers, query params and
  path segments are redacted by policy before anything is
  recorded.
- **Request pairing across the wire**: a request on the client
  and on the server pair up by connection id plus a
  per-connection request ordinal — sound for HTTP/1.1. HTTP/2
  multiplexes requests on one connection, so an ordinal cannot
  pair them: v1 records HTTP/2 requests but renders their hops
  time-ordered unless stream ids are captured (decision #22).
- Acceptance: frames carry `class#method(file:line)`; every
  declared I/O op of M3's method-flow scenarios carries its
  declared causing method; unbracketed events are marked unattributed; the
  OTEL-flavored workload's events carry the expected
  trace/span ids; the HTTP scenarios produce the expected
  http events, and the redaction expectations hold — the
  planted header, query secret and argv secret never appear;
  agent overhead stays inside the budget (decision #25).

### M4 — Controller (`iormeshctl`): push, collect, merge locally

The primary deployment and collection path for plain Linux.
Gated by S2 (the cross-host join). Mesh expansion (M6) and
Kubernetes (M7) build on it.

- `iormeshctl` runs on the controller — a laptop or MacBook,
  Linux or macOS — with no eBPF and no cgo at all
  (cross-compiled darwin/linux, amd64/arm64; the merge and
  render packages stay cgo-free).
- **Server registry**: `iormeshctl add HOSTNAME` / `rm HOSTNAME`
  manage the list automatically (kept in the controller's
  config, e.g. `~/.config/iormesh/servers.yaml`). Only
  registered hosts are ever deployed to (principle #3).
  Entries are project-aware: the same server can host several
  independent sessions, one per project.
- **Deploy**: detect the server's arch (amd64/arm64), verify
  the tracer build's signature on the controller (decision
  #28), push it over SSH to `/opt/iormesh/bin/iormesh`, and
  start it with `sudo iormesh start`. Every session is labeled
  with a project (`-project`), so completely different,
  non-interacting applications on the same server are traced
  and stored independently.
- **The tracer runs as a transient systemd unit**: `start`
  launches the session through `systemd-run` as
  `iormesh-<project>`. That gives it a life independent of the
  SSH session, cgroup CPU and memory caps (the enforced half of
  the overhead budget), logs in the journal, and a clean stop
  (`iormesh stop` → SIGTERM → flush the active segment, write
  the final event). Runs default to a 4h auto-exit
  (`-duration`, ior-style), with the unit's `RuntimeMaxSec` as
  a second, independent bound — no unbounded daemons on
  servers.
- **One sudoers rule**: every privileged action is a tracer
  subcommand — `start`, `stop`, `status`, `upgrade`,
  `uninstall` (and `run`, the foreground tracer `start`
  wraps) — so a
  single pinned rule for `/opt/iormesh/bin/iormesh` covers the
  whole lifecycle; nothing else on the server needs sudo. The
  binary is root-owned; how it gets installed and upgraded
  without turning that rule into full sudo is decision #1.
- **Trace files** land on the server as parquet under
  `/opt/iormesh/traces/<project>/` — every trace is always
  inside a project subfolder, so unrelated apps on one server
  never mix. Parquet is the bulk format behind the same schema
  as NDJSON (columnar, queryable, compact; ior already records
  parquet), compressed inside the file (zstd pages), so every
  segment stays directly queryable. Segments **rotate once per
  minute**: only the active segment is being written; on
  rotation it is closed and moved (atomic rename) into a
  `ready/` subfolder as
  `<fqdn>-<session>-<start-ts>-<seq>.parquet` (`<start-ts>` is
  when that segment was opened).
  `ready/` is the sync contract: files there are immutable and
  final, so iormeshctl can collect them while the session
  runs, and a crash or kill loses at most the current minute,
  never a synced segment. `ready/` belongs to the deploying
  user (`start` records who invoked sudo), so collection needs
  no privilege.
- **Disk watchdog**: the trace directory is configurable
  (`-trace-dir`, default `/opt/iormesh/traces`), and the
  tracer continuously monitors the partition holding it
  (statfs on the configured dir — on a short check interval
  and before every segment rotation). When usage crosses the
  limit (default 90%, `-disk-limit`), it stops tracing, flushes
  the current parquet segment, records the exit reason, and
  exits. Together with the 4h bound, tracing is bounded by
  disk too — it never fills the partition. A startup check
  fails fast if the partition is already at the limit.
- **Three commands, two jobs**:
  - `iormeshctl collect` only collects: it syncs trace segments
    from the servers to the controller. It touches the network
    and nothing else.
  - `iormeshctl build` only builds: it merges what has been
    collected into the graph, updates the service database and
    renders the outputs. It is offline, repeatable, and never
    contacts a server — so a build can be rerun after naming
    services, with other projects, or on a plane.
  - `iormeshctl refresh` does both, collect then build — the
    everyday command.
- **Collect**: iterate the registry and sync the trace files.
  **Sync is streaming
  and rsync-based**: `collect` is repeatable during a live
  session and rsyncs each server's `ready/` folder over SSH
  with `--remove-source-files`, unprivileged, so a segment is
  deleted from the server as soon as it has transferred —
  server disk holds only unsynced segments plus the minute
  currently being written, and collection is never an
  end-of-run bulk download. The active segment never enters
  `ready/`, so rsync can never grab or delete a file mid-write.
  Server-side cleanup is delete-on-transfer, not by age: an
  uncollected segment older than a minute is still data the
  operator wants, so only a confirmed transfer frees it; if
  `collect` never runs, the disk watchdog and the 4h bound cap
  what piles up.
  Synced segments land on the controller under
  `~/.local/share/iormesh/traces/<project>/` — that path on
  every platform, always — and stay there until the operator
  deletes them; nothing is auto-cleaned.
  Collect is project-aware: one project at a time, or
  several together.
- **Build**: merge the collected segments offline by stable
  ids — connection ids, 4-tuple joins with the NAT-tolerant
  key (decision #26), clock reconciliation — fusing the
  outbound side on host A with the inbound side on host B into
  single cross-host edges. An outbound edge whose far end has
  no tracer stays in the graph, marked *unresolved* (M6 feeds
  on these). Build is project-aware too: when project A's app
  calls project B's app over TCP and both are built together,
  the same join fuses both sides into one cross-project edge,
  giving the complete picture across both. All views (M5) run
  locally on the controller, from the build's output. From M4
  on, `build` ends by printing a plain-text summary of the
  merged mesh (services, edges, health, unresolved peers);
  the site and the TUI follow in M5.
- **Clock reconciliation from the traffic itself**: every
  fused connection gives send-before-receive constraints in
  both directions; together they bound the clock offset per
  host pair. The merge records the bound, and cross-host order
  is derived from happens-before (per-thread order plus
  send→receive edges), never from raw wall-clock comparison.
- **Service-interaction database**: every `build` also
  analyzes the merged traces and updates a controller-side
  database of app-to-app network interaction — per observed
  connection: client app (project, host, app identity),
  server endpoint (hostname as resolved via the agent's DNS
  hooks, IP, port), protocol hints (plain vs TLS, http
  host/path), counts and activity windows — keyed so
  rebuilding the same mesh updates rows instead of
  duplicating them. The database is **additive**: new sessions,
  hosts and projects fold in, and human input is never
  overwritten. Humans name services through the CLI, never by
  hand-editing files: `iormeshctl services` lists observed
  endpoints (unnamed ones first), and naming rules map
  hostname patterns + port numbers to service names
  (`orders*` + 8443 → `orders`); rules apply retroactively
  and to every future build, so the interaction view says
  "orders → inventory" instead of bare host:port — the
  who-talks-to-whom index of the end-goal picture (see
  Purpose); the in-order, thread-attributed story is M5's
  sequence view. Views (M5)
  and the summary rollup render the named services; storage
  format is decision #24.
- **Cleanup (`rm`)**: removes the host from the registry and
  tears down the deployment on it — `sudo iormesh uninstall`
  stops every session and removes `/opt/iormesh` (binary and
  traces). Trace files are synced off first (rsync, same as
  collect), so `rm` never destroys uncollected traces; an
  unreachable host warns and only drops from the registry.
- **No management plane**: every tracer flag and feature is
  reachable through its own CLI, and `iormeshctl` composes
  those commands over plain SSH (later `kubectl exec` in the
  k8s phase) — the tracer never needs a network socket on the
  server. Tracer flags pass through on deploy; runtime changes
  are stop-and-restart with new flags (cheap under bounded 4h
  runs); status comes from the `status` subcommand reading its
  state files. The only way into a server is the operator's
  SSH key.
- Target platform for the tracer: Rocky Linux 9 first (BTF
  present; ior's Rocky 9 build/Docker patterns apply), amd64
  and arm64. Deployed binaries come out of the container build
  pipeline (see "Container builds (Docker/Podman)").
- Acceptance: two test hosts run M4's scenarios; from a
  controller, `add` → `deploy` → `refresh` → `rm` produces the
  merged graph export with zero manual steps on the servers,
  leaves nothing behind under `/opt/iormesh`, and the merged
  mesh equals the single-host result for single-host
  scenarios. Streaming collect is proven too: `collect` run
  repeatedly during a live session pulls each `ready/`
  segment over rsync and deletes it from the server, so
  between collections the server holds only unsynced segments
  plus the one being written. A deliberately low
  `-disk-limit` proves the watchdog: the tracer exits cleanly,
  the last parquet segment is intact, and the exit reason
  shows up in the summary.
  The M0 client and server run as two projects; building
  both together fuses their connection into a single cross-project
  edge. The microservices scenario spreads one service per
  host, each as its own project — a single `refresh` over the
  whole registry produces the complete service graph with
  every REST edge (plain and TLS) fused. The NAT'ed pair
  fuses into one edge although the two ends saw different
  4-tuples, and an injected clock skew (test-only flag) is
  recovered within the recorded bound. `build` rerun with the
  network down reproduces the same graph export byte for
  byte, and `collect` alone changes nothing but the local
  segment store. Naming two of the
  services via `iormeshctl services` shows them by name in
  the graph export — no manual file edits anywhere.

### M5 — Views, query and the LLM eval

- **Every consolidation run ends in something to explore.**
  `iormeshctl build` (and so `refresh`) finishes by building
  one view model of
  the merged mesh and rendering it three ways — never three
  separate readings of the data (design:
  [docs/visualization.md](docs/visualization.md)):
  - a **local site** (static HTML/JS/SVG, opened with
    `iormeshctl open`) — the interactive explorer;
  - a **terminal UI** (`iormeshctl tui`, Bubble Tea — ior's
    libraries) for SSH sessions and browser-less hosts;
  - **plain text** (`iormeshctl show …`, also what `build`
    prints) for reading, piping and LLMs.

  All three show hosts, PIDs, sockets, files and Java stacks
  over time; endpoints named in the service-interaction
  database (M4) render as their service; unresolved edges,
  time-ordered hops and unattributed I/O are visible in each.
- **Deterministic SVG export**: iormesh builds SVG diagrams of the
  (sub)mesh itself — hosts as layers, processes, files, sockets
  and method frames as nodes, typed edges. Same normalized
  graph in, same SVG out: diffable, and M0 scenarios get visual
  goldens.
- **Interactive explorer (the local site)**: walk the mesh —
  pan/zoom, filter by host/PID/pod/time/method, click a node or
  edge, follow a connection across hosts, open a conversation
  as a sequence diagram and any step down to its Java stack.
  Static files, no server, no dependencies, no network; one
  folder per run, kept until the operator deletes it
  (decision #11).
- **Sequence view (the end-goal rendering)**: a deterministic
  sequence-diagram export of mesh conversations — lifelines
  are services (named via the service-interaction database),
  arrows are connections/requests in happens-before order
  (per-thread order plus send→receive edges — never raw
  cross-host timestamps), each hop attributed to the Java
  thread and method frame that caused it: gateway thread X →
  orders thread Y → inventory thread Z. **The same sequence
  protocols the driving threads' local
  file I/O** — data ops and meta ops alike: which files a
  thread opened, read from or wrote to (path, sizes,
  offsets), and the structural changes it made — create,
  delete, rename/move, touch, symlink — all metadata only,
  never payload, interleaved in the same order, so one
  thread's story reads as one line: received request →
  read /etc/orders.conf → created order-42.json.tmp → wrote
  response → renamed it to order-42.json → sent reply. All
  of it is already in the data —
  sockets join to threads on both ends, http events carry
  the causing stack, file ops carry the same thread
  attribution — so the view renders the correlated graph
  instead of re-inferring it. Where a hop is async (virtual
  threads, executor hand-offs), unpaired (HTTP/2 without
  stream ids) or unattributed, and no direct thread causality
  exists, the arrow is explicitly time-ordered instead —
  never a silent guess. Same rule as the SVG: same graph
  in, same diagram out.
- **Query / slice interface**: `iormeshctl query` cuts the
  merged trace by time window, host, app, thread, connection,
  file or method and emits schema-conformant NDJSON, with a
  size cap and an explicit truncation record. An event stream
  at syscall frequency never fits a context window — the slice
  is what an LLM (or a human) actually reads.
- Plus the per-run summary/report export — the LLM rollup of the
  whole mesh (see "Trace output contract").
- **LLM introspection eval**: a fixed question set over M0
  traces plus the workload's source, with answers known from
  the scenario declarations (which method wrote this file?
  what is the call chain behind this request? which service
  did this thread call next?). The model works only through the
  summary rollup and the query interface. The eval is rerun on
  every schema change; its score is the evidence behind
  principle #6.
- Acceptance: an operator can answer "what does this process touch,
  and what touches it back" from one screen; `add` → `deploy` →
  `refresh` yields the merged mesh's SVG, matching its golden;
  named services render by name in graph and SVG; the same
  build leaves a local site that opens from `file://` with
  no network request and no console error, and `iormeshctl
  tui` and `iormeshctl show` present the same view model —
  the microservices scenario can be walked from the mesh to
  one step's Java stack in each of the three;
  the microservices scenario's sequence view
  shows every service-to-service hop on its causing Java thread
  (gateway X → orders Y → inventory Z), with each thread's
  declared file work (M0 microservices: config read, data
  write via create-tmp/rename) interleaved in the same order,
  and async hops visibly time-ordered rather than
  thread-attributed; the LLM eval passes its threshold
  (pinned when the question set is written).

### M6 — Mesh expansion: follow the edges, from the controller

The mesh grows along the connections it observes — driven by
`iormeshctl`, never by the tracers. Servers hold no credentials,
tracers deploy nothing and connect to nothing; every hop is the
operator's SSH key from the controller.

- **Discover**: after a `refresh`, the merged graph holds
  unresolved peers — connections whose far end is a host with
  no tracer. `iormeshctl expand` lists them: peer host and
  port, which traced app talks to it, how often. Listing is the
  default; nothing is deployed without `--apply`.
- **Apply**: `expand --apply --hops N` enrolls every unresolved
  peer that matches the **expansion allowlist** (host patterns
  and CIDRs in the controller config), deploys the tracer
  there, and binds it to the process owning the listening
  socket on the observed port (target spec: listening port,
  M2). Peers outside the allowlist are listed, never touched.
- **Rounds**: expansion is iterative — deploy, let traffic
  flow, refresh, expand again. The hop limit counts rounds from
  the originally enrolled hosts; a max-node cap bounds the
  total; a visited set makes cycles (A→B→A) terminate instead
  of oscillating.
- **Convergence honesty**: a peer's tracer starts after the
  connection that revealed it, so earlier conversations are
  one-sided. Those edges stay in the graph marked unresolved —
  never dropped, never invented.
- The tracer's own footprint and the controller's SSH/rsync
  traffic are excluded from the traced set.
- Acceptance: the M0 microservices topology (3+ hops, mixed
  plain-HTTP/TLS links) with only the gateway host enrolled;
  expansion rounds discover and deploy to every service host,
  and after the scenario re-runs, the merged mesh equals the
  one obtained by `add`ing all hosts by hand. A hop limit of 1
  demonstrably stops at the first ring; a peer outside the
  allowlist is listed but never contacted; a cyclic topology
  terminates.

### M7 — Kubernetes: sidecar & debug-container deployment

Later phase — plain Linux servers (M4) come first.

- iormesh runs as a sidecar (or `kubectl debug` ephemeral
  container) in the target pod, with the privileged security
  context eBPF needs (capability set: decision #13). It traces
  the pod's containers — and only those, by config. (No
  systemd here: the container is the unit, and its resource
  limits are the caps.)
- `kubectl debug` deployments are driven by `iormeshctl`, with
  the kubectl context (cluster/namespace from the kubeconfig)
  and the debug container image both configurable —
  defaulting to our published tracer image, pinned by digest.
  The concrete config shape and examples are TBD (owner to
  provide them; decision #18).
- Pod identity enrichment: map host PIDs/cgroups → pod, namespace,
  container, so the graph speaks "pod checkout-7d9f (ns prod)"
  instead of bare host PIDs. In k8s mode the project defaults
  to the namespace (decision #21).
- Java agent live-attach through the shared process namespace
  (`shareProcessNamespace: true`; attach socket reached via
  `/proc/<pid>/root`) — decision #14.
- Service-NAT correlation: kube-proxy ClusterIP DNAT/SNAT means
  the two ends of a connection observe different 4-tuples; the
  mesh must join them (extends M4's NAT-tolerant join to k8s
  services).
- **Ingress route mapping**: an FQDN often terminates on an
  ingress (Traefik) behind a managed LB — no tracer can attach
  there, and the L7 hop breaks the connection join anyway
  (client→ingress and ingress→backend are separate streams).
  iormesh completes those edges from routing metadata: a
  configured map FQDN → ingress → service → deployment or
  statefulset, or (in k8s mode) discovered automatically from
  Ingress/IngressRoute objects, the Traefik API, and
  service→workload resolution via the k8s API. No payload
  parsing — Traefik's own configuration and logs are the source.
  Observed http events (host + path from the agent) can
  validate the mapping against what actually flowed.
- Collection works with the controller tooling too —
  per-pod/per-node trace fetch (decision #12), events carrying
  pod identity.
- Acceptance: M0 client and server as pods in kind with iormesh
  attached as sidecar/debug container; expectations hold with
  pod identity added; a ClusterIP connection joins both sides;
  the M0 ingress scenario renders as one routed edge — app →
  FQDN → Traefik → deployment — with no tracer on the ingress.

## Decisions

Numbers are stable ids — references elsewhere in this plan and in
the docs keep working — so the groups below are not in numeric
order. Three groups: decided, open (grouped by the milestone they
block, each with a default so work is never stalled on a missing
answer), and deferred.

### Decided

- **#3 Graph storage** — none on the server. The tracer writes
  parquet segments; the controller rebuilds the merged graph
  from them offline, on every build. There is no long-lived
  collector whose state would need to survive a restart. (The
  controller's service database is separate — #24.)
- **#4 Go eBPF library** — ior's stack (`aquasecurity/libbpfgo`,
  cgo, Mage, Docker generation host), adopted with its
  generation pattern. Mechanics: copy/adapt ior's
  `internal/generate`, recording the ior commit each copied
  file came from, so later ior fixes can be ported
  deliberately. Extracting a shared package into ior is the
  alternative to revisit once the copies diverge.
- **#12 Controller transport** — SSH with the operator's key;
  collect syncs over rsync, which must be present on servers
  (`deploy` fails fast if it is missing); server registry in
  `~/.config/iormesh/servers.yaml`. The k8s fetch path
  (kubectl exec/cp) settles with #18.
- **#16 Trace-file segmentation** — rotate once per minute,
  close-then-rename into `ready/`, collect-while-running via
  rsync with delete-after-sync (see M4). Compression is
  parquet-internal zstd — whole-file compression
  (`.parquet.zst`) would make segments unqueryable without a
  decompress step. Variable event arguments map to one
  nullable column per argument field (see "Trace output
  contract"). One sub-point rides on S3: whether a size cap
  forces an early rotation for very hot sessions.
- **#5 Test workload scope** — same repo, `workload/`
  (adopted with M0, 2026-11-18). A plain Java 21 app built with
  the JDK alone (`javac` + `jar` via `mage workloadBuild`, no
  Maven or Gradle), so the thing being traced never has an
  excuse to behave differently from a real app.
- **#19 Runtime control** — the transient systemd unit (M4):
  graceful stop via SIGTERM, flag changes via stop-and-restart.
  No control file, no socket.
- **#11 Explorer form factor** — all three, on one view model:
  a generated static site (HTML/JS/SVG, no server, opens from
  `file://`), a Bubble Tea terminal UI, and plain text. A
  local web server was rejected: it adds a listening socket
  for nothing the static site cannot do at these sizes. Design
  in docs/visualization.md.

### Open — blocks M2

- **#25 Tracer overhead and volume budget** — tracing runs at
  syscall frequency, so the cost needs explicit numbers,
  measured by S3 and enforced as acceptance criteria from M2
  on. Four numbers to pin:
  1. tracer CPU while tracing the M0 scenarios;
  2. the cost every *other* process on the host pays — ior
     measured that classic syscall tracepoints add a
     per-syscall cost to every task once enabled (its
     `docs/raw-tracepoint-design.md`), so the budget is
     host-wide, not per-target;
  3. ring-buffer loss rate;
  4. trace bytes per minute — events/s × compressed
     bytes/event × 60. (Order of magnitude only, to be
     replaced by S3's measurement: 10k events/s at 20 bytes
     each is ~12 MB per minute, ~3 GB per 4h session and
     host.) The disk watchdog default, the size-cap rotation
     (#16) and any need for in-kernel aggregation of hot
     read/write loops follow from this number.

  Also open: where the budget is enforced (a perf target in CI
  vs a documented measurement run per release). *Default: the
  owner pins initial targets from ior's `perf/` baselines
  before M1 starts; S3 replaces them with measurements.*
- **#7 Event families, default-on vs opt-in** — signals, epoll
  registrations, mmaps and renames all add volume; reuse ior's
  trace-family concept and pick defaults per measured cost
  (S3). *Default: file, socket and lifecycle on; the rest
  opt-in.*
- **#23 Target-pattern mechanics** — match on exec (argv/exe)
  vs comm; glob vs regex; concurrent matches (newest wins vs
  trace all vs error); restart semantics (wait for
  re-appearance vs end the session); and the listening-port
  target expansion needs (resolve once at start vs follow the
  port across restarts). Kernel comm's 15-char limit makes
  argv matching the primary source for Java apps. *Default:
  glob on argv, trace all matches, wait for re-appearance.*
- **#9 Capture policy for argv and env** — argv is in, but it
  is not harmless: command lines carry passwords and tokens
  (`-Dpassword=…`, `--token …`). Argv is recorded through the
  same redaction policy as HTTP (denylist patterns, values
  masked). Capturing the environment at exec
  (`/proc/<pid>/environ`) is useful for the model but a
  privacy call to make explicitly. *Default: argv redacted,
  env off.*

### Open — blocks M3

- **#2 Java agent attach strategy** — pre-launch `-javaagent`
  vs live attach. Live attach is friendlier for the "just
  trace an existing PID" story but: JEP 451 (JDK 21 warns, a
  later JDK disables dynamic loading by default), the attach
  handshake has uid and mount-namespace requirements the
  root-run tracer must satisfy, and retransforming loaded
  classes in a live JVM is the least "read-only" thing iormesh
  does. *Default: `-javaagent` primary, live attach supported
  and documented with those caveats.*
- **#6 Java stack source** — largely settled: instrument I/O
  boundaries and capture the stack there (M3); blanket method
  entry/exit instrumentation is out (non-goal). Open: whether
  async-profiler-style sampling supplements it for the
  stretches between I/O events — S1 reports the cost.
- **#27 Agent → tracer transport** — append-only spool file vs
  memory-mapped ring, in a tracer-owned directory the agent
  reaches from inside the target (through `/proc/<pid>/root`
  in k8s); back-pressure and loss accounting when the tracer
  falls behind; how the agent's own writes are tagged so they
  never appear as the target's I/O. Socket-free either way.
  *Default: S1 picks.*
- **#29 JDK version support** — the plan says JDK 21, but
  fleets run 17 and older. Boundary hooks, stacks and HTTP
  events do not need 21 (only the deferred virtual-thread
  support would). *Default: agent runs on 17+; anything
  older is traced without Java attribution (the non-Java
  node shape).*
- **#22 HTTP capture mechanics** — agent-boundary hooks (v1
  set: JDK HttpClient, HttpURLConnection, the JDK's built-in
  HttpServer (the microservices workload's REST server),
  Servlet, Apache HttpClient, OkHttp, Netty?); redaction
  policy — denylist (drop Authorization/Cookie/… by default)
  vs allowlist (record only named headers), sensitive query
  params, and URIs: verbatim, path-only, or templated
  (`/orders/{id}`) — path segments carry ids and tokens too;
  and HTTP/2 request pairing — capture stream ids at the hooks
  vs leave HTTP/2 hops time-ordered in v1. *Default: denylist,
  query values masked, HTTP/1.1 pairing only.*
- **#8 Hostname & TLS visibility** — DNS hostname↔IP via Java
  agent hooks (InetAddress) is the clean path; TLS peer
  identity (trust-manager/SSLSocket hooks) is extra value at
  extra surface. Non-Java nodes get no hostnames this way.
  *Default: DNS hooks in v1, TLS identity later.*
- **#20 OTEL integration mode** — agent-read span context
  (primary, socket-free) vs ingesting the app's OTLP stream (a
  local receiver the app dual-exports to, or the collector's
  file export) vs both; and whether the views render a
  distributed-trace overlay on the mesh. Ingested spans are
  the app's own exported telemetry — deliberately distinct
  from the no-payload-capture rule, worth stating in
  docs/schema.md. *Default: span-context tagging only.*

### Open — blocks M4

- **#26 Cross-host join key** — the 4-tuple plus a time window
  breaks under NAT and port reuse. Candidates: conntrack state
  read on the NAT host (needs a tracer or access there) vs the
  TCP initial-sequence-number pair read from both sockets (NAT
  leaves it untouched; SYN proxies and sequence-randomizing
  firewalls do not). L7 proxies terminate the connection
  either way and stay the route map's job (#15). *Default: S2
  picks; the fallback when no key matches is an unresolved
  edge, never a guess.*
- **#1 Remote bootstrapping and sudo scope** — transport is
  settled: SSH, key-based, from the controller only. The sudo
  scope is one pinned sudoers rule for exactly
  `/opt/iormesh/bin/iormesh` (ior precedent: scoped sudoers
  rules). Open: bootstrap and upgrade. The first install needs
  broader privilege once (the operator's own sudo, or
  configuration management laying down the binary and the
  rule), and the binary must be root-owned and not writable by
  the deploying user — otherwise the pinned rule is full sudo
  in disguise. Upgrades therefore go through a tracer
  subcommand that verifies the staged binary's signature
  against a root-owned public key (#28) before replacing
  itself.
- **#28 Signature scheme** — which tool signs the tracer build
  (minisign, cosign, `ssh-keygen -Y`), where the public key is
  pinned (controller config; a root-owned copy on servers for
  upgrades), and who holds the private key (the release
  pipeline). *Default: minisign, key pinned in the controller
  config.*
- **#24 Service-interaction database** — controller-side:
  sqlite via a pure-Go driver (keeps iormeshctl cgo-free;
  additive merges and queries come natural) vs a plain YAML
  file (trivially diffable and inspectable, but incremental
  updates and bigger meshes get awkward); one database per
  project vs one with project scoping; glob vs regex for the
  naming rules. Lives under `~/.local/share/iormesh/`, next to
  the collected traces. *Default: sqlite, one database, glob
  rules.*
- **#21 Project semantics** — for now a project is a manual
  label (`-project`, kept with the registry entry); later
  candidates: defaulting to the k8s namespace, or
  auto-derivation from the traced app. Cross-project
  collection: explicit project list vs merge-everything-found.
  *Default: manual label, explicit list.*
- **#17 Disk-watchdog semantics** — percent of partition vs an
  absolute free-bytes floor (or both); check interval; and
  what the controller does when a tracer exits on the limit
  (surface it, redeploy with a lower duration, or nothing).
  *Default: both limits, surface only.*

### Open — blocks M7

- **#13 K8s privilege profile** — full privileged
  SecurityContext vs the minimal capability set (CAP_BPF,
  CAP_PERFMON, and the legacy CAP_SYS_ADMIN/CAP_SYS_RESOURCE
  paths) plus BTF mounts; what to require and document.
- **#14 K8s Java attach** — live attach via the shared process
  namespace (`/proc/<pid>/root`) vs `-javaagent` injection via
  JAVA_TOOL_OPTIONS (needs deployment changes to the app);
  inherits #2's caveats.
- **#15 Route-map source of truth** — a static YAML map (works
  from outside any cluster) vs k8s API discovery (Ingress/
  IngressRoute objects, Traefik's API, service→workload
  resolution) vs Traefik access logs as a live source; and
  their precedence when they disagree.
- **#18 K8s debug-container config in iormeshctl** — the
  kubectl context (cluster/namespace) and the debug container
  image must both be configurable; where they live (flags,
  per-host registry entries, a k8s section in the server
  config) settles with concrete fleet examples, to be
  provided. Includes the k8s trace-fetch path (#12).

### Deferred — not in v1

- **#10 Java exception events** — JVMTI throw/catch hooks would
  enrich the model (error paths in source) but are hot.
  Out for v1; revisit as a sampled, opt-in family.
- **eBPF plaintext HTTP parsing for non-Java processes** (was
  part of #22) — conflicts with the no-wire-parsing rule; out.
- **OTLP span ingestion and the distributed-trace overlay**
  (the optional half of #20) — after span-context tagging has
  proven itself.
- **Virtual-thread attribution** — out of v1; the owner's apps
  do not use virtual threads yet. The problem: the kernel only
  sees the carrier thread, and a parked virtual thread may
  resume on another carrier, so thread id alone cannot name
  the virtual thread. The likely solution when it is needed:
  the agent's I/O-boundary hook records `Thread.currentThread()`
  — hooks sit at the innermost Java frame above the native
  call, where a virtual thread cannot park, so the carrier's
  thread id is stable for the length of one bracket. Fallback:
  track mount/unmount. Until then such I/O is attributed to
  the carrier and flagged (see M3). Needs JDK 21+, a
  `virtual-threads` scenario, and its own acceptance.

## What we trace — the mental-model inventory

Metadata only — nothing below is payload (see non-goals):

- **Identity & lifecycle**: exec (exe, argv — redacted per
  policy, decision #9), fork/clone, thread
  create, exit, comm/thread renames; PID incarnation ids (pid +
  boot-time start time) so a reused PID is never confused.
  Pattern-bound sessions re-attach across restarts, so the
  app's PID changes are tracked as one identity. *Why:
  the model needs a stable "who", including parentage and
  lifetimes.*
- **Filesystem**: the full syscall surface, not just data
  movement — open/close, read/write/pread, stat family,
  create (open O_CREAT, creat, mkdir), delete (unlink/rmdir),
  rename & move (renameat2, cross-directory and cross-device),
  touch-style timestamps (utimensat/futimens), symlinks
  (symlink/readlink, lstat's no-follow distinction), hard
  links (link), truncate, the chmod/chown family, xattrs,
  file mmaps (which JARs/libs/data files are mapped), and
  zero-copy paths (sendfile/splice/copy_file_range). *Why:
  what data is touched — and what happened to it
  structurally: files created, deleted, renamed, relinked,
  touched — with no holes where read/write never fires*
  (ior's scenarios already cover the zero-copy family).
- **Descriptor lifecycle**: dup/dup2/fcntl(F_DUPFD), fd inheritance
  across fork/exec, per-fd generation numbers. *Why: aliasing and
  reuse are the classic mis-attribution bugs.*
- **Network**: TCP socket lifecycle with full 4-tuples — taken
  from the socket's state transitions
  (`sock:inet_sock_set_state`), not from syscall arguments,
  so non-blocking connects are covered — plus a NAT-tolerant
  join key (decision #26); UDP per-packet peers, poll/epoll
  wait registrations, DNS hostname↔IP mappings (agent hooks —
  decision #8), optional TLS peer identity. *Why: who talks to
  whom — by hostname, not just IP — and which threads block on
  what.*
- **HTTP/HTTPS**: method, scheme, host, path/URI, status,
  sizes, latency per request — captured at the Java HTTP
  client/server API boundary by the agent (HTTPS included,
  nothing sniffed off the wire), sensitive headers, query
  params and path segments redacted by policy. *Why: URIs are
  what the mesh actually routes on — and what an LLM matches
  against source code.*
- **Local IPC**: pipes (both ends), unix sockets (peer PID where
  the kernel exposes it), socketpair, eventfd, shared memory;
  loopback TCP pairs joined locally by the correlator. *Why:
  single-host edges a "mesh" would otherwise miss entirely.*
- **Signals**: delivery with sender PID. *Why: "who touched the
  process" includes the supervisor's SIGTERM.*
- **Java**: class loading (class↔JAR/classloader), thread lifecycle
  + names (kernel TID ↔ Java thread), the Java stack at every
  I/O boundary as `class#method(file:line)` frames (line
  numbers when debug info is present), and the active OTEL span
  (trace/span ids) when the app is OpenTelemetry-instrumented.
  *Why: the source-level truth everything above gets annotated
  with.*
- **Non-Java processes**: traced in a degraded but explicit
  shape — files, sockets, IPC, signals and lifecycle, with
  optional native stacks; no Java stacks, no http events, no
  agent-sourced hostnames. Their events carry `attribution:
  none`, so a reader never mistakes "not a JVM" for "nothing
  happened". *Why: real meshes end at nginx, a database or a
  sidecar — the graph must say so honestly.*

## Trace output contract — built for LLM introspection

Outputs are a documented contract, designed so an LLM (or human)
can reconstruct behavior and check it against source code:

- **Schema, versioned & self-describing**: NDJSON event stream,
  graph export (nodes + typed edges), per-run summary rollup.
  Documented in-repo in [docs/schema.md](docs/schema.md).
- **Two serializations, one schema**: NDJSON is the live/golden
  format; parquet is the bulk trace-file format on servers —
  columnar, compact, queryable (ior already records parquet;
  reuse the writer), written as minute-rotated segments with
  parquet-internal zstd compression, so every segment is
  directly queryable. Same envelope either way: the variable
  `args` map to one nullable column per argument field (path,
  new path, local/remote address and port, flags, …) — sparse
  columns cost nothing, and no JSON blob column hides fields
  from queries. The mapping is part of docs/schema.md.
- **Event envelope (v1 sketch)**: `schema_version, project, seq,
  boot_ns, wall_ns, host_id, pid, pid_incarnation, tid, comm,
  event, fd, fd_gen, ret, err, latency_ns, bytes, args (path,
  old+new path on renames / 4-tuple / …), conn_id, stack_id
  (native, opt-in), attribution (java | none), carrier (set
  when the I/O ran for a virtual thread), java_thread,
  java_stack (method ids), otel (trace_id, span_id — present
  when the app is OTEL-instrumented), http (method, scheme,
  host, path, status, req_seq — on http events; headers per
  redaction policy)`. The Java fields are provisional until
  spike S1 reports; the connection join key until S2.
- **Method table**: `method_id, class, method, signature,
  source_file, line, jar, classloader`. Frames reference ids, so
  stacks stay compact and every frame joins to its JAR.
- **Stable ids**: PID incarnation, fd generation, connection id
  (per socket pair, assigned by the merge on the controller,
  shared across mesh hosts), method ids.
- **Ordering & clocks**: per-host sequence numbers, per-thread
  monotonic order, boot + wall clocks per host, and explicit
  cross-host clock-reconciliation records: the merge derives
  an offset bound per host pair from the fused connections
  themselves (a send always precedes its receive, in both
  directions) and records it. Cross-host order is
  happens-before — per-thread order plus send→receive edges —
  never a comparison of raw timestamps. Ring-buffer drops are
  explicit events — gaps are never silent.
- **Attribution is explicit**: every I/O event says whether a
  Java stack was joined to it (`attribution: java`) or not
  (`none` — non-JVM process, JNI or JVM-internal I/O, agent
  absent), and flags I/O that ran on a virtual thread's
  carrier (`carrier: true`), since v1 cannot name the virtual
  thread. Missing attribution is data, not a hole.
- **Cross-service causality**: connection ids + per-connection
  request ordinals (`req_seq`) + per-thread ordering + stack
  attribution are enough to reconstruct sequences across
  services — which thread on service A caused which thread on
  service B, and which files those same threads touched
  locally (data and meta ops alike) — without re-inference;
  M5's sequence view is a rendering of exactly this. The
  limits are stated, not hidden: async hops without direct
  thread causality, and HTTP/2 requests without captured
  stream ids (an ordinal cannot pair multiplexed requests),
  are marked as time-ordered, never guessed.
- **Explicit end**: every trace records how it ended — duration
  reached, disk limit reached, controller stop, every target
  exited, or error — as a final event and in the summary rollup;
  an LLM reading the trace always knows whether it is complete.
- **Normalization**: raw traces are full of run-specific
  values — PIDs, TIDs, fds, ephemeral ports, inodes,
  timestamps. The normalizer (a library, plus `iormesh
  normalize`) renumbers them to symbolic ids in order of first
  appearance and can cut the trace to a scenario's marker
  bracket. Golden checks, run-to-run diffs and visual goldens
  all operate on normalized output.
- **Deterministic exports**: canonical key order + stable ids —
  two *normalized* runs of the same scenario diff to
  near-nothing inside the marker bracket, so run comparison
  (LLM or human) is meaningful. Outside the bracket a JVM's
  background activity differs from run to run, and no claim
  is made.
- **Sliceable**: `iormeshctl query` (M5) cuts the merged trace
  by time, host, app, thread, connection, file or method into
  size-capped, schema-conformant NDJSON with an explicit
  truncation record — the unit an LLM actually reads.
- **Deterministic SVG**: fixed layout rules, no randomness — the
  diagram of a given normalized graph is byte-stable, so M0
  gets visual goldens.
- **Pod-aware identity**: under Kubernetes, the envelope gains
  pod/namespace/container (cgroup-mapped); the graph treats pod
  identity as a first-class node type.
- **Project-scoped**: every session belongs to a named project
  (`-project`); traces always live under
  `/opt/iormesh/traces/<project>/`, graph nodes carry their
  project label, and connections between projects render as
  explicit cross-project edges.
- **App identity across restarts**: sessions bound to a
  process-name pattern produce one app node linking every PID
  incarnation of the matched process — the graph says "the app
  restarted" instead of showing an unrelated new PID.
- **Proxy-hop edges**: routes through an ingress render as
  synthetic `routed_via` edges and introduce service + workload
  (deployment/statefulset) node types — FQDN → ingress →
  service → workload completes the graph across proxies no
  tracer can run on.
- **OTEL correlation**: trace/span ids in the envelope make
  every event joinable with the app's distributed traces; an
  LLM can cross-reference the mesh with span timelines and
  instrumented code paths.
- **HTTP request events**: method, scheme, host, path/URI,
  status and sizes as first-class events, correlated with
  connection, stack and span; sensitive headers, query params
  and path segments are redacted before recording. The same
  policy covers argv (policy documented in docs/schema.md).
- **Summary rollup**: per-process entry point — config files read,
  connections with hostnames, thread table, top methods by I/O —
  cheap context before diving into the event stream.
- **Goldens use the schema**: M0's declared expectations are
  schema documents with symbolic ids; validation is the golden
  check — cut to the marker bracket, normalize, assert
  declared ⊆ collected plus the must-not-appear list (see M0)
  — and the declarations double as few-shot examples for
  introspecting real runs.

## BPF/C code generation — follow ior

iormesh does not reinvent ior's generation machinery; it copies the
pattern (and, where sensible, the code) from `../ior`:

- **Generator package** (`internal/generate/` in ior): reads the
  build host's tracepoint set and emits
  - the C enter/exit handlers
    (`internal/c/generated_tracepoints.c`),
  - a Go tracepoint table
    (`internal/tracepoints/generated_tracepoints.go`),
  - C-derived Go types (`internal/types/generated_types.go`).
  Unit-tested, with harness tests exercising generated-handler
  behavior.
- **Diff gate**: the generation result is committed
  (`internal/c/generated_tracepoints_result.txt`); regenerating on a
  kernel whose tracepoint set differs shows up as a diff and fails
  the gate (force via env override, ior-style). Builds happen on a
  matching kernel — ior pins its generation host with
  `scripts/build-with-docker.sh`; iormesh does the same.
- **vmlinux.h** from the build host's BTF (`/sys/kernel/btf/vmlinux`),
  kept in the working tree (gitignored, as in ior) and
  deliberately preserved by clean targets.
- **Compile & embed**: hand-written BPF C (ior's helpers: fd name
  resolution, file identification, poll/recv handling…) plus the
  generated handlers, clang with CO-RE relocations → `.bpf.o` →
  `go:embed` into the static binary, with an env override
  (`IOR_BPF_OBJECT`-style) to load an external object for testing.
- **Runtime portability**: static binary, copyable to any BTF host;
  tracepoints missing on the target kernel are skipped — the same
  rule ior runs under.
- **Problems ior already solved** — reuse the approach and code
  rather than re-derive: fd→path resolution and file identification
  in BPF (`fdname.c`/`fileident.c`), syscall enter/exit pairing
  including restart edge cases, thread/TID and comm resolution,
  ring buffer consumption, and the libbpf link-wrapper seam that
  keeps teardown safe.
- **Build tooling**: Magefile with generate/build/test/vet/lint
  targets wired for cgo (libbpfgo paths), container builds that
  run on Docker **and Podman** (engine-agnostic scripts,
  ior's build-with-docker pattern with a podman alias), and
  generated-artifact drift checked in CI like ior does.
- **Provenance**: every file copied or adapted from ior records
  the ior commit it came from, so ior's later fixes in the same
  code can be ported on purpose instead of drifting apart
  unnoticed (decision #4).

### What ior does not give us

Checked against `../ior` on 2026-11-17 (re-check when picking
this up — ior moves). These are new work, not reuse, and M1/M2
are sized accordingly:

- **Peer addresses and 4-tuples**: ior traces the socket
  syscalls but identifies sockets only as descriptors
  (`socket:…`); it never reads local or remote addresses.
  iormesh adds the `sock:inet_sock_set_state` handler, the
  sock-pointer join back to pid/fd, and the join key (M1, S2).
- **Process-tree following**: ior's in-kernel filter accepts
  one TGID (or all); following children is a plan there
  (`docs/follow-forks-plan.md`), not code. iormesh needs
  fork/exec following, exec-time pattern matching and restart
  re-attach (M2).
- **Stack capture**: ior's BPF code captures no stacks. Native
  stacks are an opt-in addition (M2); Java stacks come from the
  agent (M3).
- **Per-syscall cost**: ior documents what classic syscall
  tracepoints cost every task on the host, and prototypes
  cheaper dispatch (`docs/raw-tracepoint-design.md`). iormesh
  inherits the cost with the pattern — S3 measures it, and
  ior's findings there are the first place to look for relief.

## Integration tests — follow ior

iormesh does not invent its end-to-end test machinery either.
When this work is picked up, study ior's `integrationtests/`
package (in `../ior`, its README first) and lift the pattern
with the workload swapped for M0's — another agent will
re-read ior before implementing, so this section is the map,
not the replacement:

- **Suite layout**: a dedicated `integrationtests/` package and
  `mage integrationTest` (plus a serial variant) that builds
  the tracer, the M0 workload and the compiled test binary,
  then runs it as root — skipping with an explanation when
  root or BPF is unavailable. Like ior, the test binary is
  compiled to the repo root so its absolute path matches a
  scoped sudoers rule.
- **Harness** (ior's `harness.go`): start the tracer against
  a known M0 workload process — or the whole microservices
  topology — collect the trace output (NDJSON live, parquet
  segments), and expose parsed rows for assertions; ior's
  readiness wait, timeouts and BPF-object override env apply
  as-is.
- **Two expectation layers** (ior's `expectations.go`):
  aggregated event assertions (path contains, event, comm,
  min count, flag bits) over the recording, and exact
  per-event fields (fd, fd_gen, ret, err, bytes, latency)
  over the parquet rows — aggregate where exactness doesn't
  matter, row-exact where it does.
- **Per-family test files**: one `*_test.go` per trace family,
  each running its M0 scenario and asserting the declared
  events — file ops (data + meta: rename, symlink, touch,
  xattr, chmod/chown…), sockets (IPv4/IPv6, TCP/UDP
  lifecycle), local IPC, signals, process/thread/fd
  lifecycle, and with M3: Java stacks,
  class loads, HTTP events (plain + TLS — including the
  redaction expectations: the planted Authorization header,
  query secret and argv secret must be absent), OTEL ids.
- **The golden check is the integration-level assertion**:
  the normalized trace, cut to the scenario's marker bracket,
  must contain everything the scenario declares and nothing
  on its must-not-appear list ("Goldens use the schema") —
  subset semantics, the same spirit as ior's min-count and
  row-presence assertions. The per-family expectations are
  the fine-grained complement, not a replacement.
- **Multi-host tests**: the microservices topology across two
  test hosts (or containers in distinct network namespaces)
  drives deploy → collect → merge (and expand, M6) and asserts
  the fused cross-host edges and the sequence view's thread
  causality.
- **Emulated hosts share a kernel — compensate**: tracers in
  network namespaces on one machine share one clock, so
  reconciliation would never be exercised. A test-only flag
  injects a clock offset per tracer; the merge must recover
  it within its recorded bound. NAT is emulated the same way
  (a DNAT hop between namespaces).
- **Kernel-support probes** (ior's `kernel_support_test.go`):
  probe for the newer syscalls/tracepoints a scenario needs
  and skip with the reason — never infer support from kernel
  version.
- **Loss honesty** (ior's `kernelloss.go` + `unexercised.go`):
  a run missing events that kernel-side loss could explain
  is re-run; a test none of whose runs exercised its case
  skips with a marker instead of passing — a silent green is
  not a green.
- **Gates, not exit codes** (ior's `gatecmd`): the runner
  verifies the required tests actually reported passed and
  prints a skip summary — exit 0 alone is not believed.
- **Parallelism**: `INTEGRATION_PARALLEL`-style knob with
  `-test.failfast` by default, serial mode for debugging.

## Container builds (Docker/Podman)

Both binaries build inside containers, with the same commands
whether the engine is Docker or Podman:

- **Tracer (`iormesh`)**: linux/amd64 + linux/arm64. The
  container pins the build environment (clang, libbpf,
  bpftool); tracepoint generation reads the host kernel, so the
  diff gate stays the honesty check — build on a kernel whose
  tracepoint set you mean to pin, ior-style. arm64
  cross-compiles from its own container with an aarch64
  toolchain and sysroot.
- **Controller (`iormeshctl`)**: pure Go, no cgo — one
  container cross-compiles darwin/amd64, darwin/arm64,
  linux/amd64, linux/arm64.
- Mage targets are engine-agnostic (`mage buildDocker` /
  `mage buildPodman`, or one target taking an engine); the
  signed tracer binary comes out of the same pipeline the
  controller pushes with.

## Component map (planned)

| Component        | Language | Role                                            |
|------------------|----------|-------------------------------------------------|
| eBPF probes      | C (CO-RE)| file/net syscall tracepoints, socket state, opt-in native stacks |
| Correlator       | Go       | ring buffer consumer, fd/inode/socket joins, agent-event join, parquet segments |
| Java agent       | Java     | I/O-boundary hooks, stacks, TID map, OTEL ids, HTTP events |
| Test workload    | Java 21  | deterministic traceable scenarios with markers (M0) |
| Output contract  | versioned JSON + parquet | NDJSON live/golden, parquet bulk files, graph, summary |
| Normalizer       | Go, cgo-free | symbolic ids, marker brackets, golden check |
| Controller (iormeshctl) | Go, cgo-free | registry, deploy, collect, offline merge, service-interaction db, expand, query, rm cleanup, projects |
| Views            | Go + SVG/HTML/JS | one view model; local site, Bubble Tea TUI, plain text; deterministic SVG + sequence exports |
| K8s mode         | manifests | sidecar/debug-container deploy, pod identity |
| Route map        | YAML + k8s API | FQDN→ingress→service/workload edges |

There is no long-lived collector and no mesh agent: the tracer
(probes + correlator) writes segments on each host, and
everything cross-host — merge, graph, expansion — happens on the
controller.

## Suggested repo layout (when code starts)

```
cmd/iormesh/          # tracer binary (Linux, eBPF loader +
                      #   correlator); runs on the targets:
                      #   run|start|stop|status|upgrade|
                      #   uninstall|normalize
cmd/iormeshctl/       # controller (Linux/macOS, no eBPF, no cgo):
                      #   add|rm registry, deploy, collect,
                      #   build, refresh, expand, query,
                      #   open, tui, show
cmd/testworkload/     # wrapper for the Java test app scenarios
internal/c/           # BPF C sources: ior-style helpers +
                      #   generated_tracepoints.c + vmlinux.h +
                      #   committed generation result (diff gate)
internal/generate/    # tracepoint/type generators (Go, ior pattern)
internal/tracepoints/ # generated Go tracepoint tables
internal/types/       # generated C-derived Go types
internal/schema/      # the output contract in Go: Event, Graph,
                      #   NDJSON + parquet mapping (cgo-free)
internal/bpf/         # libbpfgo loader, attach, ring buffer (cgo)
internal/correlate/   # event joins, pid/fd/path graphs,
                      #   agent-event join
internal/target/      # target spec: PID, pattern, listening port
internal/segment/     # minute-rotated parquet segments, ready/
                      #   contract, disk watchdog
internal/redact/      # one redaction policy: argv, headers,
                      #   query params, paths (cgo-free)
internal/agentspool/  # reader for the Java agent's spool
internal/buildgate/   # repo-wide gates: controller stays
                      #   cgo-free, no //nolint
internal/normalize/   # symbolic ids, marker brackets, golden
                      #   check (cgo-free — tracer, tests and
                      #   iormeshctl share it)
internal/federate/    # server registry, trace sync (rsync,
                      #   delete-after-transfer), offline
                      #   merge (cgo-free — shared with iormeshctl)
internal/servicedb/   # service-interaction database: observed
                      #   connections, naming rules, CLI-facing
                      #   queries (cgo-free)
internal/expand/      # controller-driven mesh expansion:
                      #   unresolved peers, allowlist, hop and
                      #   node limits (cgo-free)
internal/query/       # trace slicing for humans and LLMs
                      #   (cgo-free)
internal/render/      # deterministic SVG diagram generation
internal/viewmodel/   # the one model every surface renders
                      #   (cgo-free)
internal/site/        # local site: embedded HTML/JS, data files
internal/tui/         # Bubble Tea terminal explorer
internal/textview/    # plain-text views: show, collect summary
internal/k8s/         # cgroup→pod identity mapping, k8s metadata
internal/routemap/    # FQDN→ingress→workload map (config + k8s discovery)
internal/bpfembed.go  # go:embed of the compiled .bpf.o + env override
agent/                # Java agent source (M3): bridge jar
                      #   (bootstrap-visible hooks) + core
workload/             # traceable test app source (M0) + container image
integrationtests/     # end-to-end suite, ior pattern: harness,
                      #   expectations, per-family scenario tests,
                      #   kernel probes, multi-host topology runs
eval/                 # LLM introspection eval: questions,
                      #   results (M5)
spikes/               # throwaway spike prototypes (S1–S3);
                      #   their results live in docs/spikes/
deploy/               # k8s manifests: sidecar patch, debug container
assets/               # logo + wordmark (SVG)
Magefile.go           # generate/build/test targets, ior-style
scripts/              # container builds (docker/podman), ior-style
docs/                 # design docs: architecture, trace scope,
                      #   output contract, testing, security,
                      #   build, implementation guide;
                      #   docs/spikes/ holds the spike results,
                      #   docs/diagrams/ the generated SVGs
```

(Layout mirrors ior's generation tree so the pattern can be lifted
with minimal renaming; exact shapes settle when M0/M1 start.)

## Non-goals (for now)

- Modifying target behavior (writes, injection, parameter edits).
- Blanket Java method entry/exit tracing — the agent instruments
  I/O boundaries only; cost must scale with I/O, not with calls.
- Credentials on servers, or tracers that deploy or connect
  anywhere — expansion is the controller's job (M6).
- Kernel-mode dedup/ aggregation — correlation happens in userspace.
- Non-Linux hosts — BPF + JDK 21 on Linux is the whole universe.
- No payload capture for files and non-HTTP socket streams:
  paths, sizes, offsets, endpoints only. (Argv is metadata but
  recorded redacted; env capture is off by default — decision
  #9.) The one deliberate exception:
  HTTP(S) request metadata (method, URI, headers) via
  agent-boundary hooks, redacted per policy — decision #22.
- No Java method-argument capture: stacks and lines suffice to
  match source; args add cost and secrets risk.
- No GC/JIT events — I/O attribution only. (Exception events are
  decision #10.)
- No Kubernetes operator or auto-injection webhook — the
  deployment owner adds the sidecar explicitly, by patch.
- No packet sniffing: nothing is parsed off the wire — HTTP
  request metadata comes from the Java agent's API-boundary
  hooks; routing knowledge comes from DNS answers, the
  configured route map, the k8s API, and Traefik's own
  configuration/logs.
- Tracing the mesh itself into the mesh: the tracer's own
  footprint, the Java agent's spool writes and the controller's
  SSH/rsync traffic are out of the traced set.
- Cross-host pairing of HTTP/2 requests in v1, unless stream ids
  are captured (decision #22) — recorded, rendered time-ordered.
