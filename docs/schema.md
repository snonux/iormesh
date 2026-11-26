# Trace schema (v1, frozen)

The field-level reference of the trace output. The contract's
goals and guarantees are in [output-contract.md](output-contract.md);
this file says what is on disk. The Go form — and the authority
when this file lags — is the
cgo-free package `internal/schema`.

Version 1 is frozen: spikes S1 (Java ↔ syscall join) and S2 (cross-host
join) settled their fields, M2's acceptance (2026-11-19) the rest of the
traced surface, and M3's acceptance (2026-11-20) the Java, OTEL, `http`
and `dns` fields and the methods, threads, classes and stacks tables,
which were provisional until then. M4 appended the `fork` and
`attach` kinds and `args.pattern`, `args.parent_pid` and
`args.parent_incarnation` (task ze4, below), `args.sock_snapshot`
(task sf4), the four `tcp_sock` fields of `tcp_state`
(`args.snd_una`, `args.rcv_nxt`, `args.bytes_acked`,
`args.bytes_received`), `args.tcp_seq`, `args.bytes_partial` (task
oh4) and `args.peek` (task ok4); `args.sock_snapshot` gained the
values `initial_client` and `initial_server` (task cm4). Fields are only ever
appended, and appended fields are optional, so an older reader never
sees a changed meaning. Enum values (`drop_reason`, `end_reason`, …)
are appended the same way as event kinds, so a reader should pass an
unknown value through. The strict readers here are the exception:
`schema.DecodeEvent` and the segment reader reject an event kind
they do not know, and the NDJSON readers (`DecodeEvent`,
`ReadNDJSON`) reject an unknown key too — an appended field
included (see "Readers are strict"). So a trace with the M4 kinds
or fields (`args.sock_snapshot`, say) needs a reader of this
version; a tolerant reader of its own passes them over. One meaning
did narrow:
task vd4 moved skipped program runs out of a plain `drop`'s `args.lost`
into their own `skipped_runs` drops (see `drop` below). M3
appended the optional `otel` object (task 4b4, "OpenTelemetry span
context" below).

## Parts

This file is the index: each part below is its own file. Read the summaries,
then open only the part you need.

- [Files and the parquet mapping](schema/files-parquet.md) — the trace files
  and how events map to parquet.
  - [Files](schema/files-parquet.md#files)
  - [Parquet mapping](schema/files-parquet.md#parquet-mapping)
- [Event envelope](schema/envelope.md) — the envelope, args, every syscall
  event kind, host-level events.
  - [Args](schema/envelope.md#args)
  - [Event kinds (M1)](schema/envelope.md#event-kinds-m1)
  - [Event kinds (M2 file
    surface)](schema/envelope.md#event-kinds-m2-file-surface)
  - [Folded reads and writes (M2 step
    11)](schema/envelope.md#folded-reads-and-writes-m2-step-11)
  - [Event kinds (M2 process
    lifecycle)](schema/envelope.md#event-kinds-m2-process-lifecycle)
  - [Event kinds (M2 step 7: IPC, signals, UDP, poll,
    zero-copy)](schema/envelope.md#event-kinds-m2-step-7-ipc-signals-udp-poll-zero-copy)
  - [Host-level events: session, drop,
    end](schema/envelope.md#host-level-events-session-drop-end)
- [Native stacks (M2 step 9)](schema/native-stacks.md) — opt-in native stacks
  (M2 step 9).
- [Java attribution (M3)](schema/java.md) — the agent's fields on events (M3).
- [HTTP, DNS and OpenTelemetry](schema/http-dns-otel.md) — HTTP events, DNS
  events, span context (M3).
  - [HTTP events (M3)](schema/http-dns-otel.md#http-events-m3)
  - [DNS events (M3)](schema/http-dns-otel.md#dns-events-m3)
  - [OpenTelemetry span context
    (M3)](schema/http-dns-otel.md#opentelemetry-span-context-m3)
- [Graph](schema/graph.md) — the graph, pods as hosts, pod fields (M7).
  - [Pods as hosts (M7)](schema/graph.md#pods-as-hosts-m7)
  - [Pod fields on events (M7)](schema/graph.md#pod-fields-on-events-m7)
- [Redaction](schema/redaction.md) — what is redacted and how.
- [Query slices and summary rollup](schema/query-summary.md) — M5's query
  slices and summary rollup.
  - [Query slices (M5)](schema/query-summary.md#query-slices-m5)
  - [Summary rollup (M5)](schema/query-summary.md#summary-rollup-m5)
