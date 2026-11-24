# Milestones

The milestones iormesh was built in, in execution order: what each
one set out to land, how it was built, and what accepted it. When
this record was written (2026-11-24), M0, M1, M2, M3, M4, M6 and M7
were accepted; M5 was built but not accepted until its LLM eval has
run with a real model. Rocky Linux 9.9 (el9, kernel 5.14) is the main target.

Decision numbers (`#N`) refer to [decisions.md](decisions.md);
what is traced and the trace format are in
[trace-scope.md](trace-scope.md) and
[output-contract.md](output-contract.md). This file and
decisions.md replaced the project plan in task tk4 (2026-11-24);
the text below is the plan's milestone section as it stood, with
only its headings and cross-references adapted.

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

Sequencing relaxation (2026-11-20, owner-delegated advisor
decision): M4's real-host run (task lb4) gates acceptance, not the
start of M5–M7. Their work proceeds on the simulated rig and kind;
a status may read "passes on the rig", never "accepted", until M4
is accepted and the real-host re-check (task 3f4) has run. A
real-host finding takes priority over any M5–M7 work. Decisions
settled meanwhile are provisional.

How to build each of these — files, order of work, code
sketches, tests — is in the
[implementation guide](implementation.md). The milestones
and [decisions](decisions.md) decide; the guide shows how.

## Spikes — feasibility before commitment

Three mechanisms carry the project and none of them exists in ior.
Each gets a time-boxed spike that ends in a written result (a
short doc under `docs/spikes/`) — either "works, here are the
numbers" or "needs this other design". A spike's prototype code is
throwaway; its findings update these docs.

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
  [docs/spikes/s1-java-join.md](spikes/s1-java-join.md)):
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
  `java.thread` / `java.stack` (see output-contract.md).
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
  [docs/spikes/s2-cross-host.md](spikes/s2-cross-host.md)):
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
  [docs/spikes/s3-overhead.md](spikes/s3-overhead.md);
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

## M0 — Traceable test workload (Java): framework first

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
     shifts them (see output-contract.md);
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
  fused at their addresses. As pods (task uh4, M7
  "Microservices as pods"), `expect-pods.json` puts the gateway
  on a host of its own too, and all four hops fuse.
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
  Built by task yb4 as `ingress` (`Ingress.java`): `--role
  backend --bind A [--port N]` serves the gateway's stock query
  (`GET /inventory/stock/widget`) once; `--role gateway --fqdn
  NAME` asks for it by name — the one scenario that resolves a
  name in its bracket, since the route map matches the request's
  host. `workload/scenarios/ingress` holds the merged golden
  (both ends unresolved, TEST-NET addresses the kind test
  replaces) and the declared `routes.yaml`. The golden does not
  check the backend's `close`: the JDK HttpServer closes the
  exchange on whichever thread finishes it, so declaring the
  thread failed a from-scratch run.
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

## M1 — Walking skeleton: one fused edge, end to end

The thinnest slice that exercises every layer once: BPF →
correlator → schema → normalizer → golden → integration harness.
It proves the central thesis — two processes fused into one edge
by their connection — before the traced surface widens.

