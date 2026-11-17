# iormesh — project plan

Working plan for iormesh, the BPF-based process observability mesh.
This file tracks scope, milestones, and open decisions. Code lands
only once a milestone is picked up; nothing here is implemented yet.

## Purpose

Map what a PID touches (files, sockets), self-replicate across the
wire to the other side of each connection, and trace Java-level code
flow inside the process — correlated into one queryable graph:

hosts ↔ PIDs ↔ sockets ↔ remote PIDs ↔ files ↔ Java method stacks.

## Guiding principles

1. **The test workload comes first.** We build a purpose-built Java
   test app to be traced, and every tracer feature is validated
   against it *before* iormesh ever attaches to a live application.
   No live app is the first test subject — not in spirit, not ever.
2. **Read-only tracing.** eBPF and the Java agent observe; they never
   modify the target's behavior or data.
3. **Propagation is opt-in and bounded.** Self-replication only
   happens against an explicit allowlist, with hop limits and
   egress rules enforced before the first packet leaves.
4. **One graph, many vantage points.** Every milestone must end with
   events landing in the same correlated dataset — never a
   per-component output format that needs re-joining later.
5. **Reuse ior's BPF/C machinery.** The code-generation pattern —
   generated tracepoint handlers, generated Go tables, diff-gated
   results, CO-RE embed, Mage + Docker generation host — is ior's,
   proven and pinned by its tests. iormesh copies/adapts it
   instead of inventing its own (details below).
6. **The trace is a document, not a dump.** Outputs follow a
   versioned, self-describing schema with stable ids and explicit
   loss accounting — precise enough for an LLM to reconstruct
   behavior and check it against source code, deterministic
   enough to diff two runs.

## Milestones

Execution order note: the controller path (M6) lands before
self-propagation (M3/M4) and Kubernetes (M7) — plain Linux
servers first. M3/M4/M7 reuse the controller's push pattern.

### M0 — Traceable test workload (Java)

A deterministic Java 21 app whose entire observable behavior we can
state in advance. It is the Java sibling of ior's `cmd/ioworkload`,
which exists to make specific syscalls fire on demand.

- Scenario-driven: each scenario emits a known sequence of file
  ops (open/read/write/rename/unlink…), socket lifecycle events
  (bind/connect/accept/send/recv, IPv4 + IPv6), and known
  method-call flows (deep stacks, virtual threads, async I/O).
- Client and server modes, so socket scenarios can run between
  two processes on loopback or two test hosts — the rehearsal
  ground for mesh propagation (M3/M4).
- An ingress scenario: client → Traefik → server in kind, with
  the route map declared alongside the expected graph (M7 proves
  the mapping against it).
- Ships with the *expected* per-scenario event graph, declared in
  the trace output schema (see below) — validation is a plain
  schema-level diff: collected vs declared. The goldens double as
  few-shot examples for LLM introspection later.
- Packaged as a plain JVM app and as a container image, so the
  same scenarios run as pods for the Kubernetes milestone (M7).
