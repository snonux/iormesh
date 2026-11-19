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

## Follow-up (vc4): in-kernel aggregation

Task vc4 folds hot read/write loops in the kernel (decision #25;
docs/implementation.md M2 step 11, docs/schema.md "Folded reads
and writes"). One `spikes/s3/measure.sh` run on 2026-11-19
(`S3_OPS=200000 S3_THREADS="1 4" S3_RUNS=3`, commit `50b8ca8`;
samples in `spikes/s3/results-vc4/samples.txt`). The host was
**not quiet**: load average 12–22 (1-minute) from unrelated test
suites during every run, so the timing figures spread 4× between
runs and are not the acceptance figures for decision #25's CPU and
bystander items. Loss, the item this task owns, does not depend on
a quiet host in the direction that matters — load only makes the
consumer slower.

| Threads | Records lost (3 runs) | Events per 400 000 syscalls | NDJSON bytes |
|--:|--:|--:|--:|
| 1 | 0, 0, 0 | 184, 333, 85 | 29–110 KB |
| 4 | 0, 0, 0 | 2478, 143, 143 | 49–815 KB |

- **Loss: 0 % at 1 and 4 threads in every run**, against the
  budget of < 1 % (0 % and 0–5 % after wc4's consumer work,
  12–15 % and 38–40 % before it, 16–42 % in M1). The volume run
  (100 000 syscalls) lost nothing either. The integration suite
  (`TestSyscallBurstFoldsEveryCall`) checks the other half: the
  folded events' counts add up to exactly the burst's ops.
- **Events**: the burst's 400 000 calls arrive as 85–2478 events
  (up to ~4700 calls each). The outlier (2478 at 4 threads, run
  1, load 21) is the hot-window rule at work: a thread
  descheduled for more than 1 ms ends its loop's event and sends
  its next four calls singly.
- **Tracer CPU** is now mostly the fixed cost of load and
  attach: the traced runs' 1.4–4.9 s are comparable on most runs
  to the idle-target bystander tracer's 1.4–4.2 s in the same
  run, so a per-event figure is meaningless here and decision #25's ≤ 2 µs per event (wc4's numbers) stands
  as measured there.
- **Slowdown and bystander**: traced/untraced 1.1–2.1×,
  bystander 0.3–2.5× — noise-dominated on this host; the folding
  removes the ring-buffer work per call, not the tracepoint and
  map lookups, so the bystander tax is unchanged by design. A
  run by #25's paired CPU-time method (task md4) is still due
  for those two items; it gates M3's acceptance.
- **Volume** per event is unchanged (~330 B), but per syscall it
  fell by three orders of magnitude on this load: the volume
  run's 100 000 syscalls are 94 events, 32 KB of NDJSON, 3 KB at
  zstd -3.

**Correction (vc4, review round 2).** The tracer did not count one
kind of loss when this run was taken: a tracepoint program the
kernel *skipped* because another BPF program was running on that
CPU (its recursion guard; visible only as the program's
`recursion_misses`). A skipped read/write handler loses its record
silently — the trace showed it only as unpaired exits. Under
concurrent load this happened thousands of times per burst (a
4-thread burst while `bpftool` ran: 4096 misses, 2251 unpaired
exits, "lost records 0"; a 12-thread burst: 8 000–24 000). Since
then the attached programs' misses are kernel-side loss
(`bpf.Losses.Missed`) and reach the trace as `drop` events. The
zeros above are therefore ring-buffer loss only; a re-run that also
counts skipped programs is in "Follow-up (vc4, re-run)" below.

### Follow-up (vc4, re-run)

The same `measure.sh` run, repeated 2026-11-19 with the tracer that
counts skipped programs (samples in
`spikes/s3/results-vc4b/samples.txt`). Load average 3.3–4.1 —
quieter than the first run, still not the quiet host (load < 1)
decision #25 asks for the timing items.

| Threads | Records lost incl. skipped programs (3 runs) | Events per 400 000 syscalls |
|--:|--:|--:|
| 1 | 0, 0, 0 | 89, 83, 83 |
| 4 | 0, 0, 0 | 159, 143, 159 |

Loss stays 0 % at 1 and 4 threads with every kind of loss counted;
the volume run lost nothing either. Skips are load-dependent: the
same build reported 8 275–23 929 lost records (as `drop` events) on
a 12-thread burst, and none of 10 runs of the integration pair
(`TestSyscallBurstFoldsEveryCall` beside `TestUnixSocket`) did.

**Correction (vd4).** Counting skips as lost records overstated
the loss: the kernel skips a program before its target filter, so
`recursion_misses` includes untraced processes' calls (a
two-record root test saw 691 on a loaded host). Since task vd4
the skips are reported apart — `skipped_runs` drops and the
summary's "skipped runs" figure, `skipped_runs=` in `measure.sh`
samples — and "lost records" is ring-buffer and decoder loss
again. The 8 275–23 929 above are skipped runs, an upper bound on
the burst's lost calls; the zeros in the table hold for both. The
same goes for every later figure taken before vd4: the "Lost"
column of the ua4 table below (0, 0, 21; 3–143 k; 138–259 k) and
md4's 48–60 % counted skipped runs as lost records, so they are
upper bounds on the records lost.

