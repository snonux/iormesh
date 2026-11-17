# Trace output — built for LLM introspection

Outputs are a stable, versioned contract — not an internal dump.
Companion to [PLAN.md](../PLAN.md) ("Trace output contract") —
the plan is the source of truth.

- **Self-describing schema** — NDJSON event stream, graph export
  (typed nodes + edges), per-run summary rollup; documented
  in-repo.
- **Parquet bulk files** — the same schema serialized columnar
  for on-server trace files: minute-rotated segments, compressed
  on finalize, collected over rsync while the session runs
  (queryable, compact — ior already records parquet); NDJSON
  stays the live/golden format.
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
- **Cross-service causality** — connection ids + per-thread
  ordering + stack attribution are enough to reconstruct which
  thread on service A caused which thread on service B, and
  which files those threads touched locally (data and meta ops
  alike), without re-inference; the sequence view renders
  exactly this.
- **Explicit end** — every trace records how it ended (duration,
  disk limit, stop, error), so a reader — LLM or human — knows
  whether it is complete.
- **Source-matching frames** — `class#method(file:line)` plus the
  class's JAR origin, directly comparable with source code.
- **Deterministic exports** — canonical key order, so identical
  scenarios produce byte-identical output modulo timestamps.
- **Deterministic SVG too** — the diagram export is stable for a
  given graph, so it diffs and goldens like the data exports;
  same for the sequence-diagram export.
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
  status and sizes as first-class events, correlated with
  the connection, the method stack and the OTEL span; sensitive
  headers and query params are redacted before recording.
- **Goldens in the same schema** — M0's expected graphs are
  schema documents; validation is a plain diff, and the goldens
  double as few-shot examples for introspecting real runs.

## See also

- [Trace scope](trace-scope.md) — what lands in these events.
- [Testing](testing.md) — how the contract is validated.
