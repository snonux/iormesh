# Spikes — feasibility before commitment

Part of [Milestones](../milestones.md).

Three mechanisms carry the project and none of them exists in ior.
Each gets a time-boxed spike that ends in a written result (a
short doc under `docs/spikes/`) — either "works, here are the
numbers" or "needs this other design". A spike's prototype code is
throwaway; its findings update these docs.

- **S1 — Java↔syscall join** (gates M3, and the `java_stack`
  fields of the envelope, which stay provisional until then).
  Questions:
  - Hook set: which JDK I/O boundary methods (file streams and
    channels, `Files` operations, socket and channel
    implementations, the NIO dispatchers) bracket the syscalls of
    the M0 scenarios, and which I/O stays unbracketed (JNI
    libraries, JVM-internal I/O).
  - Join key. Front-runner, shown to work in a first
    experiment (2026-11-18, JDK 25, bpftrace): uprobes on the
    JDK's native I/O functions (`libjava`/`libnio`/`libnet`)
    mark the thread id on entry and clear it on return, and
    the syscall handler reads the mark — an exact, in-kernel
    join by TID with no clocks involved, on platform and
    virtual threads alike. It says *which native I/O call*
    issued a syscall, not which Java method or virtual thread:
    that still needs the agent, or an eBPF HotSpot unwinder —
    how the two meet is the open question. Fallbacks: fd plus a
    time window; kernel TID (`/proc/thread-self`) plus a time
    window.
  - Clocks: the agent's clock is monotonic, ior's BPF records
    use the boot clock — they differ by time spent suspended.
    Verify the conversion's error is far below a syscall's
    duration.
  - Transport: how agent events reach the tracer without a
    socket (decision #27), and how the agent's own I/O is kept
    out of the target's story.
  - Attach: `-javaagent` vs live attach under JDK 21's
    dynamic-loading rules (decision #2).
  - Cost: overhead per I/O operation on the M0 workload.

  Exit: a prototype attributes every declared I/O op of one M0
  file scenario and one socket scenario to its declared method,
  on platform threads, with the overhead measured. (Virtual
  threads are deferred — see "Deferred — not in v1".)

  Result (2026-11-18, JDK 25 and 27-ea, kernel 7.2.15, bpftrace;
  [docs/spikes/s1-java-join.md](../spikes/s1-java-join.md)):
  works. Uprobe TID marks on a curated set of leaf I/O natives
  plus the agent's bracket on the same TID attributed every
  declared op of `file-basic` (6/6) and `tcp-loopback` (server
  11/11, client 10/10) to its declared method, 0 mismatches. The
  uprobe side needs a per-native syscall allowlist (JVM work
  runs inside brackets), must drop x86_64 syscall 335 (the
  uretprobe trampoline), keys marks on probe ids and resets them
  on thread exit. Clocks: compared in CLOCK_MONOTONIC — BPF
  stamps the native's entry with `bpf_ktime_get_ns`, no
  conversion; a boot/mono pair read in BPF is off by roughly
  70–230 ns (run-dependent) and pushed 14/20 000 stamps out of
  tight brackets in one run.
  Transport: an mmap ring, no syscall per record, every record
  delivered or counted (#27). Attach: live attach works on 25
  and 27-ea with JEP 451's warning (#2). Cost (loaded host, CPU
  time, indicative): per hooked I/O call ≈3.5 µs for the uprobe
  pair and ≈8 µs for the agent's 10-frame stack walk; the agent
  without the walk ≈0.3 µs. Decisions #2, #6 and #27 are
  settled, and the envelope's Java fields with them: nested
  `java.thread` / `java.stack` (see output-contract.md).
  Untested in S1: a JDK 21 runtime and the ASM-based agent on
  21 and 17 (both since run in M3, see #29), stripped or static
  JDK libraries, multi-threaded cost, cost on
  a quiet host, the join on the native-entry stamp (the prototype
  paired on `sys_enter`), per-PID uprobe attachment, containers.
- **S2 — Cross-host join** (gates M4's merge). Questions:
  - The NAT-tolerant join key (decision #26): conntrack state
    read on the NAT host vs the TCP initial-sequence-number pair
    captured at both ends, which NAT leaves untouched.
  - Clock offsets from the traffic itself: every fused
    connection yields send-before-receive constraints in both
    directions, which bound the offset between the two hosts —
    how tight are the bounds in practice?

  Exit: two tracers in separate network namespaces with DNAT
  between them and an injected clock skew (test-only flag)
  produce one fused edge and recover the skew within the bound.

  Result (2026-11-18, kernel 7.2.15, bpftrace;
  [docs/spikes/s2-cross-host.md](../spikes/s2-cross-host.md)):
  works. The ISN pair joined 34/34 connections per pass, all
  unique, under DNAT and under DNAT plus random-port SNAT,
  including reused 4-tuples (TIME_WAIT takeover and RST) and TCP
  Fast Open. It is rebuilt from `tcp_sock` at `ESTABLISHED` or
  any later record, role-aware: connecting side
  `iss = snd_una − bytes_acked`, accepting side
  `iss = snd_una − bytes_acked − 1`, both
  `irs = rcv_nxt − bytes_received − 1` (mod 2³²). Conntrack
  joins only as an event stream on the NAT host, and only with a
  time order for reused tuples; a snapshot misattributed 10/34
  under SNAT. Clock bounds: two tracer instances with injected
  skews of +1.23 s and −0.99 s recovered them inside intervals
  ≈10–25 µs wide on veth (runs 7–9, load-dependent; midpoint
  within 1 µs), with sends and
  receives matched by TCP sequence offset. The width depends on
  the traffic: single request/response connections alone gave
  82–104 µs, ping-pong rounds tightened it. Decision #26 is
  settled. Untested: SYN cookies, connections over 4 GiB, IPv6,
  real hosts, clock drift over long runs.
- **S3 — Overhead and volume budget** (gates M2; pins decision
  #25). Measured on the M1 skeleton with ior's `perf/` +
  bench-profiles pattern: tracer CPU, the cost untraced
  processes on the host pay, ring-buffer loss rate, and trace
  bytes per minute.

  Exit: the budget's numbers are written into decision #25 and
  become acceptance criteria from M2 on.

  Result (2026-11-18,
  [docs/spikes/s3-overhead.md](../spikes/s3-overhead.md);
  medians of 3 on a busy host, load 8–12, so order of
  magnitude): on the per-syscall worst case (`syscall-burst`,
  1-byte read/write) tracing slows the target 1.3–2.3×, other
  processes on the host pay ~1.14× per syscall while a trace
  runs, the tracer spends 6.6–13 µs CPU per event, and a
  sustained burst loses 16–42 % of its ring records — the
  consumer, not the ring, is the limit (a short burst and the M0
  scenarios lose nothing). NDJSON is ~326 B/event; zstd shrinks
  it 18×. Hot read/write loops therefore need in-kernel
  aggregation (ior's `syscall_aggregate_map`) in M2; decision #25
  records the budget. The two timing items are measured by the
  load-tolerant paired CPU-time method in #25 (2026-11-19; this
  host never gets quiet); task md4 measured both inside the
  budget.
