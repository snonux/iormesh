# Bystander and consumer cost

Part of [S3 — overhead and volume budget](../s3-overhead.md).

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
