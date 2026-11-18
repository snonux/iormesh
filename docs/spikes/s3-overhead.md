# S3 — overhead and volume budget

Result of spike S3 (PLAN.md, "Spikes"; decision #25). It measures
what the M1 tracer costs on the per-syscall worst case and turns
the numbers into the budget M2 is accepted against. Every figure
below is computed from `spikes/s3/results/samples.txt`, written
by `spikes/s3/measure.sh`.

## Setup

- M1 tracer (`./iormesh run`, the commit named in the samples
  file's header): the default probe set (open\*/read/write/close/connect/
  accept\* enter + exit, `sock:inet_sock_set_state`),
  correlator, NDJSON sink.
- Load: the workload's `syscall-burst --ops 200000` (400 000
  syscalls: a 1-byte `write` + `read` per op on a file, the worst
  case per syscall) at 1, 4 and 16 platform threads, 3 runs each,
  modes interleaved per run.
- Modes: **untraced**; **traced** (the burst's own PID in
  `target_map`); **bystander** (the burst untraced while a tracer
  is attached to an idle `sleep` — its syscalls hit the
  tracepoints and are filtered out; the bystander trace holds
  only its 2 session/end events, proving the filter); **volume**
  (one traced burst of 50 000 ops, small enough to fit the ring,
  for loss-free volume and compression figures).
- Host: i7-1185G7, 8 threads, kernel 7.2.15, JDK 25, burst files
  on btrfs. **Not idle**: load average 8.1–11.6 from unrelated
  work during the runs; single samples spread up to 3× (the
  1-thread bystander ratios run 0.69–2.43). The medians are
  order-of-magnitude figures. A quiet-host re-run is expected to
  tighten them; whether it changes a conclusion is open until it
  is done (before M2 acceptance).

## Numbers (medians of 3)

| Threads | Untraced ns/syscall | Traced | Bystander | Records lost |
|--------:|--------------------:|-------:|----------:|-------------:|
| 1  | 1425 | 3314 (2.32×) | 2387 (1.67×)\* | 16 % (0.33 per syscall) |
| 4  | 1038 | 1526 (1.47×) | 1189 (1.14×) | 42 % (0.83) |
| 16 | 1047 | 1396 (1.33×) | 1198 (1.14×) | 41 % (0.74) |

\* noise-dominated: the three 1-thread runs gave 2.43, 0.69 and
1.67.

Each syscall wants two ring records (enter and exit). "Records
lost" is the tracer's lost-record count (all of it kernel ring
drops in these runs) as a share of the records wanted
(lost + 2 × emitted events), with lost records per syscall in
parentheses. The volume run (100 000 syscalls) lost nothing, and
the M0 scenarios lose nothing in the integration suite: loss
starts when a *sustained* burst outruns the consumer and fills
the ring buffer.

- **Tracer CPU**: medians of 10.5, 13.0 and 6.6 µs per emitted
  event at 1, 4 and 16 threads (utime + stime of the tracer,
  minus the median CPU of the idle-target tracer as the fixed
  cost of loading and attaching the object). The fixed cost is
  38–93 % of a traced run's CPU (medians about half to three
  quarters), so single runs span 1.5–23 µs per event; the
  medians are the usable figure.
  At ~10 µs an event, one core sustains ~100 k events/s; the
  burst issues ~0.7–1.4 M records/s per thread. The consumer is
  the bottleneck; where its time goes is not profiled yet (task
  wc4) — JSON encoding and per-event validation are the
  suspects.
- **Volume**: 325–327 bytes per event as NDJSON. On the volume
  run's trace (100 025 events), zstd compresses 18.4× at level 3
  (17.7 B/event) and 24.9× at level 19 (13.1 B/event). The burst
  is repetitive; real traces will compress less, so the budget
  assumes 30 B/event for compressed segments.

## Conclusions

1. **Hot loops need in-kernel aggregation.** The M1 consumer
   cannot keep up with a tight read/write loop, and a faster
   consumer alone would need a 10× gain to match one burst
   thread. M2 adopts ior's model (`syscall_aggregate_map`):
   repeated read/write on one fd instance by one thread are
   summed in the kernel and emitted as one event per flush, with
   a count and bytes. Opens, closes, connects, accepts, markers
   and every event that changes fd or socket state stay per
   event, so goldens and attribution are unaffected (task vc4).
2. **The consumer must get cheaper** (task wc4): profile, then
   cut encoding and validation cost.
3. **The bystander tax** appears as ~1.14× at 4 and 16 threads;
   the 1-thread figure is noise-dominated on this host. It is
   paid by every process on the host only while a trace runs
   (bounded by `-duration`); raw tracepoints are the lever if it
   must shrink.
4. **Volume**: at 10 k events/s, 326 B/event NDJSON is ~196 MB
   per minute; compressed segments at ~30 B/event are ~18 MB per
   minute, ~4.3 GB per 4 h session. The minute rotation stays; a
   64 MB size cap (≈ 3.5 minutes at that rate) forces an early
   rotation for very hot sessions, so a segment never outgrows
   what one `collect` moves comfortably (#16).
5. **Default families (#7)** stay as defaulted: file, socket and
   lifecycle on; IPC, signals, epoll and mmap opt-in — each adds
   records at syscall frequency, and the consumer's per-event
   cost is what makes them expensive.

The budget itself (the acceptance criteria) is decision #25 in
PLAN.md.
