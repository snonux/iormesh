# S3 — overhead and volume budget

Result of spike S3 (PLAN.md, "Spikes"; decision #25). It measures
what the M1 tracer costs on the per-syscall worst case and turns
the numbers into the budget M2 is accepted against. Raw samples:
`spikes/s3/results/samples.txt`; the script that took them:
`spikes/s3/measure.sh`.

## Setup

- M1 tracer (`./iormesh run`, commit `c0cdf76`): the default probe
  set (open\*/read/write/close/connect/accept\* enter + exit,
  `sock:inet_sock_set_state`), correlator, NDJSON sink.
- Load: the workload's `syscall-burst --ops 200000` (400 000
  syscalls: a 1-byte `write` + `read` per op on a file, the worst
  case per syscall) at 1, 4 and 16 platform threads, 3 runs each,
  modes interleaved per run.
- Modes: **untraced**; **traced** (the burst's own PID in
  `target_map`); **bystander** (the burst untraced while a tracer
  is attached to an idle `sleep` — its syscalls still hit the
  tracepoints and are filtered out).
- Host: i7-1185G7, 8 threads, kernel 7.2.15, JDK 25, burst files
  on btrfs. **Not idle**: load average 4.3–6.5 from unrelated
  work during the runs, so single samples spread up to 2×. The
  numbers are medians and good to the order of magnitude, which
  is what a budget needs; a quiet-host re-run should tighten
  them, not change the conclusions.

## Numbers (medians of 3)

| Threads | Untraced ns/syscall | Traced | Bystander | Records lost |
|--------:|--------------------:|-------:|----------:|-------------:|
| 1  | 1309 | 2414 (1.84×) | 1545 (1.18×) | 30 % of syscalls |
| 4  |  499 |  757 (1.52×) |  589 (1.18×) | 81 % |
| 16 |  476 |  698 (1.46×) |  835 (1.75×)\* | 75 % |

\* the 16-thread bystander samples spread 218–407 ms; treat as
noise around the 1.2× of the other rows.

"Records lost" is the tracer's kernel drop count (enter and exit
records both count) per syscall issued. A run of 100 000 syscalls
lost nothing: loss starts when a *sustained* burst outruns the
consumer and fills the ring buffer.

- **Tracer CPU**: 3.6–11 µs per emitted event (utime + stime,
  minus the 1.4–2.1 s fixed cost of loading and attaching the
  object, measured on the bystander runs). That is the
  bottleneck: one core sustains roughly 100–300 k events/s, the
  burst issues ~1.5 M records/s per thread.
- **Volume**: 324–326 bytes per event as NDJSON. zstd compresses
  a trace 19× at level 3 (16 bytes/event) and 27× at level 19
  (12 bytes/event) — the burst is repetitive, real traces will
  compress less, so the budget below assumes 30 bytes/event for
  compressed segments.

## Conclusions

1. **Hot loops need in-kernel aggregation.** No per-event
   consumer keeps up with a tight read/write loop; loss is
   structural, not a tuning gap. M2 adopts ior's model
   (`syscall_aggregate_map`): repeated read/write on one fd
   instance by one thread is summed in the kernel and emitted as
   one aggregate event per flush, with count and bytes. Opens,
   closes, connects, accepts, markers and every event that
   changes fd or socket state stay per event, so goldens and
   attribution are unaffected.
2. **The consumer must get cheaper.** 3.6–11 µs per event is
   mostly JSON encoding plus validation per event; the parquet
   segment writer (M4) and validating only in tests are the
   levers. Budget below.
3. **The bystander tax is real** — about +18 % per syscall
   (≈240 ns at 1 thread) for every process on the host while a
   trace runs, the classic-tracepoint cost ior documents. It is
   paid only while tracing (bounded by `-duration`); raw
   tracepoints are the lever if it must shrink.
4. **Volume**: at 10 k events/s, 326 B/event NDJSON is ~196 MB
   per minute; compressed segments at ~30 B/event are ~18 MB per
   minute, ~4.3 GB per 4 h session. The minute rotation stays;
   a size cap (64 MB per segment) forces an early rotation for
   very hot sessions so a segment never grows past what one
   `collect` moves comfortably (#16).
5. **Default families (#7)** stay as defaulted: file, socket and
   lifecycle on; IPC, signals, epoll and mmap opt-in — each adds
   records at syscall frequency and the measured consumer cost
   is what makes them expensive.

The budget itself (the acceptance criteria) is decision #25 in
PLAN.md.
