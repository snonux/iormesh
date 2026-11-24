# Trace output — built for LLM introspection

Outputs are a stable, versioned contract — not an internal dump.
The decisions it cites are in [decisions.md](decisions.md).

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
  time, host, app, thread, connection, file, method, event
  kind, pid, errno or OpenTelemetry trace id into
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
  Every event carries the optional `pod` object: namespace, pod
  name and the container id (task sh4;
  [schema.md](schema.md#pod-fields-on-events-m7)).
- **Project-scoped** — every session belongs to a named project;
  traces live under `/opt/iormesh/traces/<project>/`, and
  cross-project connections render as explicit edges in the
  merged mesh.
- **Proxy-hop edges** — a route through an ingress renders as
  one routed edge (client → FQDN → ingress → Service →
  workload) in the build's model and its views, so the mesh
  completes even where no tracer can run. The graph itself has
  no FQDN, ingress or workload nodes (milestones.md M7).
- **OTEL-correlated** — events carry the active OpenTelemetry
  trace/span ids when the app is instrumented, so the mesh joins
  with any tracing backend. Ingesting the app's spans to
  overlay the distributed service graph is deferred (decisions.md
  #20).
- **HTTP request events** — method, scheme, host, path/URI,
  status and sizes as first-class events, correlated with
  the connection, the method stack and the OTEL span; sensitive
  headers and query params are redacted before recording — the
  same policy covers argv. Path segments are recorded verbatim
  (decision #22): the events keep the raw path. Only the
  controller's per-path counts (the service database, the
  services view's top paths) fold id-like segments into
  templates such as `/orders/{id}` (task xh4; see
  [security.md](security.md) for the rules).
- **Goldens in the same schema** — M0's declared expectations
  are schema documents with symbolic ids. Validation is subset,
  not equality: cut to the marker bracket, normalize, assert
  everything declared is present and nothing on the
  must-not-appear list is. The declarations double as few-shot
  examples for introspecting real runs.

## Contract details

The envelope, the method table, how loss is counted and the
summary rollup, in more depth than the list above; the
field-by-field reference is [schema.md](schema.md).

- **Event envelope (v1 sketch)**: `schema_version, project, seq,
  boot_ns, wall_ns, host_id, pid, pid_incarnation, tid, comm,
  event, fd, fd_gen, ret, err, latency_ns, bytes, args (path,
  old+new path on renames / 4-tuple / …), conn_id, stack_id
  (native, opt-in), attribution (java | none), carrier (set
  when the I/O ran for a virtual thread), java_thread,
  java_stack (method ids), otel (trace_id, span_id — present
  on java events inside an OTEL span; appended after stack_id
  by task 4b4), http (method, scheme,
  host, path, status, req_seq — on http events; headers per
  redaction policy)`. S1 settled the Java fields: nested
  `java.thread` (thread name) and `java.stack` (method ids,
  innermost first), as `internal/schema` has them — the flat
  names above are shorthand; `http` was provisional until M3's
  acceptance froze it. Task 3b4 appended `version`, `req_headers` and
  `resp_headers` ("name: value", redacted) to it and made `http`
  an event kind of its own (fd -1, conn_id when found).
  Cross-host, `conn_id` comes from the merge's ISN-pair join
  (S2, decision #26).
- **Method table**: `method_id, class, method, signature,
  source_file, line, jar, classloader`. Frames reference ids, so
  stacks stay compact and every frame joins to its JAR. As built
  (tasks 2b4, fe4): the method row carries `class_id`, and JAR
  and class loader live once per class in the class table
  (`classes.ndjson`); `signature` is not recorded (StackWalker
  frames do not need it to be unique: the line tells call sites
  apart).
- **Skipped program runs are possible loss, not lost records**
  (task vd4, 2026-11-19): the kernel skips a BPF program while
  another one runs on that CPU (its recursion guard), and the
  skip happens before the program's target filter, so a
  program's `recursion_misses` counts untraced processes' calls
  too — host-wide BPF activity, not the trace's loss. It bounds
  the target records lost from above and cannot be attributed:
  nothing runs to look at the pid. So the contract reports it on
  its own and never adds it to the lost records (ring-buffer
  reservation failures and undecodable records, which are the
  trace's own): a host-level `drop` with `drop_reason`
  `skipped_runs` whose `args.lost` is the runs skipped since the
  previous one, and the summary's "skipped runs" figure beside
  "lost records". Loss stays never silent — a skip that did hit
  a target is inside that count — and never inflated. The
  integration gate stays conservative: a run with skipped runs
  cannot be judged loss-free and is retried like kernel loss
  (skipped as unexercised when every attempt had some); the
  root loader test requires zero lost records and only logs
  skipped runs.
- **Syscall handlers run behind `raw_syscalls` dispatchers**
  (task 8e4, 2026-11-19). Why runs were skipped: Linux 7.2 runs
  a per-syscall tracepoint program (`syscalls:sys_enter_<name>`)
  with preemption enabled behind a per-program, per-CPU
  recursion guard, so a task preempted inside it makes the
  kernel skip that syscall for every other task on the CPU until
  it resumes — the target's calls too, in time-slice bursts; on
  a loaded host every integration run had some, whoever else
  traced. A traced read loop sharing its CPU with an untraced
  one: ~900 k skipped runs, ~4 % of the target's reads lost.
  Raw tracepoints and `tp_btf` take the same preemptible guard;
  the classic `raw_syscalls:sys_enter`/`sys_exit` events run
  their program with preemption disabled (same test: 0 skipped,
  every read seen). So two dispatchers there tail-call the
  generated per-syscall handlers by syscall number
  (`internal/c/dispatch.c`, program arrays filled by userspace;
  per-arch number tables generated from `golang.org/x/sys`);
  handlers, records and the attach-or-skip rule per syscall pair
  are unchanged. What can still skip the dispatchers is the
  host-wide `bpf_prog_active` guard they share with kprobes and
  classic tracepoints, held by a task preempted inside a `bpf()`
  map syscall on that CPU (a tracer makes ~5 a second) — rare
  in practice (three full integration runs, one beside two
  concurrent `bpfRootTest`s: 0 skipped in all 96 tracer runs),
  real under a deliberate map-lookup loop on the target's CPU
  (114 k skipped, 2.5 % of its reads lost), and still counted
  as skipped runs. The gate's judgement stays
  as vd4 set it: a skip cannot be attributed to a pid, and the
  experiment shows it can be the target's. Cost: every syscall
  on the host now meets a dispatcher, traced kind or not. Kernel
  instructions per untraced syscall beside an idle trace
  (`perf stat`, 3 rounds, identical across rounds): dd's
  read/write 1626 → 1708 (untraced 728): ~5 % more kernel
  instructions per call of a traced kind, which puts the gated
  `syscall-burst` bystander ratio at an estimated ~1.21× (md4:
  1.19×), still under 1.25×; a syscall of a kind no family
  traces (getppid) 595 → 1142 (untraced 443) — the price of the
  `raw_syscalls` perf path. Task ae4 measured it by #25's
  paired method (s3-overhead.md, "Follow-up (ae4)"): the gated
  `syscall-burst` bystander 1.16× and 1.14× at 4 threads
  (passes); a pure `getppid` loop ~2.0× (+0.2–0.3 µs per call),
  recorded, not gated. Of that, the dispatcher programs are
  ~5 % of the loop's cycles and the kernel's perf-tracepoint
  path the rest, so no dispatcher fast path is worth adding.
  Attaching per syscall again on kernels before 7.2 (whose
  per-syscall programs run with preemption disabled), which
  would spare untraced kinds there, is not taken: the budget
  holds without it, a second attach mode would need the
  integration suite on each kernel line, and no host here runs
  one. Revisit if a real workload's bystander cost exceeds
  #25.
- **Summary rollup**: per-process entry point — config files read,
  connections with hostnames, thread table, top methods by I/O —
  cheap context before diving into the event stream.

## See also

- [Trace scope](trace-scope.md) — what lands in these events.
- [Testing](testing.md) — how the contract is validated.
