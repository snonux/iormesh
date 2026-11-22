# Trace output — built for LLM introspection

Outputs are a stable, versioned contract — not an internal dump.
Companion to [PLAN.md](../PLAN.md) ("Trace output contract") —
the plan is the source of truth.

- **Self-describing schema** — NDJSON event stream, graph export
  (typed nodes + edges), per-run summary rollup; documented
  in-repo in [schema.md](schema.md). Schema v1, drafted in M1,
  is the cgo-free `internal/schema` package; S1 and S2 have
  settled the Java and cross-host fields. With M2's acceptance
  (2026-11-19) every field outside `http` was **frozen**: its
  name, type and meaning no longer change, only new optional
  fields and kinds are appended. M3's acceptance run
  (2026-11-20) froze what M3 added or still owned: `http`,
  `dns`, `otel`, `java.carrier` and the methods, threads,
  classes and stacks tables. All of v1 is frozen now.
  Readers are strict: a record with an unknown or mis-cased
  key, a missing or null field, another schema version or an
  unknown event kind is rejected, never half-read.
- **Parquet bulk files** — the same schema serialized columnar
  for on-server trace files: minute-rotated segments with
  parquet-internal zstd compression, so each one is directly
  queryable; collected over rsync while the session runs
  (ior already records parquet). Variable event arguments map
  to one nullable column per field. NDJSON stays the
  live/golden format. As built (task 8b4, `iormesh run
  -trace-dir`): a segment is published by an atomic rename
  into `<trace-dir>/<project>/ready/` as
  `<fqdn>-<session>-<start-ts>-<seq>.parquet` plus a
  `.methods`, `.threads`, `.classes` or `.stacks` table file
  for every table that got rows meanwhile (tables renamed
  first); a session's tables are the union over its segments.
  Each footer repeats project, FQDN, session, seq and start;
  the column mapping is in
  [schema.md](schema.md#parquet-mapping).
- **Stable identifiers** — PID incarnation, fd generation,
  cross-host connection ids, a deduplicated method table.
- **Normalization** — raw traces are full of run-specific
  values (PIDs, TIDs, fds, ephemeral ports, inodes,
  timestamps). The normalizer renumbers them to symbolic ids
  in order of first appearance and cuts the trace to a
  scenario's marker bracket; golden checks and run-to-run
  diffs operate on normalized output.
- **App identity** — pattern-bound sessions link every PID
  incarnation of the app into one node: a restart shows up as
  the same app continuing, not an unrelated new PID.
- **Ordering you can trust** — per-host sequence numbers,
  per-thread monotonic order, boot + wall clocks. Cross-host
  order is happens-before — per-thread order plus send→receive
  edges — never a comparison of raw timestamps; the merge
  derives a clock-offset bound per host pair from the fused
  connections themselves and records it. Ring-buffer drops are
  explicit events, never silent gaps.
- **Explicit attribution** — every I/O event says whether a
  Java stack was joined to it or not (non-JVM process, JNI or
  JVM-internal I/O, agent absent). I/O that ran on a virtual
  thread's carrier is flagged, because v1 attributes it to the
  carrier and cannot name the virtual thread. Missing
  attribution is data, not a hole.
- **Cross-service causality** — connection ids + per-connection
  request ordinals + per-thread ordering + stack attribution
  are enough to reconstruct which thread on service A caused
  which thread on service B, and which files those threads
  touched locally (data and meta ops alike), without
  re-inference; the sequence view renders exactly this. The
  limits are stated: async hops and HTTP/2 requests without
  captured stream ids render as time-ordered, never guessed.
- **Explicit end** — every trace records how it ended (duration,
  disk limit, stop, targets exited, error), so a reader — LLM or
  human — knows whether it is complete.
- **Source-matching frames** — `class#method(file:line)` plus the
  class's JAR origin, directly comparable with source code.
- **Deterministic exports** — canonical key order, so two
  normalized runs of the same scenario diff to near-nothing
  inside the scenario's marker bracket. Outside it, a JVM's
  background activity differs per run and no claim is made.
- **Sliceable** — `iormeshctl query` cuts the merged trace by
  time, host, app, thread, connection, file or method into
  size-capped NDJSON with an explicit truncation record. A
  full trace never fits an LLM's context; the slice is what
  gets read.
- **Tested, not asserted** — an eval asks an LLM a fixed
  question set over workload traces with known answers. The
  eval is built (M5) but has not yet run with a real model, so
  M5 is not accepted; once it has, it is meant to rerun on
  every schema change.
- **Deterministic SVG too** — the diagram export is stable for a
  given normalized graph, so it diffs and goldens like the data
  exports; same for the sequence-diagram export.
- **Pod-aware identity** — under Kubernetes, an in-pod tracer
  describes its pod as the host (`host_id` `pod-<uid>`, pids in
  the pod's PID namespace; [schema.md](schema.md#pods-as-hosts-m7)).
  Pod, namespace and container fields on events are planned
  (task sh4).
- **Project-scoped** — every session belongs to a named project;
  traces live under `/opt/iormesh/traces/<project>/`, and
  cross-project connections render as explicit edges in the
  merged mesh.
- **Proxy-hop edges** — a route through an ingress renders as
  one routed edge (client → FQDN → ingress → Service →
  workload) in the build's model and its views, so the mesh
  completes even where no tracer can run. The graph itself has
  no FQDN, ingress or workload nodes (PLAN.md M7).
- **OTEL-correlated** — events carry the active OpenTelemetry
  trace/span ids when the app is instrumented, so the mesh joins
  with any tracing backend. Ingesting the app's spans to
  overlay the distributed service graph is deferred (PLAN.md
  decision #20).
- **HTTP request events** — method, scheme, host, path/URI,
  status and sizes as first-class events, correlated with
  the connection, the method stack and the OTEL span; sensitive
  headers and query params are redacted before recording — the
  same policy covers argv. Path segments are recorded verbatim
  (decision #22); templating id-like segments is planned (task
  xh4).
- **Goldens in the same schema** — M0's declared expectations
  are schema documents with symbolic ids. Validation is subset,
  not equality: cut to the marker bracket, normalize, assert
  everything declared is present and nothing on the
  must-not-appear list is. The declarations double as few-shot
  examples for introspecting real runs.

## See also

- [Trace scope](trace-scope.md) — what lands in these events.
- [Testing](testing.md) — how the contract is validated.
