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

  Result (2026-11-18, JDK 25 and 27-ea, kernel 7.2.15, bpftrace;
  [docs/spikes/s1-java-join.md](docs/spikes/s1-java-join.md)):
  works. Uprobe TID marks on a curated set of leaf I/O natives
  plus the agent's bracket on the same TID attributed every
  declared op of `file-basic` (6/6) and `tcp-loopback` (server
  11/11, client 10/10) to its declared method, 0 mismatches. The
  uprobe side needs a per-native syscall allowlist (JVM work
  runs inside brackets), must drop x86_64 syscall 335 (the
  uretprobe trampoline), keys marks on probe ids and resets them
  on thread exit. Clocks: compared in CLOCK_MONOTONIC — BPF
  stamps the native's entry with `bpf_ktime_get_ns`, no
  conversion; a boot/mono pair read in BPF is off by roughly
  70–230 ns (run-dependent) and pushed 14/20 000 stamps out of
  tight brackets in one run.
  Transport: an mmap ring, no syscall per record, every record
  delivered or counted (#27). Attach: live attach works on 25
  and 27-ea with JEP 451's warning (#2). Cost (loaded host, CPU
  time, indicative): per hooked I/O call ≈3.5 µs for the uprobe
  pair and ≈8 µs for the agent's 10-frame stack walk; the agent
  without the walk ≈0.3 µs. Decisions #2, #6 and #27 are
  settled, and the envelope's Java fields with them: nested
  `java.thread` / `java.stack` (see "Trace output contract").
  Untested in S1: a JDK 21 runtime and the ASM-based agent on
  21 and 17 (both since run in M3, see #29), stripped or static
  JDK libraries, multi-threaded cost, cost on
  a quiet host, the join on the native-entry stamp (the prototype
  paired on `sys_enter`), per-PID uprobe attachment, containers.
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

  Result (2026-11-18, kernel 7.2.15, bpftrace;
  [docs/spikes/s2-cross-host.md](docs/spikes/s2-cross-host.md)):
  works. The ISN pair joined 34/34 connections per pass, all
  unique, under DNAT and under DNAT plus random-port SNAT,
  including reused 4-tuples (TIME_WAIT takeover and RST) and TCP
  Fast Open. It is rebuilt from `tcp_sock` at `ESTABLISHED` or
  any later record, role-aware: connecting side
  `iss = snd_una − bytes_acked`, accepting side
  `iss = snd_una − bytes_acked − 1`, both
  `irs = rcv_nxt − bytes_received − 1` (mod 2³²). Conntrack
  joins only as an event stream on the NAT host, and only with a
  time order for reused tuples; a snapshot misattributed 10/34
  under SNAT. Clock bounds: two tracer instances with injected
  skews of +1.23 s and −0.99 s recovered them inside intervals
  ≈10–25 µs wide on veth (runs 7–9, load-dependent; midpoint
  within 1 µs), with sends and
  receives matched by TCP sequence offset. The width depends on
  the traffic: single request/response connections alone gave
  82–104 µs, ping-pong rounds tightened it. Decision #26 is
  settled. Untested: SYN cookies, connections over 4 GiB, IPv6,
  real hosts, clock drift over long runs.
- **S3 — Overhead and volume budget** (gates M2; pins decision
  #25). Measured on the M1 skeleton with ior's `perf/` +
  bench-profiles pattern: tracer CPU, the cost untraced
  processes on the host pay, ring-buffer loss rate, and trace
  bytes per minute.

  Exit: the budget's numbers are written into decision #25 and
  become acceptance criteria from M2 on.

  Result (2026-11-18,
  [docs/spikes/s3-overhead.md](docs/spikes/s3-overhead.md);
  medians of 3 on a busy host, load 8–12, so order of
  magnitude): on the per-syscall worst case (`syscall-burst`,
  1-byte read/write) tracing slows the target 1.3–2.3×, other
  processes on the host pay ~1.14× per syscall while a trace
  runs, the tracer spends 6.6–13 µs CPU per event, and a
  sustained burst loses 16–42 % of its ring records — the
  consumer, not the ring, is the limit (a short burst and the M0
  scenarios lose nothing). NDJSON is ~326 B/event; zstd shrinks
  it 18×. Hot read/write loops therefore need in-kernel
  aggregation (ior's `syscall_aggregate_map`) in M2; decision #25
  records the budget. The two timing items are measured by the
  load-tolerant paired CPU-time method in #25 (2026-11-19; this
  host never gets quiet); task md4 measured both inside the
  budget.

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
| M2 | Full file surface — `file-lifecycle` (create/delete, rename while open, move, truncate, touch), `file-links` (symlink/readlink/lstat, hardlink), `file-attrs` (chmod/chown by path and fd, xattr); fd lifecycle (dup, inheritance across fork/exec, reuse) — `fd-lifecycle` (these four landed ahead of the M2 tracer work); local IPC — `pipe-ipc` (pipe, child stdio pipes), `unix-socket`; signals — `signals`; UDP — `udp-loopback`; IPv4 + IPv6 — `tcp-ipv4`, `tcp-ipv6` (these six landed ahead too); restart — `restart`; non-Java peer — `non-java-peer` (these two landed ahead too); argv redaction — `argv-secret` |
| M3 | Method flows (deep stacks, async I/O) — `method-flow`; HTTP plain + TLS — `http-plain`, `http-tls`; async client — `http-async` (task wd4); redaction — `http-redaction` (these four landed ahead of the agent, their Java parts pending until tasks 2b4 and 3b4, see below); OTEL flavor — `otel` (landed ahead too, in its own module; its ids pending until task 4b4); third-party HTTP libraries — `http-servlet`, `http-apache`, `http-okhttp`, `http-netty` (task de4, in their own module) |
| M4 | Client and server on two hosts as two projects — `tcp-two-host`; microservices topology, one service per host — `microservices` (task jb4, landed ahead on loopback; the cross-host run comes with M4's multi-host test); NAT'ed pair — `tcp-nat` (these two task kb4) |
| M5 | (renders M4's microservices scenario) |
| M6 | Microservices topology with only the gateway enrolled |
| M7 | The same scenarios as pods in kind; ClusterIP; ingress |

Scenario notes:

- **HTTP scenarios** (M3): client and server exchange declared
  HTTP requests (plain and over TLS) with known method, path and
  headers — expectations assert the captured http events and the
  redaction: a planted `Authorization` header, a planted secret
  in a query parameter and a planted secret in argv must never
  appear in the trace (argv's is M2's `argv-secret`).
  `http-plain`, `http-tls` and `http-redaction` pair the JDK's
  HttpClient (HTTP/1.1 forced) with its built-in HttpServer,
  one keep-alive connection per run; `http-tls`'s server makes
  a fresh key pair and self-signed certificate in memory at run
  time and hands only the public certificate to the client,
  which pins it — no key is ever committed or written.
  `http-redaction` plants its secrets in `Authorization`,
  `X-Api-Token`, `Cookie`, `Set-Cookie` and a query value, and
  the server fails the run unless they arrived.
- **Method-flow scenario** (M3): `method-flow` issues each I/O
  op from its own named method, so the golden declares the
  causing method per op: a write 24 frames down a recursion
  (past the agent's 10-frame cut), and an executor hand-off
  across three named platform threads (virtual threads stay
  deferred).
- **M3 goldens ahead of the agent**: the M3 scenarios landed
  before the agent (tasks ya4, za4), so each shipped two files.
  Its `expect.json` declared what the tracer recorded then;
  `expect-pending.json` added what needs the agent —
  `attribution: java`, a `java` object (thread name, causing
  method as `class#method`, the innermost workload frame),
  `http` events and, for `otel`, an `otel` object (trace and
  span id) — kept in step with `expect.json` by test. The
  agent's join (task 2b4), HTTP hooks (task 3b4) and
  span-context read (task 4b4) taught the golden format the
  java, http and otel fields, so all five are promoted: one
  `expect.json` each, run under the agent by the integration
  tests, and no pending file is left. A third-party library's
  http event (task de4) names the library's own entry frame as
  its causing method (`InternalHttpClient#doExecute`,
  `HttpServlet#service`, …) — the rule's innermost non-JDK frame;
  the app's call is below it on the stack.
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
  `microservices` (task jb4) has four roles, one process each:
  the gateway places order 42 with orders (TLS), which first
  reserves stock with inventory (plain) and charges billing
  (TLS); then the gateway reads the stock left from inventory
  (plain) — chain gateway→orders→inventory, fan-out from
  orders and from the gateway, fan-in at inventory, every hop
  its own connection in a fixed order. Each service boots in
  its own `<role>.d` (config written, then read) and saves one
  record as `.tmp` + `rename`; a TLS service publishes its
  in-memory self-signed certificate to the shared `--dir`,
  where its callers pin it. The golden declares every hop as
  one fused connection with an http event on each end (scheme
  `https` on the TLS ones) and each service's file work with
  its causing Java method. Across hosts (task 1f4) a server
  binds `--bind` and its certificate names that address, and
  a caller dials `--<callee>-host` (IPv4 literals, default
  127.0.0.1, so the loopback golden is unchanged). The
  deployment copies the public certificate into the caller's
  `--dir`. `expect-spread.json` is the merged golden for the
  rig's three emulated hosts: every service `runs_on` its
  host, and the three cross-host hops (two TLS, one plain) are
  fused at their addresses.
- **Restart scenario** (M2): the app exits mid-session and
  comes back with a new PID; expectations assert both
  incarnations were traced under one app identity, with
  nothing mis-attributed across the gap. `restart` is a
  launcher that runs the app (a child JVM) twice in a row with
  the identical argv; only the state file the first
  incarnation leaves tells the second it is a restart. Each
  incarnation brackets itself, because a pattern-bound
  session (the argv glob of decision #23) never sees the
  launcher; the golden declares two distinct processes, each
  with its own file work, linked by `incarnation` edges from
  one app node.
- **File-surface and fd scenarios** (M2): `file-lifecycle`,
  `file-links`, `file-attrs` and `fd-lifecycle` declare the
  syscalls strace shows on the host JDK, as JDK-only Java can
  issue them (no `fchmod`, and `dup` only via a directory
  stream). `fd-lifecycle`'s child JVM brackets nothing itself:
  a process forked inside a bracket belongs to it. The
  correlator records each fork of a traced process as a
  `forked` graph edge (parent → child), and the normalizer's
  per-process cut gives a marker-less child whose first event
  lies inside its parent's bracket that bracket.
  `file-links`' lstat is `statx` on JDK 22+ (measured on 25
  and 27-ea); JDK 21 issues `newfstatat` instead (21 itself
  unmeasured), so the golden holds for JDK 22+ only.
- **IPC, signal, UDP and address-family scenarios** (M2):
  `pipe-ipc`, `unix-socket`, `signals`, `udp-loopback`,
  `tcp-ipv4` and `tcp-ipv6` declare what strace shows on JDK
  25–27, within what JDK-only Java can do: pipes come from
  `java.nio.channels.Pipe` and a child's stdio, and the only
  signals are SIGTERM and SIGKILL to the workload's own
  children (`Process.destroy`/`destroyForcibly`), so the golden
  declares the sender's `kill` and leaves delivery to the
  tracer. `tcp-ipv6` exits with the skip code 3 on a host
  without `::1`, so a harness reports a skip, never a pass.
- **Non-Java peer** (M2): one scenario talks to a non-JVM
  process, proving the degraded node shape — files and sockets
  only, no Java stacks, no agent-sourced hostnames or http
  events, every event marked unattributed rather than left
  ambiguous. `non-java-peer`'s peer is a minimal cgo-free Go
  echo server built in-repo (`workload/echopeer`), not ior's
  `ioworkload`, whose scenarios are all self-contained and
  none can serve a TCP client; the Java side stays JDK-only.
  The golden declares `attribution: none` on every peer event.
- **NAT'ed pair** (M4): client and server with a DNAT hop
  between them (docker port-mapped), so the two ends observe
  different 4-tuples.
- **Two-host scenarios** (M4, task kb4): `tcp-two-host` and
  `tcp-nat` are M0's client and server (file work, then one
  TCP connection) with explicit IPv4 addresses, each end on its
  own host and traced by that host's tracer as its own project.
  Each scenario has three goldens: one per host
  (`expect-client.json`, `expect-server.json` — that host's
  process, its end of the connection unresolved, since no host
  alone sees the far end) and the merged `expect.json` (both
  processes on two distinct hosts, the connection fused). The
  integration rig emulates the hosts on one machine as network
  namespaces, each tracer with a machine id of its own; the NAT
  is docker's `-p 880:80` as an nft DNAT rule on the server's
  host, the server in a container namespace, so the client
  dials 10.231.0.2:880 and the server accepts on
  172.31.0.2:80. The goldens declare the rig's addresses.
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
  expectations too. `otel` (task za4) lives in `workload/otel`
  and builds `workload-otel.jar`, which runs the core `Main`
  and joins its registry through `ServiceLoader`; its only
  dependency is the OpenTelemetry *API* (1.67.0: api, context,
  common), vendored and SHA-256-pinned like the agent's ASM,
  named in the jar's `Class-Path`, never copied into it. No
  SDK: each span is a declared span context made current
  (`Span.wrap(…).makeCurrent()`), which is what the agent reads
  (`Span.current()`), with ids a golden can name. Two calls,
  each in its own trace, cross one keep-alive connection from
  an `HttpURLConnection` client (blocking I/O on the calling
  thread, so the span is current at every client syscall —
  the JDK HttpClient's I/O runs on its selector thread, where
  no span is) to the JDK HttpServer, which extracts the W3C
  `traceparent`/`tracestate` the client's propagator wrote,
  fails the run unless they match the declaration, and answers
  inside a declared child span. Its golden declares an `otel`
  object (`trace_id`, `span_id`) on every event inside a span
  and none on the others (split like the other M3 goldens until
  task 4b4, below).

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
  (rename-aware: an open fd's path follows its file through
  renames and moves — matched by name, since a rename record
  carries no inode and a directory rename moves many; see
  docs/implementation.md M2 step 5) and pid→fd→(remote ip,port).
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
  Settled (task ua4): `iormesh run -native-stacks` takes the
  user stack of every emitted syscall enter
  (`bpf_get_stackid`, frame pointers, 64 frames; a stack whose
  map bucket another stack holds comes inline through
  `bpf_get_stack` instead of being lost, task kd4); events carry
  a per-trace `stack_id` and the stacks go once each into their
  own table, `stacks.ndjson`. Symbolization happens in the
  tracer, from `/proc/<pid>/maps` read at a process image's
  first stack (believed only while `/proc/<pid>/stat` still
  shows the record's image) and the ELF symbol tables of the
  mapped files, opened through `/proc/<pid>/map_files`:
  every frame keeps its raw address, plus module, file offset
  and GNU build id where the address lies in a file, so an
  offline symbolizer can redo what the tracer could not.
  Off, it costs nothing per call (the verifier prunes the
  walk; decision #25); on, its cost is measured in
  [s3-overhead.md](docs/spikes/s3-overhead.md) ("Follow-up
  (ua4)").
- Argv is recorded redacted (decision #9), on `exec` events.
- Output: trace schema v1 — NDJSON event stream + graph export.
- Acceptance: M2's scenarios pass the golden check, including
  edge cases (fd reuse and aliasing, exec, PID reuse, pattern
  restarts (new PID, same app), threaded I/O, loopback pairs,
  socket races) and the non-Java peer, proven by the
  integration suite ("Integration tests — follow ior"); the
  run stays inside the overhead and volume budget (decision
  #25).

  Status (2026-11-19): accepted; the two #25 timing items were
  split to task md4, which gated M3's acceptance (not its
  start — no M3 step reads a #25 number) and has since passed
  both (below). The integration
  suite (27 tests) is green on two consecutive runs with zero
  loss; every edge case above
  is covered end to end (docs/testing.md, "M2 acceptance") — PID
  reuse and socket races too, since task id4 forces them with a
  Go helper (`workload/edgepeer`). Of the #25 budget, loss (0) and
  volume (15.9 B/event compressed) pass; the tracer-CPU and
  bystander timings were only taken on a loaded host (load ~3),
  which says little either way; a rough unfolded run put tracer
  CPU at ~2.7 µs/event, an artefact of subtracting two runs'
  load-and-attach cost. md4 re-measured both by the paired
  CPU-time method on the same busy host: tracer CPU 1.2 µs/event
  at 1 thread and 1.7 µs at 4 (unfolded, medians of 5), the
  bystander 1.19× and 1.15× at 4 threads (CPU time, two sets of
  10 pairs; wall clock 1.21× and 1.14×) — all four #25 items
  passed at that moderate load (before 8e4's dispatch). ae4
  re-ran both after it: the bystander passes (1.16× and
  1.14×); tracer CPU shows no 8e4 regression but is over 2 µs
  at load ≥ 10 (3.0 / 3.1 µs; the build before 8e4 likewise),
  so that item passes at moderate load (md4) and is over at
  load ≥ 10 (ae4); task ee4 re-measures it on M4's segment
  writer
  (docs/spikes/s3-overhead.md, "M2 acceptance measurement",
  "Follow-up (md4)" and "Follow-up (ae4)"). Schema v1 is
  frozen outside `http`.

### M3 — Java agent: code-flow attribution

Gated by S1 — done; the design below is S1's result
([docs/spikes/s1-java-join.md](docs/spikes/s1-java-join.md)).

- **Instrument I/O boundaries, not every method.** The agent
  rewrites only the JDK methods where Java hands I/O to the
  kernel (hook set from S1) and captures the Java stack there.
  Cost scales with the I/O rate, not the call rate; blanket
  method entry/exit tracing is a non-goal.
- **Join to kernel events** (S1): uprobes on a curated set of
  the JDK's leaf native I/O functions mark the thread id with a
  per-probe id; the syscall handler reads the mark and keeps it
  only for the syscall kinds that native issues (anything else is
  JVM work inside the bracket), drops the uretprobe trampoline
  (x86_64 nr 335), and marks are reset on thread exit. The BPF
  side also stamps the native's entry in CLOCK_MONOTONIC. The
  agent's boundary hook supplies what the kernel cannot see —
  the Java stack id and thread — as a bracket keyed by kernel TID
  (one `/proc/thread-self` lookup per thread); the correlator
  pairs a marked syscall with the agent bracket on its TID that
  contains the native's entry, same clock, no conversion. (S1
  paired on the `sys_enter` stamp; td4 pairs on the native-entry
  stamp, the same clock one step earlier.) Events
  no hook brackets (JNI libraries, JVM-internal I/O) and
  ambiguous matches stay in the trace marked `attribution:
  none` — never guessed. Built in task 2b4 (`iormesh run
  -spool`, `correlate.JavaJoin`) first without the uprobe mark,
  the hook's syscall allowlist the only guard against JVM work
  of an allowed kind inside a bracket; task td4 built the mark
  and resolved that deviation. The uprobes (`uprobe_multi`
  links: Linux ≥ 6.10, or a 6.6–6.9 kernel with the stable fix
  of commit 46ba0e49 — before it the pid filter matched one
  thread only; the joiner warns when a JVM's threads close
  brackets without a mark) attach per JVM, scoped to its pid, to the
  natives its ring's hook table names, once the tracer read that
  table; a syscall without a mark, or with another native's,
  is JVM work and stays `none` whatever its kind, the allowlist
  staying as a second guard; a JVM whose natives cannot be
  probed keeps all its syscalls `none`. Cost: see
  docs/spikes/s3-overhead.md, "Follow-up (td4)". Method and
  thread tables are NDJSON (`methods.ndjson`, `threads.ndjson`)
  until M4's parquet writer.
- **Platform threads only in v1.** Virtual-thread attribution
  is deferred (see "Deferred — not in v1"). I/O issued by a
  virtual thread is still traced and still joined to its native
  I/O call, but it is attributed to the carrier thread and
  flagged `carrier: true` — visible as a known gap, never
  passed off as the carrier's own work. As built (task 2b4):
  the agent re-reads the carrier tid at a virtual thread's
  bracket enter and exit (none if it moved carriers), the event
  keeps the virtual thread's stack, `java.carrier` is set and
  `java.thread` names the carrier.
- **Agent → tracer transport**: socket-free like everything
  else — a memory-mapped ring per JVM in a tracer-owned
  directory, written lock-free by the agent and drained by the
  correlator; when it is full the agent drops and counts, never
  blocks (decision #27). The agent's own syscalls (ring setup,
  one thread-id lookup per thread, its watermark thread's
  sleeps) are tagged and kept out of the target's story.
- **Attach**: `-javaagent` at launch is the primary path. Live
  attach stays supported for the "trace an existing PID" story,
  with its cost stated: the JVM prints JEP 451's warning on
  stderr (seen on 25 and 27-ea), a later release disables
  dynamic loading by default, after which the app must opt in
  with `-XX:+EnableDynamicAgentLoading`, and a live-attached
  agent misses everything before the attach (decision #2).
  As built (task ge4): the tracer attaches itself, speaking
  HotSpot's attach protocol (no JDK on the host) — `iormesh run
  -agent JAR` for the JVMs a session starts with, `iormesh
  attach` for one at any time — and never signals a process
  that could die of SIGQUIT; caveats in docs/java-attach.md.
- **Stack capture cost**: the per-I/O `StackWalker` walk is the
  agent's dominant cost (S1: ≈8 µs per call on a loaded host vs
  ≈0.3 µs for the rest of the bracket); M3 engineers it down
  (fewer frames, cached stacks for repeated call sites) and
  measures it against decision #25.
- Supporting mappings the model needs: class-load map
  (class↔JAR/classloader), thread names (kernel TID ↔ Java
  thread), DNS hostname↔IP (InetAddress hooks), `file:line` on
  frames when debug info is present; TLS peer identity if adopted
  (decision #8).
  As built (task fe4): the class-load map lists the classes a
  recorded frame names, not every class the JVM loads — a
  frame's class is what the model joins to a JAR, and a whole
  application's load (tens of thousands of classes, most never
  on an I/O stack) would cost ring space and agent work for
  nothing. The agent describes a class once, when a new stack
  first names it (so per distinct call site, never per bracket):
  its binary name, its class loader (`bootstrap`, the loader's
  name, else the loader's class name) and its code source — the
  JAR or directory path of a `file:` location, `jrt:/<module>`
  for a runtime-image class, other URLs with userinfo, query and
  fragment stripped (redacted again by the tracer), empty when
  the class has none. Code sources are paths like the file
  events' and are recorded verbatim. A per-class `ClassValue`
  holds the id, so no class or loader is pinned and no lock is
  held across anything the app runs (no class is initialized).
  The STACK record carries each frame's class id; the tracer
  writes `classes.ndjson` (`class_id, class, loader,
  code_source`, deduplicated across the trace's JVMs) and each
  method row gains `class_id`, so a method id is a frame of one
  class: the same frame text from two JARs is two ids. Threads:
  a thread renamed after its first bracket gets another THREAD
  record at its next bracket, and its later events carry the
  new name; `threads.ndjson` lists one row per name change.
- **OpenTelemetry introspection**: when the app runs the OTEL
  API (SDK or not), the agent reads the active span context and
  tags every I/O event with its OTEL trace/span ids — iormesh
  events join 1:1 with the app's distributed traces in any OTEL
  backend. Task 4b4 built decision #20's default: the agent
  reads `Span.current().getSpanContext()` reflectively, in the
  class loader of each OTEL copy, at a bracket's enter and when
  an HTTP request is recorded, and the joined event gets an
  optional `otel` object (`trace_id`, `span_id`; attribution
  java only, absent outside a span). A copy is read only once
  the app itself has initialized OTEL's context storage (the
  agent rewrites the end of `LazyStorage`'s static
  initializer): the agent does not trigger that initialization,
  which runs a ServiceLoader and freezes the app's storage
  wrappers — except that a live attach arms a `LazyStorage`
  already loaded, and one caught between loading and
  initializing is initialized on the attaching thread. An app
  without OTEL never arms anything.
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
  recorded. Task 3b4 built the JDK's three — HttpServer (at
  the exchange's close, or the handler's return), HttpClient
  (synchronous `send`) and HttpURLConnection (its response
  read): one `http` event per request with method, scheme,
  host (client side), path, status, protocol version and the
  request and response headers, redacted in the agent before
  the spool and again by the tracer; joined to its connection
  by the socket ends the library still holds. Task wd4 added
  latency and sizes: the call that began the request (the
  client's send or response read, the server's dispatch) stamps
  its entry, so the event spans the request (`boot_ns` its
  start, `latency_ns` its duration) when that entry is provably
  the request's own; body sizes are what the library knows
  (declared or counted lengths), absent otherwise — never
  estimated. `sendAsync` is recorded at its future's completion
  under the sending thread's stack and span (scenario
  `http-async`), without latency or `req_seq`: the agent's
  completion runs after the app's own callbacks, so its stamp is
  no evidence of the response's time or order. The DNS hooks (#8) landed with it: a `dns`
  event per new host-to-addresses mapping of a JVM. Task de4
  started the third-party libraries, which the agent rewrites
  in the application class loader that defines them (only one
  that sees the agent's bridge; per-class reflective probes, no
  library resolved at install, nothing held past its class):
  Servlet — `HttpServlet.service(ServletRequest,
  ServletResponse)`, jakarta and javax, any container, the
  original dispatch only (scenario `http-servlet`, embedded
  Tomcat); Apache HttpClient 5 classic — `doExecute` for the
  request, httpcore5's `HttpRequestExecutor.execute` counting its
  exchanges and naming the connection (scenario `http-apache`);
  OkHttp 4/5 — the call's interceptor chain, its
  `CallServerInterceptor` per exchange (`http-okhttp`); Netty's
  HTTP/1.x codec — decoded requests paired per channel with the
  answers the app's handlers write (`http-netty`). Decision #22's
  v1 set is complete; HTTP/2 stays time-ordered (no stream ids),
  and Apache HttpClient 4, OkHttp 3, Netty 4.1 and async Apache
  are not hooked.
- **Request pairing across the wire**: a request on the client
  and on the server pair up by connection id plus a
  per-connection request ordinal — sound for HTTP/1.1. HTTP/2
  multiplexes requests on one connection, so an ordinal cannot
  pair them: v1 records HTTP/2 requests but renders their hops
  time-ordered unless stream ids are captured (decision #22).
  The tracer numbers a connection's requests (`req_seq`) per
  end in completion order, and leaves the ordinal out over
  HTTP/2, without a connection, once a JVM may have lost an
  HTTP record — a lost request would shift the count silently —
  from a request the client library retried by itself
  (redirect, 401/407) on, which the server counted as several,
  and — for java.net.http's client ends, JVM-wide — from the
  JVM's first `sendAsync` call on: an async request's connection
  returns to the pool before its delayed record, so a later
  request's record on it could take its ordinal (the agent marks
  the call in the ring before the request reaches a connection).
- Acceptance: frames carry `class#method(file:line)`; every
  declared I/O op of M3's method-flow scenarios carries its
  declared causing method; unbracketed events are marked unattributed; the
  OTEL-flavored workload's events carry the expected
  trace/span ids; the HTTP scenarios produce the expected
  http events, and the redaction expectations hold — the
  planted header, query secret and argv secret never appear;
  agent overhead stays inside the budget (decision #25). The
  join itself (`-spool`) measured 5.2 µs per event when every
  event of the S3 burst is joined (task 2b4,
  docs/spikes/s3-overhead.md "Follow-up (2b4)") — over the
  ≤ 2 µs budget; task ud4 cut it by a third in instructions
  (3.8 µs at 1 thread, 5.6 µs at 4, at load 8–13; "Follow-up
  (ud4)"), still over budget.

  Status (2026-11-19): accepted functionally — the integration
  suite is green, and of decision #25 the bystander (1.16× /
  1.14× after 8e4's dispatch), loss and volume items pass. The
  tracer-CPU item is **open and over**: plain unfolded 2.28 µs
  at 4 threads (its 30–58 % record loss passes only under the
  default fold), `-spool` 3.8 / 5.6 µs at 1 / 4 threads. With
  the remaining cost spread thin, the next step is M4's output
  path, which itself needs M3 accepted, so the item is split to
  task ee4 (owner-delegated advisor decision, the M2 pattern):
  it re-measures on M4's segment writer, may cut the join and
  correlator too, and gates M4's acceptance, not its start; the
  limit is unchanged, and it ends in a pass or an explicit owner
  decision recorded here, never a quiet relaxation. Note for
  ee4 (task 8b4): the segment writer costs ≈ 3× the NDJSON sink
  per event (≈ 10.4 k vs ≈ 3.4 k instructions), not less;
  `DataPageStatistics(false)` (≈ −7 %) is the known lever.

  Acceptance run (2026-11-20, task 5b4): `mage integrationTest`
  40 / 40 on three of four runs with no skips and zero lost
  or skipped records at load 9–12 (Java method flow, HTTP —
  JDK client and server, sendAsync, TLS, redaction, Servlet,
  Apache, OkHttp, Netty — OTEL ids, DNS, live attach at start
  and mid-run, the class-load map); `mage agentTest` green on
  JDK 17, 21, 25 and 27-ea; workloadTest, unit tests and lint
  green. The fourth run failed method-flow: 17 events that
  carried a Java mark were judged unbracketed although all 180
  brackets arrived (attributed 21 instead of 37) — an
  intermittent join defect under load, never a misattribution.
  Task se4 traced it to the boot − monotonic offset: one
  user-space clock triple preempted between its reads skewed it
  by milliseconds, so the stale-mark check (a native's last
  syscall starts ~100 ns before its bracket closes) judged whole
  batches unbracketed. The offset is now the narrowest of eight
  triples and the summary counts stale marks apart; three more
  full runs were 40 / 40, the last at load 21 with stale 0 in
  every scenario. With se4 closed, M3 is accepted; the
  tracer-CPU item stays open in ee4 (2026-11-20: plain unfolded
  passes 2 µs on the segment path; `-spool` missed it, the owner
  changed its limit for that case to ≤ 4 µs and ≤ 12.5 k
  instructions per event, which it passes — decision #25 item 1).
  Schema v1 is frozen in
  full (docs/output-contract.md). Open
  beyond ee4: the third-party hook gaps listed above, the Netty
  client side unit-tested only, attaching JVMs a pattern session
  sees start, and cross-mount-namespace attach (M7).

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
  #28), push it over SSH into the deploying user's staging
  directory, install it as `/opt/iormesh/bin/iormesh` with
  `sudo iormesh upgrade`, and start it with `sudo iormesh
  start`. Every session is labeled with a project (`-project`),
  so completely different, non-interacting applications on the
  same server are traced and stored independently. Built (task
  db4): `iormeshctl deploy [-project P] HOST...` refuses an
  unregistered host before contacting any; stages in
  `~/.iormesh/staging` (never `/opt` or `/tmp`); never passes
  `-allow-downgrade`; an upgrade refusal starts nothing; a live
  session is restarted (stop, start; #19) when its start argv
  or build differs from the one the controller last launched
  (`~/.local/share/iormesh/deployed.json`) or with `-restart`,
  else left alone, so a re-deploy is a no-op. Deploy only
  upgrades: a host without the installed tracer is reported as
  needing the bootstrap of #1, never installed.
- **The tracer runs as a transient systemd unit**: `start`
  launches the session through `systemd-run` as
  `iormesh-<project>`. That gives it a life independent of the
  SSH session, cgroup CPU and memory caps (the enforced half of
  the overhead budget), logs in the journal, and a clean stop
  (`iormesh stop` → SIGTERM → flush the active segment, write
  the final event). Runs default to a 4h auto-exit
  (`-duration`, ior-style), with the unit's `RuntimeMaxSec` as
  a second, independent bound — no unbounded daemons on
  servers. Built (task 9b4): the caps default to one core
  (`CPUQuota=100%`) and 1 GiB (`MemoryMax=1G`), overridable
  with `start -cpu-quota`/`-memory-max`; `RuntimeMaxSec` is
  `-duration` + 5 min and `TimeoutStopSec` 30 s before systemd
  SIGKILLs. The run itself writes
  `<trace-dir>/<project>/state.json` (running once attached,
  then ended with its end reason); `start` waits for it, `status`
  reads it, and `start` refuses a project whose unit is live —
  flags change by stop and start (#19).
- **One sudoers rule**: every privileged action is a tracer
  subcommand — `start`, `stop`, `status`, `upgrade`,
  `uninstall` (and `run`, the foreground tracer `start`
  wraps) — so a
  single pinned rule for `/opt/iormesh/bin/iormesh` covers the
  whole lifecycle; nothing else on the server needs sudo. The
  binary is root-owned; how it gets installed and upgraded
  without turning that rule into full sudo is decision #1.
  Since every argument under the rule is the caller's, a
  session started through it is confined (task 9b4): `start`
  always, and `run` of the installed binary under sudo, refuse
  `-out`, `-cpuprofile` and `-ready`, pin `-trace-dir` to
  `/opt/iormesh/traces` and `-spool` to
  `/run/iormesh/spool/<project>`, and give `ready/` only to the
  sudo caller; no existing directory is ever chmod'ed. Through
  the rule only `run`, `start`, `stop`, `status`, `upgrade`
  (signature-checked, decision #1) and `uninstall` run — every
  other subcommand is refused, fail closed
  ([docs/security.md](docs/security.md) has the rule's text
  and the install's ownership requirements).
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
  A segment is its events file plus one file per table that
  got rows while it was active —
  `<…>-<seq>.methods.parquet`, `.threads`, `.classes`,
  `.stacks` — published tables first, so a collected events
  segment never names a Java table row that is not yet in
  `ready/`; the union of a session's segments is its tables.
  `<session>` is 8 random hex digits per run (names never
  collide, even in the same second); every file's footer
  repeats project, FQDN, session, seq and start. A segment
  without a single row is not published (an idle session
  leaves no empty files), and a crashed run's footer-less
  active file is removed by the next start.
  Status (2026-11-20, task 8b4): built — `internal/segment`
  and `iormesh run -trace-dir` (the default without `-out`;
  `-out` keeps writing NDJSON, both together tee). The
  parquet library is ior's, `parquet-go` v0.29.0, with ior's
  one-encoder zstd codec; per event the writer costs ≈ 3× the
  NDJSON sink (≈ 10 k vs 3.4 k instructions on the burst
  event, ≈ 4.6 B/event on disk for that burst) — ee4 measures
  what that does to decision #25's CPU item. Task ee4 replaced
  the events file's writer with the segment package's own
  columnar encoder (the tables keep `parquet-go`): ≈ 2.5 k
  instructions per burst event, below the NDJSON sink, the same
  records read back, files smaller; one data page v2 per column
  chunk, null-count statistics only, no column or offset index
  (docs/schema.md, "Parquet mapping").
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
  As built (task 8b4, decision #17's default): both limits —
  `-disk-limit 90` (percent used, df's view, so root's
  reserved blocks are never eaten) and `-disk-min-free
  256MiB` (four segments at the size cap), either 0 for off;
  checked every 5 s from the run loop and before every
  rotation; a trip stops the trace with end reason
  `disk_limit`, publishes the last segment and exits 0; the
  startup check runs before the BPF object loads.
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
  Built (task eb4, `internal/ctl/collect.go`): `iormeshctl
  collect [-project P]... [HOST...]` — no HOST is the whole
  registry; every host and project is checked against the
  registry before any host is contacted. Per host an `ssh
  true` probe (an unreachable host fails, it is never "nothing
  to collect"), then per project `test -d` of its `ready/` (a
  project never started — exit status 1, nothing else — is
  skipped) and the Transport's Fetch (`rsync -rt
  --remove-source-files`, only the non-dot `*.parquet` files
  directly in `ready/`, no sudo). Each host
  lands in its own directory,
  `~/.local/share/iormesh/traces/<project>/<host>/` (the
  registry's host name): a server picks its own segment
  names, so a shared directory would let one host overwrite
  another's. The store and project directories are created
  0700 and refused when foreign-owned or writable by others;
  the host directory is forced to 0700 by every fetch. A
  failed host is reported, the others still collected, and
  the command exits 1 naming it. The build reads every
  `*.parquet` under `<project>/*/` and skips dot files
  (rsync's in-flight temporaries).
- **Build**: merge the collected segments offline by stable
  ids — connection ids, the ISN-pair join key (decision #26),
  clock reconciliation — fusing the
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
  The merge is built (task fb4, `internal/federate`): it reads
  each host session through a format-neutral source (NDJSON
  trace directories, and parquet segments since task ib4), fuses only a
  mutual single ISN-pair match inside a wall-clock window
  (default 2 minutes — boot clocks of two hosts share no epoch),
  and keeps every other TCP end unresolved with its reason
  (`no_role`, `no_key`, `key_conflict`, `no_match`,
  `ambiguous`). The `tcp_state` schema carries the four
  `tcp_sock` fields, and the tracer captures them (task ve4,
  `tcp_state_event` 88 → 112 bytes, one CO-RE read per field
  and record — negligible), so real traces now fuse: the
  integration test rebuilds one key from both ends of a real
  connection.
  `build` and `refresh` are built (task ib4, `internal/ctl/
  build.go`, `internal/federate/segments.go`): `iormeshctl
  build [-project P]...` (default: every project in the store)
  reads each events segment as its own merge source —
  `internal/segment`'s reader, strict like the NDJSON one — so
  the service database's per-source counts stay exact; the
  server keeps no graph (#3), so each session's graph is
  rebuilt from its events by the correlator's rules. What the
  graph knew that no event said is in the trace since task
  ze4, as appended optional kinds and fields (schema v1 stays
  append-only): a `fork` event per new traced process (on the
  child, with `args.parent_pid` and `args.parent_incarnation`
  — the forked edge, and the point where the child inherits
  its parent's fd table, which the rebuild copies from its own
  view of the parent), an `attach` event per process the
  session took on (a `-pid` or port target, with
  `args.pattern` for a pattern's incarnation — the app node
  and its incarnation edge), and `fcntl(F_SETFD)`'s argument in
  `args.flags`; once per process, so decision #25's budget
  does not notice them. A graph rebuilt from a single-host
  run's segments equals that run's graph.json — the
  integration suite checks it on every run (M4 acceptance:
  the merged mesh equals the single-host result). The build merges,
  calls `Observe` and `Import`, builds the view model (V1:
  run, health, services, edges — docs/visualization.md) and
  writes `~/.local/share/iormesh/builds/<project>[+<project>
  …]/graph.json` (the merged graph export) and `model.json`,
  then prints the summary; `iormeshctl show` prints it again,
  or the services, edges or health view, from `model.json`
  alone. A rerun over the same segments writes the same bytes.
  A segment that cannot be read, whose footer disagrees with
  its name or directory, or whose events do not validate is
  skipped whole (its session's graph rebuilt without it),
  named in the summary and the model, and the command exits 1
  after writing the outputs: one damaged file does not hide
  the rest of the mesh, and the loss is never silent.
  A server may be compromised, so a collected segment is
  hostile input (ib4's review): the reader bounds the file
  (128 MiB) and its footer (8 MiB) before parsing, turns
  decoder panics into errors, and each segment is decoded
  first in a child process with a capped address space, so a
  file built to exhaust memory fails itself, not the build.
  Each host identity (host id, FQDN) belongs to one store
  directory — the one thing a server cannot choose: the first
  build pins it (`hosts.json`). A session whose directory is
  pinned to another host, or whose host id or FQDN is pinned
  to another directory, is bad — only that claimant: the
  pinned host's own sessions stay good. Among unpinned hosts,
  a directory holding two identities, or an identity under
  two directories, makes all sessions involved bad (which one
  is forged cannot be told). Event clocks past int64 (an
  overflowing wall_ns + latency_ns) make their segment bad, not
  the build's import fail. Text from the traces — in views and
  in every error — is printed terminal-safe.
  `refresh` is collect then build; a host whose collect fails
  is reported (exit 1) and what the store holds is built
  anyway.
- **Clock reconciliation from the traffic itself**: every
  fused connection gives send-before-receive constraints in
  both directions (send = send-syscall enter, receive =
  receive-syscall exit, the two ends of a message matched by
  TCP sequence offset, which NAT leaves alone); together they
  bound the clock offset per host pair. The merge records the
  bound and its width — S2 measured ≈10–25 µs on veth with
  interactive traffic (load-dependent), 82–104 µs with single
  exchanges only — computed per time window so clock drift
  over a long session shows up instead of emptying the
  interval; an empty intersection is reported, never hidden.
  This needs every send and receive record to carry its TCP
  sequence range, not just its byte count — captured in the
  data calls' own syscall hooks (task xe4), not in new
  `tcp_sendmsg`/`tcp_recvmsg` kprobes. Cross-host order
  is derived from happens-before (per-thread order plus
  send→receive edges), never from raw wall-clock comparison.
  Built (task gb4, `internal/federate`): send and receive
  records carry `args.tcp_seq` (the sequence number of the
  call's first byte; the tracer fills it since task xe4: the
  socket's `write_seq`/`copied_seq` read at the call's enter
  and again at its exit, given only when the two differ by
  exactly the call's return — so concurrent senders or readers
  on one socket, MSG_PEEK, kTLS and the like give none, never a
  guess; one ring record per call as before, the exit record
  carrying it; decision #25's burst figure unchanged, +3 %
  tracer instructions per TCP data event — s3-overhead.md,
  "Follow-up (xe4)"),
  the SYN adds one client→server edge (connect enter before
  the server's accept exit), and the merge keeps one bound per
  host pair per 1-minute window (`Result.Clocks`), one-sided
  when only one direction has edges, empty and flagged when
  they contradict. The offset drifts within a window, so each
  bound is widened by an assumed drift rate (default 1 ppm,
  NTP-disciplined hosts: 60 µs per side per minute); a clock
  stepped or slewed harder breaks the assumption. The `build`
  summary and the view model print them per host pair
  (windows, widths, one-sided and empty ones; task ib4); M5's
  views will show them too. The bound never feeds back
  into fusion — it is proven by the fused connections, so it
  cannot choose them; the fusion window stays a wrap guard,
  and hosts skewed beyond it get neither edge nor bound.
  Cross-host order (`Timeline`) is decided only when the
  widened bound — summed along a chain of hosts when two never
  talked directly — leaves no doubt; otherwise, including a
  message's own send and receive, it stays unordered.
  Contradictions around a cycle of three or more hosts are not
  detected yet.
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
  Built (task hb4, `internal/servicedb`, #24's default: one
  sqlite database via `modernc.org/sqlite`, glob rules): one
  row per observed TCP connection — fused, unresolved with the
  merge's reason, or a same-host pair — keyed by its anchor end
  (the client half's host-local conn id when the client was
  traced, else the server half's), so a rebuild upserts. The
  window, endpoint and verdict merge as MIN, MAX or
  keep-if-unknown, and a fused verdict never reverts. HTTP
  request counts (method, scheme, host, redacted path) are
  kept per source — the trace unit a build reads — and summed
  at read time, so they stay exact when a pooled connection's
  requests span segments and old segments were deleted; the
  client's and the server's view of the same requests are
  kept apart and the larger counts, never both (exact while
  one view's segments were all imported at some point, a lower
  bound otherwise). Re-import is
  idempotent and a build over fewer segments loses nothing.
  The requests give the TLS and path hints; the hostname is
  the client's HTTP host when that is a name, else its DNS
  lookup of the endpoint's address (a best guess: one address
  may serve many names). Rules match
  hostname or IP, case-insensitively; port-specific beats
  any-port, then the longer glob, then the older rule; a
  duplicate is refused, never overwritten. The file is 0600 in
  a 0700 directory, refused when open to others; reading
  commands never create it.
  `iormeshctl services` lists (unnamed first), shows the edges
  (`edges -from orders`: what orders talks to), and adds,
  lists and removes rules (`name`, `rules`, `unname`). The
  build calls `Observe` and `Import` (task ib4: one source per
  collected segment) and reads the projects it built back
  through `Filter.Projects`, so a cross-project connection is
  one edge, counted once.
- **Cleanup (`rm`)**: removes the host from the registry and
  tears down the deployment on it — `sudo iormesh uninstall`
  stops every session and removes `/opt/iormesh` (binary and
  traces). Trace files are synced off first (rsync, same as
  collect), so `rm` never destroys uncollected traces unless
  the operator says so with `-force`; an unreachable host
  warns and only drops from the registry.
  Built (task eb4, `internal/ctl/rm.go`): per removed project
  a live session is stopped first (`sudo iormesh stop`, which
  publishes its last segment and now fails when `state.json`
  does not say `ended` — a run SIGKILLed after the stop
  timeout left its minute unpublished), then collected.
  `uninstall` runs only when the host leaves the registry
  (`rm HOST`, or its last project), and first every other
  project directory under `/opt/iormesh/traces` (started by
  hand, or dropped by an earlier rm of an unreachable host)
  is stopped and collected too; then any segment file still
  in a `ready/` or active refuses the uninstall, naming it.
  The registry changes only after all of that succeeded — a
  failed or unclean stop, a failed sync, a remote check that
  fails other than with "no" (exit status 1; ssh's 255 is a
  failure), leftovers, or a failed uninstall leave it as it
  was. `-force` turns an unclean stop and leftovers into
  warnings and uninstalls, losing them. A host without the
  installed tracer is just unregistered.
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
- Acceptance: decision #25's tracer-CPU item passes on the
  segment-writer path, plain and `-spool` (task ee4).
  Status (2026-11-20, task ee4): plain unfolded passes (0.85 µs
  at 1 thread, 1.62 µs at 4); `-spool` with every event joined
  missed 2 µs (2.89 / 4.82 µs). Owner decision (2026-11-20,
  delegated to an advisor): the original ≤ 2 µs covered `-spool`
  with every event joined and was not met (2.89 / 4.82 µs after
  ee4). The limit is hereby changed for that case only: ≤ 4 µs
  per emitted event and ≤ 12.5 k instructions per event, valid
  at load ≤ 10 with every event attributed and zero loss. This is
  a relaxation, argued by the extra agent records each joined
  event consumes (one core still sustains 250 k joined events/s)
  and by the default fold keeping JVM hot loops out of the join.
  Plain stays ≤ 2 µs. After one more round of join cuts, by those
  rules (load 4.4–8.7, every `-spool` run attributed with zero
  loss, medians of 5): `-spool` 1.90 µs / 11.1 k instructions at
  1 thread and 3.75 µs / 11.8 k at 4 — passes; plain 1.59 /
  1.95 µs — passes, with the plain 4-thread runs' 30–50 % ring
  loss recorded, not gated; both 4-thread CPU-time passes are
  narrow, inside the ~±20 % run-to-run noise (decision #25
  item 1; s3-overhead.md, "Follow-up (ee4): owner decision").
  The item is met. Two test
  hosts run M4's scenarios; from a
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
  Status (2026-11-20, task lb4): **awaiting the owner's
  two-host run** — no criterion above counts as accepted on
  real hosts yet. `mage m4Acceptance HOST1 HOST2`
  (`internal/acceptance`) runs the whole sequence against
  exactly the two named, bootstrapped hosts and prints PASS,
  FAIL, SKIP or NOT RUN per criterion; the runbook, its
  prerequisites and the local results are in
  [docs/testing.md](docs/testing.md#m4-acceptance-on-two-real-hosts-task-lb4).
  On real hosts the clock criterion reports the real clocks'
  bound; the injected skew stays the rig's
  (`TestClockSkewRecovered`, task ye4). The microservices run
  covers both layouts: all four services on HOST1 (M4-8), and
  the spread (M4-9, task 1f4). In the spread, gateway and
  inventory run on HOST1 and orders and billing on HOST2, each
  as its own project, so gateway→orders (TLS) and
  orders→inventory (plain) cross hosts. The rig spreads them
  over three emulated hosts (`TestMicroservicesAcrossHosts`).

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
  *Built (task mb4):* conversations in the view model —
  hops paired by `req_seq`, calls nested by their thread's
  span only where the thread held the request alone (the
  `http.held` flag the agent sets for synchronous libraries,
  never Netty's event loop or a virtual thread's carrier), an
  unpaired hop joined only when the clock bounds prove it
  (unique, server span inside the client's, no startless end
  on the connection), each thread's file work interleaved; every step `causal`,
  `time-ordered` (clocks prove it: one host's, or two through
  the bounds) or `unordered` (they cannot) — raw cross-host
  timestamps never order a step. `show conversations`, `show
  sequence [ID]`, `query -ref`, and per build `graph.svg` and
  `sequence/<ID>.svg`. *Built (task 0f4):* per-thread
  lifelines (service, host, pid@incarnation, tid, thread; a virtual
  thread's I/O on its carrier a lifeline of its own, never the
  carrier's), each thread's story across the conversations in
  its own host's clock order (`show thread SERVICE/THREAD`:
  causal only inside a span it held alone; a carrier's
  virtual-thread lifeline all unordered), and the processes
  per host — threads with carried virtual-thread I/O counted
  apart, files, sockets, restarts — projected from the rollup
  (`show processes`)
  ([docs/visualization.md](docs/visualization.md) "V2 as
  built").
- **Query / slice interface**: `iormeshctl query` cuts the
  merged trace by time window, host, app, thread, connection,
  file or method and emits schema-conformant NDJSON, with a
  size cap and an explicit truncation record. An event stream
  at syscall frequency never fits a context window — the slice
  is what an LLM (or a human) actually reads. *Built (task
  pb4):* the input is the collected store, read through build's
  checked path (memory-capped child per segment and per methods
  table, host pins) and merged again, so a slice speaks merged
  conn ids; query writes nothing. Filters AND; `-follow` widens
  a connection to its threads' other events while they used it;
  `-file` follows the fds a matching open returned; the default
  cap is 1 MiB, the record
  `{"event":"truncated","args":{"emitted":N,"remaining":M}}`;
  lines are escaped for terminals; bad segments are named and
  exit 1. Predicate pushdown waits for a cheaper check
  (docs/implementation.md M5).
- Plus the per-run summary/report export — the LLM rollup of the
  whole mesh (see "Trace output contract"). *Built (task qb4):*
  `build` feeds `internal/report` the events the merge reads (no
  extra pass) plus the sessions' methods tables (checked in the
  child like segments; a bad one is left out, named, exit 1) and
  writes `report.json` beside `model.json`; `iormeshctl show
  report` prints the summary, then one block per process — files
  (opens, bytes, meta ops, causing methods), connections (merged
  id, role, service label, traced peer), threads with their
  requests in order, top methods by I/O — every list capped and
  counted, the collector's state capped at 2²⁰ entries
  (docs/schema.md "Summary rollup").
- **LLM introspection eval**: a fixed question set over M0
  traces plus the workload's source, with answers known from
  the scenario declarations (which method wrote this file?
  what is the call chain behind this request? which service
  did this thread call next?). The model works only through the
  summary rollup and the query interface. The eval is rerun on
  every schema change; its score is the evidence behind
  principle #6. *Built (task qb4):* `eval/questions/
  microservices.yaml` holds ten questions over the
  microservices scenario, every answer string held by a test to
  its `expect.json`; the threshold is **pinned at 0.8 (8 of
  10)**. `internal/eval` (`cmd/iormesheval`, `mage eval`) builds
  the store into a scratch database and builds directory, hands
  the model the `show report` text and the workload's source
  listing, and runs its replies — one `QUERY <slice flags>`,
  `SOURCE <path>` or `ANSWER:` per turn, at most 8 turns, slices
  capped at 32 KiB, only the slice flags accepted, source read
  only below `workload/src` — then scores each answer with the
  declared matchers (contains, in-order sequence, excludes,
  regex, excluded regex — an answer listing every candidate
  fails every question, by test) into `eval/results/<date>-<scenario>.json`. The model is
  a command (prompt on stdin, reply on stdout) the operator
  must name: there is no default, since the prompt carries trace
  data, so the eval never runs on its own and never in `mage
  test`; a local model keeps the data on the machine. `mage
  evalTrace` (root) records the store from one judged
  `TestMicroservices` run. The first scored run with a real
  model is still to be recorded.
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
  peer that matches the **expansion allowlist** (address globs
  and CIDRs in the controller config), deploys the tracer
  there, and binds it to the process owning the listening
  socket on the observed port (target spec: listening port,
  M2). Peers outside the allowlist are listed, never touched.
- **Rounds**: expansion is iterative — deploy, let traffic
  flow, refresh, expand again. The hop limit counts rounds from
  the originally enrolled hosts; a max-node cap bounds the
  total; a visited set makes cycles (A→B→A) terminate instead
  of oscillating.
- **Never guess a peer** (settled by task sb4): only a client
  end the merge left `no_match` proposes its peer; an
  ambiguous, keyless, roleless or conflicting end does not (its
  far end may be traced). A peer is the address the client
  reached, never a DNS name; an address the registry or a
  traced host already owns is visited, one a fused connection
  proves NAT'ed to a traced server is left alone, and IPv6
  peers wait for an ssh_config alias. Only fused-proven NAT is
  skipped: any other front (VIP, load balancer, SYN proxy) is
  proposed by its own address, and excluding such ranges is
  the operator's job through the allowlist. The allowlist and
  `max_nodes` live in the registry's `expand:` section (absent:
  nothing is allowed); one hop and 16 nodes are the defaults
  (owner may revisit). Every skipped peer is listed with its
  reason and evidence (docs/implementation.md M6).
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
  `~/.config/iormesh/servers.yaml`. The controller never
  trusts a host key on first use: ssh runs with
  `StrictHostKeyChecking=yes` and `BatchMode=yes`, so a server's
  key must already be in the operator's known_hosts, and no
  password prompt can hang a run. The k8s fetch path
  (kubectl exec/cp) settles with #18.
- **#16 Trace-file segmentation** — rotate once per minute,
  close-then-rename into `ready/`, collect-while-running via
  rsync with delete-after-sync (see M4). Compression is
  parquet-internal zstd — whole-file compression
  (`.parquet.zst`) would make segments unqueryable without a
  decompress step. Variable event arguments map to one
  nullable column per argument field (see "Trace output
  contract"). A size cap forces an early rotation for very hot
  sessions (settled by S3): a segment rotates at one minute or
  64 MB (≈ 3.5 minutes at the #25 reference rate of 18 MB per
  minute), whichever comes first, so a segment never outgrows
  what one `collect` moves comfortably.
- **#26 Cross-host join key** — the TCP initial-sequence-number
  pair (decided by S2, 2026-11-18): the client's `(iss, irs)`
  equals the server's `(irs, iss)`, which NAT leaves untouched.
  Each tracer records `snd_una`, `rcv_nxt`, `bytes_acked` and
  `bytes_received` with the socket's role on its TCP state
  records, at `ESTABLISHED` or later; the merge rebuilds the pair
  (connecting side `iss = snd_una − bytes_acked`, accepting side
  one less, both `irs = rcv_nxt − bytes_received − 1`, mod 2³²)
  and joins on it, inside a coarse time window that guards
  against ISN-clock wrap. The 4-tuple plus a time window breaks
  under NAT and port reuse and is not a fallback across hosts.
  Conntrack on the NAT host is optional — it needs a tracer or
  netlink access there and adds nothing the two ends lack. When
  no pair matches (sequence-randomizing firewalls, SYN proxies,
  an untraced far end), the edge stays unresolved, never a
  guess; L4/L7 proxies that terminate the connection stay the
  route map's job (#15). Details:
  [docs/spikes/s2-cross-host.md](docs/spikes/s2-cross-host.md).
- **#5 Test workload scope** — same repo, `workload/`
  (adopted with M0, 2026-11-18). A plain Java 21 app built with
  the JDK alone (`javac` + `jar` via `mage workloadBuild`, no
  Maven or Gradle), so the thing being traced never has an
  excuse to behave differently from a real app. The OTEL
  flavor (M3, task za4) is the one dependency, kept outside the
  core jar: its own module and jar, run against the vendored
  OpenTelemetry API jars (`workload/otel/third_party`). The
  third-party HTTP libraries of decision #22 (task de4) follow
  the same pattern: `workload/libs`, `workload-libs.jar`,
  vendored under `workload/libs/third_party/<library>`.
  Decided (review of de4): the repo carries these jars — about
  13 MB, each SHA-256-pinned in the magefiles and documented in a
  PROVENANCE file — so the workload builds offline and
  reproducibly; the size is accepted as the price of that.
- **#25 Tracer overhead and volume budget** — settled by S3
  (2026-11-18; numbers and method in
  [docs/spikes/s3-overhead.md](docs/spikes/s3-overhead.md)). The
  four numbers, as M1 measured them on `syscall-burst`, and the
  budget M2 onward is accepted against:
  1. *tracer CPU* — M1: 6.6–13 µs per event (medians). Budget:
     ≤ 2 µs per emitted event on `syscall-burst` (median) — one
     core then sustains 500 k events/s, five times M1, which
     together with aggregation covers the burst (task
     wc4 brought it to ~1.5–2 µs on a busy host; task md4
     measured 1.2 µs at 1 thread and 1.7 µs at 4, unfolded,
     medians of 5 — s3-overhead.md, "Follow-up (wc4)" and
     "Follow-up (md4)"; at md4's load, 4.6–9, the item passes).
     Task ae4 (after 8e4's dispatch, load 9.8–11.8): 3.0 µs at
     1 thread and 3.1 µs at 4, over budget; interleaved against
     the build before 8e4 at load 8–14, 2.18 / 3.55 µs before
     and 2.08 / 2.15 µs after — no 8e4 regression, but both
     builds over 2 µs at load ≥ 10 (s3-overhead.md, "Follow-up
     (ae4)"); task ee4 re-measures it on M4's segment writer
     (it gates M4's acceptance), the limit unchanged. With the
     Java join (`-spool`, every event joined): 5.2 µs, over budget (2b4; td4's Java mark
     measured +0.7 µs per marked event on top, within the host's
     noise, s3-overhead.md "Follow-up (td4)"). Task ud4 cut the
     consumer's per-event work (a per-event clock syscall, the
     mark's reflective decode, the join's map keys, the event's
     copies, integer and string encoding): instructions per event
     −34 % with `-spool` and −19 % without; at load 8–13 the
     `-spool` tracer still spends 3.8 µs at 1 thread and 5.6 µs at
     4 (from 7.6 / 9.2, task clock, medians of 5); the plain
     unfolded burst is under at 1 thread (1.7 µs) and over at 4
     (2.28 µs, 30–58 % of records lost), load 6.5–10 — `-spool` stays
     **over** budget, its next step being M4's output format
     (s3-overhead.md, "Follow-up (ud4)"); task ee4 re-measures
     on M4's segment writer, the limit unchanged. That writer
     (task 8b4) costs ≈ 3× the NDJSON sink per event (≈ 10.4 k
     vs ≈ 3.4 k instructions on the burst event), so the
     parquet path starts further over, not under;
     `DataPageStatistics(false)` (≈ −7 %) is the one known
     cheap lever. Task ee4 (2026-11-20) replaced the events
     file's `parquet-go` writer with a columnar encoder of its
     own (writer 11.2 k → 2.5 k instructions per event) and
     measured the segment path, interleaved against the build
     before it, medians of 5: plain unfolded **passes** —
     0.85 µs at 1 thread and 1.62 µs at 4 (from 2.68 / 3.14;
     5.8 k / 6.0 k instructions, 2.6 k / 3.1 k cycles per event,
     load 2.5–3.6); `-spool` with every event joined stays
     **over** — 2.89 µs at 1 thread (load 3–41) and 4.82 µs at 4
     (load 41–46), 11.5 k / 12.0 k instructions and 5.8 k /
     7.7 k cycles per event (from 4.43 / 5.82 µs). Owner
     decision (2026-11-20, delegated to an advisor): the
     original ≤ 2 µs covered `-spool` with every event joined and
     was not met (2.89 / 4.82 µs after ee4). The limit is hereby
     changed for that case only: ≤ 4 µs per emitted event and
     ≤ 12.5 k instructions per event, valid at load ≤ 10 with
     every event attributed and zero loss. This is a relaxation,
     argued by the extra agent records each joined event consumes
     (one core still sustains 250 k joined events/s) and by the
     default fold keeping JVM hot loops out of the join. Plain
     stays ≤ 2 µs.
     Re-measured by those rules after one more round of join cuts
     (task ee4: unboxed agent-ring drain, chunked bracket lists,
     no per-event java allocation), interleaved, medians of 5, load
     4.4–8.7, every `-spool` run attributed and lossless: `-spool`
     1.90 µs and 11.1 k instructions at 1 thread, 3.75 µs and
     11.8 k at 4 — **passes** the changed limit; plain 1.59 µs at
     1 thread and 1.95 µs at 4 (5.8 k instructions both, unchanged
     by the cuts) — **passes** 2 µs, though the unfolded plain
     4-thread runs lost 30–50 % of their records to a full ring
     (recorded, not gated). Both CPU-time passes are narrow: the
     medians sit inside the ~±20 % run-to-run noise (2 of 5 runs
     over the limit at 4 threads in each case), so the instruction
     counts — well under 12.5 k — are the robust part of the pass
     (s3-overhead.md, "Follow-up (ee4): owner decision"). Measured as the
     tracer's own user+sys CPU time per event from its ready
     file to its exit (load and attach excluded), which host
     load moves less than wall time — not nothing: on a loaded SMT host one thread's CPU
     time still spreads 2–3×, hence medians of ≥ 5 runs;
  2. *bystander cost* (host-wide, every untraced process) — M1:
     ~1.14× per traced-kind syscall at 4 and 16 threads. Budget: ≤ 1.25× on
     `syscall-burst` at 4 threads, paid only while a trace runs.
     Measured as the median of ≥ 10 interleaved traced/untraced
     pairs, pinned with taskset (tracer on its own core), on the
     bystander's user+sys CPU time; the wall-clock ratio is
     recorded alongside, and if its median exceeds 1.25× while
     CPU time passes, the item stays open and one run is taken
     on a host with its background load paused (2026-11-19: the
     quiet-host rule replaced, the limits unchanged). Task md4:
     1.19× and 1.15× (CPU time) in two sets of 10 pairs, wall
     clock 1.21× and 1.14×, so the guard did not trip. Task
     ae4, after 8e4's `raw_syscalls` dispatch: 1.16× and 1.14×
     (wall clock 1.16× and 1.19×) — passes; a bystander of a
     kind no family traces (a `getppid` loop) pays ~2.0×,
     recorded, not gated (s3-overhead.md, "Follow-up (ae4)");
  3. *loss* — M1: 16–42 % of ring records on a sustained burst,
     0 on a short burst and on the M0 scenarios. Budget: zero
     lost records on every M0/M2 scenario, and < 1 % on
     `syscall-burst` at 1 and 4 threads with aggregation on, both
     counted in lost records — skipped program runs are reported
     apart as possible loss, not in the budget (task vd4, "Trace
     output contract"). Any loss is an explicit `drop` event.
     Task vc4's aggregation lost 0 in 3 runs each at 1 and 4
     threads, on a busy host (s3-overhead.md, "Follow-up (vc4)");
  4. *volume* — M1: ~326 B/event NDJSON, zstd ×18. Budget:
     ≤ 30 B/event compressed (≈ 18 MB per minute at 10 k
     events/s, ≈ 4.3 GB per 4 h session), measured as zstd -3
     on the NDJSON until the M4 parquet writer exists.

  In-kernel aggregation (ior's model): repeated read/write on
  one fd instance by one thread are summed per flush into one
  event with a count; every event that changes fd, socket or
  process state stays individual, so goldens and attribution
  are unaffected. Implemented by task vc4
  ([docs/implementation.md](docs/implementation.md) M2 step 11,
  [docs/schema.md](docs/schema.md) "Folded reads and writes"):
  a call folds only once its key (thread, fd instance,
  direction) has sent four hot calls individually — hot meaning
  within 1 ms of the previous one and itself returning data
  within 1 ms — so declared single calls stay exact; the folded
  event carries `args.count` (≥ 2) and summed `bytes`; every
  other traced call of the thread flushes first; `iormesh run
  -aggregate=false` turns it off for debugging. Enforcement: the
  budget is checked by a documented `spikes/s3/measure.sh` run
  per milestone by the paired CPU-time method above, load
  average recorded, not gated (CI machines are too noisy for a
  perf gate); the zero-loss criterion is part of the integration
  suite.
- **#19 Runtime control** — the transient systemd unit (M4):
  graceful stop via SIGTERM, flag changes via stop-and-restart.
  No control file, no socket. Built by task 9b4 (`start`,
  `stop`, `status`, `uninstall`); `state.json` is a status
  record the run writes, read-only to the control commands —
  not a control file.
- **#11 Explorer form factor** — all three, on one view model:
  a generated static site (HTML/JS/SVG, no server, opens from
  `file://`), a Bubble Tea terminal UI, and plain text. A
  local web server was rejected: it adds a listening socket
  for nothing the static site cannot do at these sizes. Design
  in docs/visualization.md.

- **#2 Java agent attach strategy** — `-javaagent` at launch
  is primary; live attach is supported (decided by S1,
  2026-11-18). Live attach works on JDK 25 and 27-ea and prints
  JEP 451's warning on the target's stderr;
  `-XX:+EnableDynamicAgentLoading` silences it,
  `-XX:-EnableDynamicAgentLoading` (and a future default) makes
  the load fail, which the tracer reports instead of retrying.
  A root attacher reaches a JVM of another uid; the handshake is
  a client connection to the target's own attach socket, so the
  tracer still listens on nothing. Documented caveats: the
  warning, the retransformation of loaded classes, and that a
  live-attached agent misses all I/O before the attach. The k8s
  flavour is #14. Built and tested end to end by task ge4
  (`run -agent`, `iormesh attach`; 21, 25 and 27-ea warn and
  refuse under `-EnableDynamicAgentLoading` alike, 17 attaches
  silently).
- **#6 Java stack source** — the agent captures the stack at
  the I/O boundary with `StackWalker` (decided by S1,
  2026-11-18); blanket method entry/exit instrumentation is a
  non-goal; a plain BPF stack walk gave only unsymbolized Java
  frames, and a HotSpot-aware eBPF unwinder is not needed for
  v1. No async-profiler-style sampling between I/O events
  in v1. The walk is the agent's largest cost (≈8 µs per I/O
  call, S1), so M3 must reduce it and measure it against #25.
- **#27 Agent → tracer transport** — a memory-mapped ring per
  JVM (decided by S1, 2026-11-18): a file in the tracer-owned
  spool directory (reached through `/proc/<pid>/root` in k8s),
  mapped shared by agent and tracer; lock-free reservation,
  records published with a release store, zero syscalls per
  record. Back-pressure: when the reader is a ring behind, the
  agent drops the record and counts it in the ring header; the
  tracer turns the counters into the trace's loss accounting.
  The agent's own syscalls are its ring setup and one
  `/proc/thread-self` lookup per thread, tagged by path and
  never shown as the target's I/O, plus the sleeps of its
  watermark thread, which names itself in the ring. An
  append-only file was rejected: every flush is a target-thread
  syscall to filter, and it grows unbounded when the tracer
  lags. Built in M3 (task 1b4): one shared ring with a CAS on
  `head`, not a ring per thread — the thread count is unknown at
  setup, and the stack walk, not the CAS, bounds a bracket's
  cost; format versioned in the header, reader strict
  (`internal/agentspool`).

### Open — blocks M2

- **#7 Event families, default-on vs opt-in** — signals, epoll
  registrations, mmaps and renames all add volume; reuse ior's
  trace-family concept and pick defaults per measured cost
  (S3). *Default: file, socket and lifecycle on; the rest
  opt-in.* S3 supports the default: the consumer's per-event
  cost, not the probes, limits throughput, so every family
  that fires at syscall frequency stays opt-in.
  M2 step 7 (task sa4) implements the default as iormesh's own
  families (ior's concept, iormesh's grouping:
  `internal/tracepoints`): `file` and `socket` on — socket now
  includes bind and the datagram calls sendto/recvfrom, since
  unix sockets and UDP are sockets — and lifecycle always on
  (its probes decide what is traced); `ipc` (pipes,
  socketpairs), `signal` (kill/tgkill/tkill and
  signal_generate), `poll` (epoll, poll) and `zerocopy`
  (sendfile, splice, tee, copy_file_range) opt-in through
  `iormesh run -trace-families ipc,signal,…|all`. The flag only
  adds: file and socket cannot be turned off (the scenario
  markers are opens). Renames and mmaps are not families: the
  renames are the file surface's, mmaps are not traced yet.
- **#23 Target-pattern mechanics** — match on exec (argv/exe)
  vs comm; glob vs regex; concurrent matches (newest wins vs
  trace all vs error); restart semantics (wait for
  re-appearance vs end the session); and the listening-port
  target expansion needs (resolve once at start vs follow the
  port across restarts). Kernel comm's 15-char limit makes
  argv matching the primary source for Java apps. *Default:
  glob on argv, trace all matches, wait for re-appearance.*
  M2 steps 1–3 (task pa4) implement the default: the glob runs
  over argv and the exe path, never the comm, and the
  exec-to-attach window is written as an `attach_gap` drop. The
  listening-port target resolves once at start for now; whether
  it follows the port across restarts stays open for M6.
- **#9 Capture policy for argv and env** — argv is in, but it
  is not harmless: command lines carry passwords and tokens
  (`-Dpassword=…`, `--token …`). Argv is recorded through the
  same redaction policy as HTTP (denylist patterns, values
  masked). Capturing the environment at exec
  (`/proc/<pid>/environ`) is useful for the model but a
  privacy call to make explicitly. *Default: argv redacted,
  env off.* M2 step 8 (task ta4) implements the default:
  `internal/redact` masks option and KEY=VALUE values by a
  denylist, program-scoped flags (mysql's `-p`), URL userinfo
  and query values; every exec of a traced process becomes an
  `exec` event carrying its redacted argv, and the environment
  is never read. A process already running when the trace
  attaches has no exec, so its argv is not recorded (yet).

### Open — blocks M3

- **#29 JDK version support** — the plan says JDK 21, but
  fleets run 17 and older. Boundary hooks, stacks and HTTP
  events do not need 21 (only the deferred virtual-thread
  support would). S1 (from sources, no 21 runtime then) found
  the hook set's natives identical from 21 to 25 (27-ea changes
  two signatures outside the set), while 17 differs (no
  `UnixFileDispatcherImpl`/`UnixDispatcher`, RAF `readBytes`
  instead of `readBytes0`); the hooked *Java* methods differ
  more, which sc4 settled as three variants (below). M3 (task 0b4) builds the agent for 17 with
  shaded ASM and ships per-JDK table variants. A real JDK 21
  (task sc4) showed one hooked Java method S1's source reading
  missed: `Net.serverSocket` keeps its `boolean` stream flag
  through 24 and drops it in 25 — so three variants, `jdk17`
  (17–20), `jdk21` (21–24) and `jdk25` (25+). The agent runs and
  its hooks fire on 17, 21 (21.0.6, 21.0.12), 25 and 27-ea; 22
  and 24 were checked by `javap` of their class files only, 23
  and 18–20 are untested.
  *Default: agent runs on 17+; anything
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
  query values masked, HTTP/1.1 pairing only.* Task 3b4
  implements the default for the JDK's HttpServer, HttpClient
  and HttpURLConnection: headers on internal/redact's denylist
  and every query value are masked in the agent (and again by
  the tracer), paths are verbatim, and `req_seq` is recorded
  over HTTP/1 only. Task de4 adds the third-party libraries in
  #22's order, rewritten in the app's class loaders: Servlet,
  Apache HttpClient 5, OkHttp and Netty's HTTP/1.x codec.
- **#8 Hostname & TLS visibility** — DNS hostname↔IP via Java
  agent hooks (InetAddress) is the clean path; TLS peer
  identity (trust-manager/SSLSocket hooks) is extra value at
  extra surface. Non-Java nodes get no hostnames this way.
  *Default: DNS hooks in v1, TLS identity later.* Task wd4
  implements the DNS half: the agent hooks
  `InetAddress.getAllByName(String)` (stable 17–27) and records
  each new host-to-addresses mapping of a JVM as a `dns` event
  (literals and failed lookups not); TLS identity stays later.
- **#20 OTEL integration mode** — agent-read span context
  (primary, socket-free) vs ingesting the app's OTLP stream (a
  local receiver the app dual-exports to, or the collector's
  file export) vs both; and whether the views render a
  distributed-trace overlay on the mesh. Ingested spans are
  the app's own exported telemetry — deliberately distinct
  from the no-payload-capture rule, worth stating in
  docs/schema.md. *Default: span-context tagging only.*
  Built so (task 4b4): span-context tagging, the `otel`
  envelope object; ingestion stays deferred (below).

### Open — blocks M4

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
  itself. Built so (task bb4, the upgrade half; bootstrap stays
  open): `iormesh upgrade FILE` runs through the rule and takes
  only the staged file and `FILE.minisig`. It reads both once
  into memory, verifies them against
  `/opt/iormesh/etc/minisign.pub`, requires the signed name to
  be this arch's tracer (`iormesh-linux-<arch>`) and the signed
  version not older than the installed binary's (checked under
  an exclusive lock on `bin/`; through the rule the installed
  binary must still be the running process's own file, and the
  same version with other bytes is refused, so every release
  bumps the version), then installs exactly the verified
  bytes: a new root-owned 0755 file in `bin/`, renamed over
  `/opt/iormesh/bin/iormesh`. It first
  checks the layout — `/opt`, `/opt/iormesh`, `bin/`, `etc/`
  root-owned directories nobody else may write, the installed
  binary and the key regular root-owned files, never a
  symlink — and refuses rather than repairs. A downgrade needs
  `-allow-downgrade`, which root may pass but the rule may not:
  an older signed build may carry a fixed hole. The first
  install — binary, key and rule — is still the operator's own
  sudo or configuration management.
- **#28 Signature scheme** — which tool signs the tracer build
  (minisign, cosign, `ssh-keygen -Y`), where the public key is
  pinned (controller config; a root-owned copy on servers for
  upgrades), and who holds the private key (the release
  pipeline). *Default: minisign, key pinned in the controller
  config.* Built with the default (task bb4; the owner has not
  decided otherwise): minisign signatures made and checked with
  `aead.dev/minisign` (pinned in go.mod/go.sum; the files are
  plain minisign, so the `minisign` tool checks them too). The
  signed trusted comment carries `file:` and `version:`, so a
  renamed or older build cannot pass as the one wanted.
  `mage sign` signs `dist/` with the release pipeline's
  encrypted secret key from `$IORMESH_MINISIGN_KEY`, which must
  lie outside the repository; no private key is ever committed,
  tests generate throwaway keys. The controller pins the public
  key at `~/.config/iormesh/minisign.pub` (`iormeshctl verify`,
  which deploy will run); servers hold the root-owned copy at
  `/opt/iormesh/etc/minisign.pub`. Key rotation is replacing
  both files — the server's with the operator's own sudo, as
  in bootstrap.
- **#24 Service-interaction database** — controller-side:
  sqlite via a pure-Go driver (keeps iormeshctl cgo-free;
  additive merges and queries come natural) vs a plain YAML
  file (trivially diffable and inspectable, but incremental
  updates and bigger meshes get awkward); one database per
  project vs one with project scoping; glob vs regex for the
  naming rules. Lives under `~/.local/share/iormesh/`, next to
  the collected traces. *Default: sqlite, one database, glob
  rules.* Task hb4 built the default (`modernc.org/sqlite`,
  `~/.local/share/iormesh/services.db`, every row carrying its
  projects, `-project` filtering the listings, rules global);
  the owner has not decided otherwise.
- **#21 Project semantics** — for now a project is a manual
  label (`-project`, kept with the registry entry); later
  candidates: defaulting to the k8s namespace, or
  auto-derivation from the traced app. Cross-project
  collection: explicit project list vs merge-everything-found.
  *Default: manual label, explicit list.* The registry (task
  cb4) implements the default: each server entry lists its
  projects by label, each with its own target and flags.
- **#17 Disk-watchdog semantics** — percent of partition vs an
  absolute free-bytes floor (or both); check interval; and
  what the controller does when a tracer exits on the limit
  (surface it, redeploy with a lower duration, or nothing).
  *Default: both limits, surface only.* The tracer side took the
  default (task 8b4): 90% and a 256 MiB floor, checked every
  5 s and before every rotation; the controller half waits
  for `collect`/`build`.

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
  Edges (M2 step 7): a pipe or socketpair is one connection
  node, fused at creation, joined by whoever inherits an end; a
  unix stream connection fuses through the kernel's own pairing
  of its two sockets; UDP has no connection, so its unit is the
  flow — one local/remote address pair — whose two loopback
  sides fuse like TCP's ends, while a flow to another host stays
  unresolved. A signal one traced process sends another is a
  `signaled` edge; a kernel-raised one (timer, SIGCHLD) has no
  sender.
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
  on java events inside an OTEL span; appended after stack_id
  by task 4b4), http (method, scheme,
  host, path, status, req_seq — on http events; headers per
  redaction policy)`. S1 settled the Java fields: nested
  `java.thread` (thread name) and `java.stack` (method ids,
  innermost first), as `internal/schema` has them — the flat
  names above are shorthand; `http` was provisional until M3's
  acceptance froze it. Task 3b4 appended `version`, `req_headers` and
  `resp_headers` ("name: value", redacted) to it and made `http`
  an event kind of its own (fd -1, conn_id when found).
  Cross-host, `conn_id` comes from the merge's ISN-pair join
  (S2, decision #26).
- **Method table**: `method_id, class, method, signature,
  source_file, line, jar, classloader`. Frames reference ids, so
  stacks stay compact and every frame joins to its JAR. As built
  (tasks 2b4, fe4): the method row carries `class_id`, and JAR
  and class loader live once per class in the class table
  (`classes.ndjson`); `signature` is not recorded (StackWalker
  frames do not need it to be unique: the line tells call sites
  apart).
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
- **Skipped program runs are possible loss, not lost records**
  (task vd4, 2026-11-19): the kernel skips a BPF program while
  another one runs on that CPU (its recursion guard), and the
  skip happens before the program's target filter, so a
  program's `recursion_misses` counts untraced processes' calls
  too — host-wide BPF activity, not the trace's loss. It bounds
  the target records lost from above and cannot be attributed:
  nothing runs to look at the pid. So the contract reports it on
  its own and never adds it to the lost records (ring-buffer
  reservation failures and undecodable records, which are the
  trace's own): a host-level `drop` with `drop_reason`
  `skipped_runs` whose `args.lost` is the runs skipped since the
  previous one, and the summary's "skipped runs" figure beside
  "lost records". Loss stays never silent — a skip that did hit
  a target is inside that count — and never inflated. The
  integration gate stays conservative: a run with skipped runs
  cannot be judged loss-free and is retried like kernel loss
  (skipped as unexercised when every attempt had some); the
  root loader test requires zero lost records and only logs
  skipped runs.
- **Syscall handlers run behind `raw_syscalls` dispatchers**
  (task 8e4, 2026-11-19). Why runs were skipped: Linux 7.2 runs
  a per-syscall tracepoint program (`syscalls:sys_enter_<name>`)
  with preemption enabled behind a per-program, per-CPU
  recursion guard, so a task preempted inside it makes the
  kernel skip that syscall for every other task on the CPU until
  it resumes — the target's calls too, in time-slice bursts; on
  a loaded host every integration run had some, whoever else
  traced. A traced read loop sharing its CPU with an untraced
  one: ~900 k skipped runs, ~4 % of the target's reads lost.
  Raw tracepoints and `tp_btf` take the same preemptible guard;
  the classic `raw_syscalls:sys_enter`/`sys_exit` events run
  their program with preemption disabled (same test: 0 skipped,
  every read seen). So two dispatchers there tail-call the
  generated per-syscall handlers by syscall number
  (`internal/c/dispatch.c`, program arrays filled by userspace;
  per-arch number tables generated from `golang.org/x/sys`);
  handlers, records and the attach-or-skip rule per syscall pair
  are unchanged. What can still skip the dispatchers is the
  host-wide `bpf_prog_active` guard they share with kprobes and
  classic tracepoints, held by a task preempted inside a `bpf()`
  map syscall on that CPU (a tracer makes ~5 a second) — rare
  in practice (three full integration runs, one beside two
  concurrent `bpfRootTest`s: 0 skipped in all 96 tracer runs),
  real under a deliberate map-lookup loop on the target's CPU
  (114 k skipped, 2.5 % of its reads lost), and still counted
  as skipped runs. The gate's judgement stays
  as vd4 set it: a skip cannot be attributed to a pid, and the
  experiment shows it can be the target's. Cost: every syscall
  on the host now meets a dispatcher, traced kind or not. Kernel
  instructions per untraced syscall beside an idle trace
  (`perf stat`, 3 rounds, identical across rounds): dd's
  read/write 1626 → 1708 (untraced 728): ~5 % more kernel
  instructions per call of a traced kind, which puts the gated
  `syscall-burst` bystander ratio at an estimated ~1.21× (md4:
  1.19×), still under 1.25×; a syscall of a kind no family
  traces (getppid) 595 → 1142 (untraced 443) — the price of the
  `raw_syscalls` perf path. Task ae4 measured it by #25's
  paired method (s3-overhead.md, "Follow-up (ae4)"): the gated
  `syscall-burst` bystander 1.16× and 1.14× at 4 threads
  (passes); a pure `getppid` loop ~2.0× (+0.2–0.3 µs per call),
  recorded, not gated. Of that, the dispatcher programs are
  ~5 % of the loop's cycles and the kernel's perf-tracepoint
  path the rest, so no dispatcher fast path is worth adding.
  Attaching per syscall again on kernels before 7.2 (whose
  per-syscall programs run with preemption disabled), which
  would spare untraced kinds there, is not taken: the budget
  holds without it, a second attach mode would need the
  integration suite on each kernel line, and no host here runs
  one. Revisit if a real workload's bystander cost exceeds
  #25.
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
  stacks are an opt-in addition (M2, done: `-native-stacks`);
  Java stacks come from the agent (M3).
- **Per-syscall cost**: ior documents what classic syscall
  tracepoints cost every task on the host, and prototypes
  cheaper dispatch (`docs/raw-tracepoint-design.md`). iormesh
  inherits the cost with the pattern — S3 measures it, and
  ior's findings there are the first place to look for relief.
  Task 8e4 left the pattern for correctness, not cost: the
  handlers run behind classic `raw_syscalls` dispatchers, which
  makes traced kinds slightly dearer and untraced kinds about
  twice as dear in kernel instructions ("Syscall handlers run
  behind `raw_syscalls` dispatchers").

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
  (a DNAT hop between namespaces). Built (task ye4): the knob
  is the environment variable `IORMESH_TEST_CLOCK_SKEW` on
  `run` — not a flag, so no usage text shows it and `start`
  cannot forward it; refused through the sudoers rule and by
  `start` — and shifts `wall_ns` only, the clock the fusion
  window and the bounds use; `boot_ns`, shared by the emulated
  hosts, is the truth the tests order against. Skews of
  −0.25 s and +1.25 s fuse and are recovered inside two-sided
  bounds ≈250–800 µs wide, with no cross-host order
  contradicting the truth; +3 min fuses nothing (`no_match`,
  no bound) — docs/testing.md.
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
- **Settled by the build (task ab4)**: amd64 builds on ior's
  Rocky 9 builder; arm64 on a Debian builder, because Rocky 9
  has no aarch64 cross compiler or static libelf — both
  tracers link fully static, so the builder distro never
  reaches a server. The containers compile the *committed*
  generated sources and do not run `mage generate`: the diff
  gate runs on the generation host, which also keeps the
  builds unprivileged (no tracefs). Base images are pinned by
  digest. The arm64 BPF object compiles against the build
  host's `vmlinux.h` unless an arm64 host's is supplied; CO-RE
  relocates by field name, but an unguarded x86-only field
  would fail only at load on arm64, and the arm64 object has
  never been loaded on a real arm64 kernel. Details:
  [docs/build.md](docs/build.md), "Container builds".

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
cmd/iormesheval/      # the LLM introspection eval's runner (M5)
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
internal/report/      # per-run summary rollup (M5, cgo-free)
internal/eval/        # LLM eval harness: question sets, model
                      #   command protocol, scoring (M5)
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
- Kernel-mode dedup or aggregation — correlation happens in
  userspace. The one exception is decision #25's fold of hot
  read/write loops, which keeps every call (as a count) and its
  thread's order.
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
