# Decided

Part of [Decisions](../decisions.md).

The settled decisions, grouped by area; the groups are not in
numeric order — the "By number" list in
[decisions.md](../decisions.md#by-number) is.

## Parts

Each part is its own file; open only the one you need.

- [Storage, transport and the join key](decided/storage-transport.md)
  — #3 graph storage, #4 Go eBPF library, #12 controller
  transport, #39 patched distribution rsync on the controller, #16
  trace-file segmentation, #26 cross-host join key.
- [Test workload and overhead budget](decided/workload-overhead.md)
  — #5 test workload scope, #25 tracer overhead and volume budget.
- [Runtime control, explorer and the Java
  agent](decided/runtime-java.md) — #19 runtime control, #11
  explorer form factor, #2 Java agent attach strategy, #6 Java
  stack source, #27 agent → tracer transport.
- [Repository layout, BPF C, release and
  Go](decided/layout-release.md) — #30 repository-root layout,
  #31 verifier-safe array indexes, #32 unsigned release archives,
  #33 invalid UTF-8 in NDJSON and the supported Go range, #38
  `iormeshctl keygen` makes the operator's key without a terminal,
  #40 an untagged build for tests, marked and kept apart.
- [Container ids, pre-session connections, docs and
  agents](decided/tracing-docs-agents.md) — #34 the pod sandbox's
  container id, #35 connections open before the session, #36
  large docs as an index plus parts, #37 a smaller agent installs
  and uses iormesh, #41 a `-file` glob that matches only a last
  element says so.
