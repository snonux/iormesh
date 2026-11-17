# Architecture

How iormesh is put together. Companion to
[PLAN.md](../PLAN.md) — the plan is the source of truth; this
doc gives the narrative.

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

## Components

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
    deterministic scenarios (file ops — data and meta alike —,
    loopback/TCP traffic, known method-call flows, and a
    microservices topology of several service roles talking REST
    over plain HTTP and TLS) whose expected event graph we can
    state in advance, so every tracer feature can be checked
    end-to-end before it ever attaches to anything real. This is
    the Java sibling of ior's `cmd/ioworkload`.
- **Collector / map**
  - Single source of truth; owns the trace output contract (events,
    graph, summary — see [output-contract.md](output-contract.md)).
- **Views: SVG, sequence diagrams, interactive explorer**
  - iormesh itself builds SVG diagrams of the mesh it generates —
    deterministic for a given graph, so diagrams diff and golden
    like data exports — plus deterministic sequence diagrams:
    lifelines are services, arrows are requests in time order,
    each hop attributed to the Java thread that caused it,
    interleaved with that thread's local file I/O.
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
    default), then collects the trace segments over rsync —
    streaming, deleting each synced segment on the server — and
    builds the merged mesh, its SVG and sequence views locally.
  - Additively maintains a service-interaction database of
    app-to-app network interaction; humans name services through
    the CLI via hostname-pattern + port rules (see PLAN.md, M6).
  - Project-aware: unrelated apps on the same server trace as
    separate projects — every trace lives under
    `/opt/iormesh/traces/<project>/`. Collect can merge
    projects, and when one app talks to another over TCP, the
    mesh fuses both sides into one cross-project picture.
  - Every tracer flag and feature is controllable through
    iormeshctl — composed over plain SSH (and `kubectl exec` in
    the k8s phase), never over network sockets.
  - `rm` also cleans up the server: it stops the tracer and
    removes `/opt/iormesh` — trace files are synced off first,
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

## See also

- [Trace scope](trace-scope.md) — what is traced, and why each
  family is in the model.
- [Output contract](output-contract.md) — the trace format.
- [PLAN.md](../PLAN.md) — milestones and open decisions.