## Follow-up (ua4): the cost of native stacks

Task ua4 added opt-in native stacks (`iormesh run
-native-stacks`, docs/implementation.md M2 step 9, docs/schema.md
"Native stacks"). `measure.sh` gained `S3_TRACER_FLAGS`, and four
runs on 2026-11-19 (`S3_OPS=200000 S3_THREADS="1 4" S3_RUNS=3`,
on top of commit `8eb697f`; samples in
`spikes/s3/results-ua4/{base,stacks,noagg,noagg-stacks}/`)
compare the default with `-native-stacks`, and — to see the
per-call price, which the fold otherwise hides —
`-aggregate=false` with and without it. Load average 2.8–9.9, so
not a quiet host: the figures are rough, medians of 3.

| Flags | Threads | Traced elapsed (ms) | Tracer CPU (s) | Events | Lost |
|---|--:|--:|--:|--:|--:|
| default | 1 | 400 | 1.46 | 83–87 | 0 |
| `-native-stacks` | 1 | 461 | 1.74 | 83–89 | 0 |
| default | 4 | 220 | 1.77 | 143–153 | 0 |
| `-native-stacks` | 4 | 343 | 1.97 | 151–205 | 0, 0, 21 |
| `-aggregate=false` | 1 | 487 | 1.86 | 400 067 | 0 |
| `-aggregate=false -native-stacks` | 1 | 939 | 2.26 | 400 067 | 0 |
| `-aggregate=false` | 4 | 368 | 2.89 | 329–399 k | 3–143 k |
| `-aggregate=false -native-stacks` | 4 | 443 | 2.88 | 313–354 k | 138–259 k |

- **Per traced call, in the kernel**: ~1.1 µs (1 thread, no
  fold: +450 ms over 400 000 calls) — the frame-pointer walk of
  `bpf_get_stackid` plus a second ring record (32 bytes when
  measured; review round 1 added the 8-byte image tag). Paid by
  the traced process only, and only for enters that are sent:
  with the default fold the burst sends ~85 events, so the walk
  barely runs; the difference to the default is small but
  visible on this loaded host (400 → 461 ms at 1 thread,
  220 → 343 ms at 4, medians) — more than ~85 walks explain, so
  part of it is load, which a quiet-host run should settle.
- **Per event, in the tracer**: ~1 µs (+0.4 s over 400 000
  events): decoding the side record (reflective
  `binary.Read`), the pairing, and one map read per *distinct*
  stack; symbolization runs once per stack on a worker. That
  doubles decision #25's ≤ 2 µs per event while it is on — the
  reason it is opt-in.
- **Bystanders pay nothing**: the walk runs after `filter()`,
  and with the flag off the verifier prunes it from every
  handler (`IORMESH_NATIVE_STACKS` is a load-time constant) and
  the stack map shrinks to one entry.
- **Loss** grows with the extra ring traffic where the ring is
  already the bottleneck: without the fold at 4 threads (a
  debugging mode that misses the budget anyway). With the fold,
  one of three 4-thread runs lost 21 records (< 0.01 % of the
  calls, inside the < 1 % budget); the scenarios lose none
  (`TestNativeStacks`).
- **Volume**: `stack_id` adds ~15 B per NDJSON event (+4.5 %),
  +2 % after zstd -3; the table holds one line per distinct
  stack (~200 for the non-java-peer run: a JVM start-up plus the
  Go peer).

## M2 acceptance measurement (2026-11-19)

`spikes/s3/measure.sh` on HEAD (all M2 families default, folding
on), S3_OPS=200000, 1 and 4 threads, 3 runs —
`spikes/s3/results-m2/`; a volume run with `-aggregate=false` —
`spikes/s3/results-m2-noagg/`. Load average 2.8–3.1: **not the
quiet host (< 1) #25 asks for**, so the timings are rough
loaded-host figures — load inflates the untraced baseline too,
so it can make a ratio look better as well as worse.

