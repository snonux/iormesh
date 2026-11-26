# Paired CPU-time method; Java and HTTP costs

Part of [S3 — overhead and volume budget](../s3-overhead.md).

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
