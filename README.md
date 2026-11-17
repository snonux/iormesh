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
plan ([PLAN.md](./PLAN.md)), the design docs ([docs/](./docs/)),
this README, and the logo. Everything described — flags, paths,
schema, milestones — is subject to change as the PoC evolves.

## Goals

1. **Full I/O map per PID** — every file op and every network
   socket with resolved IPs and ports, plus the HTTP requests
   riding them (method, URI, headers, status).
2. **Self-propagating mesh** — when an *outbound* external
   connection is detected, iormesh deploys a lightweight tracer
   on the remote host, attached to the process accepting that
   connection — under an allowlist, signed binaries and hop
   limits.
3. **Java code-flow instrumentation** (JDK 21) — attribute I/O
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

- **Standalone** — `sudo iormesh` on a host; traces that host.
- **Controller-managed (primary)** — `iormeshctl` on any laptop
  (Linux or macOS) manages the server list, pushes and starts
  tracers over SSH (`add` enrolls, `rm` tears down and removes
  `/opt/iormesh` on the host), collects the traces and renders
  the mesh locally. Plain Linux servers first; Kubernetes is a
  later phase.
- **Kubernetes (later phase)** — sidecar or `iormeshctl`-driven
  `kubectl debug` container (context and image configurable)
  next to the app; pod-aware identity.
- **Self-propagating mesh** — tracers deploy themselves to
  connection peers under the allowlist and hop limits (M3/M4).

## Security in brief

- Read-only tracing, metadata only — no file or socket payload
  is ever recorded. One deliberate exception: HTTP(S) request
  metadata captured at the Java API boundary, with sensitive
  headers and query params redacted by policy.
- Self-replication only against an explicit allowlist, with a
  signed tracer binary, hop limits and egress rules.
- No management sockets — the tracer listens on nothing; control
  flows only through SSH (or `kubectl exec`), i.e. the
  operator's key.

Details: [docs/security.md](./docs/security.md).

## Documentation

- [PLAN.md](./PLAN.md) — the working plan: scope, milestones
  M0–M7, open decisions. The source of truth.
- [docs/architecture.md](./docs/architecture.md) — components
  and how they fit together.
- [docs/trace-scope.md](./docs/trace-scope.md) — what iormesh
  traces, and why each family is in the model.
- [docs/output-contract.md](./docs/output-contract.md) — the
  trace output format, built for LLM introspection.
- [docs/testing.md](./docs/testing.md) — workload-first testing
  and the ior-style integration suite.
- [docs/security.md](./docs/security.md) — security & scope.
- [docs/build.md](./docs/build.md) — build, code generation and
  tech choices.