- An optional OpenTelemetry-instrumented flavor (separate
  module — the core workload stays JDK-only per decision #5)
  that emits spans with declared trace ids, so M2's OTEL
  attribution has goldens too.
- Acceptance: M1–M7 each cite which scenarios prove them.

### M1 — Trace a PID

- eBPF probes for file syscalls and socket lifecycle, per-event
  PID/TID + stacks; userspace correlator building pid→fd→path and
  pid→fd→(remote ip,port).
- Built on the ior-pattern generation pipeline (see "BPF/C code
  generation — follow ior" below); probes reuse ior's fd/path
  resolution helpers where the problems are the same.
- Full mental-model scope (see "What we trace" below): process &
  thread lifecycle (exec/fork/exit, renames), fd lifecycle (dup,
  inheritance, generation numbers), signals, local IPC (pipes,
  unix sockets, loopback TCP joined locally), zero-copy I/O paths,
  poll/epoll wait events.
- Output: trace schema v1 — NDJSON event stream + graph export.
- Acceptance: M0 goldens diff clean, including edge cases (fd
  reuse and aliasing, exec, PID reuse, threaded I/O, loopback
  pairs, socket races).

### M2 — Java agent: code-flow attribution

- JDK 21 agent via `-javaagent` or live attach; method entry/exit
  events; per-TID stack tracking (async-profiler style) so each
  syscall I/O event is annotated with the executing Java method and
  owning `Thread`.
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
- Acceptance: frames carry `class#method(file:line)`; M0's
  declared method flows show up as the annotated cause of the
  matching syscalls; virtual threads attribute correctly; the
  OTEL-flavored workload's events carry the expected
  trace/span ids.

### M3 — Mesh: single-hop propagation

- Watch the graph for new outbound external connections; deploy a
  minimal iormesh tracer to the peer host (SSH, reusing the
  iormeshctl push pattern), attach
  to the remote PID accepting that connection, report back.
- Bidirectional view: both ends of the socket in one combined
  graph, over one cross-host connection id; clocks reconciled
  (see "Trace output contract").
- Acceptance: M0 client ↔ M0 server across two test hosts; the
  graph shows both sides correlated. Guardrails (allowlist, signed
  binary, hop limit) enforced from the first hop.

### M4 — Multi-hop propagation

- Peer's peers: the remote tracer follows its own outbound
  connections, under the same allowlist and a strict hop limit /
  max-node cap; loop detection so the mesh closes instead of
  oscillating.
- Auth between iormesh nodes.
- Acceptance: a 3+ hop M0 topology fully mapped with depth limits
  demonstrably terminating propagation; correlation survives NAT
  (docker port-mapped topology).

### M5 — Views: TUI, SVG, interactive explorer

- TUI and/or JSON export rendering the full mesh: hosts, PIDs,
  sockets, files, and Java stacks, over time.
- **Deterministic SVG export**: iormesh builds SVG diagrams of the
  (sub)mesh itself — hosts as layers, processes, files, sockets
  and method frames as nodes, typed edges. Same graph in, same
  SVG out: diffable, and M0 scenarios get visual goldens.
- **Interactive explorer**: walk the generated mesh — pan/zoom,
  filter by host/PID/pod/time/method, click a node or edge,
  follow a connection across hosts, scrub time. Form factor is
  decision #11 (self-contained HTML vs local web UI vs
  TUI-first).
- Plus the per-run summary/report export — the LLM rollup of the
  whole mesh (see "Trace output contract").
- Acceptance: an operator can answer "what does this process touch,
  and what touches it back" from one screen; an M0 scenario's SVG
  matches its golden.

### M6 — Controller (`iormeshctl`): push, collect, build locally

The primary deployment and collection path for plain Linux,
landing before self-propagation (M3/M4) and Kubernetes (M7,
later phase):

- `iormeshctl` runs on the controller — a laptop or MacBook,
  Linux or macOS — with no eBPF and no cgo at all
  (cross-compiled darwin/linux, amd64/arm64; the merge and
  render packages stay cgo-free).
- **Server registry**: `iormeshctl add HOSTNAME` / `rm HOSTNAME`
  manage the list automatically (kept in the controller's
  config, e.g. `~/.config/iormesh/servers.yaml`). The registry
  doubles as the deployment allowlist (principle #3).
- **Deploy**: detect the server's arch (amd64/arm64), push the
  matching tracer build over SSH to `/opt/iormesh/bin/iormesh`
  (signature verified), and start it in the background as root
  via sudo. Runs default to a 4h auto-exit (`-duration`,
  ior-style) — no unbounded daemons on servers.
- **Trace files** land on the server as parquet under
  `/opt/iormesh/traces/<fqdn>-<session>-<start-ts>.parquet` —
  the bulk format behind the same schema as NDJSON (columnar,
  queryable, compact; ior already records parquet). Segments
  flush periodically, so a crash or kill loses minutes, not
  hours.
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
- **Collect**: iterate the registry, download the trace files,
  merge offline by stable ids — connection ids, 4-tuple joins,
  clock reconciliation — fusing the outbound side on host A
  with the inbound side on host B into single cross-host
  edges. All views (M5) then run locally on the controller.
- **Cleanup (`rm`)**: removes the host from the registry and
  tears down the deployment on it — stops the running tracer
  and removes `/opt/iormesh` (binary and traces). Trace files
  are downloaded first, so `rm` never destroys uncollected
  traces; an unreachable host warns and only drops from the
  registry.
- **No management plane**: every tracer flag and feature is
  reachable through its own CLI, and `iormeshctl` composes
  those commands over plain SSH (later `kubectl exec` in the
  k8s phase) — the tracer never needs a network socket on the
  server. Tracer flags pass through on deploy; runtime changes
  are stop-and-restart with new flags (cheap under bounded 4h
  runs); status comes from a tracer subcommand reading its
  state files. The only way into a server is the operator's
  SSH key.
- Target platform for the tracer: Rocky Linux 9 first (BTF
  present; ior's Rocky 9 build/Docker patterns apply), amd64
  and arm64. Deployed binaries come out of the container build
  pipeline (see "Container builds (Docker/Podman)").
- Acceptance: two test hosts run M0 scenarios; from a
  controller, `add` → `deploy` → `collect` → `rm` produces the
  merged mesh and its SVG with zero manual steps on the
  servers, leaves nothing behind under `/opt/iormesh`, and the
  merged mesh equals the single-host result for single-host
  scenarios. A deliberately low `-disk-limit` proves the
  watchdog: the tracer exits cleanly, the last parquet segment
  is intact, and the exit reason shows up in the summary.

### M7 — Kubernetes: sidecar & debug-container deployment

Later phase — plain Linux servers (M6) come first.

- iormesh runs as a sidecar (or `kubectl debug` ephemeral
  container) in the target pod, with the privileged security
  context eBPF needs (capability set: decision #13). It traces
  the pod's containers — and only those, by config.
- `kubectl debug` deployments are driven by `iormeshctl`, with
  the kubectl context (cluster/namespace from the kubeconfig)
  and the debug container image both configurable —
  defaulting to our published tracer image, pinned by digest.
  The concrete config shape and examples are TBD (owner to
  provide them; decision #18).
- Pod identity enrichment: map host PIDs/cgroups → pod, namespace,
  container, so the graph speaks "pod checkout-7d9f (ns prod)"
  instead of bare host PIDs.
- Java agent live-attach through the shared process namespace
  (`shareProcessNamespace: true`; attach socket reached via
  `/proc/<pid>/root`) — decision #14.
- Service-NAT correlation: kube-proxy ClusterIP DNAT/SNAT means
  the two ends of a connection observe different 4-tuples; the
  mesh must join them (extends M4's NAT work to k8s services).
- **Ingress route mapping**: an FQDN often terminates on an
  ingress (Traefik) behind a managed LB — no tracer can attach
  there, and the L7 hop breaks the 4-tuple join anyway
  (client→ingress and ingress→backend are separate streams).
  iormesh completes those edges from routing metadata: a
  configured map FQDN → ingress → service → deployment or
  statefulset, or (in k8s mode) discovered automatically from
  Ingress/IngressRoute objects, the Traefik API, and
  service→workload resolution via the k8s API. No payload
  parsing — Traefik's own configuration and logs are the source.
- Collection works with the controller tooling too —
  per-pod/per-node trace fetch (decision #12), events carrying
  pod identity.
- Acceptance: M0 client and server as pods in kind with iormesh
  attached as sidecar/debug container; goldens match with pod
  identity added; a ClusterIP connection joins both sides; the
  M0 ingress scenario renders as one routed edge — app →
  FQDN → Traefik → deployment — with no tracer on the ingress.

## Open decisions to pin down before/while implementing

1. **Remote bootstrapping transport** — settled by the
   controller design: SSH, key-based. Open sub-point: the sudo
   scope on servers — full sudo vs a pinned sudoers rule for
   exactly `/opt/iormesh/bin/iormesh` (ior precedent: scoped
   sudoers rules).
2. **Java agent attach strategy** — pre-launch `-javaagent` vs live
   attach (JDK 21 attach API). Live attach is friendlier for the
   "just trace an existing PID" story; needs a call per use case.
3. **Graph storage/backing** — in-memory only vs a small embedded DB
   (sqlite / RisingWave) so the mesh survives collector restarts.
4. **Go eBPF library** — largely settled by the "follow ior" rule
   above: adopting ior's BPF/C generation and loading pattern means
   adopting its stack too (`aquasecurity/libbpfgo`, cgo, Mage,
   Docker generation host). The only open point is mechanics:
   copy/adapt ior's `internal/generate` into iormesh (simplest;
   the code diverges over time) vs import ior's generator as a Go
   module (shared upstream, tighter coupling). Start with
   copy/adapt; revisit once iormesh's generation needs diverge.
5. **Test workload scope** — single repo or separate module? It must
   stay a plain, dependency-light Java 21 app (ideally just the
   JDK) so the thing being traced never has an excuse to behave
   differently from a real app.
6. **Java stack source** — bytecode instrumentation of
   entry/exit vs async-profiler-style sampling vs both
   (instrument for causality, sample for depth). Cost per event
   matters at syscall frequency.
7. **Event families, default-on vs opt-in** — signals, epoll
   registrations, mmaps and renames all add volume; reuse ior's
   trace-family concept and pick defaults per measured cost.
8. **Hostname & TLS visibility** — DNS hostname↔IP via Java agent
   hooks (InetAddress) is the clean path; TLS peer identity
   (trust-manager/SSLSocket hooks) is extra value at extra
   surface. Decide what v1 ships.
9. **Env capture policy** — argv is in; capturing the environment
   at exec (`/proc/<pid>/environ`) is useful for the model but is
   a privacy call to make explicitly.
10. **Java exception events** — JVMTI throw/catch hooks would
    enrich the model (error paths in source) but are hot; in,
    out, or sampled?
11. **Explorer form factor** — generated self-contained HTML
    (embeds the graph, opens anywhere, zero server) vs embedded
    local web UI (live filtering over bigger meshes) vs
    TUI-first; deterministic SVG ships regardless.
12. **Controller transport details** — SSH with the same keys as
    mesh propagation; server registry in
    `~/.config/iormesh/servers.yaml`; in k8s (later phase),
    kubectl exec/cp or a sidecar endpoint.
13. **K8s privilege profile** — full privileged SecurityContext vs
    the minimal capability set (CAP_BPF, CAP_PERFMON, and the
    legacy CAP_SYS_ADMIN/CAP_SYS_RESOURCE paths) plus BTF mounts;
    what to require and document.
14. **K8s Java attach** — live attach via the shared process
    namespace (`/proc/<pid>/root`) vs `-javaagent` injection via
    JAVA_TOOL_OPTIONS (needs deployment changes to the app).
15. **Route-map source of truth** — a static YAML map (works
    from outside any cluster) vs k8s API discovery (Ingress/
    IngressRoute objects, Traefik's API, service→workload
    resolution) vs Traefik access logs as a live source; and
    their precedence when they disagree.
16. **Trace-file segmentation** — one parquet per session vs
    time/size-rotated segments for 4h runs (rotation interval,
    download-while-running); both must keep the deterministic
    export story intact.
17. **Disk-watchdog semantics** — percent of partition vs an
    absolute free-bytes floor (or both); check interval; and
    what the controller does when a tracer exits on the limit
    (surface it, redeploy with a lower duration, or nothing).
18. **K8s debug-container config in iormeshctl** — the kubectl
    context (cluster/namespace) and the debug container image
    must both be configurable; where they live (flags, per-host
    registry entries, a k8s section in the server config)
    settles with concrete fleet examples, to be provided.
19. **Runtime control mechanics** — restart-only (stop and
    redeploy with new flags; cheapest, and 4h bounds make it
    cheap) vs signals for graceful stop/flush vs a watched
    control file for live flag changes; all socket-free by
    design.
20. **OTEL integration mode** — agent-read span context (primary,
    socket-free) vs ingesting the app's OTLP stream (a local
    receiver the app dual-exports to, or the collector's file
    export) vs both; and whether the views render a distributed-
    trace overlay on the mesh. Ingested spans are the app's own
    exported telemetry — deliberately distinct from the
    no-payload-capture rule, worth stating in docs/schema.md.

## What we trace — the mental-model inventory

Metadata only — nothing below is payload (see non-goals):

- **Identity & lifecycle**: exec (exe, argv), fork/clone, thread
  create, exit, comm/thread renames; PID incarnation ids (pid +
  boot-time start time) so a reused PID is never confused. *Why:
  the model needs a stable "who", including parentage and
  lifetimes.*
- **Filesystem**: open/close, read/write/pread, stat family,
  rename/unlink/mkdir, file mmaps (which JARs/libs/data files are
  mapped), zero-copy paths (sendfile/splice/copy_file_range).
  *Why: what data is touched — with no holes where read/write
  never fires* (ior's scenarios already cover the zero-copy
  family).
- **Descriptor lifecycle**: dup/dup2/fcntl(F_DUPFD), fd inheritance
  across fork/exec, per-fd generation numbers. *Why: aliasing and
  reuse are the classic mis-attribution bugs.*
- **Network**: TCP socket lifecycle with full 4-tuples, UDP
  per-packet peers, poll/epoll wait registrations, DNS
  hostname↔IP mappings (agent hooks — decision #8), optional TLS
  peer identity. *Why: who talks to whom — by hostname, not just
  IP — and which threads block on what.*
- **Local IPC**: pipes (both ends), unix sockets (peer PID where
  the kernel exposes it), socketpair, eventfd, shared memory;
  loopback TCP pairs joined locally by the correlator. *Why:
  single-host edges a "mesh" would otherwise miss entirely.*
- **Signals**: delivery with sender PID. *Why: "who touched the
  process" includes the supervisor's SIGTERM.*
- **Java**: class loading (class↔JAR/classloader), thread lifecycle
  + names (kernel TID ↔ Java thread), method entry/exit with
  `class#method(file:line)` when debug info is present,
  virtual-thread continuation attribution, and the active OTEL
  span (trace/span ids) when the app is OpenTelemetry-
  instrumented. *Why: the source-level
  truth everything above gets annotated with.*

## Trace output contract — built for LLM introspection

Outputs are a documented contract, designed so an LLM (or human)
can reconstruct behavior and check it against source code:

- **Schema, versioned & self-describing**: NDJSON event stream,
  graph export (nodes + typed edges), per-run summary rollup.
  Documented in-repo (`docs/schema.md` once code starts).
- **Two serializations, one schema**: NDJSON is the live/golden
  format; parquet is the bulk trace-file format on servers —
  columnar, compact, queryable (ior already records parquet;
  reuse the writer). Same envelope either way.
- **Event envelope (v1 sketch)**: `schema_version, seq, boot_ns,
  wall_ns, host_id, pid, pid_incarnation, tid, comm, event, fd,
  fd_gen, ret, err, latency_ns, bytes, args (path / 4-tuple / …),
  stack_id, java_stack (method ids), otel (trace_id, span_id —
  present when the app is OTEL-instrumented)`.
- **Method table**: `method_id, class, method, signature,
  source_file, line, jar, classloader`. Frames reference ids, so
  stacks stay compact and every frame joins to its JAR.
- **Stable ids**: PID incarnation, fd generation, connection id
  (per socket pair, assigned by the collector, shared across mesh
  hosts), method ids.
- **Ordering & clocks**: per-host sequence numbers, per-thread
  monotonic order, boot + wall clocks per host, explicit
  cross-host clock-reconciliation records; ring-buffer drops are
  explicit events — gaps are never silent.
- **Explicit end**: every trace records how it ended — duration
  reached, disk limit reached, controller stop, or error — as a
  final event and in the summary rollup; an LLM reading the
  trace always knows whether it is complete.
- **Deterministic exports**: canonical key order + stable ids —
  two runs of the same scenario diff to near-nothing, so run
  comparison (LLM or human) is meaningful.
- **Deterministic SVG**: fixed layout rules, no randomness — the
  diagram of a given graph is byte-stable, so M0 gets visual
  goldens.
- **Pod-aware identity**: under Kubernetes, the envelope gains
  pod/namespace/container (cgroup-mapped); the graph treats pod
  identity as a first-class node type.
- **Proxy-hop edges**: routes through an ingress render as
  synthetic `routed_via` edges and introduce service + workload
  (deployment/statefulset) node types — FQDN → ingress →
  service → workload completes the graph across proxies no
  tracer can run on.
- **OTEL correlation**: trace/span ids in the envelope make
  every event joinable with the app's distributed traces; an
  LLM can cross-reference the mesh with span timelines and
  instrumented code paths.
- **Summary rollup**: per-process entry point — config files read,
  connections with hostnames, thread table, top methods by I/O —
  cheap context before diving into the event stream.
- **Goldens use the schema**: M0's expected graphs are schema
  documents; validation is a plain diff, and the goldens double
  as few-shot examples for introspecting real runs.

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
  kept in-tree and deliberately preserved by clean targets.
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
| eBPF probes      | C (CO-RE)| file/net syscall tracepoints, per-event stacks  |
| Correlator       | Go       | ring buffer consumer, fd/inode joins, graph     |
| Mesh agent       | Go       | egress watch, remote deploy, allowlist rules    |
| Java agent       | Java 21  | method entry/exit, TID→stack, OTEL span ids |
| Test workload    | Java 21  | deterministic traceable scenarios (M0)         |
| Collector/map    | Go       | ring buffer → append-only log, dedup, graph, export |
| Output contract  | versioned JSON + parquet | NDJSON live/golden, parquet bulk files, graph, summary |
| Views            | Go + SVG | deterministic SVG export, interactive explorer, TUI |
| Controller (iormeshctl) | Go, cgo-free | registry, deploy, collect, rm cleanup, local SVG |
| K8s mode         | manifests | sidecar/debug-container deploy, pod identity |
| Route map        | YAML + k8s API | FQDN→ingress→service/workload edges |

## Suggested repo layout (when code starts)

```
cmd/iormesh/          # tracer binary (Linux, eBPF loader +
                      #   correlator); runs on the targets
cmd/iormeshctl/       # controller (Linux/macOS, no eBPF, no cgo):
                      #   add|rm registry, deploy, collect, svg
cmd/meshd/            # mesh agent + collector (or same binary, TBD)
cmd/testworkload/     # wrapper for the Java test app scenarios
internal/c/           # BPF C sources: ior-style helpers +
                      #   generated_tracepoints.c + vmlinux.h +
                      #   committed generation result (diff gate)
internal/generate/    # tracepoint/type generators (Go, ior pattern)
internal/tracepoints/ # generated Go tracepoint tables
internal/types/       # generated C-derived Go types
internal/correlate/   # event joins, pid/fd/path graphs
internal/federate/    # server registry, trace download, offline
                      #   merge (cgo-free — shared with iormeshctl)
internal/render/      # deterministic SVG diagram generation
internal/explore/     # interactive explorer (self-contained HTML/JS)
internal/k8s/         # cgroup→pod identity mapping, k8s metadata
internal/routemap/    # FQDN→ingress→workload map (config + k8s discovery)
internal/mesh/        # propagation logic, guardrails
internal/bpfembed.go  # go:embed of the compiled .bpf.o + env override
agent/                # Java agent source (M2)
workload/             # traceable test app source (M0) + container image
deploy/               # k8s manifests: sidecar patch, debug container
Magefile.go           # generate/build/test targets, ior-style
scripts/              # container builds (docker/podman), ior-style
docs/                 # design notes as they emerge
```

(Layout mirrors ior's generation tree so the pattern can be lifted
with minimal renaming; exact shapes settle when M0/M1 start.)

## Non-goals (for now)

- Modifying target behavior (writes, injection, parameter edits).
- Kernel-mode dedup/ aggregation — correlation happens in userspace.
- Non-Linux hosts — BPF + JDK 21 on Linux is the whole universe.
- No payload capture: file/socket contents are never recorded —
  paths, sizes, offsets, endpoints only. (Argv is metadata; env
  capture is decision #9; anything deeper is out.)
- No Java method-argument capture: stacks and lines suffice to
  match source; args add cost and secrets risk.
- No GC/JIT events — I/O attribution only. (Exception events are
  decision #10.)
- No Kubernetes operator or auto-injection webhook — the
  deployment owner adds the sidecar explicitly, by patch.
- No L7 packet parsing to recover hostnames or routes — TLS SNI
  and HTTP Host headers are never sniffed from payloads; routing
  knowledge comes from DNS answers, the configured route map,
  the k8s API, and Traefik's own configuration/logs.
- Tracing the mesh itself into the mesh (that's what M4 loop
  detection is *for*, but the mesh agent's own control-plane traffic
  is out of the traced set).
