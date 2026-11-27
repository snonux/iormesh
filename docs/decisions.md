# Decisions

Every design decision iormesh has taken, numbered, each with its
reasons. A decision is recorded here first — in the part for its
group below, and in the "By number" list — and the topic docs
([architecture](architecture.md), [trace scope](trace-scope.md),
[output contract](output-contract.md), [build](build.md),
[testing](testing.md), …) narrate it, and
[milestones.md](milestones.md) says where it was built. A new
decision takes the next free number.

Numbers are stable ids — references in the docs, the code and the
tests keep working — so the groups below are not in numeric
order; "By number" at the end is. Four groups: decided, open (grouped by the milestone they
were raised for, each with a default so work was never stalled on
a missing answer; those milestones are accepted on the defaults,
which stand until the owner decides otherwise), provisional
(settled under the 2026-11-20 sequencing relaxation, each naming
what the owner must confirm and what would overturn it), and
deferred.

## Parts

This file is the index: each part below is its own file. Read the summaries,
then open only the part you need.

- [Decided](decisions/decided.md) — settled decisions; an index of its
  own, by area.
  - [Storage, transport and the join
    key](decisions/decided/storage-transport.md) — #3, #4, #12, #39,
    #16, #26, #43.
  - [Test workload and overhead
    budget](decisions/decided/workload-overhead.md) — #5, #25.
  - [Runtime control, explorer and the Java
    agent](decisions/decided/runtime-java.md) — #19, #11, #2, #6, #27.
  - [Repository layout, BPF C, release and
    Go](decisions/decided/layout-release.md) — #30–#33, #38, #40.
  - [Container ids, pre-session connections, docs and
    agents](decisions/decided/tracing-docs-agents.md) — #34–#37,
    #41, #42.
- [Open — built on the default](decisions/open.md) — open questions raised for
  M2, M3 and M4, each with the default built.
  - [Open — raised for M2 (built on the
    default)](decisions/open.md#open--raised-for-m2-built-on-the-default)
  - [Open — raised for M3 (built on the
    default)](decisions/open.md#open--raised-for-m3-built-on-the-default)
  - [Open — raised for M4 (built on the
    default)](decisions/open.md#open--raised-for-m4-built-on-the-default)
- [Provisional — M7 (2026-11-20)](decisions/provisional-m7.md) — the Kubernetes
  decisions settled provisionally.
- [Deferred — not in v1](decisions/deferred.md) — what v1 leaves out.

## By number

Every decision, in numeric order, with the part that holds it. The
next free number is #44.

- **#1** [Remote bootstrapping and sudo scope](decisions/open.md) — open
- **#2** [Java agent attach
  strategy](decisions/decided/runtime-java.md) — decided
- **#3** [Graph storage](decisions/decided/storage-transport.md) — decided
- **#4** [Go eBPF library](decisions/decided/storage-transport.md) —
  decided
- **#5** [Test workload scope](decisions/decided/workload-overhead.md) —
  decided
- **#6** [Java stack source](decisions/decided/runtime-java.md) — decided
- **#7** [Event families, default-on vs opt-in](decisions/open.md) — open
- **#8** [Hostname & TLS visibility](decisions/open.md) — open
- **#9** [Capture policy for argv and env](decisions/open.md) — open
- **#10** [Java exception events](decisions/deferred.md) — deferred
- **#11** [Explorer form factor](decisions/decided/runtime-java.md) —
  decided
- **#12** [Controller transport](decisions/decided/storage-transport.md) —
  decided
- **#13** [K8s privilege profile](decisions/provisional-m7.md) — provisional
- **#14** [K8s Java attach](decisions/provisional-m7.md) — provisional
- **#15** [Route-map source of truth](decisions/provisional-m7.md) —
  provisional
- **#16** [Trace-file
  segmentation](decisions/decided/storage-transport.md) — decided
- **#17** [Disk-watchdog semantics](decisions/open.md) — open
- **#18** [K8s debug-container config in
  iormeshctl](decisions/provisional-m7.md) — provisional
- **#19** [Runtime control](decisions/decided/runtime-java.md) — decided
- **#20** [OTEL integration mode](decisions/open.md) — open
- **#21** [Project semantics](decisions/open.md) — open
- **#22** [HTTP capture mechanics](decisions/open.md) — open
- **#23** [Target-pattern mechanics](decisions/open.md) — open
- **#24** [Service-interaction database](decisions/open.md) — open
- **#25** [Tracer overhead and volume
  budget](decisions/decided/workload-overhead.md) — decided
- **#26** [Cross-host join key](decisions/decided/storage-transport.md) —
  decided
- **#27** [Agent → tracer transport](decisions/decided/runtime-java.md) —
  decided
- **#28** [Signature scheme](decisions/open.md) — open
- **#29** [JDK version support](decisions/open.md) — open
- **#30** [Repository-root layout](decisions/decided/layout-release.md) —
  decided
- **#31** [Verifier-safe array indexes in the BPF
  C](decisions/decided/layout-release.md) — decided
- **#32** [Unsigned release archives; operators
  sign](decisions/decided/layout-release.md) — decided
- **#33** [Invalid UTF-8 in NDJSON, and the supported Go
  range](decisions/decided/layout-release.md) — decided
- **#34** [The pod sandbox's container id is shown
  bare](decisions/decided/tracing-docs-agents.md) — decided
- **#35** [Connections open before the session are
  seeded](decisions/decided/tracing-docs-agents.md) — decided
- **#36** [Large docs are an index plus a folder of
  parts](decisions/decided/tracing-docs-agents.md) — decided
- **#37** [A smaller agent installs and uses iormesh from the docs
  and a skill](decisions/decided/tracing-docs-agents.md) — decided
- **#38** [`iormeshctl keygen` makes the operator's key without a
  terminal](decisions/decided/layout-release.md) — decided
- **#39** [Patched distribution rsync on the
  controller](decisions/decided/storage-transport.md) — decided
- **#40** [An untagged build for tests, marked and kept
  apart](decisions/decided/layout-release.md) — decided
- **#41** [A `-file` glob that matches only a last element says
  so](decisions/decided/tracing-docs-agents.md) — decided
- **#42** [`query -frames` names a Java stack's
  frames](decisions/decided/tracing-docs-agents.md) — decided
- **#43** [A one-sided connection in a build's tail is pending, not
  unresolved](decisions/decided/storage-transport.md) — decided