| #25 item | Budget | Measured |
|----------|--------|----------|
| tracer CPU | ≤ 2 µs per emitted event | **passed** by md4 (see "Follow-up (md4)"); at M2: the folded burst (83–151 events) shows only the idle-target fixed cost; not re-measured per event here — wc4 gave 1.5–2.0 µs (boundary), and the unfolded run gives a rough (1370 − 1260 ms) / 40 067 events ≈ 2.7 µs, above budget but weak: two separate runs at 10 ms CPU granularity |
| bystander | ≤ 1.25× at 4 threads | **passed** by md4 (see "Follow-up (md4)"); at M2: 1.17× (runs 1.20, 1.14, 1.20) on a loaded host; 1 thread noisy (1.26–1.93) |
| loss | 0 on scenarios, < 1 % on the burst | 0 records lost in every run at 1 and 4 threads (skipped programs counted); 0 in both integration runs |
| volume | ≤ 30 B/event compressed | 325 B/event NDJSON, zstd -3 15.9 B/event (unfolded volume run, 100 067 events) |

Loss and volume pass. The two timing items moved to task md4
(2026-11-19), which gates M3's acceptance: this host never gets
quiet, so PLAN.md #25 now measures them load-tolerantly — the
tracer's own CPU time per event, and the bystander as the median
of ≥ 10 interleaved, pinned traced/untraced pairs on CPU time,
with the wall-clock ratio recorded alongside. The limits are
unchanged. Task md4 took both and both pass — the rough
per-event figure above was an artefact of its method; see
"Follow-up (md4)" below.

## Follow-up (md4): the timing items by the paired CPU-time method

Task md4 took decision #25's two timing items by the load-tolerant
method PLAN.md adopted on 2026-11-19. The host was as busy as ever
(load average 4.6–9.0, a nice-19 desktop indexer plus unrelated
builds), which is what the method is for. `spikes/s3/measure.sh`
now pins every process — the tracer to CPU 3, the burst JVM to
CPUs 0-2,4-6 (on this host CPU k and k+4 are SMT siblings, so the
tracer has core 3 to itself apart from CPU 7, which nothing of the
run uses) — and reads CPU times from `/proc/PID/stat`: the
tracer's from its ready file to its exit (`tracer_run_cpu_ms`),
the burst's from its startup gate to its result line
(`burst_cpu_ms`, JVM startup excluded; the burst is held at exit
by `IORMESH_WORKLOAD_HOLD_FILE` so its CPU can still be read).
`summary.txt` holds the medians. Samples and profiles:
`spikes/s3/results-md4/`, commit `45d2ac6`.

**Tracer CPU per event** (`S3_MODES=traced
S3_TRACER_FLAGS=-aggregate=false S3_OPS=500000 S3_RUNS=5
S3_THREADS="1 4"`,
`results-md4/cpu/`): unfolded, so every call is an event (1 000 067
events per 1-thread run), and the fixed load-and-attach cost stays
out of the numerator.

| Threads | ns/event, median of 5 | Range | Lost records |
|--:|--:|--:|--:|
| 1 | **1200** | 940–2660 | 0 |
| 4 | **1697** | 1430–2460 | 48–60 % (ring full, unfolded) |

Both medians are inside the ≤ 2 µs budget. The M2 figure of
~2.7 µs was an artefact of its method: it subtracted the
idle-target tracer's *whole-run* CPU from the traced one's, and
the fixed cost it removed — loading the BPF object, mostly
verifier time in `bpf_object__load` — is itself 1.4–5.0 s per
run here (tracer CPU at its ready file) and swings with host load,
so the difference of two such runs says little about 40 000
events. At 4 threads unfolded the burst
outruns the consumer (a debugging mode; the default fold sends
~150 events for the same burst, see "Follow-up (vc4)"), so the
figure is per *emitted* event under a full ring.

**Profile** (`iormesh run -cpuprofile`, the same unfolded 1-thread
burst, `results-md4/profile/t1.pprof`; profiling adds its own
overhead, this run spent 1.8 µs/event): of 3.3 s, 1.44 s is
`bpf_object__load` (the fixed cost). The per-event 1.8 s for
1 000 068 events splits into

1. NDJSON encoding (`schema.AppendEvent`): 0.64 s, half of it
   `strconv.AppendUint` on the ~10 integer fields (the 19-digit
   `boot_ns` and `wall_ns` cost most);
2. `write(2)` of the 326 MB of NDJSON: 0.28 s — output volume,
   which M4's parquet segments change;
3. the correlator itself (`exit`, `release`, maps, queue): ~0.3 s;
4. returning records to their pool (`types.Recycle`,
   `sync.Pool`): 0.17 s;
5. the ring reader (`bpf.ringReader.read`, decode): 0.16 s.

