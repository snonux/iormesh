# S3 — overhead and volume budget

Result of spike S3 (docs/milestones.md, "Spikes"; decision #25). It measures
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
[decisions.md](../decisions.md).

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
quiet, so docs/decisions.md #25 now measures them load-tolerantly — the
tracer's own CPU time per event, and the bystander as the median
of ≥ 10 interleaved, pinned traced/untraced pairs on CPU time,
with the wall-clock ratio recorded alongside. The limits are
unchanged. Task md4 took both and both pass — the rough
per-event figure above was an artefact of its method; see
"Follow-up (md4)" below.

## Follow-up (md4): the timing items by the paired CPU-time method

Task md4 took decision #25's two timing items by the load-tolerant
method decision #25 adopted on 2026-11-19. The host was as busy as ever
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
Task ud4 cut it (to 3.8 / 5.6 µs, "Follow-up (ud4)") but not under
the budget; the item moved to task ee4, which re-measures it on M4's
segment writer and gates M4's acceptance.

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
classic `raw_syscalls:sys_enter`/`sys_exit` tracepoints
(docs/output-contract.md, "Syscall handlers run behind `raw_syscalls`
dispatchers"), so every syscall on the host now meets a BPF program while a
trace runs, whether or not any family traces its kind. Task ae4 re-ran
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
integration suite would have to cover on each kernel line (docs/decisions.md,
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
≥ 10 (ae4); the limit stays, and task ee4 re-measures it on M4's
segment writer (it gates M4's acceptance). Loss in the unfolded
4-thread runs is the full ring, as in md4; skipped runs were 0 in every run.

| #25 item | Budget | ae4 verdict |
|----------|--------|-------------|
| bystander | ≤ 1.25× at 4 threads (CPU-time median of ≥ 10 pairs) | **passes** with 8e4's dispatch: 1.16× and 1.14× in two sets of 10 pairs; wall clock 1.16× and 1.19× (guard not tripped) |
| bystander, untraced kind | recorded, not gated | ~2.0× on a pure `getppid` loop (+0.2–0.3 µs per call), the perf-tracepoint path's price |
| tracer CPU | ≤ 2 µs per emitted event (median) | passes at moderate load (md4: 1.2/1.7 µs at load 4.6–9), **over at load ≥ 10** (ae4: 3.0/3.1 µs; A/B 2.08/2.15 µs after 8e4, 2.18/3.55 µs before); no 8e4 regression; task ee4 re-measures on M4's segment writer (gates M4 acceptance) |

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

Status (2026-11-19): M3 is accepted with the tracer-CPU item open and
over (plain unfolded 2.28 µs at 4 threads, `-spool` 3.8 / 5.6 µs);
it is split to task ee4 on the M2 pattern, gating M4's acceptance,
limit unchanged (docs/milestones.md, M3 status).

## Follow-up (xe4): the cost of `args.tcp_seq`

Task xe4 makes every unfolded TCP `write`, `writev`, `sendto`,
`sendfile64`, `read`, `readv` and `recvfrom` read its socket's
sequence field at enter and exit (internal/c/tcpseq.c). It adds
work in two places: in the target's syscalls (the BPF hooks — on
every hooked call a typed fd walk to the inode's mode, on a TCP
socket a per-tid park and the exit's re-read) and in the tracer
(one more field per data event). Measured A/B against the build
before it (`0733b36`), interleaved, pinned as `measure.sh` pins,
load 9.7–18 throughout — a busy host, so CPU times spread and the
instruction counts (`perf stat`) are the figures to trust.
Samples, the TCP load and its driver: `spikes/s3/results-xe4/`.

**`syscall-burst`, unfolded** (decision #25's item; file I/O, so
the hooks only walk to the inode's mode and park nothing;
`S3_TRACER_FLAGS=-aggregate=false S3_OPS=500000`, 5 rounds of one
run each per build): tracer user instructions per event 6.56 k
(base) and 6.57 k (new) at 1 thread, 6.40 k and 6.45 k at 4 —
unchanged. Tracer CPU medians 3.24 / 3.15 µs (base) and 2.49 /
2.65 µs (new) at 1 / 4 threads: noise of the load, both builds
over the 2 µs budget at this load as before (the item stays with
task ee4, limit unchanged). The burst's own CPU, where the BPF
hooks run, moved within its spread.

A first build carried the sequence number into the correlator's
per-thread enter record, which pushed that struct from 120 to 136
bytes — past Go's 128-byte limit for map values stored inline, so
every syscall's enter then cost an allocation: +1.1 k tracer
instructions per event (+17 %) on the burst, which has no TCP at
all. The sequence number now goes straight from the exit record to
the event (correlate/syscalls.go, `exitSeq`), and the struct stays
at 120 bytes, with a comment saying why.

**TCP loopback, unfolded** (`tcpload.c`: one writer thread sends
100 000 × 64 B, one reader reads them, both traced; a 1 ms pause
every 100 writes keeps the ring from overflowing, so no record is
lost; 200 014 events per run, 200 000 of them with `tcp_seq`; 5
rounds):

| | base | new |
|---|--:|--:|
| tracer user instructions per event | 11.2 k | 11.5–11.7 k (+3 %) |
| tracer CPU per event (median; includes the paced idle wake-ups) | 5.15 µs | 4.9 µs (noise) |
| target kernel instructions per data call (untraced: ~2.8 k) | ~4.45 k | ~4.95 k (+~500, ~+11 %) |
| target CPU (median) | 490 ms | 520 ms |

A first design sent the sequence number in a record of its own,
right before the exit record: three ring records per call instead
of two. Each record costs the tracer one ring callback across cgo,
and that design measured +2.5 k tracer instructions per event
(`tcp-paced-own-record.txt`). The record now *is* the exit record
(`tcp_seq_event`, a `ret_event` plus the socket and the number, 48
bytes instead of 40), which leaves the +~400 instructions above:
decoding 8 more bytes, the `tcp_sock` check in the correlator, and
encoding one more field.

**Verdict**: decision #25's tracer-CPU item is unchanged by the
capture — the burst's per-event instructions are the same. On TCP
traffic the tracer pays ~3 % more per data event and the traced
process ~500 kernel instructions per unfolded data call. The
default fold keeps hot loops out of both, and the calls that clock
reconciliation needs — request/response calls that wait on the
peer — are mostly the ones that stay single. Not all: a read or
write the fold held at its enter on a hot streak parks nothing, so
when it then blocks past 1 ms (the read waiting on the peer right
after a burst) it is sent alone without `tcp_seq`. Parking held
enters as well would cover it, at the price of the walk on every
folded call — not taken.

## Follow-up (ee4): tracer CPU on M4's segment writer

Task ee4 re-measured decision #25's tracer-CPU item on M4's output
path — parquet segments under `-trace-dir`, no `-out` — and cut the
segment writer, which task 8b4 had measured at ≈ 3× the NDJSON sink
per event. Same host (i7-1185G7, 8 CPUs, kernel 7.2.15, JDK 25),
busy as ever. Samples, the A/B script and benchmark output:
`spikes/s3/results-ee4/`.

`measure.sh` gained `S3_SEGMENT=1`: the traced run writes segments to
a `-trace-dir` in its work dir with `-disk-limit 0` (the developer
laptop's root is 95 % used, over the watchdog's 90 % default; the
256 MiB floor stays), and the emitted events are counted from the
published segments' footers (`spikes/s3/rowcount.go`, run with `go
run`). `S3_PERF=1` now records cycles (user and kernel) beside the
instructions.

**Where the time went** (`-cpuprofile` and `perf stat`, the build
before ee4, `2d04f09`): on the plain unfolded burst the tracer spent
12.3 k instructions per event on the segment path against 6.6 k on
NDJSON, and lost 20–25 % of the records at 1 thread, which NDJSON
never did. `segment.Writer.Event` was three quarters of the per-event
work: `parquet-go`'s reflection over `schema.Event`'s 68 leaf columns
(each nil pointer — the args, the java, http, session, otel and dns
objects — walked row by row into ~50 null levels), then the page flush:
level histograms, page bounds computed twice, and zstd. Neither the
library's `WriteRows` (`parquet.Value` rows, 17.8 k instructions per
event) nor `DataPageStatistics(false)` (−7 %) came near.

**The cut**: the events file now has its own columnar encoder
(`internal/segment/column.go`, `eventcols.go`, `eventfile.go`; the
tables keep `parquet-go`, their rows are rare):

- a hand-written mapping appends each field to a typed column buffer
  — a definition level per slot of an optional leaf, a repetition
  level per list element, values only for set slots — with no
  reflection and no `parquet.Value`. It is parquet-go v0.29's mapping
  for the struct tags (a nil pointer and an empty list are null; an
  optional non-pointer field is always present, its zero value
  included), checked against the schema the library derives (path,
  physical type, levels) when a writer starts, and held to the
  library's own `Deconstruct`, value by value, over 3 000 random
  events by `TestEventColumnsMatchDeconstruct`;
- slot and null counts are derived from the levels once per row group,
  not counted per row: most of a syscall event's columns are nulls;
- per row group (8192 rows, or 4 MiB buffered) and column: a
  dictionary page for the six dict-tagged strings, one data page v2
  (levels RLE, values PLAIN, `RLE_DICTIONARY`, or `DELTA_BINARY_PACKED`
  for `seq`, `boot_ns`, `wall_ns` and `latency_ns`), zstd. Delta
  encoding mattered: as PLAIN int64 those four high-entropy columns
  cost zstd ~13 ns per byte, ~100 ns per event each, on a real
  `-spool` segment. The footer's schema, column orders and created_by
  are copied from an empty file `parquet-go` wrote, so the file
  describes itself as the library's did. Not written: min/max page
  statistics (null counts only), column and offset indexes;
- 8192 rows per row group, not more: 16 k and 32 k saved instructions
  but cost cycles once the open row group's buffers outgrew L2.

The output is the same records: the round-trip tests read the files
back with `parquet-go` and compare NDJSON lines, rich events and 16 k
random ones over several row groups included; and the files are
smaller (plain at 1 thread 8.1 → 5.2 B/event, `-spool` 10.2 → 7.0).

**Benchmarks** (`results-ee4/bench.txt`, `perf stat` per op as ud4's
`insn.sh`, pinned to CPU 3; `BenchmarkWriterJoinedEvent` is new, the
read attributed java with a four-frame stack):

| Benchmark (per op) | Instructions before → after | Cycles before → after |
|---|--:|--:|
| `WriterEvent` | 11 151 → 2 483 | 6 071 → 1 299 |
| `WriterJoinedEvent` | 12 619 → 2 777 | 7 244 → 2 148 |

For comparison, the NDJSON sink (`schema.BenchmarkNDJSONSink`) costs
3 369 instructions per event (ud4).

**End to end, by #25's method** (`results-ee4/ab.sh`: `measure.sh
S3_SEGMENT=1 S3_MODES=traced S3_TRACER_FLAGS=-aggregate=false
S3_OPS=500000 S3_PERF=1`, with and without `S3_AGENT=1`; five rounds,
the build before ee4 and ee4's interleaved, base first; tracer CPU
from its ready file to its exit per emitted event, medians of 5;
instructions and cycles user + kernel):

| Run | Threads | µs/event before → after | Instructions/event | Cycles/event | Lost before → after | Load |
|---|--:|--:|--:|--:|--:|--:|
| plain | 1 | 2.68 → **0.85** | 15 187 → 5 765 | 5 366 → 2 570 | 20–25 % → 0 | 2.8–3.4 |
| plain | 4 | 3.14 → **1.62** | 15 380 → 5 963 | 6 092 → 3 098 | 67–71 % → 27–46 % | 2.5–3.6 |
| `-spool`, agent | 1 | 4.43 → **2.89** | 22 836 → 11 503 | 9 583 → 5 834 | 0 → 0 | 3.1–41 |
| `-spool`, agent | 4 | 5.82 → **4.82** | 23 320 → 12 027 | 12 540 → 7 652 | 0 → 0 | 41–46 |

(The base's instruction counts look high against its cycles: the
library's reflection loops run at an IPC near 3.) The last six
`-spool` runs met another agent's test run at load 33–46; under that
load the join attributed only 67–89 % of the events in three base and
two ee4 rounds at 4 threads — the host, not a build.

**Verdict**: plain unfolded **passes** at 1 and 4 threads (0.85 and
1.62 µs; it now spends fewer instructions per event than the NDJSON
path did, 5.8 k against 6.6 k, and at 1 thread loses no record).
`-spool` with every event joined is **over**: 2.9 µs at 1 thread
(load 3–41) and 4.8 µs at 4 (load 41–46). In cycles it is 5.8 k and
7.7 k per event; this host's tracer core runs at an effective ~2 GHz
under load (cycles over CPU time), so 2 µs needs about 4 k cycles,
a further third off. The writer is no longer the problem — of the
joined event's cost the writer is ~2.8 k instructions; the rest is the
agent ring's drain, the join and the correlator, which ud4 already
cut. What is left there, from a `-spool` profile after this cut: the
agent ring's drain boxing every record into an interface (an
allocation per bracket), the bracket list's growth (`slices.Insert`
and `growslice` copying 10 s of 72-byte brackets, ~8 % of the loop),
one `schema.Java` allocation per joined event, the bracket search and
the per-tid map lookups — no single item above ~10 %. The limit was
left unchanged and the item went to the owner, whose decision follows
("Follow-up (ee4): owner decision").

### Follow-up (ee4): independent readers

`parquet-go`'s reader ignores most of the metadata the new encoder
writes by hand — a chunk's num_values, the null counts, the encodings
list, the sizes and offsets — while Arrow stops reading a chunk at its
num_values and DuckDB prunes `IS NULL` by null_count, and
docs/schema.md promises DuckDB and pandas can query the files. So the
files were read with two readers that share no code with
`parquet-go`: pyarrow 26.0.1 and DuckDB 2.0.0, from a Python venv
(`TestIndependentReaders`, `internal/segment/indep_test.go` with
`testdata/pqcheck.py`, opt-in with `IORMESH_PARQUET_CHECK=<venv>`,
docs/testing.md). Two files: the rich events plus 16 884 random ones
over several row groups (nested java stacks, every nullable arg
including the four tcp_state fields, http, otel and dns objects,
uint64 values past 2⁶³), and 8 202 burst reads whose args and
objects are all null in every row group. Both readers return every
row equal to the NDJSON of the same events, and DuckDB's `IS NULL`
on all 31 nullable columns and objects counts exactly the events
NDJSON omits them in. A planted wrong null count made DuckDB's
`IS NULL` answer 0 rows instead of 8 525, a chunk num_values one
short made DuckDB refuse the file — the check catches both.
`TestEventFileMetadata` checks the same metadata in-process: per
chunk, the pages laid end to end fill exactly its compressed size,
headers and bodies sum to its uncompressed size, the data pages'
num_values and num_nulls equal the chunk's and the slots and nulls
its decoded definition levels hold, and row groups tile the file up
to the footer. Two writes of the same events give the same bytes
(`TestEventFileDeterministic`).

### Follow-up (ee4): owner decision

Owner decision (2026-11-20, delegated to an advisor): the original
≤ 2 µs covered `-spool` with every event joined and was not met
(2.89 / 4.82 µs after ee4). The limit is hereby changed for that case
only: ≤ 4 µs per emitted event and ≤ 12.5 k instructions per event,
valid at load ≤ 10 with every event attributed and zero loss. This is
a relaxation, argued by the extra agent records each joined event
consumes (one core still sustains 250 k joined events/s) and by the
default fold keeping JVM hot loops out of the join. Plain stays
≤ 2 µs. The decision came with one bounded round of the join cuts
listed above first (docs/decisions.md, decision #25 item 1 and M4 status).

**The cuts** (commit `83b5c61`; output unchanged — the join, ring
and segment tests, the integration suite):

- the agent ring's drain hands brackets and watermarks to a typed
  visitor (`agentspool.Ring.DrainTo`) instead of boxing every record
  into a `Record` interface — an allocation per record;
- a thread's brackets live in 1024-bracket chunks, pruned a chunk
  at a time with one spare kept for reuse, instead of one slice
  regrown by copying all of Retain's brackets;
- the join lends one `schema.Java` and one `schema.Otel` per emitted
  event instead of allocating them (`schema.Sink` now says the
  objects an event points to are borrowed too).

| Benchmark (per op) | Instructions before → after | Cycles before → after |
|---|--:|--:|
| `JavaJoinJoined` | 3 944 → 3 369 | 2 427 → 1 850 |
| `DrainBrackets` (a bracket and a watermark) | 2 237 → 1 283 | 1 350 → 655 |

**Valid re-measure** (`results-ee4/ab-valid.sh`, `results-ee4/valid/`):
the build before the cuts (`a7194d6`) and after (`83b5c61`)
interleaved, five rounds, segment path, by the decision's rules — a
run counts only at load ≤ 10 (before it starts and as recorded after
it), with zero lost records and, with the agent, every burst event
attributed (≥ 2 × ops); none was rejected. The plain unfolded burst
at 4 threads overruns the ring by design, so its loss is recorded,
not gated. Medians of 5, instructions and cycles user + kernel:

| Run | Threads | µs/event before → after | Instructions/event | Cycles/event | Load | Budget | Verdict |
|---|--:|--:|--:|--:|--:|--|--|
| `-spool`, every event joined | 1 | 2.33 → **1.90** | 11 498 → 11 100 | 5 564 → 5 581 | 4.4–7.5 | ≤ 4 µs, ≤ 12.5 k | pass |
| `-spool`, every event joined | 4 | 3.52 → **3.75** | 12 240 → 11 839 | 8 522 → 7 735 | 5.2–8.7 | ≤ 4 µs, ≤ 12.5 k | pass |
| plain | 1 | 1.32 → **1.59** | 5 741 → 5 775 | 3 137 → 2 915 | 5.1–8.0 | ≤ 2 µs | pass |
| plain | 4 | 1.78 → **1.95** | 5 772 → 5 826 | 3 357 → 3 318 | 4.8–5.4 | ≤ 2 µs | pass |

Every `-spool` run attributed all 1 000 000 burst events (1 000 023
and 1 000 040–47 with the JVM's own I/O) and lost none; plain at 4
threads lost 30–50 % of records unfolded in both builds, as before.
In situ the cuts show less than in the benchmarks: −3 % instructions
with `-spool` and −9 % cycles at 4 threads; CPU time moved within its
spread (at 4 threads 2.8–5.8 µs per run, the median 3.75 µs close to
the limit).
The plain path did not regress: its instructions are unchanged
(+0.6–0.9 %) and its cycles slightly lower; its CPU-time medians
moved up within their spread (0.9–1.7 µs per run at 1 thread and
1.3–2.6 µs at 4, across both builds).

**The default fold** (`-spool`, `-aggregate` on, 4 threads, three
rounds each build, same rules less the every-event-attributed one, as
folded calls are never joined): the 1 000 000 burst calls leave
1 911–17 140 events, 1 343–13 492 of them attributed, and the tracer
spends 0.28–0.97 s for the whole burst after the cuts (0.46–0.77 s
before) — 0.3–1.0 µs per traced call, lost 0. Per emitted event that
is 57–152 µs, a figure the fold makes meaningless (a folded event
stands for hundreds of calls): what the fold shows is that a JVM's
hot loop sends the join a few thousand events, not a million.

**Verdict**: under the changed limit `-spool` with every event joined
passes at 1 and 4 threads (1.90 µs / 11.1 k and 3.75 µs / 11.8 k
instructions), plain passes 2 µs at 1 and 4 threads (1.59 / 1.95 µs).
Decision #25's tracer-CPU item is met on the segment path.

## Follow-up (4f4): PID-namespace-aware targeting

Task 4f4 (docs/milestones.md M7, decision #13; docs/implementation.md M7 step
1) numbers every task by its pid in the tracer's PID namespace.
On the host that namespace is the initial one and the BPF side is
the old host read, pruned at load; in a namespace every `filter()`
— each traced kind's enter and exit of every task on the host —
has to find the current task's number there. Same host as above,
samples in `spikes/s3/results-4f4/` (`old` = the build before the
task, `host` = this build on the host, `ns` = tracer and burst in
`unshare --pid --fork --mount-proc`), all `-aggregate=false`.

**Kernel instructions per untraced `close(-1)`** (`closeloop.c`,
3 M calls pinned to one CPU beside a tracer of an idle `sleep`;
`perf stat -e instructions:k`; three interleaved rounds, which
agreed to ±0.5 %; `closeloop-kinsn.txt`):

| | per call | over no tracer |
|---|--:|--:|
| no tracer | 399 | — |
| build before 4f4, host | 1380 | +981 |
| this build, host | 1397 | +998 |
| namespace, walk + helper, no cache | 1800 | +1401 |
| namespace, with `ns_ids_cache` | 1490 | +1091 |
| loop outside the namespace, no cache | 2011 | +1612 |
| loop outside the namespace, with cache | 1287 | +888 |

The first namespace build walked `pids[...]->numbers[level]` for
every call (eleven probe reads): an untraced burst in the
namespace then paid 1.9× its CPU time (`bench1-*`). The helper
fast path (`bpf_get_ns_current_pid_tgid`) and the per-CPU cache
brought the in-namespace bystander to +93 kernel instructions per
call over host mode (+9 % of the tracer's per-call cost), and a process outside the namespace — a node
process beside a pod's tracer — below it (its cached answer is 0,
and no `target_map` lookup follows).

**`syscall-burst`, unfolded** (5 interleaved rounds, medians):

- Host mode (`bench-*`, load 4.5–12): tracer user+kernel
  instructions per event 7.0 k (old) and 7.0 k / 6.9 k (new) at
  1 / 4 threads — unchanged; tracer CPU 1.72 / 2.02 µs (old) and
  1.75 / 2.16 µs (new); bystander CPU ratio 1.23 / 1.14 (old) and
  1.20 / 1.21 (new), 10 pairs each — unchanged within noise.
- Namespace mode with the cache, re-measured on a quiet host
  (`bench3-*`, build `dfa551f`, host and namespace mode
  interleaved, 5 rounds, 10 bystander pairs per thread count,
  both bursts' work dir on the same btrfs, load 1.6–2.6
  throughout, checked before every run):

  | | bystander CPU ratio (pairs over 1.25) | wall clock | tracer µs / instructions per event |
  |---|--:|--:|--:|
  | host mode, 1 thread | 1.27 (6 of 10) | 1.30 | 0.95 / 6.8 k |
  | namespace, 1 thread | 1.25 (5 of 10) | 1.28 | 0.92 / 7.1 k |
  | host mode, 4 threads | 1.20 (3 of 10) | 1.23 | 1.05 / 6.7 k |
  | namespace, 4 threads | 1.30 (7 of 10) | 1.27 | 1.09 / 7.0 k |

  The namespace medians sit at and over 1.25×, and the wall-clock
  medians (1.28 / 1.27) trip #25's guard: **the namespace
  bystander item is open**. Host mode beside it measured 1.27 /
  1.20 (wall 1.30 / 1.23), so this set does not tell the
  namespace's ~90 extra instructions per call apart from the
  host's own narrow margin; it does not pass either way. Tracer
  CPU per event passes in both modes (≤ 1.1 µs), lossless at 1
  thread; at 4 threads one run in five lost records in each mode
  (unfolded, recorded, not gated). An earlier set (`bench2-*`,
  1.22 / 1.23) ran at load 25–31 and is no verdict; the sets
  before it ran the namespace burst on tmpfs or without the
  cache.

**Load**: the walk inlined into every handler made the object's
load cost ~6× host mode's kernel CPU (12.4 s against 2.1 s); as
global functions verified once per program, each opening with the
host-mode guard, three interleaved loads gave 2.4 / 5.1 / 3.0 s
(old), 2.6 / 5.2 / 4.4 s (host mode) and 5.0 / 7.1 / 3.9 s
(namespace mode) of kernel CPU — a busy host's spread
(`load-cpu.txt`).

**Verdict**: host mode is unchanged — same instructions per
traced event, per bystander call within 2 %, the same bystander
ratio as the build before. In a namespace the bystander pays ~90
more kernel instructions per traced-kind call than in host mode
(+9 % of the tracer's per-call cost), and the tracer's per-event
work is the same. The namespace bystander item of #25 is **open**:
on a quiet host its medians are 1.25× / 1.30× with wall clock
1.28× / 1.27×, at and over the limit. Ways down, untried: skip
the walk for tasks at a level below the tracer's before the
helper call, or let the dispatcher answer the target-set test
once per syscall instead of each handler. (Task 9f4 took the
second, with a bitmap in front of the lookup; the item passes
there — "Follow-up (9f4)" below.)

## Follow-up (9f4): cutting the namespace bystander's cost

Task 9f4 took on 4f4's open item. Same host, samples in
`spikes/s3/results-9f4/` (`base` = `98747ba`, the build 4f4 left;
`new` = this task's), every tracer `-aggregate=false`, the
namespace runs under `unshare --pid --fork --mount-proc`.

**Where the cost went.** `perf record -e instructions:k` on the
`close(-1)` loop beside a host tracer put ~21 % of each call's
kernel instructions in `target_map`'s hash lookup
(`__htab_map_lookup_elem` and its bucket walk) — the handlers'
`filter()`, at enter and exit, for a task that is no target. The
rest of the ~1000 instructions the tracer adds is mostly the
classic `raw_syscalls` tracepoints' own path (`trace_call_bpf`,
`perf_trace_run_bpf_submit`, `trace_syscall_enter`/`_exit`),
which 8e4 chose for losslessness and which no BPF change
reaches. In a namespace, `iormesh_ns_cached_ids` (a global
function call per handler) was the extra ~9 %.

**Three cuts**, each checked by kernel instructions per call:

1. A *target hint* (`internal/c/targethint.c`): one bit per pid
   in front of `target_map`. A clear bit rules a pid out with an
   inlined array access; only a set bit goes on to the lookup,
   which still decides. The kernel's bitmap is written only by a
   bit's owner (task_newtask for a child, the exec watch for its
   process, the group-dead exit clears it — only if its
   `target_map` delete succeeded, since kernels without the
   tracepoint's `group_dead` can report two threads' exits as
   group-dead, and a second subtraction would borrow a
   neighbour's bit) with `BPF_XADD`, so 5.8 stays the floor;
   AddTarget's bits live in a second,
   userspace-only map. 1397 → 1288 per call in host mode (perf's
   21 % share was sampling attribution; the lookups cost ~55 each),
   1495 → 1386 in the namespace.
2. The dispatcher answers the scope test before the tail call
   (`iormesh_dispatch_in_scope`, dispatch.c): the tracer itself
   and a hint-negative task never enter a handler. Alone, this
   made every syscall of an *untraced* kind pay the test
   (a `getppid` loop +115 instructions per call on the host,
   +200 in a namespace), so the dispatcher first asks a mirror of
   its program array (`syscall_{enter,exit}_traced`, filled with
   the slots) whether the syscall is traced at all.
3. The dispatcher's tgid comes inlined: in host mode a direct load
   of the BTF task's `tgid` instead of the helper call, in a
   namespace the per-CPU cache hit read in place (only the two
   dispatchers inline it; the handlers keep the global function,
   whose verification cost 4f4 measured).

**Kernel instructions per call** (`kinsn.txt`, three interleaved
rounds, identical to ±1):

| | `close(-1)` | over no tracer | `getppid` |
|---|--:|--:|--:|
| no tracer | 400 | — | 429 |
| base, host mode | 1397 | +997 | 1137 |
| base, in the namespace | 1495 | +1095 | 1147 |
| base, outside the namespace | 1292 | +892 | 1147 |
| new, host mode | 1233 | +833 | 1128 |
| new, in the namespace | 1306 | +906 | 1140 |
| new, outside the namespace | 1205 | +805 | 1140 |

The tracer's cost to an in-namespace bystander's traced-kind call
fell by 17 %, now below what host mode cost before; host mode fell
by 16 %; untraced kinds got slightly cheaper. The namespace still
pays ~73 more per call than host mode (the cache's key compare
against one load). The whole `syscall-burst` (200 k ops, the JVM
run's kernel instructions, three rounds) says the same: no tracer
~651 M; base +404 M (host) / +442 M (namespace); new +336 M /
+366 M at 1 and 4 threads alike.

**`syscall-burst`, bystander CPU ratio** (`bench-*.txt`: base and
new, host and namespace, interleaved in one set — 2 sets × 5
rounds, 20 pairs per thread count; the first set's last rounds ran
at load 8–9 under another session's builds, the second waited for
load ≤ 4 before every run and saw 2.1–5.3):

| | 1 thread (pairs > 1.25) | wall | 4 threads (pairs > 1.25) | wall |
|---|--:|--:|--:|--:|
| base, host mode | 1.24 (10 of 20) | 1.23 | 1.16 (4 of 20) | 1.18 |
| base, namespace | 1.23 (7 of 20) | 1.24 | 1.23 (8 of 20) | 1.25 |
| new, host mode | 1.19 (3 of 20) | 1.19 | 1.15 (4 of 20) | 1.15 |
| new, namespace | 1.23 (8 of 20) | 1.22 | 1.23 (7 of 20) | 1.21 |

The second set alone: new namespace 1.21 / 1.23 (wall 1.22 /
1.23), base namespace 1.23 / 1.23 (wall 1.24 / 1.25), new host
1.18 / 1.18. Tracer CPU per event and instructions per event are
unchanged (7.0 k); loss in traced runs as before (none at
1 thread, some 4-thread runs lossy in every build, not gated);
load-and-attach CPU within the busy host's spread of the base.

**Verdict**: the namespace bystander item of #25 **passes**:
1.23× at 4 threads (and at 1), wall clock 1.21× / 1.22×, under
the 1.25× guard. Honestly told, the margin is thin and the burst
ratio does not separate the builds the way the instruction counts
do: the base build measured 1.23× / 1.23× in the same set (wall
1.24× / 1.25×, at the guard), where 4f4's set had 1.25× / 1.30×,
and host mode on this machine sits at 1.15–1.24× by itself. The
firm result is the kernel cost: the tracer now adds 17 % fewer
kernel instructions to every bystander syscall in either mode,
and an in-namespace bystander pays less than a host-mode one did
before. What is left is mostly the tracepoint path itself.

## Re-measure on Rocky 9.9 with the msghdr handlers (ak4, 2026-11-26)

S3 and every follow-up above ran on the 7.2 laptop. After them came
the eight default sendmsg/recvmsg/sendmmsg/recvmmsg handlers with
their msghdr peer read (oh4, nk4: a second, 232-byte address record
per call on a non-stream socket; an mmsg exit reads up to 1024
`msg_len`s), the `MSG_PEEK` mark (ok4), the held-enter mark (ti4),
the FIN rule (wi4) and the late attaches of `run -agent` (lh4).
Task ak4 re-ran decision #25's items by the methods above, on the
main target instead of the laptop (owner decision), A/B against
the build before those changes, and added the loads they are about.

**Conditions.** `iom1`: Rocky 9.9, kernel
5.14.0-741.22.1.el9_9, JDK 25.0.5.1, a bhyve guest with **2 vCPUs**
(Intel N100) and 2 GiB, nothing else running (load 0.5–4, all of it
the runs' own). Absolute figures are a small VM's; the ratios and
the A/B are the result. The tracer was pinned to CPU 1 and every
load to CPU 0 (`S3_TRACER_CPU=1 S3_LOAD_CPUS=0`), so "4 threads"
are four burst threads on one CPU. Builds: **new** = `4948da0`
(main), **base** = `f049b03` (the commit before ti4, after the el9
fixes gf4/hf4: no msghdr handlers, no late attach), both linked
static on the laptop as `ReleaseTracer` does and run against the
same workload jar; each round ran base then new. iom1 has no GNU
time, git, `perf` or `zstd`, so `measure.sh` got `S3_TIME` (a
static build of `spikes/s3/cputime.c`), `S3_REV`, and the
`volume` mode and instruction counts were not taken. The VM ran
07:52–08:16 UTC, the measurements 19 minutes of it; the laptop
only built. Samples: `spikes/s3/results-ak4/` (each `samples.txt`
concatenates the rounds' files, headers included; `log.txt` is the
driver's).

**New loads** (`spikes/s3/msgload.c`, `measure.sh`'s new opt-in
`msg` mode: per round an untraced run, one traced by its PID, and
one beside a tracer on an idle process, each reporting its own CPU
time from its startup gate; one thread):

- `udp-mmsg:5000` — 5000 × (one `sendmmsg` of 64 datagrams of
  64 bytes from an unconnected socket with `msg_name` set, then
  `recvmmsg` until they are back) on 127.0.0.1;
- `udp-mmsg:1000:1024` — the same at 1024 per `sendmmsg`, the
  kernel's limit and the mmsg exit's worst case (the receiver's
  buffer keeps ~250 per batch; the rest are dropped and counted,
  not waited for);
- `netlink:100000` — 100 000 `RTM_GETLINK` dumps, a `sendmsg` and
  two `recvmsg` each on a `NETLINK_ROUTE` socket (what `ip -s link`
  does): every call an event plus its address record;
- `exec:300` — 300 × fork/exec of `ip -s link` and `ss -s`, the
  netlink load as hosts run it; once more under
  `run -agent … -spool …`, where every exec of a traced process
  goes to the late attacher.

### The gated items (`syscall-burst`, `-aggregate=false` for the CPU items)

| #25 item | base `f049b03` | new `4948da0` | Budget |
|---|--:|--:|---|
| tracer CPU per event, 1 thread (5 runs) | 1.02 µs | **1.06 µs** | ≤ 2 µs: passes |
| tracer CPU per event, 4 threads (5 runs) | 1.00 µs | **1.05 µs** | ≤ 2 µs: passes |
| bystander CPU, 4 threads (30 pairs) | 1.245× (wall 1.261×) | **1.255×** (wall 1.269×) | ≤ 1.25×: **over, by a hair, in both** |
| bystander CPU, 1 thread (10 pairs, not gated) | 1.254× | 1.243× | — |
| loss, folded burst, 1 and 4 threads (3 runs) | 0 | **0** | < 1 %: passes |
| traced slowdown, 1 / 4 threads (wall, recorded) | 1.64× / 1.63× | 1.64× / 1.67× | — |
| `getppid` bystander, 1 / 4 threads (3 pairs, recorded) | 1.43× / 1.50× | 1.49× / 1.47× | — |

The unfolded burst lost nothing at either thread count here (one
CPU's burst is slower than the tracer). The folded runs sent
107–137 events at 1 thread and 2199–2317 at 4.

The tracer's own cost per burst event is the laptop's (md4 1.2 /
1.7 µs, ee4 1.59 / 1.95 µs) or lower, and the new handlers did not
move it. The bystander did not move either — +0.01 is one 10 ms
CPU tick on the burst's ~1.25 s — but on el9 both builds sit **on**
the 1.25× line (13 and 17 of 30 pairs above it, ratios 1.20–1.33×),
and the wall-clock guard trips in both, on an idle VM, so no paused
host can settle it; the laptop measured 1.15–1.19× on 7.2. A
bystander read or write here costs ~1.25 µs and the trace adds
~0.3 µs to it, 5.14's tracepoint path being the likely share (the
getppid kind pays 1.43–1.50×, the laptop's 2.0×, so the
dispatcher's untraced-kind path is not where el9 is dearer). Over
budget, unrelated to this task's handlers: task 9m4.

### The new loads (5 rounds each)

| Load (new build) | traced CPU | added per call | tracer per event | events/s | NDJSON B/event | lost | bystander CPU |
|---|--:|--:|--:|--:|--:|--:|--:|
| `udp-mmsg:5000` | 1.057× | +4.2 µs (66 ns a message) | 10.0 µs | 7.9 k | 432 | 0 | 1.008× |
| `udp-mmsg:1000:1024` | 1.031× | +20.7 µs | 83 µs | 0.8 k | 402 | 0 | 0.999× |
| `netlink:100000` | **1.439×** | +1.35 µs | **3.87 µs** | 221 k | 325 | 0 | 1.117× |
| `exec:300` | 1.114× | +213 µs an exec | 6.0 µs | 29.7 k | 326 | 0 | 1.049× |
| `exec:300`, `-agent` | 1.135× | +252 µs an exec | 8.1 µs | 29.2 k | 328 | 0 | 1.056× |

The base build traces none of these calls, so its traced runs show
what the dispatchers cost on them: `udp-mmsg` 1.005× / 0.999×,
`netlink` 1.104× (+0.32 µs a call), `exec` 1.112× (61.5 events an
exec against new's 63.5: `ip`'s and `ss`'s own netlink calls), and
`-agent` 1.116× with 6.0 µs per event. The untraced loads cost
74 µs per 64-datagram call, 3.1 µs per netlink call and 1.9 ms per
exec'd command.

- **mmsg**: the per-message reads are cheap: 64 messages add
  4.2 µs to a 74 µs call (+5.7 %), 1024 add 20.7 µs to a 666 µs
  one (+3.1 %). The high per-event tracer figures are not a
  per-message cost: at 0.8–8 k events/s the tracer is woken for
  nearly every record, and the wake-up dominates — the 1024 run's
  profile (`profile/new-udp-mmsg_3000_1024.pprof`, 9 007 events in
  10.8 s) is 80 % `runtime.cgocall` (the ring poll), and the tracer
  used ~6 % of a core in all. The address fields put 75–105 bytes
  on each NDJSON event.
- **netlink** is the expensive one: every call is an event plus an
  address record, nothing folds, and at 221 k events/s the tracer
  spends 3.87 µs per event — 3.7× a burst event, ~85 % of its core,
  with no loss yet. The profile (`profile/new-netlink_200000.pprof`)
  puts 40 % of the tracer's CPU in `types.NewSockAddrEvent`, the
  address record's reflective `encoding/binary` decode — the kind
  of decode task ud4 removed from the other hot records. Item 1 of
  #25 is gated on `syscall-burst` only, so this is not a budget
  failure, but it is the cheapest large cut on offer: task am4.
  As a bystander, a netlink caller pays 1.10× before and 1.12×
  after (these calls now reach the dispatcher's scope test).
- **Late attach**: under `-agent` the tracer spends ~90 ms more per
  600 execs (~0.15 ms of tracer CPU per exec of a traced process),
  and the traced load 2 points more; no JVM was involved.

**Task am4** took the reflection out: the generator
(`internal/generate/typesgo_decode.go`) now emits fixed-offset
little-endian loads for every record whose C layout has no internal
padding — 38 of the 42 constructors that used `binary.Read`, the
address record among them; the four padded ones (socketpair, accept,
pipe, poll) keep it behind their hand-written fast decoders.
`BenchmarkNewSockAddrEvent` on the laptop (i7-1185G7, 7.3): 4.5–5.4 µs
and 2 allocations (288 B) per record before, 51–53 ns and none after.
The el9 netlink re-measure (`S3_MODES=msg`) is in the next section
(task 9m4): 1.50 µs per event, from 3.87.

| #25 item | ak4 verdict (el9, iom1) |
|---|---|
| tracer CPU | **passes**: 1.06 / 1.05 µs per burst event at 1 / 4 threads, base 1.02 / 1.00; a msghdr event costs 3.9 µs on the netlink load (not gated; task am4) |
| bystander | **over by a hair** in both builds: 1.255× new, 1.245× base, wall 1.269× / 1.261×; the new handlers do not change it; el9's own cost, task 9m4 |
| loss | **passes**: 0 on the folded burst and on every msghdr load, up to 221 k events/s |
| volume | not re-measured (no `zstd` on iom1); NDJSON 325 B per burst event as before, 402–432 B per mmsg event with its peer |

## Follow-up (9m4, 2026-11-26): where el9's bystander cost goes

Task ak4 left decision #25's bystander item on the 1.25× line on el9
in both builds, where the 7.2 laptop had measured 1.15–1.19×. Task
9m4 took it apart on the same VM: `iom1`, Rocky 9.9,
5.14.0-741.22.1.el9_9, 2 vCPUs (N100), idle but for the runs; the
tracer pinned to CPU 1 and every load to CPU 0 as in ak4,
`syscall-burst` at 4 threads and 500 k ops, `-aggregate=false`. The
laptop only built (static tracers of `bd2bbe4`, main with am4).
Samples, drivers and the pooled medians:
`spikes/s3/results-9m4/` (`pooled.txt`, `drivers/`). iom1 has no
`perf`, so the cost was split by tracer builds that differ only in
the dispatchers (`internal/c/dispatch.c`, throwaway, not committed):

- **empty**: both dispatchers `return 0` at once — the tracer
  attached, nothing traced: the `raw_syscalls` tracepoint path and
  an empty program;
- **mirror**: return after the traced-kind mirror lookup — what a
  syscall of an untraced kind pays;
- **nocompat**, **tgidonly**, **kernonly**: the full dispatcher
  without the compat test, with the scope test cut to the tgid
  read, and with the target hint's user-bitmap lookup left out;
- **full**: main as it is.

`getppid.c` grew a `close` mode (`close(-1)`, a traced kind, EBADF
at once) and `measure.sh` an `S3_KIND_CALL` for it, so the kind
pairs price a traced and an untraced kind from C, without the JVM.

**The burst, by variant** (bystander pairs, ABAB, the variants
interleaved by round; CPU-time medians; "added" is the bystander's
extra CPU per read or write):

| Variant | Pairs | CPU ratio | Wall ratio | Added per syscall | Pairs > 1.25 |
|---|--:|--:|--:|--:|--:|
| empty | 30 | 1.179× | 1.186× | 225 ns | 0 |
| mirror | 50 | 1.202× | 1.212× | 255 ns | 3 |
| tgidonly | 20 | 1.235× | 1.245× | 305 ns | 7 |
| kernonly | 20 | 1.228× | 1.246× | 290 ns | 7 |
| nocompat | 20 | 1.248× | 1.256× | 310 ns | 9 |
| **full** | 50 | **1.252×** | 1.265× | 320 ns | 25 |

An untraced read or write cost 1.26 µs of CPU in every set. Of the
~320 ns the trace adds, ~225 ns (70 %) is there with an empty
program: the kernel's `raw_syscalls` perf-tracepoint path at enter
and exit, which no BPF change reaches. The mirror lookup adds
~30 ns, the scope test (tgid read and target-hint lookups) ~55 ns;
the compat test is free (nocompat = full), so moving the scope
test ahead of it — which spares a bystander exactly the compat
test — would buy nothing. The scope test is the only part that
could go, and it cannot: it is what keeps a bystander out of the
handlers' `target_map` lookup (task 9f4). A dispatcher with no
logic at all would still be at 1.18–1.20×.

The dispatchers' own run time (`kernel.bpf_stats_enabled`,
`bpfstats.txt`) says the same at a finer grain: the enter program
runs 94–96 ns empty in the burst and 129–133 ns full; exit 41 and
61 ns. An empty program costs twice as much in the burst as in the
`close(-1)` loop (45 ns): the burst's I/O path evicts the program
and its maps between calls.

**Not el9's tracer, el9's syscall.** The added cost per bystander
syscall is no larger here than on the laptop: 320 ns on iom1
against 275 ns (9f4, 4 threads) and 290–370 ns (ae4) on 7.2. What
differs is the denominator: a 1-byte read or write on iom1's xfs,
one CPU, costs 1.26 µs, on the laptop's btrfs under host load
1.9–2.7 µs. The same ~0.3 µs is 1.15× of the one and 1.25× of the
other.

**Mitigations.** The guest runs with the full spectre/meltdown set
(`/sys/devices/system/cpu/vulnerabilities`): retpolines
(spectre_v2), PTI (meltdown) and MDS buffer clearing, because bhyve
does not pass the N100's `ARCH_CAPABILITIES` through and the kernel
assumes a vulnerable CPU. The laptop (7.3) runs Enhanced IBRS with
no PTI and no MDS clearing. One run on iom1 booted with
`mitigations=off` (then reverted and rebooted) shows how much of
the tax that is:

| iom1 | untraced read/write | added, empty | added, full | burst CPU ratio | wall | `getppid` untraced / added |
|---|--:|--:|--:|--:|--:|--:|
| mitigations on (50 / 30 pairs) | 1.26 µs | 225 ns | 320 ns | **1.252×** | 1.265× | 432 / 198 ns |
| `mitigations=off` (15 pairs) | 0.77 µs | 130 ns | 150 ns | **1.195×** | 1.220× | 121 / 62 ns |

The mitigations raise the bystander's syscall by 1.6× and the
tracer's addition to it by 2.1× — the tracepoint path and the
program run are indirect calls and kernel entries, which retpolines
and PTI tax hardest. Without them el9 measures what the laptop
measures. BPF JIT settings: `bpf_jit_enable` 1 on both;
`bpf_jit_harden` is 1 on el9 (0 on the laptop), which blinds only
unprivileged programs, so not the tracer's; `bpf_stats_enabled` 0
on both.

**Verdict.** No cheap tracer change brings the item under 1.25× on
iom1: the whole dispatcher logic is ~95 ns of the 320, the part
that could be cut without the scope test ~0, and an empty program
alone sits at 1.18×. The
gap is the platform: a guest with retpolines, PTI and MDS clearing
on a cheap xfs syscall. Decision #25 records it and a proposed
amendment for the owner; the limit is unchanged.

**Netlink after am4** (`netlink:100000`, 5 rounds,
`-aggregate=false` as in ak4; a 4-round set with aggregation on in
`netlink-agg/` agrees: 1.58 µs):

| `netlink:100000` | ak4 (`4948da0`) | 9m4 (`bd2bbe4`, with am4) |
|---|--:|--:|
| tracer CPU per event | 3.87 µs | **1.50 µs** |
| events/s | 221 k | 223 k |
| traced load CPU | 1.439× | 1.436× |
| added per call (load) | +1.35 µs | +1.36 µs |
| bystander CPU | 1.117× | 1.108× |
| lost | 0 | 0 |

am4 cut the tracer's cost per netlink event by 61 %; at 1.50 µs it is now under #25's 2 µs per event (that item is gated on
the burst only; this load is information). The traced load's own
cost is kernel-side and did not move.

| #25 item | 9m4 verdict (el9, iom1) |
|---|---|
| bystander | **1.252×** (50 pairs, wall 1.265×): on the line, as in ak4; 70 % of it is the tracepoint path with an empty program; with `mitigations=off` 1.195×; platform, limit unchanged, amendment proposed in #25 |
| tracer CPU, netlink (not gated) | 1.50 µs per event after am4, from 3.87 |
