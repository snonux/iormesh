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
  the bottleneck; where its time went is in "Follow-up (wc4)"
  below.
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
2. **The consumer must get cheaper** (task wc4): done, see
   "Follow-up (wc4)".
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

## Follow-up (wc4): the consumer's per-event CPU

Task wc4 profiled and cut the tracer's userspace cost per event
(decision #25, budget item 1). Measured 2026-11-19 on the same
host, **again not quiet**: load average 2.6–4.1 from unrelated
work, so single runs spread ~2×; the figures are medians and are
for comparison, not the acceptance run (a quiet-host
`measure.sh` run is still due before M2's acceptance).

**Where the time went** (`go test ./internal/correlate -bench
SyscallBurst -cpuprofile`, and `iormesh run -cpuprofile FILE` on
a traced burst):

1. `json.Marshal` — over half of the correlator + sink time
   (reflection walk, one allocation per line);
2. the record path from the ring: libbpfgo's cgo callback per
   record, a `GoBytes` copy, then two channel hops (raw, decoded),
   each waking a goroutine — on the real tracer ~130 k voluntary
   context switches per 400 k events and about as much CPU as the
   correlator itself;
3. pooled records never returned (`Recycle` was not called), so
   each decode allocated, plus the GC that caused (~20 %);
4. smaller items: a 4 KiB `bufio` buffer (a `write(2)` per ~12
   events), the correlator's queue reallocating as it slid
   forward, and `Validate` copying the 330-byte event into each
   of its checks (62 ns).

Per-event validation itself was cheap: once the copies were gone
it costs ~15 ns, so it stays on the hot path — a producer bug
still fails the write instead of reaching a file the strict
reader rejects.

**Changes**: `schema.AppendEvent`, a hand-written encoder held
byte-identical to `json.Marshal` by a property test on randomly
filled events (every field) and a fuzz target; the export goldens
are unchanged. The sink encodes into a 64 KiB `bufio` buffer in
place. `types.Recycle` returns every record to its pool once the
session handled it. `internal/bpf` reads `event_map` from its own
memory mapping (`ringbuf.go`) instead of libbpfgo's callback, and
delivers records in batches of up to 256, pausing 200 µs after a
partial batch and blocking in epoll on an empty ring.

**Numbers** — Go benchmark (decode + correlate + encode, writing
to `io.Discard`; 3 interleaved runs each, load ~3.7):

| | ns/event | allocs/record |
|--|--:|--:|
| before (`359e726`) | 3122–3152 | 2 (665 B) |
| after | 811–896 | 0 |

Real tracer on `syscall-burst --ops 200000`: tracer utime+stime
from the ready file to exit (load and attach excluded, so the
fixed cost no longer swamps the figure), divided by emitted
events; 3 interleaved runs each, load 3.2–4.1. "Lost" is as
above, lost records over records wanted:

| Threads | before ns/event | after ns/event | before lost | after lost |
|--:|--:|--:|--:|--:|
| 1 | 5803 (5523–6118) | 2000 (1375–2625) | 12–15 % | 0 |
| 4 | 6294 (6181–11617) | 1532 (1425–2499) | 38–40 % | 0–5 % |

Voluntary context switches of the tracer fell from ~45 k to
~6–9 k per run. The medians sit at or under the 2 µs budget on
this busy host; the after-profile is now dominated by the
correlator and encoder themselves (~1.4 µs of ~2 µs per event
under contention; in the Go benchmark — no ring, no contention,
also run at load ~3.7 — decode, correlate and encode together
take 0.8–0.9 µs), the BPF load being the only larger item. The
1-thread median sits exactly at 2.0 µs (range 1.4–2.6), so the
budget is met only at the boundary until a quiet-host
`measure.sh` run settles it (due before M2 acceptance). The 1-thread burst now loses no
records; the 4-thread one still can, which is what in-kernel
aggregation (task vc4) addresses.