The 4-thread profile (`t4.pprof`) has the same shape, with load
(4.6 s of verifier under load 9) dominating. No cut was made: the
budget holds, and the cuts on offer are small — a faster integer
formatter for the two timestamps (~0.1 µs), fewer pool round
trips (~0.1 µs). They are the first candidates if M3's per-event
javajoin work (2b4) pushes the 4-thread median over 2 µs; M3's
acceptance re-runs this measurement.

**Bystander** (`S3_MODES=bystander S3_OPS=1000000 S3_PAIRS=10
S3_THREADS="1 4"`,
`results-md4/bystander/` at 1 and 4 threads, and a second
4-thread set, `results-md4/bystander-4b/`): each pair is an
untraced burst followed by one beside a tracer attached to an idle
`sleep` (ABAB); the ratio is per pair, then the median.

| Threads | Set | CPU-time ratio, median | Wall-clock ratio, median | Pairs ≤ 1.25× (CPU) | Load average |
|--:|--|--:|--:|--:|--:|
| 4 | 1 | **1.19×** | 1.21× | 9 of 10 (outlier 1.84×) | 6.3–8.4 |
| 4 | 2 | **1.15×** | 1.14× | 9 of 10 (max 1.253×) | 5.4–7.0 |
| 1 | 1 | 1.41× | 1.44× | 4 of 10 (0.43–3.66×) | 6.4–8.6 |

The gated figure — 4 threads, CPU time — passes in both sets, and
the wall-clock median passes too, so the guard (wall clock over
1.25× while CPU time passes) did not trip and no quiet-host run is
needed. The 1-thread figure is not gated and is not a measurement:
the same 1 000 000 untraced ops took 1.55–5.08 s of CPU, so CPU
time is *not* load-invariant for a single thread on this host —
an SMT sibling shared with the indexer or a build, and the clock
frequency that follows host load, change the CPU time per op
by 3×. Four threads average over more of both, which is why their
pairs span 1.01–1.25× apart from one outlier. A 1-thread figure
would need the burst pinned to one core with its sibling kept idle,
which this host cannot promise.

| #25 item | Budget | md4 verdict |
|----------|--------|-------------|
| tracer CPU | ≤ 2 µs per emitted event (median) | **passes**: 1.20 µs at 1 thread, 1.70 µs at 4 (unfolded, 5 runs each) |
| bystander | ≤ 1.25× at 4 threads (CPU-time median of ≥ 10 pairs) | **passes** (before 8e4's dispatch; ae4 re-ran it after: 1.16× and 1.14×, below): 1.19× and 1.15× in two sets of 10 pairs; wall clock 1.21× and 1.14× |

## Follow-up (2b4): the Java join's cost

Task 2b4 added the Java join (`iormesh run -spool`,
`correlate.JavaJoin`), which sits between the correlator and the
events file. Same method as md4 (`S3_MODES=traced
S3_TRACER_FLAGS=-aggregate=false S3_OPS=500000 S3_THREADS=1`),
host load 2.5–10 and changing between runs, so only the large
differences mean anything. Samples are not kept in the repo.

| Run | ns/event, median | Range | n |
|---|--:|--:|--:|
| before 2b4, no `-spool` (load 5–10) | 1760 | — | 5 |
| after 2b4, no `-spool` (load 2.5–4) | 960 | — | 5 |
| after, `-spool`, burst JVM under the agent (load 3.5–5.6) | 5230 | 2960–6090 | 5 |
| the same, one profiled run | 2760 | — | 1 |

Without `-spool` the join is not in the path, so the two first
rows differ by host load only. With the agent on the burst every
one of the million events is a hooked one-byte call and is
joined: the profile puts the join's own share (holding the event
until its watermark passes, the bracket search, the ring drain)
at about 1.1 µs per event on top of the 1–1.8 µs md4 measured, so
this worst case exceeds the ≤ 2 µs budget. The first profile
also showed the held queue reallocating on every append (slicing
its head off shrank its capacity); 2b4 replaced that with a head
index before the five runs above. Not yet cut: each event is
copied into the queue and out again (≈400 bytes twice), and the
default fold (`-aggregate`) turns this burst into ~150 events,
so a real JVM's join cost scales with its unfolded I/O calls.
Task ud4 brings it under the budget; it gates M3's acceptance
(task 5b4).

## Follow-up (3b4): the HTTP events' share

Task 3b4 put two things on the per-event path. In the agent,
nothing: the HTTP hooks fire once per request, at the libraries'
API boundary, and the I/O brackets are unchanged. In the tracer,
`JavaJoin` now looks at every event it receives (envelope fields,
the latest completion time, the socket ends of an event that
carries them) and numbers what it emits itself, and its release
loop checks for a due http event before each held one. Measured
with a Go benchmark of that path, an event of a process without a
ring (`BenchmarkJavaJoinPassThrough`, `internal/correlate`; 8 runs
each, on HEAD before the change and after it, load 12–14):