- **Probes**: open/read/write/close and TCP connect/accept only,
  built on the ior-pattern generation pipeline (see
  [build.md](build.md#code-generation--follow-ior), "Code generation —
  follow ior").
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

## M2 — Trace a PID: the full surface

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
  A listening-port session follows its port across restarts of
  the same program (decision #23, task mh4).
- Probes reuse ior's fd/path resolution helpers where the
  problems are the same; peer addresses, process-tree following
  and exec matching are new work (see "What ior does not give
  us").
- Full mental-model scope (see [trace-scope.md](trace-scope.md)): process &
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
  [s3-overhead.md](spikes/s3-overhead.md) ("Follow-up
  (ua4)").
- Argv is recorded redacted (decision #9), on `exec` events.
- Output: trace schema v1 — NDJSON event stream + graph export.
- Acceptance: M2's scenarios pass the golden check, including
  edge cases (fd reuse and aliasing, exec, PID reuse, pattern
  restarts (new PID, same app), threaded I/O, loopback pairs,
  socket races) and the non-Java peer, proven by the
  integration suite (testing.md, "Integration tests follow ior"); the
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

## M3 — Java agent: code-flow attribution

Gated by S1 — done; the design below is S1's result
([docs/spikes/s1-java-join.md](spikes/s1-java-join.md)).

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
  whole table (a record after it shows it complete; task tf4); a
  syscall without a mark, or with another native's,
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
  Late attach (task lh4): `run -agent` also loads the agent
  into a JVM that appears during the session — a pattern match
  exec'd later, a traced process (a child included) that execs
  a JVM, a restarted port owner the session adopts (#23). Each
  is tried from its exec (or adoption) every 250 ms until the
  load succeeds, for at most 30 s; a process that maps no
  `libjvm.so` within 5 s of its exec is no JVM and dropped
  silently, a JVM still starting (no SIGQUIT handler yet) is
  tried again. A JVM that refuses (`-XX:+DisableAttachMechanism`,
  dynamic loading disabled, a failed stage or handshake), runs
  out of the 30 s or exits first is named once on stderr and
  never tried again. Every check of the start-time attach
  applies unchanged — the same loader, the same JVM, uid and
  namespace checks, the same trusted jar path (pinned in a
  confined session) and stages inside a foreign root, removed
  at the session's end. The attaches run off the trace loop: a
  goroutine per JVM, at most 2 handshakes at a time and 64 JVMs
  waiting (an exec beyond them is skipped, the first of each
  overflow named, the next overflow named again once a slot
  freed); the loop only collects
  their lines and stages at its tick. A JVM forked without an
  exec is no new JVM and gets nothing. Each JVM the start-time
  attach loads is its own until that JVM's own load ends (tasks
  7l4, 9l4): an exec of it meanwhile is not tried beside it —
  two attaches could each send a SIGQUIT, and the second prints
  a thread dump on the app's stdout — but handed to the late
  attach afterwards, its windows from that exec, only if its
  start-time load failed before any signal (no `libjvm.so` or
  no handler yet). So a JVM gets at most one SIGQUIT from the
  session; an exec after its own load ended is a new image,
  tried late like any other, even while other pids' start-time
  loads still run. One that execs during its own successful
  (or refused) load gets no agent in its new image. An exec is
  placed by its time, not by when its record arrives (task
  dl4): a record that arrives after the load ended, of an exec
  made before, is left to that load too — a load that timed out
  on the socket after its SIGQUIT takes 10 s — unless the pid
  is another process by then.
  A late JVM's start-up (task mk4): *Decided (2026-11-24):* the
  gap is accepted and documented, not closed by iormesh. A late
  attach loads the agent up to 30 s after the exec, so the
  JVM's start-up I/O (class loading, config reads, first
  connections) stays unattributed; only `-javaagent` at launch
  covers it, and that is a change to the application's start
  configuration, which the operator makes — iormesh never
  writes a unit, drop-in, environment or pod spec of the traced
  application, and deploy applies nothing to it.
  docs/java-attach.md shows the line: a systemd drop-in for the
  app's unit setting `JAVA_TOOL_OPTIONS` to `-javaagent:` the
  installed jar with `spool=/run/iormesh/spool/<project>`, with
  what it costs (the `Picked up` line, inheritance by child
  JVMs, a hardened unit's `ReadWritePaths`) and what happens
  while no session runs (the agent finds no spool, prints
  `iormesh-agent: disabled` and stays out; the next session's
  start-time attach loads it live). No helper prints the
  drop-in: it is fixed text that varies only by project, so a
  command would add surface without adding information, and it
  could not see the unit's hardening or SELinux domain. In a
  pod `-javaagent` needs the jar and a spool shared with the
  tracer inside the app container — a workload change with no
  recipe shipped or tested (#14 stays the owner's call); the
  docs state the limit there. Overturned if the owner wants a
  print-only `iormeshctl` helper for the drop-in, or a tested
  pod recipe.
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
  ever touching the wire; sensitive headers and query params
  are redacted by policy before anything is recorded, path
  segments are verbatim in the events (#22); the controller's
  per-path views fold id-like segments into templates (task
  xh4, see #22). Task 3b4 built the JDK's three — HttpServer (at
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
  no evidence of the response's time or order. The DNS hooks
  (#8) landed with it: a `dns` event per new host-to-addresses
  mapping of a JVM. Task de4
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
  answers the app's handlers write (`http-netty`, Netty 4.2).
  Decision #22's v1 set is complete; HTTP/2 stays time-ordered
  (no stream ids), and Apache HttpClient 4, OkHttp 3 and async
  Apache are not hooked. Netty 4.1 is hooked only from the
  release whose `HttpObjectEncoder` has a `write` of its own
  (4.1.110 has it, untested; 4.1.79 and older do not): a
  loader's Netty records only when both its decoder's `decode`
  and its encoder's `write` were rewritten, so an older 4.1
  records nothing and never costs the JVM its `req_seq` (task
  6j4).
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

## M4 — Controller (`iormeshctl`): push, collect, merge locally

The primary deployment and collection path for plain Linux.
Gated by S2 (the cross-host join). Mesh expansion (M6) and
Kubernetes (M7) build on it.

- `iormeshctl` runs on the controller — a laptop or MacBook,
  Linux or macOS — with no eBPF and no cgo at all
  (cross-compiled darwin/linux, amd64/arm64; the merge and
  render packages stay cgo-free).
- **Command-line conventions** (both binaries, `iormesh` and
  `iormeshctl`; task vh4): exit 0 on success, 1 when the command
  failed, 2 on a usage error (an unknown subcommand or flag, a
  missing argument — printed with the full usage); the tracer's
  `normalize -check` adds 3 for golden violations. `help`, `-h`
  and `--help` print the full usage to stdout and exit 0;
  `SUBCMD -h` (or `--help`) is a request too: it prints only that
  subcommand's flags (stderr, Go's flag format) and exits 0 —
  never 2, never followed by "flag: help requested" or the full
  usage. The full usage names every subcommand, verb, view and
  flag; a test holds `iormeshctl`'s text to its registered
  subcommands and flag sets, so it cannot drift.
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
  needing the bootstrap of #1 (`iormeshctl bootstrap`), never
  installed. The Java agent
  jar ships on the same trust path (task kf4, #28): a host with
  a project that uses the agent (`-spool` or `-agent`) also gets
  the signed `dist/iormesh-agent.jar`, verified on the
  controller, staged with the tracer and installed by `sudo
  iormesh upgrade` as `/opt/iormesh/lib/iormesh-agent.jar`;
  every refusal starts nothing.
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
  `/opt/iormesh/traces`, `-spool` to
  `/run/iormesh/spool/<project>` and `-agent` to the installed,
  signature-checked `/opt/iormesh/lib/iormesh-agent.jar` (task
  kf4), and give `ready/` only to the
  sudo caller; no existing directory is ever chmod'ed. Through
  the rule only `run`, `start`, `stop`, `status`, `upgrade`
  (signature-checked, decision #1) and `uninstall` run — every
  other subcommand is refused, fail closed
  ([docs/security.md](security.md) has the rule's text
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
  repeats project, FQDN, session, seq and start. FQDN and
  session hold only `[A-Za-z0-9_.:-]`: the writer refuses
  others and the controller ignores a file named with them,
  since refs built from the name reach shell commands (task
  3i4). A segment
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
  operator wants, so only a confirmed transfer frees it —
  confirmed meaning fsynced on the controller (rsync
  `--fsync`; the pod path syncs file and directory before its
  remove, task ii4); if
  `collect` never runs, the disk watchdog and the 4h bound cap
  what piles up.
  Synced segments land on the controller under
  `~/.local/share/iormesh/traces/<project>/` — that path on
  every platform, always — and stay there until the operator
  deletes them; nothing is auto-cleaned. Deleting is an
  explicit operator command (task yh4, owner-approved
  2026-11-22): `iormeshctl prune -older-than DUR` lists the
  store's own segment files whose time range ended more than
  DUR ago and deletes them only with `-apply` — never a file it
  cannot parse as a segment, a dot file or a symlink (see
  **Build** below); no command prunes on its own. What prune
  deletes, the service database forgets in the same run (task
  gk4, see **Build window and prune**).
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
  the command exits 1 naming it. A hung host counts as
  failed: ssh gives up on a dead connection after about 45 s
  (`ServerAliveInterval=15`, `ServerAliveCountMax=3`), rsync
  on a transfer that moves no data for 60 s (`--timeout=60`),
  and each server host gets an hour in all (collect, deploy
  and rm alike) before the controller moves on — generous,
  since an interrupted collect loses nothing and a backlog may
  be large. Pod entries get the same hour (each entry in
  collect and rm, each pod in deploy); below it every kubectl
  call that answers and exits (get, debug, a short exec) has
  2 min, and the fetch stream an idle timeout of 2 min without
  a byte — silence, not length, so a long backlog still
  moves (task pi4). A Ctrl-C or SIGTERM cancels the command's
  calls, lets it clean up, and it reports "interrupted". The
  build reads every
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
  `ambiguous`). Two sessions of one host tracing one process
  give its socket two conn ids (each hashes its own BPF
  establish stamp); the merge folds such halves — same host,
  role, ISN pair and 4-tuple, inside the window — into one
  before the join, so they fuse rather than turn `ambiguous`
  (task ei4). The `tcp_state` schema carries the four
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
  **Build window and prune** (task yh4, owner-approved
  2026-11-22): `build -from T -to T` or `-last DUR` builds only
  the segments whose time range overlaps the window, T as
  query's (RFC 3339, or a UTC time of day on a store within one
  UTC day: **A time of day's date** below), and echoes the
  resolved window. A segment's range comes
  from its name — its start, a minute before, to its one-minute
  interval and a minute after — so a window takes whole
  segments and never decodes one outside it once its host
  directory is pinned; an unpinned directory is checked whole,
  so the host vetting, and what the build pins, are a full
  build's. The range bounds when a segment was written, not its
  events' wall clocks: a call blocked past the slack is written
  later than it was stamped, and a `-to` can miss it. The
  service database holds every import ever made, so a windowed
  build reads its services and edges back narrowed to the
  connections (and per-segment request rows) whose seen range
  overlaps the window (`servicedb.Filter` From/To, wall ns);
  the import itself is never narrowed. A windowed build is a
  different build: it writes
  `<projects>[@NS]~window/` (one per project set and namespace,
  overwritten by the next windowed build, read by `show`,
  `open` and `tui -windowed`, task hk4) and never replaces the
  full build; its model records the window (`run.window`), so
  its site and TUI say which window they show. `prune
  -older-than DUR [-apply]` is the store's only delete: it
  takes what `-last DUR` would never select, regular files
  directly in a valid `<project>/<host>/` whose names parse as
  segments, a table file only once its session keeps no newer
  segment; without `-apply` it lists, with it it deletes and
  prints each file. Auto-cleaning the store stays out of scope:
  prune is the operator's explicit command.
  **A time of day's date** (task 1l4): `-from`/`-to` take RFC
  3339, a date and time of day in UTC
  (`YYYY-MM-DDTHH:MM:SS[.frac]`), or a bare UTC time of day.
  One rule places a bare time of day for `build`, `query` and
  `query -window-segments` alike: on the UTC date the listed
  events segments of the projects start on (their names,
  readable or not), when they all start on one date; a store
  whose segments start on more than one UTC date refuses it as
  a usage error that names the dates and asks for the date.
  Why refuse, not anchor: the earliest date moves when `prune`
  deletes the oldest day, and the latest moves when a new day
  is collected, so any anchor lets the same command silently
  mean another day; refusing on a multi-day store means a bare
  time of day only ever resolves where one date is possible.
  The listed segments, not the readable ones, decide, because
  build must place the window before it decodes anything; an
  unreadable segment's name therefore counts too. An instant
  needs no date, so it never lists the store. `build` echoes the
  resolved window on its summary and `query` on stderr (stdout
  is the slice).
  **Prune forgets** (task gk4): `prune -apply` also makes the
  service database forget the files it deletes, so a full build
  afterwards shows no service, edge or request count that only
  deleted segments held. The database records which sources
  (segments) named each connection (`conn_source`, schema
  version 4, seeded from the request rows' sources);
  `servicedb.DB.Forget` drops a source's request rows and every
  connection no other source names, and narrows the rest's
  window to their remaining sources. Naming rules, verdicts,
  hostnames and TLS hints stay. Order: the database forgets
  first, in one transaction, then the files go. A failed
  database update deletes nothing and exits non-zero; a failed
  delete after it leaves a file the database forgot, which the
  next build that reads it imports again — so the database
  never counts a file that is gone without prune saying so. No
  database yet (no build) is nothing to forget, and prune does
  not create one. Rows of a database from before version 4
  without requests carry no source and are kept. A build
  running alongside prune can import a segment again after it
  was forgotten; prune is meant to run between builds.
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
  exactly the call's return, or by one more for a receive that
  also consumes the peer's FIN, task wi4 — so concurrent
  senders or readers on one socket, MSG_PEEK, kTLS and the like
  give none, never a guess; one ring record per call as
  before, the exit record carrying it; decision #25's burst
  figure unchanged, +3 % tracer instructions per TCP data
  event — s3-overhead.md,
  "Follow-up (xe4)"),
  the SYN adds one client→server edge (connect enter before
  the server's accept exit). Offsets repeat every 4 GiB, so a
  data message counts only when it is shown to pair bytes of
  one lap (task jf4, `internal/federate/clockproof.go`), by a
  stream check or a counter proof. The stream check covers a
  direction: the byte sums of all its data records (folded
  ones too) and the `tcp_sock` counters stay below 4 GiB, no
  recorded offset ends past the sender's sum, and no drop of
  the *sending* host may have hidden a send — a drop's records
  were lost at or before its own instant, so one before the
  socket opened there (connect or accept enter) hid none;
  unattributed drops, skipped runs on the connecting end (a
  skipped `task_newtask` leaves a child untraced holding a
  socket that may predate connect) and ends without an opening
  call always taint. Receiver-side drops only remove receives.
  The msghdr data calls count since task oh4: `sendmsg` and
  `recvmsg` carry their return as bytes and `tcp_seq` by the
  same exact-move rule; `sendmmsg` and `recvmmsg` return a
  message count, so their exit sums the messages' `msg_len`
  from the caller's mmsghdr array (a bounded loop capped at
  the kernel's own 1024-message clamp) and checks `tcp_seq`
  against that sum. el9's 5.14 verifier accepts the loop: on
  `iom1` (5.14.0-741.22.1.el9_9, task rj4, 2026-11-23) it
  processes 42,078 instructions for the exit handler
  (`TestMmsgVerifierHeadroom`) against the 1M limit, and the
  whole root BPF gate passes there — docs/testing.md, "el9
  runbook and runs". A sum the
  tracer could not complete (a faulting read, the cap, a lost
  park) is marked `args.bytes_partial`, carries no `tcp_seq`
  and fails its direction's stream check — never a short sum
  passed off as the whole.
  A `recvfrom`, `recvmsg` or `recvmmsg` with `MSG_PEEK`
  copies bytes but consumes none, so it must not count twice
  with the receive that later consumes them (task ok4): the
  tracer captures the call's flags argument at its enter (the
  three share `fd_size_event`; `recvmmsg` moved there from
  `fd_event`, giving up the unused file identity word), the
  correlator marks the event `args.peek` (always true when
  set; never with `tcp_seq`) and keeps its `bytes`, and the
  receiver's stream sums — the merge's and the report's —
  skip it. The report's per-thread and per-method bytes skip it
  too (task cl4): they are bytes moved, not bytes copied, so a
  peek-then-read counts once everywhere in the rollup.
  The stream check is not a proof: it trusts the sums to hold
  every byte, and bytes escape them with no drop recorded —
  untraced `splice`, io_uring; a fork
  between the untraced `socket()` and `connect()`; an exec
  keeping the socket (its fd binding is dropped); an fd passed
  by `SCM_RIGHTS` or `pidfd_getfd`; and it reads only the drops
  of sessions that reported the half. What limits the damage:
  an offset past the sender's sum proves untraced bytes and
  fails the check, and so does a sender's `bytes_acked` above
  its sum plus SYN and FIN (task sf4) — the kernel's own count
  of the stream; the receiver's sums are a second witness, and
  any counter record at 4 GiB or more fails it.
  The counter proof covers one message and ignores drops and
  untraced calls: a `tcp_state` record of the sender after the
  send's exit with `bytes_acked` below 512 MiB (the kernel
  keeps less than 3 GiB queued: `sk_sndbuf` is an int) and one
  of the receiver after the receive's exit with
  `bytes_received` below 4 GiB put both offsets below 2³²,
  where equal mod 2³² means equal. A stale socket binding after
  a lost CLOSE cannot feed it another connection's low
  counters: those records rebuild another key, and the
  conflicting half fuses with nothing. Every end gets a
  counter record after its last call (task sf4): a socket
  closed in the session has its CLOSE, and for every socket
  still registered when the session ends the tracer takes a
  final snapshot — `tcp_state` with `args.sock_snapshot:
  "final"` (an appended optional arg, schema v1), the socket's
  state, tuple and the four `tcp_sock` fields. So the counter
  proof covers every message of a direction whose sender
  acknowledged under 512 MiB and whose receiver received under
  4 GiB, whatever the trace missed, and the stream check
  decides only longer directions (or an end whose CLOSE run
  the kernel skipped, recorded as `skipped_runs`); there,
  when the session ended because its targets exited, a
  `bytes_acked` read after every call leaves unseen only
  untraced bytes no more than the data still unacknowledged
  at that reading. A session stopped while its app still ran
  (duration, signal, disk limit, error) gives its final
  snapshots no say in that sum check — the app may have sent
  after the last record the trace kept — only in the counter
  proof. The snapshot is taken in
  the tracer's `finish`, after the last records were handled
  and before the tracer closes: userspace walks
  `sock_owner_map` (each key once, the walk capped) and runs a
  raw tracepoint program per entry
  through `BPF_PROG_TEST_RUN` (Linux 5.10; el9 5.14 has it),
  which checks the owner's inode generation as a state change
  does, skips `TCP_CLOSE`, and reads the socket's four fields
  once, unlocked. Walking the owners, not the kernel's socket
  tables, reaches every network namespace — a `bpf_iter` over
  tcp and `INET_DIAG` each list one, which misses a pod's
  sockets, and `tcp_info` lacks `snd_una`/`rcv_nxt`, which the
  schema's all-four rule needs. The merge reads only its
  counters, and only when its four fields rebuild the fused
  connection's key: a final snapshot never keys a half, so a
  stale owner's record could not become a key conflict as a
  state change does, and the key check drops it — and a torn
  read with it. It dates neither SYN nor handshake. No final
  snapshot exists for a socket whose owner entry the LRU map
  evicted, nor for one still in `SYN_SENT` or `SYN_RECV` at
  the end (a TFO child accepted mid-handshake, say); those
  ends rely on their CLOSE or the stream check. A snapshot
  that cannot be taken is a warning, not a failed trace. The
  merge keeps one bound per
  host pair per 1-minute window (`Result.Clocks`), one-sided
  when only one direction has edges, empty and flagged when
  they contradict. The offset drifts within a window, so each
  bound is widened by an assumed drift rate (default 1 ppm,
  NTP-disciplined hosts: 60 µs per side per minute); a clock
  stepped or slewed harder breaks the assumption. The `build`
  summary and the view model print them per host pair
  (windows, widths, one-sided and empty ones; task ib4); M5's
  views show them too (the health view's clock bounds). The
  bound never feeds back into fusion — it is proven by the
  fused connections, so it cannot choose them; the fusion
  window stays a wrap guard, and hosts skewed beyond it get
  neither edge nor bound.
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
  idempotent and a build over fewer segments loses nothing;
  only `prune -apply` makes it forget the segments it deletes
  (task gk4, **Build window and prune**).
  The requests give the TLS and path hints; the hostname is
  the client's HTTP host when that is a name, else its DNS
  lookup of the endpoint's address (a best guess: one address
  may serve many names). An endpoint is hostname, IP and
  port, plus the anchor host for a loopback, link-local or
  unspecified IP (task ai4): that address is a different
  machine on every host, so two hosts' local services never
  fold into one.
  Rules match
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
  traces), then the sudoers rule `iormeshctl bootstrap`
  installed (decision #1). Trace files are synced off first (rsync, same as
  collect), so `rm` never destroys uncollected traces unless
  the operator says so with `-force`; an unreachable host
  warns and only drops the host (or the `-project`) from the
  registry — its tracer and traces stay on it, and deploy's
  record keeps the dropped projects as tombstones so a later
  `rm` of the host still collects them.
  Built (task eb4, `internal/ctl/rm.go`): per removed project
  a live session is stopped first (`sudo iormesh stop`, which
  publishes its last segment and now fails when `state.json`
  does not say `ended` — a run SIGKILLed after the stop
  timeout left its minute unpublished), then collected.
  `uninstall` runs only when the host leaves the registry
  (`rm HOST`, or its last project), and first every other
  project directory under `/opt/iormesh/traces` that the
  registry or deploy's record (tombstones included) lists for
  the host is stopped and collected too. A directory neither
  lists (started by hand, or named by a compromised server)
  is not collected (task hi4): the server never picks the
  store's project.
  Then any segment file still in a `ready/` or active —
  an unlisted project's included — refuses the uninstall,
  naming it.
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
  SSH key. Built (task ph4, owner-approved 2026-11-22):
  `iormeshctl status [-project P]... [HOST|POD...]` asks every
  registered target (or the named ones) — a host through the
  sudoers rule's `status -json`, which the rule already allows,
  a pod through `kubectl exec … iormesh ready -status` — and
  prints one row per target and project: state, end reason,
  ends-by, tracer version and the segments in `ready/` awaiting
  collection; a target that fails is a row, never an abort (exit
  1 after all were asked). `iormeshctl list` prints the registry
  (hosts, pod entries, projects, targets, flags) and contacts
  nothing. The run records its tracer version and argv in
  `state.json` so a skew between the deployed build and a
  running session is visible; a state from an older tracer
  lacks them and shows the version as unknown.
  Built (task zh4, owner-approved 2026-11-22): **tracing over
  days**. A session ends after its `-duration` and its
  transient unit has no `Restart=`, so a reboot, an OOM kill
  or a disk-limit trip ends it for good. `iormeshctl ensure
  [-project P]... [HOST...]` reads each host's `status -json`
  and starts again (deploy's start argv and start path) every
  registered project whose session ended on its own —
  `duration`, `targets_exited`, `error`, a `stop` that was not
  the operator's, a run that died — then collects the host.
  An explicit stop stays distinguishable from an end: `iormesh
  stop` sets `operator_stop: true` in `state.json` once the
  unit is dead (never while the run may write the file; the run
  writes `false` from its start), since a reboot's SIGTERM ends
  a run with the same `stop` reason; ensure leaves those
  stopped. A deploy restart whose start fails after its stop
  leaves the project marked stopped, so ensure does not recover
  it. A tracer from before the field (`operator_stop`
  absent) cannot tell, so its `stop` ends are left too. Both
  status texts say which ends ensure leaves (task 0l4): the
  `iormeshctl status` NOTE and the host's `iormesh status`
  name an operator's stop and a `stop` of unknown origin;
  `-json` carries the field itself. A
  `disk_limit` end is collected first and restarted only when
  the collect emptied its `ready/` (a second status read);
  should the partition still be over the limit, the new run's
  watchdog refuses at its start, so no restart loop fills the
  disk. ensure starts no project the host never ran and never
  upgrades — both are deploy's — and run again only collects,
  so a systemd user timer or a cron line drives it
  ([architecture.md](architecture.md#tracing-for-days); the
  timer in [the guide](guide/09-tracing-for-days.md#from-a-systemd-user-timer)).
  Decided (task pk4): **ensure covers pod entries** too, every
  registered one without a target named. A pod's tracer has no
  unit or `state.json` ensure can read once its container
  stopped (`kubectl exec` cannot enter it), so ensure decides a
  debug pod by its newest tracer container as the API server
  reports it, and restarts only through deploy's pod path (a
  fresh `iormesh-N`, at most `MaxDebugTracers`, then the attach
  wait): a container that terminated with a non-zero exit code
  — killed with its app container, OOM-killed, crashed — is
  restarted; one that exited 0 was ended on purpose (only a
  SIGTERM or SIGINT ends a `-linger` run cleanly; a session that
  had failed exits 1 on the signal and is restarted) and is
  left stopped, its collect skipped and reported rather than
  failed. A terminating pod (`deletionTimestamp` set, still
  phase Running) is left whatever its tracer: a selector never
  returns one, but a `pod:` entry's named pod is returned
  marked during its grace period (a collect there may still
  save segments), and a tracer started in it would die before
  attaching. A running tracer is asked `ready -status`: running or
  attaching is nothing to do; ended (its `-duration`,
  `disk_limit`) lingers in a container that cannot be restarted,
  so ensure reports that a new session needs a pod restart and
  leaves it. A pod without a tracer container (never deployed,
  or the pod restarted under the same name or selector) is
  deploy's, as a host's never-started project is; a `pod:`
  entry's pod that is gone, or a selector matching no running
  pod, fails the entry as collect does — nothing to restart or
  collect, deploy starts a tracer in its replacement. A sidecar
  is the kubelet's to restart: ensure only collects it. Every
  other pod of the entry is then collected as collect does.
- Target platform for the tracer: Rocky Linux 9 first (BTF
  present; ior's Rocky 9 build/Docker patterns apply), amd64
  and arm64. Deployed binaries come out of the container build
  pipeline (see build.md, "Container builds").
  Owner decision (2026-11-21): **Rocky Linux 9.9 is the main
  target**, and everything must work on its el9 kernel
  (5.14.0-741.22.1.el9_9 on the M4 test hosts), not only on
  the 7.x the suite was developed on. Kernel-version floors
  below are upstream numbers: RHEL backports BPF, and on 9.9
  `bpf_rdonly_cast`, uprobe_multi (the Java marks) and BTF are
  present (task lb4). An el9 load failure, verifier rejection
  or record-layout difference is a defect to fix behind a
  load-time probe or CO-RE, never a skip — as gf4 (verifier:
  ctx memcpy) and hf4 (12-byte `trace_entry`) were.
  Status (2026-11-21, task nf4): the root BPF suite (27 tests)
  passes on 9.9, the uprobe_multi pid filter (46ba0e49) and the
  `group_dead` fallback included; the integration suite passed
  50 of 51 on the final tree — `TestJavaLiveAttach` flakes
  there (about 1 run in 8, task tf4)
  ([testing.md](testing.md#root-suites-on-rocky-99-task-nf4-2026-11-21)).
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
  Status (2026-11-21, task lb4): **accepted** — the sixth run
  on two Rocky 9.9 hosts (VMs iom1 and iom2, kernel
  5.14.0-741.22.1.el9_9, build of 35c7de7) printed every
  criterion M4-0 to M4-14 PASS. Limits of the evidence: both
  VMs share one physical host (one hardware clock, one
  physical network), and two runs passed all 15 (the sixth, and
  3f4's below); the five before the sixth each failed on defects
  since fixed (gf4–nf4, qf4, tf4).
  Owner-delegated advisor decision; the run was made by agents
  on hosts the owner provided for it. Task 3f4 re-ran the
  acceptance on a HEAD build (5eedf77, after pf4 changed the
  BPF object) on the same VMs: every criterion PASS again, so M4
  stays accepted. History: the first run passed 11 of 15;
  the microservices TLS label (kf4), one cross-host hop of the
  spread (if4), the clock bound (jf4) and the watchdog reason in
  the summary (lf4) fail on defects filed as tasks, none of them
  the kernel's. Two el9 kernel defects that stopped the tracer
  were fixed in the run (gf4, hf4). After if4, lf4 and kf4 (the
  agent jar shipped by deploy, #28), a fourth run passed 14 of
  15, M4-8 and M4-9 with their TLS labels; only the clock bound
  (jf4, fixed after that build) failed; a fifth, with jf4 and
  kf4's review fixes, passed M4-10 but lost two plain hops of the
  spread M4-9 to skipped runs (qf4: the merge keys a client from
  its `CLOSE`, the correlator splits a socket address reused after
  a lost `CLOSE`); a sixth, with qf4, printed every criterion
  PASS (build of 35c7de7). No criterion counts as
  accepted until one run passes all — the sixth did. `mage
  m4Acceptance HOST1 HOST2` (`internal/acceptance`) runs the
  whole sequence against exactly the two named, bootstrapped
  hosts and prints PASS,
  FAIL, SKIP or NOT RUN per criterion; the runbook, its
  prerequisites, the local results and the Rocky 9.9 runs are
  in
  [docs/testing.md](testing.md#m4-acceptance-on-two-real-hosts-task-lb4).
  On real hosts the clock criterion reports the real clocks'
  bound; the injected skew stays the rig's
  (`TestClockSkewRecovered`, task ye4). The microservices run
  covers both layouts: all four services on HOST1 (M4-8), and
  the spread (M4-9, task 1f4). In the spread, gateway and
  inventory run on HOST1 and orders and billing on HOST2, each
  as its own project, so gateway→orders (TLS) and
  orders→inventory (plain) cross hosts. The rig spreads them
  over three emulated hosts (`TestMicroservicesAcrossHosts`).

## M5 — Views, query and the LLM eval

- **Every consolidation run ends in something to explore.**
  `iormeshctl build` (and so `refresh`) finishes by building
  one view model of
  the merged mesh and rendering it three ways — never three
  separate readings of the data (design:
  [docs/visualization.md](visualization.md)):
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
  *Built (task nb4):* every `build` writes
  `builds/<projects>/sites/<Run.Last>-<hash>/` — `index.html`
  with its script, stylesheet and the whole model inline (from
  `file://` nothing else loads reliably, so nothing does),
  `graph.svg`, `sequence/<ID>.svg` — and swaps `sites/latest` to
  it; `iormeshctl open` prints the newest page and runs
  `xdg-open` (macOS `open`) when there is one. The CSP is
  `default-src 'none'` plus the inline script's and stylesheet's
  hashes; trace strings reach the page only as text (the JSON
  block escapes `<`, `>`, `&`, U+2028/9; the script never writes
  markup). Mesh with pan/zoom, service and edge panels that
  follow a connection to its other side, the sequence view per
  lifeline with each step's Java stack and `query -ref` command,
  thread stories, processes, health; a host/text/time filter;
  URL-hash deep links. Same model, same bytes, same folder
  ([docs/visualization.md](visualization.md) "V3 as
  built").
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
  ([docs/visualization.md](visualization.md) "V2 as
  built"). *Decided (task vg4, 2026-11-21): realistic noise
  folds, it is never filtered.* On Rocky 9.9 the checkout had
  1696 steps — the serving threads reading the JDK's
  `lib/modules` hundreds of times while loading classes on their
  first request, plus name-service lookups — and the 200-step cap
  cut it before the inventory hop. The rig never saw this: it
  attaches by PID after the JVMs started, so the open of
  `lib/modules` (and of the jars) at JVM start is not in its trace
  and the later reads, on an fd whose open the trace never showed,
  are no file work it can name; the real hosts' pattern sessions
  trace from exec and name every one. The views now fold a run of
  back-to-back repeats of one call by one thread (same verb and
  path, the same outcome — success, or the same errno; return
  values and byte counts may differ, so EOF and short reads fold
  — offsets aside, never across a client request the thread made)
  into one step that counts its calls (`count`, label `(N
  calls)`, bytes and latency summed, the first call's stack and
  ref, the last call's ref, and the `query` that prints the whole
  run); every surface labels a run's fields as sums or its first
  call's; the caps count steps after folding, the calls cut past
  a request's cap folding too; a conversation's `hops` and
  `services` cover its whole length, past the cap included; and
  past 32 files a process keeps its written or changed files
  first, then read, then only opened, then only failed ones — a
  ranked selection, not a prefix, listed in first-seen order
  (task vg4 review). Weighed and
  not taken: a noise class for JDK runtime-image and jar reads
  (a path list to maintain, and a guess about what is noise that
  the operator cannot audit — folding needs no list and hides
  nothing), and per-segment caps (folding brought the real
  checkout to 100 steps; a tail cut stays the one rule while it
  holds). Same model, so the site, the TUI and the SVG show the
  folded steps alike ([docs/visualization.md](visualization.md)
  "V2 as built" "Folded runs"). *Built (task ob4):* the
  terminal UI, `iormeshctl
  tui`, on the same `model.json` through `show`'s loader —
  services, edges, conversations, a conversation's steps, thread
  stories, processes and health, each a list and a detail;
  `⏎` walks service or edge → conversations → sequence → a
  step's Java stack and `query -ref` → its thread's story;
  Bubble Tea and Lip Gloss pinned at ior's versions (pure Go,
  the controller stays cgo-free); the model made terminal-safe
  once on load; no TTY, no UI (exit 1, pointing at `show`)
  ([docs/visualization.md](visualization.md) "V4 as
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
  (docs/implementation.md M5). *Task wh4* adds exact-value
  filters, ANDed with the rest: `-event KIND` (a schema kind;
  an unknown one is a usage error listing the kinds), `-pid`,
  `-errno` (a Linux errno name or number, matched against the
  recorded `err` name) and `-otel-trace ID` (`otel.trace_id`:
  the I/O inside one distributed trace's spans). All are
  checked before anything is read. *Task ik4:* a window stays
  exact by default — query reads the whole store and cuts the
  events to `-from`/`-to` afterwards, a time of day placed as
  build places it (**A time of day's date**, task 1l4: the
  store's one date, refused on a multi-day store). The opt-in
  `-window-segments` (with `-from` or `-to`) decodes only the
  segments build's selection takes (**Build window and
  prune**: the same resolution and anchor, the same
  `federate.Span` widening) — faster on a multi-day store, not
  exact. Its guarantee is build's: an event inside the window
  is found when it was written less than a minute after the
  window's end; a call blocked longer is missed. The slice is
  computed from those segments alone, as a windowed build is: a
  connection established before them keeps its host-local id,
  and `-app`, `-file` and `-follow` see only the attaches,
  opens and connection use inside them. A window no segment
  overlaps is the empty slice; a project with no segment at
  all fails as the default query does (no readable segment,
  task al4).
- Plus the per-run summary/report export — the LLM rollup of the
  whole mesh (see output-contract.md). *Built (task qb4):*
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
  fails every question, by test) into
  `eval/results/<date>-<scenario>.json`. The model is a command
  (prompt on stdin, reply on stdout) the operator must name:
  there is no default, since the prompt carries trace data, so
  the eval never runs on its own and never in `mage
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
  *Status (task rb4, 2026-11-20): passes on the rig — not
  accepted* (Sequencing relaxation). `TestViewsAcceptance` runs
  the microservices spread on three emulated hosts, fills a
  store as collect lays one out (the rig has no SSH deploy or
  collect), names the servers and builds with `iormeshctl`: the
  mesh walks to the rename step's Java stack in `show`, the TUI
  and the site (its embedded model is `model.json`; with
  `IORMESH_SITE_BROWSER=1`, headless Chrome renders every route
  from `file://` with an empty console); the checkout's sequence
  is the declared story — every hop on its causing thread, each
  serving thread's create-tmp/write/rename in its place, all
  causal; a rebuild writes the same bytes, and the mesh and the
  checkout match their SVG goldens. Found and fixed: `graph.svg`
  drew machine ids and "java PID" where every other surface said
  "orders" on "hostb" — it now takes the model's names. The scenario reads
  its config on the main thread at startup, so the config read
  is in the process view, not the sequence; its clients are
  synchronous, so async hops are held by unit tests only; and
  without an app (the rig targets PIDs) a client is named by its
  comm ("java"). Remaining: the eval with a real model —
  *awaiting the owner's model command* (`mage eval`; only a
  scripted stand-in has run the pipeline, 10/10, and a wrong one
  is refused) — and the real-host re-check (task 3f4)
  ([docs/testing.md](testing.md) "M5 on the rig").
  *Real-host re-check (task 3f4, 2026-11-21): still not
  accepted.* On the store of 3f4's M4 run (Rocky 9.9, the
  spread with the agent, collected over SSH) every surface
  works — `show`, `query -ref`, threads, processes, the TUI walk
  to a step's Java stack, the site (headless Chrome, every route,
  empty console), byte-identical rebuilds, the stand-in eval —
  but the sequence criterion fails: the checkout has 1696 steps,
  orders' serving thread loading classes from the JDK's
  `lib/modules` (744 `pread64`) on its first request, and the
  200-step cap cuts it before the inventory hop, so neither the
  later hops nor any service's create-tmp/rename are shown; the
  process view's file cap hides orders' config and record the
  same way (task vg4). The eval still awaits the owner's model.
  ([docs/testing.md](testing.md) "M5 on Rocky 9.9").
  *Views fixed (task vg4, 2026-11-21):* rebuilt from the same
  store with folded runs, the checkout is 100 steps holding all
  1696 calls, none cut — every hop causal and paired on its
  thread (gateway main → orders HTTP-Dispatcher → inventory, →
  billing), inventory's, billing's and orders' create-tmp, write
  and rename each in its place, all four services named — and
  `show processes` lists orders' `orders.conf` and its
  `order-42.json` record. The sequence criterion now holds on
  the real store; M5 stays not accepted until the eval has run
  with the owner's model.

## M6 — Mesh expansion: follow the edges, from the controller

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
  Settled by task tb4: one `-apply` is one round; a candidate
  becomes a registry host written as its address, under the
  first project whose connections revealed it, with one
  project per host (another port of it is listed for the
  operator to add); the registry is saved before any host is
  contacted, a host whose machine-id a traced host already has
  is unregistered again, and the rest is M4's deploy unchanged
  (no new sudo path). A damaged segment refuses `-apply`.
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
  traffic are excluded from the traced set. Expansion never
  counts the latter as an edge (task tb4): a peer on port 22,
  or a connection of a traced ssh, sshd or rsync process,
  proposes nothing (`transport`).
- Acceptance: the M0 microservices topology (3+ hops, mixed
  plain-HTTP/TLS links) with only the gateway host enrolled;
  expansion rounds discover and deploy to every service host,
  and after the scenario re-runs, the merged mesh equals the
  one obtained by `add`ing all hosts by hand. A hop limit of 1
  demonstrably stops at the first ring; a peer outside the
  allowlist is listed but never contacted; a cyclic topology
  terminates.
  Status (2026-11-20, task tb4): passes on the rig —
  `TestMeshExpansion` (docs/testing.md "M6 on the rig"). Not
  accepted: M4's real-host run (task lb4) and the re-check
  (task 3f4) come first (Sequencing relaxation), and the rig
  starts an enrolled host's tracer itself, where a real round
  deploys it over SSH.
  Real-host re-check (task 3f4, 2026-11-21): passes on the rig
  and on two Rocky 9.9 hosts except one criterion — still not
  accepted. With only iom1 (gateway, inventory, billing)
  enrolled, round 1 proposed exactly iom2's orders port at hop
  1; with the allowlist narrowed, it was listed `not_allowed`,
  `-apply` enrolled nothing and iom2 saw no login; `-apply`
  then enrolled and deployed iom2 over SSH, bound to orders'
  listening port; later rounds proposed nothing (the cycle back
  to iom1 is visited), and a re-run's merge has the same network
  shape as a by-hand build of both hosts, all three cross-host
  hops fused. A hop limit of 1 stopping at the first ring needs
  a third host, the hop-2 peer two hosts cannot provide (task
  wg4) ([docs/testing.md](testing.md) "M6 on Rocky 9.9").
  Status (2026-11-21, task wg4): **accepted** — every criterion
  passes on three Rocky 9.9 hosts (VMs iom1, iom2, iom3, build
  of 5eedf77), M4 being accepted and 3f4's re-check run
  (Sequencing relaxation). iom1 (gateway, inventory) alone
  enrolled, orders on iom2, billing on iom3, which a firewall
  rule lets only iom2 reach: round 1 proposed iom2's orders port
  at hop 1 and round 2's `-apply` deployed it; billing then
  surfaced as hop 2 — `hop_limit` at `-hops 1` and `not_allowed`
  with iom3 off the allowlist, `-apply` enrolling nothing and
  iom3 seeing no login either way — and at `-hops 2` `-apply`
  enrolled and deployed iom3 over SSH, bound to billing's port.
  Later rounds propose nothing (orders → inventory returns to a
  registered host); round 3's merge, all three cross-host hops
  fused, has the same network shape as a by-hand build of all
  three hosts; the store keeps earlier rounds' one-sided ends
  unresolved. Limits of the evidence: the three VMs share one
  physical host, and it is one run. Owner-delegated advisor
  decision; the run was made by an agent on hosts the owner
  provided for it ([docs/testing.md](testing.md) "M6 on
  three Rocky 9.9 hosts").

## M7 — Kubernetes: sidecar & debug-container deployment

Built after plain Linux servers (M4); accepted on kind and el9
(Status, below).

- iormesh runs as a sidecar (or `kubectl debug` ephemeral
  container) in the target pod, with the capabilities eBPF and
  the Java attach need — a minimal set, `privileged` only as the
  fallback (decision #13, provisional). It traces the pod's
  containers — and only those: targeting becomes PID-namespace
  aware, so a tracer in the pod's namespace keys its targets by
  the pods' own pids and cannot name a process outside it. (No
  systemd here: the container is the unit, and its resource
  limits are the caps.)
  Status (2026-11-20, task 4f4): the namespace-aware targeting is
  built (docs/implementation.md M7 step 1) and checked in a PID
  namespace on the host, and on kind since (task yb4). Every pid the tracer
  keys by or reports — `pid`, `tid`, `target_pid`, the agent's
  ring names — is the one its own namespace shows, so on the
  host nothing changes; records carry no host tgid (an in-pod
  tracer could not resolve one, and no join needs it). The #25
  bystander item in namespace mode, open after 4f4 (1.25× /
  1.30×), passes in task 9f4's set (2026-11-21): 1.23× / 1.23×,
  wall 1.22× / 1.21× — as did the 4f4 build in the same set,
  at the guard; 9f4 cut the tracer's kernel instructions per
  bystander call by ~17 % (see #25).
- `kubectl debug` deployments are driven by `iormeshctl`, with
  the kubectl context (cluster/namespace from the kubeconfig)
  and the debug container image both configurable —
  defaulting to our published tracer image, pinned by digest.
  The config shape is provisional and the owner's examples are
  pending (decision #18).
  Status (2026-11-21, task wb4): the kubectl transport is built
  (`internal/k8s`, docs/implementation.md M7 step 9) and tested
  against a fake kubectl, since run on kind (task yb4) — `exec`, the
  exec-streamed tar fetch through a new `iormesh ready`
  subcommand (with `-state`, the session's attach state, which
  deploy waits for: task ef4), and `debug --target` into prepared pods only;
  `Push` is refused (the image carries the tracer, and #12 rules
  out `kubectl cp`).
  Status (2026-11-21, task af4): wired into iormeshctl and tested
  against a fake kubectl, since run on kind (task yb4) — the registry's `k8s:`
  and `pods:` sections, `-context`/`-namespace`/`-image`, and
  deploy, collect, refresh and rm taking pod entries (named
  `<cluster>/<namespace>/<pod or selector>`) beside hosts, with
  no shell utility in the image (#18's "Wired" note). Each pod
  is its own store directory, `pod-<uid>`, pinned to the host
  identity task 7f4 gives an in-pod tracer (`host_id`
  `pod-<uid>`); the tracer derives it with the same helpers
  (`k8s.PodHostID`, `PodFQDN`) and name rules (below). Deploy
  reports a debug tracer only once it is attached (task ef4):
  it waits, bounded by `-attach-timeout` (default 2 min), for the
  session's `state.json` to say running, and fails the pod when
  the session ended or the container exited first — a workload
  started on deploy's word is traced from its first syscall.
  Since task 5g4 it also fails at once when the probe itself
  fails in the running container or a tracer left running
  traces another project, refuses an answer `state.json` could
  carry but iormesh never writes (the session id it prints is
  8 lowercase hex), bounds each probe on its own rather than
  by the wait's deadline, and says "interrupted" for a Ctrl-C.
- Pod identity enrichment: map host PIDs/cgroups → pod, namespace,
  container, so the graph speaks "pod checkout-7d9f (ns prod)"
  instead of bare host PIDs. In k8s mode the project defaults
  to the namespace (decision #21). **The pod is the host**
  (task 7f4; provisional, an advisor's call the owner delegated):
  an in-pod tracer's `host_id` is `pod-<uid>` and its FQDN
  `<name>.<namespace>.<uid[:8]>`, both from the downward API
  (`POD_NAME`, `POD_NAMESPACE`, `POD_UID`, set by the sidecar
  patch and the debug container spec), with no resolver lookup
  and no `/etc/machine-id` read even if the image has one. A
  partial or malformed set stops the tracer rather than fall
  back. Outside a pod: the machine-id, as before. Pods of one
  node are grouped by the session's `boot_id`. **One tracer per
  pod** is an invariant: the debug path refuses a pod that has a
  tracer already (a debug container or a sidecar), and sidecar
  mode never starts a second. With it the pod is one PID
  namespace, so `proc:<host_id>/<pid>@<inc>` — pids numbered per
  pod since task 4f4 — stays unique without a PID-namespace key.
  To confirm with the owner: the pod as the host node in k8s
  mode, the 8-hex FQDN suffix, the node's machine-id should a
  hostPID mode ever come, and service rows of dead pods.
  Status (2026-11-21, task vb4): built and unit-tested, and run
  on kind by task yb4 (Acceptance, below) — the tracer image
  (`scripts/container/tracer-image.Containerfile`: the static
  tracer and agent jar on `scratch`), the prepare and sidecar
  patches and the `kubectl debug --custom` profile, generated
  into `deploy/` with #13's fields and a digest-pinned image (a
  placeholder until one is published), the cgroup → pod UID and
  container parser, and the project defaulting to the namespace
  (#21). The trace dir sits one level inside the emptyDir, whose
  root the kubelet makes 0777, and a byte cap below its
  `sizeLimit` ends the trace before an eviction would. In a
  pod the byte cap replaces the percent limit (task df4): the
  sidecar patch and a debug deploy both pass `-disk-limit 0
  -disk-max-bytes` ¾ of the pod's own `sizeLimit`, since statfs
  of an emptyDir reads the node's partition and a 90 % limit
  stopped every tracer on a full node at once; `-disk-min-free`
  stays as the node's floor, and a registry may not set either
  flag for a pod entry. Since task 5g4 the cap is the smaller
  of ¾ and the `sizeLimit` less 96 MiB (a segment in flight
  and the end files), a `sizeLimit` under 128 MiB is refused,
  and any byte-size quantity is read (`2G`, a byte count,
  `2e9`, `1.5Gi`). Accepted trade-off: without a percent limit
  a busy trace on a full node counts toward the traced pod's
  ephemeral storage when the kubelet's node-pressure eviction
  picks pods; a 98 % limit would not change that, the kubelet
  acting at 90 % used by default (docs/implementation.md M7
  step 5). At the cap a run in a pod does not exit (task ki4):
  whether the cap ended its session (`disk_limit`) or refused
  its start (the trace dir still over it), it frees its BPF and
  waits, as `-linger` does, until collect has drained the dir
  under the cap or SIGTERM arrives. An exit crash-looped the
  sidecar — every restart refused for the same full dir — which
  took the pod NotReady and left collect no running container
  to exec into, so the segments died with the pod. Once
  drained the run exits and the restart starts a fresh
  session; outside a pod a full disk still fails the run at
  once. The
  image has no `/etc/machine-id`, which made every run in it
  fail; since task 7f4 a run in a pod needs none (above,
  docs/implementation.md M7 step 6). Pod identity status
  (2026-11-21, task 7f4): built and unit-tested, including two
  pods with one pod-local pid merging into two process nodes;
  on kind (task yb4) five pods on one node, five hosts, one
  `boot_id`.
  **Pod fields on events** (task sh4, owner-approved
  2026-11-22): an in-pod tracer stamps every event with an
  optional nested envelope object `pod` — `namespace` and
  `name` from the downward API (`POD_NAMESPACE`, `POD_NAME`,
  the values the host id and FQDN already come from), and
  `container_id`, the 64-hex runtime id of the container the
  process runs in. An envelope object, not args: it describes
  who did the I/O, like `host_id`, on every kind, and an LLM
  reading a slice of events sees the pod without a join.
  Host-level events (`session`, `drop`, `end`, pid 0) carry
  `namespace` and `name` only. The container comes per process
  from `/proc/<pid>/cgroup` (`k8s.ParseProcCgroup`: the
  relative `/../<container>` form a private cgroup namespace
  shows, and the full kubelet paths of both drivers — kind's
  `kubelet-kubepods-…` slices, k3s on el9's systemd
  `kubepods-…` slices, cgroupfs), read once when the
  correlator meets the process (attach or first record), and
  inherited at fork, where the kernel puts the child in its
  parent's cgroup; never per event. A process in the tracer's
  own container (its cgroup reads `/`), in the pod cgroup
  itself or in another pod's cgroup gets no `container_id`.
  The container's *name* is not in the cgroup and the tracer
  has no API access, so the id is what a trace carries; the
  controller joins the name (task jk4). **Container names**:
  every collect of a pod reads its status anyway
  (`k8s.Lister`); from it the controller takes each
  container's name with its `containerID` — `containerStatuses`,
  `initContainerStatuses` and `ephemeralContainerStatuses`, the
  running id and `lastState.terminated`'s, so a container that
  restarted once between collects keeps its old id named — and
  merges them into `containers.json` (`{"<id>": "<name>"}`) in
  the pod's store directory of every project collected,
  `<store>/<project>/pod-<uid>/`, beside the segments: a build,
  of a copied store too, names containers offline and
  reproducibly, never asking the cluster. Ids are normalised to
  the trace's form: the API's `<runtime>://<id>` (containerd,
  cri-o, docker) loses its scheme, and an id that is not then 64
  lowercase hex digits, or a name that is not an RFC 1123
  label, is left out. A recorded id is never renamed (ids are
  unique; the first name stays); the file is written
  atomically, and a pod cannot write it (the fetch unpacks
  segment names only). The store listing skips it. Build reads
  each pod directory's map; the rollup keeps each process's
  `container_id` (from its events) and the model's process
  `container_id` and `container`, the name. A session collected
  before the map, a container that restarted twice between
  collects, or a map that does not read (its whole file, listed
  as ignored in health with why) shows the id. A map that does
  not read — bad JSON, a bad id or name, over 1 MiB, or no
  regular file: it is opened without following a symlink or
  waiting on a FIFO, and read no further than the bound — is
  never rewritten by collect on its own (that would drop the
  names of ids the status no longer lists, and hide a file that
  went bad); collect refuses the merge and says to remove it,
  after which the next collect writes it anew from the pod's
  current status. Outside a pod there is no
  `pod` object. Backward compatible: an older segment has no pod
  column and reads as before (no `pod`). The controller
  checks a segment's `pod` against its pinned FQDN
  (`<name>.<namespace>.<uid[:8]>`), so a session cannot claim
  another namespace. **Namespace filter**: `iormeshctl build
  -namespace NS` builds only the sessions of pods in NS (by
  their pinned FQDN, so segments from before the pod fields
  count too; a host outside a pod is left out), and the
  service database's views of the build only the connections
  one of those pods traced; the build goes to
  `<projects>@<NS>/`, which `show`, `open` and `tui
  -namespace NS` read (task hk4). Bad
  segments stay listed whatever their namespace (a damaged file
  cannot say whose it was). Status (2026-11-22, task sh4):
  built and unit-tested against kind's captured cgroup files and
  constructed k3s-on-el9 shapes; not yet run on a cluster.
- Java agent live-attach through the shared process namespace
  (`kubectl debug --target`, or `shareProcessNamespace: true`
  for a sidecar; attach socket reached via `/proc/<pid>/root`),
  with the agent jar and spool staged inside the JVM's root —
  decision #14, provisional.
- Service-NAT correlation: kube-proxy ClusterIP DNAT/SNAT means
  the two ends of a connection observe different 4-tuples; the
  mesh must join them (extends M4's NAT-tolerant join to k8s
  services).
  Settled (2026-11-21, task xb4): S2's ISN pair applies, so M4's
  join fuses ClusterIP connections unchanged and conntrack on the
  node is not needed. kube-proxy's iptables and nftables modes
  NAT through `nf_nat`, and its IPVS mode through IPVS's own NAT
  forwarding (`ip_vs_nat_xmit`; kube-proxy uses masquerade mode
  only); all rewrite addresses and ports, never the initial
  sequence numbers. `nf_nat`'s sequence adjustment (`seqadj`,
  for ALG helpers such as FTP or SIP) only shifts sequence
  numbers after a helper resized payload, past the handshake, so
  the pair survives it too; a `SYNPROXY` target, which answers
  the handshake itself, is what breaks it, and kube-proxy
  installs none. A socket-level load balancer (Cilium's
  kube-proxy replacement) rewrites the address at `connect()`
  and touches no sequence number either — but the client's
  `tcp_sock` then holds the backend's address, not the
  ClusterIP, so the edge fuses without its Service named. Both
  ends compute the pair from their own `tcp_sock`, so only a box
  rewriting sequence numbers in flight could break it — S2's
  "Limits" list, which stays unresolved, never guessed. Caveat:
  a proxy that terminates TCP — an Istio or Linkerd sidecar,
  Istio's ztunnel, Cilium's L7 proxy — splits each call into two
  connections, so the two apps' ends never fuse; that is the
  route map's kind of hop, not the ISN join's. What the route map
  adds
  (`internal/routemap`, #15): the Service named on a fused
  ClusterIP edge and its pod checked against the Service's
  endpoints (by pod UID — an in-pod tracer's host id is
  `pod-<uid>` — else by address, for an in-pod end only to
  endpoints without a UID, since pod IPs are reused; a pod not
  listed flags the map `stale_service`, the fused fact stands);
  a ClusterIP edge whose pod is untraced completed to the
  Service and its workload, to a pod only when the Service
  lists exactly one endpoint; a routed (ingress) edge ends at
  the pod by the same rule, provided the build also traced that
  pod (task ff4: no trace of the client's flow reaches past the
  ingress, so the pod's own trace vouches for the map's one
  endpoint). The route rule is stricter on purpose (task 5g4,
  kept rather than unified): an untraced ClusterIP edge rests
  on one map lookup from what the client's trace saw — it
  dialed the Service's address, which kube-proxy forwards only
  to the Service's endpoints — while a route's flow ends at the
  ingress, whose controller picks the backend from its own
  cache and rules (canary weights, a default backend), so
  FQDN → Service → endpoint is two lookups no trace confirms.
  A traced pod behind a ClusterIP fuses instead, so the two
  rules never meet on one edge. The `pod-` prefix is one
  constant (`k8sname.PodHostPrefix`) for `k8s.PodHostID`, the
  route join and the registry's pod directories. An
  address inside a declared Service CIDR no Service holds
  flagged `unknown_service`. Confirmed on kind (task yb4): a
  ClusterIP connection between two traced pods fuses in the
  build under kube-proxy's iptables and nftables modes, and so
  does the hairpin — a pod calling its own Service, both ends in
  one pod, the server end seeing the node's masquerade source
  (10.244.0.1).
- **Ingress route mapping**: an FQDN often terminates on an
  ingress (Traefik) behind a managed LB — no tracer can attach
  there, and the L7 hop breaks the connection join anyway
  (client→ingress and ingress→backend are separate streams).
  iormesh completes those edges from routing metadata: a
  configured map FQDN → ingress → service → deployment or
  statefulset — a static map first, then (in k8s mode)
  discovery from Ingress/IngressRoute objects and
  service→workload resolution via the k8s API (#15). No payload
  parsing — routing configuration is the source.
  Observed http events (host + path from the agent) can
  validate the mapping against what actually flowed.
  Status (2026-11-21, task xb4): the static map is built and
  unit-tested, and run on kind (task yb4) — `routes.yaml` beside the
  registry (its `routes:`, plus the `services:` with ClusterIPs
  and endpoints the ClusterIP join reads), read under the
  registry's rules and refused whole on any bad entry; `build`
  prints and models the routed edges (`routed` lines,
  `model.json` `routes` and `health.route_flags`). An unresolved
  client edge whose hostname (its HTTP host, else its DNS
  lookup) has a route becomes client → FQDN → ingress → Service
  → workload; an HTTP host whose route names another Service
  than the one reached is flagged `route_contradicted`. A route
  may name its ingress controller's own Service
  (`ingress_service`): an in-cluster client reaching it — by its
  ClusterIP, or fused to one of its pods — while asking for the
  FQDN gets the route's edge, not a contradiction, and an address
  in the Service CIDR that no Service holds is flagged and the
  route still tried. The three surfaces draw them (task bf4):
  each routed edge is one row — client → FQDN → ingress →
  Service → workload (→ pod) — in a band below `graph.svg`'s
  columns and the site's mesh, and as rows after the traced
  edges in the TUI; an edge the map completed is drawn dashed,
  only a fused one solid, and every route flag is shown beside
  them (graph.svg, the mesh, the TUI's edges and health). The
  graph has no FQDN, ingress or workload nodes: those hops
  exist only in the routed edge.
  Discovery is built too (task xb4), tested against a fake
  kubectl and on kind (task yb4): `iormeshctl routes discover` reads a
  registered cluster's namespaces with read-only `kubectl get`
  (Ingress, IngressRoute under both Traefik API groups, Service,
  EndpointSlice, pods, ReplicaSets) into a snapshot beside the
  service database; `build` reads the snapshot, never kubectl, so
  it stays offline and deterministic. One answer or none: a host
  routed to several Services (by path or by two objects) gets no
  route (`ambiguous_route`), a Service whose endpoints span
  workloads names none (`ambiguous_workload`), an object whose
  names fail the map's checks is `skipped`, and a static entry
  shadowing a discovered one is `shadowed`.
- Collection works with the controller tooling too —
  per-pod/per-node trace fetch (decision #12), events carrying
  pod identity.
- Acceptance: M0 client and server as pods in kind with iormesh
  attached as sidecar/debug container; expectations hold with
  pod identity added; a ClusterIP connection joins both sides;
  the M0 ingress scenario renders as one routed edge — app →
  FQDN → Traefik → deployment — with no tracer on the ingress.
  Status (2026-11-21, tasks yb4, cf4–ff4): **accepted** — every
  criterion passes on kind (`mage k8sAcceptance` from scratch,
  no workaround or test-side compensation left), M4 being
  accepted and 3f4's re-check run (Sequencing relaxation).
  Limits of the evidence: one single-node kind cluster on the
  Fedora host's 7.2 kernel with no LSM active, containerd 2.2.0
  only — no el9 node, no SELinux-enforcing node, no older
  runtime, no Pod Security level below `privileged`. Decisions
  #13, #14, #15 and #18 stay provisional; a finding on an el9 or
  fleet node reopens this status. Owner-delegated advisor
  decision. History: first passed with one test-only workaround
  (cf4) and two test-side compensations (df4, ef4), all since
  removed, then re-run green without any — `mage k8sAcceptance` from
  scratch, green (`integrationtests/kind`; kind 0.31, Kubernetes
  1.35.0, containerd 2.2.0, one node; docs/testing.md "M7
  acceptance on kind"). tcp-nat's client and server as pods
  under debug containers, deployed by iormeshctl from a local
  registry (imageID carries the registry digest), match their
  per-pod and merged goldens with the ClusterIP and pod
  addresses; the connection fuses (iptables and nftables) and
  the build names Service, Deployment and pod; each pod is its
  own `pod-<uid>` host, all on one `boot_id`; the debug
  containers carry exactly #13's capabilities under
  `RuntimeDefault` seccomp; the pod's deletion ends the tracer on
  SIGTERM. The new `ingress` scenario (gateway → Traefik →
  inventory, golden and `routes.yaml` in
  `workload/scenarios/ingress`), its backend under a sidecar and
  its gateway under a debug container, renders as one routed
  edge gateway → `shop.iormesh.test` → the Ingress → Service →
  Deployment → the backend's pod (since ff4) in the summary,
  `model.json` and `graph.svg`, from
  the routes `routes discover` found with the operator's admin
  context; a list-only ServiceAccount finds the same two routes.
  The workaround (cf4): the gateway's HTTP host comes from the
  Java agent, and step 3 of docs/implementation.md M7 (#14's
  staging inside the JVM's root) was not built, so the test
  mounted the trace emptyDir into the app container and shipped
  the agent jar in the workload image. cf4 built the staging and
  dropped the workaround; the re-run on its tracer passed every
  subtest, the ingress routed edge included (the staged agent
  reported the FQDN). The two compensations stood in for product
  gaps the run found — debug deploys carried no byte cap and
  tripped the percent disk limit on a full node (df4:
  `-disk-limit=0 -disk-max-bytes` through registry flags), and
  deploy returned before the tracer was attached (ef4: the test
  waited for the tracer's `attached` log line) — and a route edge
  never named a sole endpoint's pod (ff4). All three are built:
  deploy passes the pod's disk bounds and waits for the tracer's
  attach state, and a routed edge names a sole traced endpoint;
  the re-run (2026-11-21, tasks df4–ff4) with neither
  compensation passed every subtest of both tests, deploy
  reporting each tracer attached and the ingress edge ending at
  the inventory pod. Fixed on
  the way: `build -registry`, selectors skipping terminating
  pods, kubectl 1.30's `--custom` gate. The target tears down
  only what it made (`mage k8sTeardown` alone, also after an
  interrupted run; never a `docker system prune`). Of the base
  images it removes only the exact IDs a run's own steps used
  (kind's node for a cluster it created, the registry for a
  container it created, the pinned JRE for a build it ran) that
  were absent before both the first run and that run, never
  another image pulled into those repositories (task oi4).
  Against an
  existing cluster (task li4) the run refuses namespaces
  `traefik`, `iormesh-m7`, `iormesh-m7-clusterip` and
  `iormesh-m7-micro` (task uh4) that are already there and not
  its own, records the context and what it
  creates before its helm step, and the teardown removes only
  what that record names, in that context — nothing in the
  cluster without one, with one naming no context, or from a run
  refused over another context's record.
  **Reopened on el9** (2026-11-21, task zg4): accepted on kind
  only. On a Rocky 9.9 k3s node (el9 5.14.0-741.22.1, SELinux
  enforcing, k3s v1.35.10, containerd 2.2.10, container-selinux
  2.250, k3s-selinux 1.6), run by `mage k8sAcceptance` against
  an existing cluster (docs/testing.md "M7 on an existing
  cluster"), #13's pod spec fails: every tracer, sidecar and
  debug container alike, dies on libbpf's first program load
  with EPERM — `avc: denied { prog_load }` for `container_t`,
  class `bpf`, #13's named trigger. With a node-local SELinux
  module granting `container_t` the tracer's `bpf`, `perf_event`
  and tracefs permissions (a diagnostic, not a fix: it widens
  every container on the node), the unchanged #13 spec passed
  every subtest of both tests — ClusterIP fused under iptables
  and nftables, the hairpin, pod identity, the ingress routed
  edge ending at the inventory pod, the agent live-attached by a
  debug container, list-only discovery, SIGTERM exit 0 in
  0.62 s. M7 stays reopened until #13 has an el9 profile
  (task 5h4) and the run passes with it and no node-local
  policy.
  **Accepted on el9** (2026-11-21, task 5h4): with #13's el9
  profile — the tracer container alone at `seLinuxOptions:
  {type: spc_t, level: s0}`, the seven capabilities,
  `RuntimeDefault` and no privilege escalation unchanged — and
  no node-local SELinux module (only the packaged `container`
  and `k3s` ones), the same node passed every subtest of both
  tests under iptables and again under nftables: ClusterIP
  fused, the hairpin, pod identity, the agent live-attached by a
  debug container (a `container_t` JVM sharing the `emptyDir`
  spool, ring and attach socket with the `spc_t` tracer), the
  ingress routed edge to the inventory pod, list-only discovery,
  SIGTERM exit 0 (0.81 s, 0.72 s). The raw audit log held no
  AVC; the app containers ran `container_t` at their pods' own
  MCS pairs; `kubectl debug --custom` took the field; the kind
  run passed with it too. A first pass with the type alone
  failed the ingress subtest (containerd gave the tracer its own
  MCS pair and relabelled the pod's `/etc/hosts` to it, #13),
  hence the level. Limits: one single-node k3s on one Rocky 9.9
  VM (containerd 2.2.10, container-selinux 2.250, k3s-selinux
  1.6, firewalld trusting the pod and Service CIDRs, run by
  `el9-k3s.sh`), kubectl 1.35, no multi-node cluster, no CRI-O,
  no OpenShift, no Pod Security level below `privileged`; the
  tracer is unconfined by SELinux there. Task 7h4's opt-in
  `iormesh_container_t` module confines it and passed the same
  acceptance on the same node, at the pods' pinned MCS pair
  (#13). #13, #14, #15 and #18 stay provisional.
  **A debug tracer ends with its app container** (2026-11-21,
  task ah4, after 8h4's el9 restart check). `kubectl debug
  --target` puts the tracer in the target container's PID
  namespace. When that container's init dies (a crash, an OOM
  kill, a liveness kill), the kernel kills the tracer with it,
  exit 137, and the kubelet never restarts an ephemeral
  container. What is lost is the unsealed segment, up to a
  rotation interval of events. A sidecar survives the same
  restart, because the pause container is init of a shared
  process namespace. **For pods that restart, use sidecar mode**
  (with `-javaagent` for their JVMs, #14). Debug mode is for
  tracing a pod that stays up. Recovery: when every debug tracer
  in the pod has terminated, deploy adds a fresh one under the
  next unique name, `iormesh-2` up to `iormesh-8`, since
  ephemeral container names can never be reused or removed. Past
  eight it refuses and points to sidecar mode or a pod restart.
  It never starts one while an earlier tracer runs, waits or has
  no status yet, so the pod keeps one tracer. The new tracer gets
  the same pinned `--image`, disk bounds and attach wait as the
  first. Until the new run writes its own `state.json`, the
  predecessor's stays in the shared project dir, and so it does
  for a deploy rerun that leaves an `iormesh-N` (N > 1) running.
  `iormesh ready -state` ends its `running`, `ended` and `dead`
  answers with the session's `started=` time (task eh4). The
  wait compares it with the new container's
  `running.startedAt` from one pod read. A state that started
  more than a 2 s skew window before the container is the
  predecessor's, and the wait waits past it. Any later state is
  the new run's own: `running` is attached, and `ended` or
  `dead` fails at once. Both instants come from the same node's
  clock; the window covers the status's whole seconds. The time
  is parsed strictly (RFC3339 or unix ns, 2020 to 2200), since
  the pod writes it. Without a time to compare (an older image,
  a failed pod read), the fallback is the first rule: the
  predecessor's `dead` or `ended` is pending, and an answer that
  changes from the first one seen fails at once. A new run that
  fails before attaching writes no `state.json` and lingers
  (`-linger`), so the wait still times out naming its logs.
  Collect and every exec name the pod's newest tracer. The trace
  `emptyDir` is the pod's, so the new tracer's `iormesh ready`
  serves the segments its predecessors published, collected into
  the same `pod-<uid>` directory: one host (task 7f4), one
  session per tracer. Fake-kubectl tested, and checked on kind
  (2026-11-21, `mage k8sDebugRestart`). The tracer ended with
  exit 137 when the app container's init was SIGKILLed. Deploy
  started `iormesh-2`, and a rerun during its attach left it
  alone with the same session. Collect brought both sessions'
  segments into one `pod-<uid>`
  ([testing.md](testing.md#debug-tracer-replacement-on-kind-task-ah4-2026-11-21)).
  **Microservices as pods** (task uh4, owner-approved
  2026-11-22): M0's microservices scenario "runs on plain hosts
  (M4/M6) and as pods (M7)", but the M7 acceptance ran tcp-nat
  and ingress only. `TestKindMicroservices` (`mage
  k8sMicroservices` alone, and part of `mage k8sAcceptance`
  since its first green run) runs gateway, orders, inventory
  and billing each in a pod of its own, one host per service,
  every pod under a debug tracer with the Java agent, one
  registry project for all. Each JVM
  starts behind the workload's startup gate, its tracer is
  deployed and joined, the certificate it pins is shipped into
  its pod (as the two-host acceptance copies it), and it is
  released, leaves first. The services dial each other's pod
  addresses at the rig spread's ports, not Services: a TLS
  server's certificate names the one address it binds. Every
  hop crosses pods, so all four fuse, gateway→inventory too.
  The run is collected and built through iormeshctl's k8s path
  and held to the same cross-service assertions as the rig's
  spread (`TestMicroservicesAcrossHosts`), shared as one
  function, `internal/meshcheck.Check`: every hop fused at its
  server's address with a mirrored 4-tuple, nothing unresolved,
  every request paired across the wire and timed, the golden
  held and its wrong variants caught. The golden is
  `expect-pods.json`, which is `expect-spread.json` with the
  gateway on a host of its own (a unit test pins that), at the
  pods' addresses. The run also checks sh4's pod fields: every
  event carries its pod's namespace and name, and a process's
  events the id of the app container, as the pod status lists
  it. It also checks pod identity. Status: accepted on kind
  2026-11-22 (`mage k8sMicroservices`: mesh, pod_fields and
  identity PASS in 152 s, scoped teardown clean); not run on
  el9 k3s.
