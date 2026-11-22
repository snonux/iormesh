# iormesh

<img src="assets/logo.svg" alt="iormesh logo" width="150" />

BPF-based process observability mesh: map what a PID touches (files,
sockets), follow each connection across the wire to its other side,
and trace Java-level code flow inside the process.

Companion project to [ior](https://github.com/snonux/ior). Where `ior`
answers *"what is this process doing to its I/O right now?"*, iormesh
answers *"what does this process touch — and what touches it back?"* —
across hosts, not just one.

**Status: proof of concept.** Milestones M0, M1, M2, M3, M4, M6
and M7 are accepted; M5 (views, query and the LLM eval) is built
but not accepted until its eval has run with a real model (see
[PLAN.md](./PLAN.md)). Rocky Linux 9.9 (el9, kernel 5.14) is the
main target. The repo holds the tracer (`iormesh`), the
controller (`iormeshctl`), the Java agent and test workload, the
plan and the design docs ([docs/](./docs/)). Flags, paths and
schema may still change as the PoC evolves.

## Goals

1. **Full I/O map per PID** — every file op and every network
   socket with resolved IPs and ports, plus the HTTP requests
   riding them (method, URI, headers, status).
2. **A mesh that grows along its edges** — when a connection to
   an untraced host is observed, `iormeshctl` can deploy a
   tracer there, attached to the process accepting that
   connection — from the controller, under an allowlist, signed
   binaries and hop limits. Tracers never deploy themselves.
3. **Java code-flow instrumentation** (JDK 17+) — attribute I/O
   events to the method stack that caused them, and to the
   OpenTelemetry span when the app is instrumented.
4. **Correlated, queryable map** — hosts, PIDs, sockets, files
   and Java call stacks in one dataset over time. The end-goal:
   the full picture of how every service talks to every other
   service — in sequence, down to the driving Java threads.
5. **LLM-introspectable trace outputs** — a stable,
   self-describing contract an LLM (or human) can reconstruct
   behavior from and verify against source code.
6. **Rendered mesh** — deterministic SVG and sequence diagrams
   of the mesh, plus an interactive explorer to walk it.
7. **Controller-driven operation** — `iormeshctl` runs on any
   laptop (Linux or macOS, no eBPF), manages the server list,
   pushes tracers over SSH, collects traces over rsync, and
   builds the complete mesh locally.

Nothing gets instrumented "in production" before it has been
proven against iormesh's own traceable test workload — no live
app is ever the first test subject.

## Deployment modes

- **Standalone** — `sudo iormesh run -project NAME (-pid PID |
  -pattern GLOB | -listen-port N)` on a host; traces the matched
  processes there.
- **Controller-managed (primary)** — `iormeshctl` on any laptop
  (Linux or macOS) manages the server list, pushes and starts
  tracers over SSH (`add` enrolls, `rm` tears down and removes
  `/opt/iormesh` on the host), collects the traces and renders
  the mesh locally.
- **Kubernetes (M7)** — sidecar or `iormeshctl`-driven
  `kubectl debug` container (context and image configurable)
  next to the app; each pod is traced as its own host
  (`pod-<uid>`), and every event names its namespace, pod and
  container.
- **Mesh expansion** — `iormeshctl expand` follows observed
  connections to untraced peers and deploys tracers there, under
  the allowlist and hop limits (M6). Driven by the controller;
  servers hold no credentials.

## Security in brief

- Read-only tracing, metadata only — no file or socket payload
  is ever recorded. One deliberate exception: HTTP(S) request
  metadata captured at the Java API boundary. One redaction
  policy covers headers, query params and argv; URI path
  segments are recorded verbatim (decision #22); the
  controller's per-path counts fold ids into templates.
- Mesh expansion only from the controller, against an explicit
  allowlist, with a signed tracer binary, a hop limit and a
  node cap. Tracers never deploy and never connect anywhere.
- No management sockets — the tracer listens on nothing; control
  flows only through SSH (or `kubectl exec`), i.e. the
  operator's key.

Details: [docs/security.md](./docs/security.md).

## Documentation

- [PLAN.md](./PLAN.md) — the working plan: scope, spikes,
  milestones M0–M7, decisions. The source of truth.
- [docs/architecture.md](./docs/architecture.md) — components
  and how they fit together, with diagrams.
- [docs/implementation.md](./docs/implementation.md) — the
  implementation guide: work packages in execution order, with
  code sketches.
- [docs/visualization.md](./docs/visualization.md) — exploring
  a consolidated mesh: local site, terminal UI, plain text.
- [docs/trace-scope.md](./docs/trace-scope.md) — what iormesh
  traces, and why each family is in the model.
- [docs/output-contract.md](./docs/output-contract.md) — the
  trace output format, built for LLM introspection.
- [docs/schema.md](./docs/schema.md) — the field-level trace
  schema: envelope, args, event kinds, graph, files.
- [docs/testing.md](./docs/testing.md) — workload-first testing
  and the ior-style integration suite.
- [docs/java-attach.md](./docs/java-attach.md) — getting the
  Java agent into a JVM: `-javaagent`, or live attach and its
  caveats (JEP 451).
- [docs/kubernetes.md](./docs/kubernetes.md) — the operator
  runbook for tracing pods: prerequisites, debug vs sidecar,
  SELinux, collecting, limits, cleanup, troubleshooting.
- [docs/security.md](./docs/security.md) — security & scope.
- [docs/build.md](./docs/build.md) — build, code generation and
  tech choices.
- [docs/spikes/s1-java-join.md](./docs/spikes/s1-java-join.md)
  — spike S1's result: the Java↔syscall join, clocks, the agent
  spool, attach mode and cost.
- [docs/spikes/s2-cross-host.md](./docs/spikes/s2-cross-host.md)
  — spike S2's result: the NAT-proof cross-host join key and
  clock bounds from the traffic.
- [docs/spikes/s3-overhead.md](./docs/spikes/s3-overhead.md)
  — spike S3's result: tracer overhead, loss and volume, and
  the budget M2 is accepted against.