| Run | ns/event, median | Range |
|---|--:|--:|
| before 3b4 | 287 | 240–363 |
| after 3b4 | 267 | 164–312 |

No difference shows above the host's noise; the http bookkeeping
is a few field copies and one comparison per event (the socket
index parses addresses only on events that carry them — tcp_state,
connect, accept). The S3 burst itself was not re-run: it makes no
HTTP request, and its join cost is task ud4's.

## Follow-up (td4): the Java mark's cost

Task td4 added the kernel's Java mark (`internal/c/javamark.c`): a
uprobe and a uretprobe on every hooked JDK native of a traced JVM
(scoped to its pid), and, for each syscall a thread enters from
inside one, a mark record ahead of its enter record. Same method
and load as 2b4's row with the agent (`S3_MODES=traced
S3_TRACER_FLAGS="-aggregate=false -spool DIR" S3_OPS=500000
S3_RUNS=5 S3_THREADS=1`, the burst under `-javaagent` through
`JAVA_TOOL_OPTIONS`), the commit before td4 against td4, back to
back on a host at load 7.6–25 (other agents' builds and tests).
Every one of the million calls is hooked, so each event comes with
a mark: twice the ring records of the run before. A separate check
run (20 000 ops, so 40 000 hooked write and read calls) attributed
40 023 events through the mark: the 40 000 calls and 23 more
hooked calls of the burst and the JVM (the burst's file opens and
closes, its result line's writes).
Samples are not kept in the repo.

| Run | tracer ns/event, median | Range | burst CPU traced/untraced, median of pairs | Lost |
|---|--:|--:|--:|--:|
| before td4 (allowlist only), load 7.6–25 | 4260 | 3170–7390 | 1.15× (0.62–1.93) | 0 |
| td4, the mark (load 10.7–15.3) | 4970 | 4460–8200 | 1.29× (0.81–1.48) | 0 |

On this host only the large differences mean anything, and the
pair ratios span 3× in both rows. Read so: the +0.7 µs of tracer
CPU per marked event is within the noise of these runs (the
ranges overlap); what the mark adds by construction is decoding
one extra record per marked event and pairing it with its event,
on top of 2b4's join,
which is already over the ≤ 2 µs budget for this worst case — task
ud4 still owns bringing the `-spool` path under it. The target
pays the uprobe pair per hooked call (S1: ≈3.5 µs on a loaded
host, against the agent's ≈8 µs stack walk); the pairs here
cannot resolve it from noise. Loss stayed 0. Bystanders pay
nothing new by construction — the uprobes are attached per JVM
pid (`uprobe_multi` links, so other processes do not even trap),
the per-enter lookup sits behind the target filter and is pruned
at load without `-spool` — so the bystander item was not re-run.
The default fold (`-aggregate`) keeps most burst calls out of the
ring, and with them their marks.

## Follow-up (ae4): the bystander after 8e4's `raw_syscalls` dispatch

Task 8e4 moved the syscall handlers behind two dispatchers on the
classic `raw_syscalls:sys_enter`/`sys_exit` tracepoints (PLAN.md,
"Syscall handlers run behind `raw_syscalls` dispatchers"), so every
syscall on the host now meets a BPF program while a trace runs,
whether or not any family traces its kind. Task ae4 re-ran
decision #25's bystander item by md4's method, added a bystander of
an untraced kind, and re-checked the tracer's CPU per event. Same
host as md4 (i7-1185G7, 8 CPUs, kernel 7.2.15, JDK 25), as busy or
busier: load 3.8–7.4 for the bystander sets, 8–14 later (another
agent's builds and tests). Samples: `spikes/s3/results-ae4/`,
commit `1255da6` (8e4 and its review fixes; `+dirty` where the run
already had the `kind` mode below in `measure.sh`).

**Bystander, `syscall-burst`** (`S3_MODES=bystander S3_OPS=1000000
S3_PAIRS=10`, `results-ae4/bystander/` at 1 and 4 threads, a second
4-thread set in `results-ae4/bystander-4b/`):

| Threads | Set | CPU-time ratio, median | Wall-clock ratio, median | Pairs ≤ 1.25× (CPU) | Load average |
|--:|--|--:|--:|--:|--:|
| 4 | 1 | **1.16×** | 1.16× | 9 of 10 (outlier 2.02×) | 5.5–7.4 |
| 4 | 2 | **1.14×** | 1.19× | 9 of 10 (0.67–1.72×) | 4.9–6.3 |
| 1 | 1 | 1.21× | 1.22× | 6 of 10 (0.93–2.62×) | 3.8–4.6 |

md4 measured 1.19× and 1.15× at 4 threads before the dispatch; 8e4's
`perf stat` estimate of ~1.21× (5 % more kernel instructions per
read or write) is inside the noise of these pairs. The gated figure
passes in both sets, and so does the wall clock, so the guard did
not trip. The 1-thread figure is information only, for md4's reason
(one thread's CPU time is not load-invariant on this host).

**Bystander of an untraced kind** (new `kind` mode: `S3_MODES=kind
S3_PAIRS=10 S3_THREADS="1 4"`, `results-ae4/kind/`): the same ABAB
pairs with `spikes/s3/getppid.c` in place of the burst — a C loop
of 5 000 000 `getppid` calls per thread, pinned to the load CPUs,
reporting its own CPU time (`getrusage`) and wall time. No family
traces `getppid`, so before 8e4 it met no iormesh program at all.

| Threads | CPU-time ratio, median | Wall-clock ratio, median | Added CPU per call, pairs | Load average |
|--:|--:|--:|--:|--:|
| 1 | **2.04×** | 1.98× | +120–470 ns | 6.1–13.9 |
| 4 | **2.05×** | 2.00× | +60–360 ns | 10.3–13.2 |

An untraced `getppid` costs 120–320 ns of CPU here (clock frequency
follows host load); beside a trace it costs about twice that, +0.2–0.3 µs
per call (pair medians: +287 ns at 1 thread, +194 ns at 4) — 8e4's kernel-instruction count (443 → 1142 per
call) said the same. This is a pure-syscall loop, the worst case:
nothing in it dilutes the per-call tax, which is why it is recorded,
not gated (#25 gates `syscall-burst`). Where the time goes (`perf
record` of the 1-thread loop beside an idle trace,
`results-ae4/kind/getppid-profile.txt`): the two dispatcher programs
are 4.9 % of the loop's cycles; the kernel's perf-tracepoint path
around them — `trace_call_bpf` 10.8 %, `trace_syscall_enter`/`exit`
12.3 %, `perf_trace_*`, the buffer and recursion-context helpers and
RCU ~19 % (part of the RCU share is `getppid`'s own) — is the
rest of the added cost. So a cheaper dispatcher
fast path (testing the program-array slot first, or a pid pre-filter
before the tail call; the handlers already filter on entry) could
recover at most ~5 % of the loop: not taken. The only cut that
removes the tax is not to meet the `raw_syscalls` path, i.e.
attaching per syscall again on kernels before 7.2, whose per-syscall
programs run with preemption disabled. That was not done: the gated
item passes, this host runs 7.2 only (the other path could not be
tested here), and it would bring back a second attach mode the
integration suite would have to cover on each kernel line (PLAN.md,
decision #25).

**Tracer CPU per event** (`S3_MODES=traced
S3_TRACER_FLAGS=-aggregate=false S3_OPS=500000 S3_RUNS=5
S3_THREADS="1 4"`, `results-ae4/cpu/`): 3.0 µs at 1 thread and
3.1 µs at 4, medians of 5, at load 9.8–11.8 — against md4's 1.2 and
1.7 µs at load 4.6–9. The dispatch changes only kernel-side work,
so the gap was checked against the commit before 8e4 (`8bfd6c5`),
built in a worktree and run interleaved with `1255da6`, base
first (`results-ae4/cpu-ab/`): the base build in three rounds —
two runs per thread count in `base-1` and `base-2`, and `base-3`
cut short after one run per thread count, when a burst JVM did
not start within the timeout under load (so it has no
`summary.txt`) — and `1255da6` in two rounds, `head-1` and
`head-2`, two runs per thread count each:

| Build | 1 thread, ns/event median | 4 threads, ns/event median | Load average |
|--|--:|--:|--:|
| before 8e4 (`8bfd6c5`), 5 runs each | 2180 (1800–3490) | 3546 (2187–3751) | 7.9–13.8 |
| 8e4 (`1255da6`), 4 runs each | 2084 (1690–3530) | 2153 (1876–3555) | 9.1–13.3 |

No regression from 8e4: the two builds overlap, the newer one no
slower. But every median of both builds at load 10 and more is
over the 2 µs budget (ae4's own 3.0 / 3.1 µs; the A/B's 2.18 /
3.55 µs before 8e4 and 2.08 / 2.15 µs after) — the tracer's core 3
shares its SMT sibling, CPU 7, with the rest of the host. So the
item passes at moderate load (md4, 4.6–9) and is over at load
≥ 10 (ae4); the limit stays, and M3's acceptance (5b4)
re-measures it. Loss in the unfolded 4-thread runs is the full
ring, as in md4; skipped runs were 0 in every run.

| #25 item | Budget | ae4 verdict |
|----------|--------|-------------|
| bystander | ≤ 1.25× at 4 threads (CPU-time median of ≥ 10 pairs) | **passes** with 8e4's dispatch: 1.16× and 1.14× in two sets of 10 pairs; wall clock 1.16× and 1.19× (guard not tripped) |
| bystander, untraced kind | recorded, not gated | ~2.0× on a pure `getppid` loop (+0.2–0.3 µs per call), the perf-tracepoint path's price |
| tracer CPU | ≤ 2 µs per emitted event (median) | passes at moderate load (md4: 1.2/1.7 µs at load 4.6–9), **over at load ≥ 10** (ae4: 3.0/3.1 µs; A/B 2.08/2.15 µs after 8e4, 2.18/3.55 µs before); no 8e4 regression; 5b4 re-measures |

## Follow-up (ud4): cutting the consumer's per-event cost

Task ud4 took decision #25's tracer-CPU item for the worst case 2b4
left over: every event of the S3 burst joined (`-spool`, the burst
under the agent), plus the plain unfolded burst, which ae4 found over
2 µs at load ≥ 10 (the cuts: commit `1426147`). Same host (i7-1185G7,
8 CPUs, kernel 7.2.15, JDK 25), as busy as ever: load 5.8–13 from
other agents' builds and tests.
Samples, scripts and benchmark output: `spikes/s3/results-ud4/`.

`measure.sh` gained what the job needed: `S3_AGENT=1` runs every burst
under the agent and gives each traced run a fresh `-spool` dir (the
agent maps its ring in `premain`, so the dir must exist before the JVM
starts); `S3_TRACER` points it at another build, for an A/B;
`S3_PERF=1` counts the tracer's instructions (`perf stat`) from its
ready file to its exit; each traced sample records `attributed=`. That
last one matters: the repo's `iormesh-agent.jar` was stale at first (a
ring version the tracer skips), so the first `-spool` profile here
joined nothing — `measure.sh` now warns when an agent run attributes
no event; rebuild the jar with `mage agentBuild`.

**Where the time went** (`iormesh run -cpuprofile`, one 1-thread
`-spool` run of the commit before ud4, `e57633d`, load 4.0, 3.9 µs per
event; of 5.6 s of samples 1.7 s are the BPF load):

1. `clock_gettime(CLOCK_BOOTTIME)` as a real syscall, once per event:
   0.55 s. `JavaJoin.Event` read the boot clock to see whether a spool
   scan was due, and `x/sys/unix.ClockGettime` never takes the vDSO
   (Go's vDSO path serves only the runtime's own monotonic and
   realtime reads);
2. NDJSON encoding: 0.69 s, `strconv.AppendUint` 0.42 s of it (the
   19-digit `boot_ns`/`wall_ns`, `pid_incarnation`);
3. the Java mark's decode: 0.24 s — the generated decoder is
   `binary.Read`, by reflection, two allocations per record, and with
   `-spool` every hooked call sends one;
4. the join's own state: the marks map with its 16-byte struct key
   (hashed field by field), the bracket list growing, a map store per
   bracket and per mark;
5. smaller: the event, ~400 bytes, copied by value at every hop
   (correlator queue → `Sink.Event` → the join's queue → `Sink.Event`
   → the writer), and `sync.Pool` Put on the consumer's goroutine
   while every Get ran on the reader's, so each Get stole across Ps.

**Cuts** (all byte-identical output: the encoder's `json.Marshal`
property test, its fuzz target and the export goldens are unchanged
and pass):

- `bootClock` (`correlate/bootclock.go`): `Event` reads the boot clock
  once a second and extrapolates on Go's monotonic clock in between
  (vDSO, ~30 ns against ~260 ns); it never runs ahead of the boot
  clock, so a scan is never claimed for a moment it did not cover.
  `Poll`/`Close` still read it precisely;
- `types.NewJavaMarkEventFast`: field by field, 24 ns against
  340–410 ns and no allocation;
- marks per thread in a FIFO (`markQueue`): a thread's marks arrive,
  and its events leave the correlator, in enter order, so the join
  only looks at the head; bracket lists searched from a per-thread
  hint (the last answer) by galloping (`exitSearch`), pruned by
  slicing instead of moving the kept part down (that move would have
  copied 10 s of brackets every 50 ms once a run outlived `Retain`);
  map stores replaced by lookups where the value rarely changes;
- `schema.Sink.Event(*Event)`: the event is lent for the call, so it is
  copied once, into the join's queue;
- `appendUint`: eight digits per group by fixed-point multiplication,
  no division by 100 (1287 against 1874 instructions for the event's
  11 integers), and an 8-byte-at-a-time safe-ASCII scan in
  `appendJSONString`;
- `bpf.Tracer.Release`: the session hands each handled batch back; the
  reader recycles its records and reuses the batch, so pool Get and Put
  run on one goroutine; the decoder table is a slice, not a map; the
  correlator keeps its last process lookup.

**Benchmarks** (`spikes/s3/results-ud4/bench/`): user instructions and
cycles per op from `perf stat` over two run lengths (setup cancels),
pinned to CPU 3, three interleaved rounds; the instruction counts are
load-independent, the cycles are not. `JavaJoinJoined` is new (every
event marked, bracketed and joined; built onto the base commit for the
"before" column):

| Benchmark (per op) | Instructions before → after | Cycles before → after (medians) |
|---|--:|--:|
| `AppendEvent` | 3409 → 2965 | 1353 → 1449 |
| `NDJSONSink` (validate + encode + buffer) | 3920 → 3369 | 1789 → 1467 |
| `SyscallBurst` (per record: decode, correlate, encode) | 3242 → 2843 | 1802 → 1616 |
| `JavaJoinPassThrough` | 765 → 509 | 496 → 329 |
| `JavaJoinJoined` | 5362 → 5288 | 3775 → 2696 |

`JavaJoinJoined`'s instructions are a third the Go GC's (the fake
ring boxes its records, the held queue is pointerful); the cut there
shows in cycles. The clock (`BenchmarkBootClock`) and the mark decode
(`BenchmarkJavaMarkEventDecode`) are the two items the Go benchmarks
above do not see.

**End to end, by #25's method** (`measure.sh S3_MODES=traced
S3_TRACER_FLAGS=-aggregate=false S3_OPS=500000 S3_THREADS="1 4"
S3_PERF=1`, with and without `S3_AGENT=1`), five rounds, the build
before ud4 and ud4's run interleaved, base first (`results-ud4/ab.sh`):

| Run | Threads | ns/event before → after (median of 5) | Instructions/event before → after | Lost before → after | Load |
|---|--:|--:|--:|--:|--:|
| `-spool`, agent | 1 | 4360 → 4699 | 18435 → 12227 | 0 → 0 | 5.8–8.4 |
| `-spool`, agent | 4 | 5675 → 3860 | 18653 → 12463 | 6–56 % → 0 | 6.4–9.0 |
| plain | 1 | 2030 → 1690 | 8188 → 6623 | 0 → 0 | 6.3–9.7 |
| plain | 4 | 2895 → 2282 | 7984 → 6435 | 31–60 % → 30–58 % | 6.5–10.2 |

The instructions per event fell by a third with `-spool` and a fifth
without, the same in every run. The CPU time did not follow at
`-spool`, 1 thread, and that row is not to be trusted: in all five of
its rounds the burst *itself* ran 1.5–2× slower in ud4's half — its
untraced run as much as its traced one, though no tracer is attached
to the untraced run — so the host, not the build, was slower then, and
a slower target spreads the tracer over more small batches. A second
interleaved set took it by `perf stat` on the tracer alone
(`results-ud4/perf-stat/`, `pstat.sh`, five pairs per thread count,
load 8.4–13.3): task clock 7592 → 3793 ns per event at 1 thread and
9200 → 5573 at 4; cycles 13525 → 8220 and 13545 → 8525 per event.
At 4 threads the consumer now keeps up with the unfolded burst under
the agent: no lost records, against 6–56 % before.

**Verdict**: `-spool` is **not** under 2 µs. With every event joined
the tracer spends ~8.2–8.5 k cycles per event (from ~13.5 k), 3.8 µs
at 1 thread and 5.6 µs at 4 at load 8–13 (task clock), 3.9–4.7 µs at
load 6–9 (`measure.sh`); even with the core to itself that cycle count
is ~2.7 µs at 3 GHz. Without `-spool` the unfolded burst is under budget
at 1 thread (1.7 µs) and over at 4 (2.28 µs, with 30–58 % of the
records lost to the full ring), at load 6.5–10. The limit is unchanged. What is
left is spread thin (a `perf record` of cycles after the cuts: no
symbol much above 5 %): NDJSON encoding ~25 % (string escaping, integers,
the args object), the correlator's pairing ~15 %, the join ~15 %
(bracket search, the stack and thread lookups, one `schema.Java`
allocation per joined event), ring reading and decoding, the agent
ring's drain (each record boxed into an interface), `write(2)`. The
next large step is the output format (M4's parquet segments replace
the per-event text encoding), not another micro-cut; the default fold
(`-aggregate`) still keeps a real JVM's hot loops out of the join.
