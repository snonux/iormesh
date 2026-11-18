# Architecture

How iormesh is put together. Companion to
[PLAN.md](../PLAN.md) — the plan is the source of truth; this
doc gives the narrative.

![How iormesh works](diagrams/architecture.svg)

There is no long-lived collector and no mesh agent. Each server
runs one tracer (probes + correlator) that writes trace segments;
everything cross-host — merging, the graph, mesh expansion, the
views — happens on the controller.

## Components

- **eBPF layer (C, loaded from Go)**
  - Tracepoints for file syscalls and the socket lifecycle. TCP
    4-tuples come from the socket's state transitions
    (`sock:inet_sock_set_state`), not from syscall arguments —
    Java's non-blocking connects return before the connection
    exists — and join back to pid/fd through the sock pointer.
  - Uprobes on the JDK's native I/O functions (`libjava`,
    `libnio`, `libnet`) bracket each Java I/O call per thread
    id, so a syscall can be tied in the kernel to the native
    call that issued it — the front-runner for the Java↔syscall
    join, verified in a first experiment (PLAN.md, S1).
  - Native stack capture is opt-in and meant for non-JVM
    targets; BPF cannot unwind JIT-compiled Java frames
    reliably.
- **Correlator (Go, inside the tracer)**
  - Consumes the BPF ring buffer, resolves inode→path, keeps
    pid→fd→path and pid→fd→4-tuple, attaches the Java agent's
    stacks to the syscalls they caused (by thread id; the exact
    mechanism is spike S1's pick), and writes the parquet
    segments.
- **Java agent**
  - Loaded via `-javaagent` (primary) or the attach API for live
    processes — with JDK 21's dynamic-loading caveats, see
    PLAN.md decision #2.
  - Instruments only the JDK methods where Java hands I/O to the
    kernel and captures the Java stack there. Cost follows the
    I/O rate, not the call rate; blanket method entry/exit
    tracing is a non-goal.
  - Platform threads only in v1. I/O from virtual threads is
    traced but attributed to the carrier thread and flagged;
    naming the virtual thread is deferred (PLAN.md,
    "Deferred — not in v1").
  - Hands events to the tracer through a spool in a tracer-owned
    directory — no socket. Its own I/O is tagged and kept out of
    the target's story.
  - OpenTelemetry-aware: when the app runs the OTEL SDK, the
    agent reads the active span context and tags every I/O
    event with its OTEL trace/span ids.
  - HTTP/HTTPS instrumentation: hooks at known client/server API
    boundaries record method, URI, headers (redacted), status
    and latency — joined to the socket, the stack and the span.
  - The whole design is gated by spike S1 (PLAN.md), which
    proves the join and measures its cost first.
- **Traceable test workload (Java)**
  - A purpose-built Java app — never a live app — that iormesh is
    developed and validated against first. It runs scripted,
    deterministic scenarios (file ops — data and meta alike —,
    loopback/TCP traffic, known method-call flows, and a
    microservices topology of several service roles talking REST
    over plain HTTP and TLS). Each scenario brackets itself with
    markers and declares the events it must produce, so every
    tracer feature can be checked end-to-end before it ever
    attaches to anything real. This is the Java sibling of ior's
    `cmd/ioworkload`. The catalogue grows milestone by
    milestone.
- **Normalizer**
  - Renumbers run-specific values (PIDs, TIDs, fds, ports,
    inodes, timestamps) to symbolic ids and cuts a trace to a
    scenario's marker bracket. Golden checks, run-to-run diffs
    and visual goldens all work on normalized output.
- **Controller (`iormeshctl`)**
  - Runs on the controller — a laptop or MacBook, Linux or macOS —
    with no eBPF at all. Keeps the server list automatically
    (`iormeshctl add|rm HOSTNAME`), verifies and pushes the
    matching Linux build (amd64/arm64) to Rocky 9 servers as
    `/opt/iormesh/bin/iormesh`, and starts it with
    `sudo iormesh start`.
  - The tracer runs as a transient systemd unit: independent of
    the SSH session, capped in CPU and memory by its cgroup,
    bounded to 4h by default and by a disk-space watchdog, and
    stopped cleanly with a flush.
  - Every privileged action is a tracer subcommand (`start`,
    `stop`, `status`, `uninstall`), so one pinned sudoers rule
    covers the whole lifecycle.
  - Collects the trace segments over rsync — streaming,
    unprivileged, deleting each synced segment on the server —
    and merges them offline: both ends of a connection fuse into
    one cross-host edge (NAT-tolerant join key), and clock
    offsets between hosts are bounded from the traffic itself.
  - Additively maintains a service-interaction database of
    app-to-app network interaction; humans name services through
    the CLI via hostname-pattern + port rules (see PLAN.md, M4).
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
- **Mesh expansion (in the controller)**
  - After a collect, the merged graph holds unresolved peers:
    connections whose far end has no tracer. `iormeshctl expand`
    lists them, and with `--apply` deploys tracers to the ones
    matching the expansion allowlist, bound to the process
    listening on the observed port — round by round, under a hop
    limit and a node cap.
  - Tracers never deploy anything and never connect anywhere;
    servers hold no credentials. The mesh grows only through the
    operator's SSH key.
- **Views, query and the LLM eval**
  - iormesh itself builds SVG diagrams of the mesh it generates —
    deterministic for a given graph, so diagrams diff and golden
    like data exports — plus deterministic sequence diagrams:
    lifelines are services, arrows are requests in
    happens-before order, each hop attributed to the Java thread
    that caused it, interleaved with that thread's local file
    I/O.
  - Every `collect` ends in something to explore: a local
    static site, a Bubble Tea terminal UI and plain text, all
    rendered from one view model — see
    [visualization.md](visualization.md).
  - `iormeshctl query` slices the merged trace into size-capped
    NDJSON — what an LLM or a human actually reads — and an eval
    with known answers tests whether the outputs really are
    LLM-introspectable.
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

## Operator workflow

![Operator workflow](diagrams/workflow.svg)

Everything is driven from the laptop. `collect` repeats while a
session runs; `expand` turns the unresolved edges of one round
into the newly enrolled hosts of the next; `rm` syncs, stops and
removes. See PLAN.md, M4 and M6.

## The two joins

![Two joins make one story](diagrams/join.svg)

Two joins turn raw events into the end-goal picture. On each
host, a syscall is joined to the Java stack of the thread that
issued it. On the controller, the two hosts' views of one TCP
connection are fused into a single edge. Order across hosts is
happens-before — each thread's own order plus send→receive —
never a comparison of clocks.

## Roadmap

![Roadmap](diagrams/roadmap.svg)

The step-by-step version, with code sketches, is the
[implementation guide](implementation.md).

## See also

- [Trace scope](trace-scope.md) — what is traced, and why each
  family is in the model.
- [Output contract](output-contract.md) — the trace format.
- [Implementation guide](implementation.md) — work packages
  with code sketches.
- [PLAN.md](../PLAN.md) — milestones, spikes and decisions.

The diagrams are generated: edit `diagrams/gen.py` and run
`python3 docs/diagrams/gen.py docs/diagrams`.
