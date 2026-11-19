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
| bystander | ≤ 1.25× at 4 threads (CPU-time median of ≥ 10 pairs) | **passes**: 1.19× and 1.15× in two sets of 10 pairs; wall clock 1.21× and 1.14× |

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
a mark: twice the ring records of the run before. A check run
joined 40 023 of its 40 000 bursts' calls through the mark.
Samples are not kept in the repo.

| Run | tracer ns/event, median | Range | burst CPU traced/untraced, median of pairs | Lost |
|---|--:|--:|--:|--:|
| before td4 (allowlist only), load 7.6–25 | 4260 | 3170–7390 | 1.15× (0.62–1.93) | 0 |
| td4, the mark (load 10.7–15.3) | 4970 | 4460–8200 | 1.29× (0.81–1.48) | 0 |

On this host only the large differences mean anything, and the
pair ratios span 3× in both rows. Read so: the mark adds about
0.7 µs of tracer CPU per marked event (decoding the extra record
and pairing it with its event), on top of 2b4's join,
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
