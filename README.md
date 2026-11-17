# iormesh

<img src="assets/logo.svg" alt="iormesh logo" width="150" />

BPF-based process observability mesh: map what a PID touches (files,
sockets), self-replicate across the wire to the other side of each
connection, and trace Java-level code flow inside the process.

Companion project to [ior](https://github.com/snonux/ior). Where `ior`
answers *"what is this process doing to its I/O right now?"*, iormesh
answers *"what does this process touch — and what touches it back?"* —
across hosts, not just one.

**Status: early phase — PoC.** This project is a proof of
concept in the making: the repo currently holds the project
plan ([PLAN.md](./PLAN.md)), this README, and the logo.
Everything described here — flags, paths, schema, milestones —
is subject to change as the PoC evolves.

## Goals

Build a live "connectome" of a process's interaction with the outside
world, from multiple vantage points:

1. **Full I/O map per PID** — every file read/write/open and every
   network socket with resolved IPs and ports — plus the HTTP
   requests riding them (method, URI, headers, status).
2. **Self-propagating mesh** — when an *outbound* external connection
   is detected, iormesh deploys a lightweight tracer on the remote
   host, attached to the process accepting that connection. The mesh
   grows organically, host by host, node by node.
3. **Java code-flow instrumentation** (JDK 21) — attribute I/O events
   to the method stack that caused them, so you see *which call
   triggered which syscall* — and, when the app runs
   OpenTelemetry, which span.
4. **Correlated, queryable map** — one dataset tying hosts, PIDs,
   sockets, files, and Java call stacks together over time.
5. **LLM-introspectable trace outputs** — a stable, self-describing
   output contract: an LLM (or human) can reconstruct what the app
   did and verify it against the source code.
6. **Rendered mesh** — iormesh builds SVG diagrams of the mesh it
   generates, plus an interactive explorer to walk it.
7. **Controller-driven operation** — `iormeshctl` runs on any
   machine without eBPF (Linux or macOS), manages the server
   list, pushes tracers to plain Linux servers, and builds the
   complete mesh — including its SVG — locally.

Nothing gets instrumented "in production" before it has been proven
against iormesh's own traceable test workload (see below) — no live
app is ever the first test subject.

## Architecture

```
┌─────────────────────────── host A ───────────────────────────┐
│  target process (PID, optionally JVM 21)                     │
│      │                                                       │
│      ├── eBPF probes ──────────► userspace correlator        │
│      │    - file: open/read/write/poll/statx                  │
│      │    - net:  socket/bind/connect/accept/send/recv        │
│      │      with peer IP + ports (inet/ipv6)                  │
│      │    - exits: per-event → PID/TID + stack (user+bpf)     │
│      │                                                       │
│      ├── Java agent (JDK 21) ─► userspace correlator          │
│      │    - bytecode instrumentation of method entry/exit      │
│      │    - async-profiler-style TID→stack sampling            │
│      │                                                       │
│      └── mesh agent ◄── propagates on detected egress:        │
│           detects outbound connect to external endpoint →     │
│           ships the tracer to the peer (SSH, iormeshctl      │
│           push pattern), attaches to the remote PID, and    │
│           reports back                                       │
└──────────────────────────────────────────────────────────────┘
                │
                ▼
        central collector / storage (per-event ring buffer,
        dedup, graph construction: pid ↔ socket ↔ remote pid)
```

### Components

- **eBPF layer (Go)**
  - Tracepoints / kprobes for file syscalls and tcp/udp socket
    lifecycle; sockops/tracepoints for per-packet metadata as needed.
  - Per-PID TID correlation; maps: pid→fd→(remote ip,port), pid→fd→path.
- **Userspace correlator (Go)**
  - Consumes the BPF ring buffer, resolves inode→path, joins events,
    builds the per-PID I/O graph.
- **Mesh agent**
  - Watches the graph for new *outbound external* connections.
  - Boots a minimal iormesh tracer on the peer host, scoped to the
    remote PID of that socket. Bidirectional: both directions of
    the socket land in one combined view.
- **Java instrumenter (JDK 21)**
  - Loaded via `-javaagent` (or attach API for live processes).
  - Method entry/exit events; per-TID stack tracking (async-profiler
    style) so any syscall I/O event can be annotated with the
    executing Java method + owning `Thread`.
  - OpenTelemetry-aware: when the app runs the OTEL SDK, the
    agent reads the active span context and tags every I/O
    event with its OTEL trace/span ids.
  - HTTP/HTTPS instrumentation: hooks at known client/server API
    boundaries record method, URI, headers (redacted), status
    and latency — joined to the socket, the stack and the span.
- **Traceable test workload (Java)**
  - A purpose-built Java app — never a live app — that iormesh is
    developed and validated against first. It runs scripted,
    deterministic scenarios (file ops, loopback/TCP traffic,
    known method-call flows) whose expected event graph we can state
    in advance, so every tracer feature can be checked end-to-end
    before it ever attaches to anything real. This is the Java
    sibling of ior's `cmd/ioworkload`.
- **Collector / map**
  - Single source of truth; owns the trace output contract (events,
    graph, summary — see below).
- **Views: SVG + interactive explorer**
  - iormesh itself builds SVG diagrams of the mesh it generates —
    deterministic for a given graph, so diagrams diff and golden
    like data exports.
  - Interactive explorer over the collected graph: filter by
    host/PID/pod/time/method, click a node or edge, follow a
    connection across hosts (form factor TBD — see PLAN.md).
- **Controller (`iormeshctl`)**
  - Runs on the controller — a laptop or MacBook, Linux or macOS —
    with no eBPF at all. Keeps the server list automatically
    (`iormeshctl add|rm HOSTNAME`), pushes the matching Linux
    build (amd64/arm64) to Rocky 9 servers as
    `/opt/iormesh/bin/iormesh`, starts it in the background as
    root via sudo (4h auto-exit and a disk-space watchdog by
    default), then downloads the
    parquet traces and builds the mesh SVG locally.
  - Project-aware: unrelated apps on the same server trace as
    separate projects — every trace lives under
    `/opt/iormesh/traces/<project>/`. Collect can merge
    projects, and when one app talks to another over TCP, the
    mesh fuses both sides into one cross-project picture.
  - Every tracer flag and feature is controllable through
    iormeshctl — composed over plain SSH (and `kubectl exec` in
    the k8s phase), never over network sockets.
  - `rm` also cleans up the server: it stops the tracer and
    removes `/opt/iormesh` — trace files are downloaded first,
    so nothing is lost.
- **Kubernetes sidecar / debug container**
  - Runs as a sidecar (or `kubectl debug` ephemeral container) in
    the target pod with a privileged security context for eBPF;
    enriches every event with pod/namespace/container identity;
    live-attaches the Java agent through the shared process
    namespace.
  - Later phase, launched by `iormeshctl` with the kubectl
    context and the debug container image configurable.
- **Route map: proxy-hop resolution**
  - A DNS name often terminates on an ingress (e.g. Traefik)
    behind a managed LB — no tracer can run there, so the far
    end of the edge is unidentifiable. iormesh takes a mapping
    FQDN → ingress → service → deployment/statefulset (or
    discovers it from Ingress/IngressRoute objects and
    Traefik's API in k8s mode) and completes the edge: app →
    FQDN → ingress → workload, with no tracer on the ingress.

## Deployment modes

- **Standalone** — `sudo iormesh` on a host; traces that host.
- **Controller-managed (primary)** — `iormeshctl` on any laptop
  (Linux or macOS) manages the server list, pushes and starts
  tracers over SSH (`add` enrolls, `rm` tears down and removes
  `/opt/iormesh` on the host), pulls the parquet traces, and
  renders the mesh locally. Plain Linux servers first;
  Kubernetes sidecar push is a later phase.
- **Kubernetes (later phase)** — sidecar or `iormeshctl`-driven
  `kubectl debug` container (context and image configurable)
  next to the app; pod-aware identity.
- **Self-propagating mesh** — tracers deploy themselves to
  connection peers under the allowlist and hop limits (M3/M4).

## What we trace

A mental model of an app needs more than file and socket events:

- **Identity & lifecycle** — exec (exe, argv), fork/clone, thread
  create/exit, thread renames; PID incarnation ids so a reused
  PID is never confused with the old one. Sessions bound to a
  process-name pattern auto-detect restarts and follow the app
  to its new PID. *Who exists, spawned by
  whom, for how long.*
- **Filesystem** — open/read/write/close, stat family,
  rename/unlink/mkdir, file mmaps (which JARs, libs, data files),
  zero-copy paths (sendfile/splice/copy_file_range) so the file
  map has no holes. *What data is touched.*
- **Descriptor lifecycle** — dup/dup2/fcntl(F_DUPFD), fd
  inheritance across fork/exec, per-fd generation numbers.
  *Which fd is really which file/socket after aliasing or reuse.*
- **Network** — TCP lifecycle with full 4-tuples, UDP per-packet
  peers, poll/epoll wait registrations, DNS hostname↔IP maps,
  optional TLS peer identity, and ingress route maps for proxy
  hops that hide the peer. *Who talks to whom, under what
  name — and which threads wait on what.*
- **HTTP/HTTPS** — method, scheme, host, path/URI, status,
  sizes, latency per request — captured at the Java HTTP
  client/server API boundary by the agent (so HTTPS included,
  nothing sniffed off the wire), sensitive headers and query
  params redacted by policy. *What is actually said, at the
  level the mesh routes on.*
- **Local IPC** — pipes, unix sockets (peer PID where the kernel
  exposes it), eventfd, shared memory; loopback TCP pairs joined
  locally. *Single-host edges a "mesh" would otherwise miss.*
- **Signals** — delivery with sender PID. *Who touched the
  process — e.g. which supervisor sent the SIGTERM.*
- **Java** — class↔JAR map, thread names (kernel TID ↔ Java
  thread), method entry/exit as `class#method(file:line)` when
  debug info is present, virtual-thread attribution, and the
  active OTEL span (trace/span ids) when the app is
  OpenTelemetry-instrumented. *The
  source-level truth every event above is annotated with.*

## Trace output — built for LLM introspection

Outputs are a stable, versioned contract — not an internal dump:

- **Self-describing schema** — NDJSON event stream, graph export
  (typed nodes + edges), per-run summary rollup; documented
  in-repo.
- **Parquet bulk files** — the same schema serialized columnar
  for on-server trace files (queryable, compact — ior already
  records parquet); NDJSON stays the live/golden format.
- **Stable identifiers** — PID incarnation, fd generation,
  cross-host connection ids, a deduplicated method table — two
  runs of the same scenario diff to almost nothing.
- **App identity** — pattern-bound sessions link every PID
  incarnation of the app into one node: a restart shows up as
  the same app continuing, not an unrelated new PID.
- **Ordering you can trust** — per-host sequence numbers,
  per-thread monotonic order, boot + wall clocks, cross-host
  reconciliation records; ring-buffer drops are explicit events,
  never silent gaps.
- **Explicit end** — every trace records how it ended (duration,
  disk limit, stop, error), so a reader — LLM or human — knows
  whether it is complete.
- **Source-matching frames** — `class#method(file:line)` plus the
  class's JAR origin, directly comparable with source code.
- **Deterministic exports** — canonical key order, so identical
  scenarios produce byte-identical output modulo timestamps.
- **Deterministic SVG too** — the diagram export is stable for a
  given graph, so it diffs and goldens like the data exports.
- **Pod-aware identity** — under Kubernetes, events carry
  pod/namespace/container alongside host PID identity.
- **Project-scoped** — every session belongs to a named project;
  traces live under `/opt/iormesh/traces/<project>/`, and
  cross-project connections render as explicit edges in the
  merged mesh.
- **Proxy-hop edges** — routes through an ingress render as
  synthetic edges (FQDN → ingress → service → workload), so the
  graph completes even where no tracer can run.
- **OTEL-correlated** — events carry the active OpenTelemetry
  trace/span ids when the app is instrumented, so the mesh joins
  with any tracing backend; spans can be ingested (opt-in) to
  overlay the distributed service graph.
- **HTTP request events** — method, scheme, host, path/URI,
  status and sizes as first-class events, correlated with the
  connection, the method stack and the OTEL span; sensitive
  headers and query params are redacted before recording.
- **Goldens in the same schema** — M0's expected graphs are
  schema documents; validation is a plain diff, and the goldens
  double as few-shot examples for introspecting real runs.

## Testing strategy

The test workload comes *first*, not last:

- Build the workload app before (or alongside) the tracer, so every
  milestone lands against a known target.
- Each scenario declares the exact I/O and method-flow it produces;
  validation = diff the collected graph against that expectation.
- The app has client and server modes so single-hop and multi-hop
  mesh propagation can be rehearsed on loopback or between two test
  VMs/containers — without going near anything live.
- Only once a milestone passes against the workload is it considered
  ready for a live process.
- Expected graphs are declared in the same trace schema as real
  output, so validation is a plain machine diff — and the goldens
  double as few-shot examples for LLM introspection.
- Deterministic SVG gives M0 visual goldens; the controller flow
  (add → deploy → collect → rm) is rehearsed against M0 on two
  test hosts, and its pull-merged mesh must equal M3's propagated
  result; the k8s mode rehearses the same scenarios as pods in
  kind.

## MVP milestones

- [ ] **M0**: traceable test workload — Java app with deterministic
      file/socket/method-flow scenarios, each shipping its expected
      event graph in the trace schema (goldens; the target
      everything else is validated against).
- [ ] **M1**: trace a PID — or a process-name pattern that
      follows the app across restarts to new PIDs — process/fd
      lifecycle, all file ops (incl. zero-copy), all sockets,
      local IPC, signals → versioned NDJSON + graph JSON.
      Validated by diff against M0 goldens.
- [ ] **M2**: Java agent: attribute syscalls to `class#method(file:line)`
      stacks (JDK 21); class/JAR map, thread names, hostname↔IP
      resolution, OTEL span attribution, HTTP/HTTPS request
      events (URI + redacted headers) when instrumented.
      Checked against M0's known call flows.
- [ ] **M3**: mesh: detect external outbound connection, deploy
      tracer to peer, correlate both sides under one connection id
      (M0 client ↔ M0 server); cross-host clock reconciliation.
- [ ] **M4**: multi-hop propagation (peer's peers), loop/depth
      control, auth, NAT-proof correlation.
- [ ] **M5**: views — deterministic SVG diagram export,
      interactive mesh explorer, TUI, summary export (the LLM
      rollup).
- [ ] **M6**: controller (`iormeshctl`) — server registry
      (`add|rm`, with host cleanup of `/opt/iormesh`), SSH push
      of amd64/arm64 builds to Rocky 9 servers, sudo background
      start with 4h auto-exit and disk-space watchdog, parquet
      trace files in project folders (configurable trace dir),
      cross-project merge, download + local mesh SVG; must match
      M3's propagated result.
- [ ] **M7** (later phase): Kubernetes — sidecar/debug-container
      deployment, iormeshctl-driven with configurable kubectl
      context and debug image, pod-identity enrichment, Java
      live-attach, ClusterIP NAT correlation, ingress route
      mapping (FQDN → Traefik ingress → deployment/statefulset);
      M0 client ↔ server as pods in kind.

## Security & scope

- Privileged (root) for eBPF; read-only tracing only — no tampering.
  - Remote deployment needs an explicit allowlist of hosts/destinations
    and a signed tracer binary.
- Self-replication guardrails: hop limit, max concurrent nodes,
  domain egress rules. This is the part that makes it a *mesh* — and
  the part that must be tightly controlled.
- Metadata-only tracing: no file or socket payload contents are
  recorded — paths, sizes, offsets, endpoints, stacks. The one
  deliberate exception: HTTP(S) request metadata (method, URI,
  headers), captured at the Java API boundary — never sniffed
  off the wire — with sensitive headers and query params
  redacted by policy. (Argv is metadata; env capture is a policy
  decision, see PLAN.md.)
- Kubernetes: eBPF privileges are node-kernel-wide — a sidecar
  with CAP_BPF can in principle observe any process on the node.
  iormesh scopes itself by config to the pod it runs in, is added
  only by the deployment owner's explicit patch, and its
  container image is signed like the tracer binary.
- No management sockets: the tracer listens on nothing — control
  flows only through SSH (or kubectl exec), i.e. the operator's
  key.
- The test workload keeps guardrails honest too: propagation rules
  are rehearsed in a sandbox before they exist in the wild.

## Build & code generation

iormesh follows ior's BPF/C generation pattern wholesale, so nothing
below gets invented twice:

- Tracepoint discovery from the build host's kernel → generated C
  enter/exit handlers, generated Go tracepoint tables and
  C-derived Go types — all from a unit-tested `internal/generate`
  package, driven by Mage targets.
- Generated artifacts are committed, with a committed result file as
  the diff gate pinning the kernel tracepoint set: regenerating on a
  different kernel shows up as a diff and stops the build.
- `vmlinux.h` from BTF, clang CO-RE compile, the `.bpf.o` embedded
  into the static binary via `go:embed` (env override to load an
  external object for testing).
- Container builds with Docker **or Podman**: the container pins
  the build environment; the static binary is
  copyable to any BTF host, and tracepoints missing there are
  skipped at runtime. The tracer builds for linux/amd64 + arm64
  from its container; the cgo-free controller cross-compiles
  darwin/linux for both arches from the same pipeline.

Where ior already solved a problem — fd→path resolution in BPF,
syscall enter/exit pairing and restart edge cases, thread/TID
handling, ring buffer consumption, safe libbpf link teardown —
iormesh reuses that approach and code rather than re-deriving it.

## Tech choices

- Go + libbpfgo + eBPF (CO-RE) — the same stack as ior, adopted
  along with its code-generation pattern (see above).
- Java 21 agent; async-profiler-style frame walking.
- Controller (`iormeshctl`): pure Go, no cgo — cross-compiled
  for Linux and macOS; tracer: linux/amd64 + linux/arm64 with
  the embedded CO-RE object.
- Storage: in-memory ring buffer → parquet trace files on
  servers (ior's parquet pattern); graph + summary exports.
