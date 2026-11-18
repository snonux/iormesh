# Trace output — built for LLM introspection

Outputs are a stable, versioned contract — not an internal dump.
Companion to [PLAN.md](../PLAN.md) ("Trace output contract") —
the plan is the source of truth.

- **Self-describing schema** — NDJSON event stream, graph export
  (typed nodes + edges), per-run summary rollup; documented
  in-repo in [schema.md](schema.md). The v1 *draft* (M1) is
  the cgo-free `internal/schema` package; it stays a draft
  until spikes S1 and S2 settle the Java and cross-host fields.
  Readers are strict: a record with an unknown or mis-cased
  key, a missing or null field, another schema version or an
  unknown event kind is rejected, never half-read.
- **Parquet bulk files** — the same schema serialized columnar
  for on-server trace files: minute-rotated segments with
  parquet-internal zstd compression, so each one is directly
  queryable; collected over rsync while the session runs
  (ior already records parquet). Variable event arguments map
  to one nullable column per field. NDJSON stays the
  live/golden format.
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
  disk limit, stop, error), so a reader — LLM or human — knows
  whether it is complete.
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
  question set over workload traces with known answers; it is
  rerun on every schema change.
- **Deterministic SVG too** — the diagram export is stable for a
  given normalized graph, so it diffs and goldens like the data
  exports; same for the sequence-diagram export.
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
  headers, query params and path segments are redacted before
  recording — the same policy covers argv.
- **Goldens in the same schema** — M0's declared expectations
  are schema documents with symbolic ids. Validation is subset,
  not equality: cut to the marker bracket, normalize, assert
  everything declared is present and nothing on the
  must-not-appear list is. The declarations double as few-shot
  examples for introspecting real runs.

## See also

- [Trace scope](trace-scope.md) — what lands in these events.
- [Testing](testing.md) — how the contract is validated.
