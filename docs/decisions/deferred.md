# Deferred — not in v1

Part of [Decisions](../decisions.md).

- **#10 Java exception events** — JVMTI throw/catch hooks would
  enrich the model (error paths in source) but are hot.
  Out for v1; revisit as a sampled, opt-in family.
- **eBPF plaintext HTTP parsing for non-Java processes** (was
  part of #22) — conflicts with the no-wire-parsing rule; out.
- **OTLP span ingestion and the distributed-trace overlay**
  (the optional half of #20) — after span-context tagging has
  proven itself.
- **Virtual-thread attribution** — out of v1; the owner's apps
  do not use virtual threads yet. The problem: the kernel only
  sees the carrier thread, and a parked virtual thread may
  resume on another carrier, so thread id alone cannot name
  the virtual thread. The likely solution when it is needed:
  the agent's I/O-boundary hook records `Thread.currentThread()`
  — hooks sit at the innermost Java frame above the native
  call, where a virtual thread cannot park, so the carrier's
  thread id is stable for the length of one bracket. Fallback:
  track mount/unmount. Until then such I/O is attributed to
  the carrier and flagged (see M3). Needs JDK 21+, a
  `virtual-threads` scenario, and its own acceptance.
