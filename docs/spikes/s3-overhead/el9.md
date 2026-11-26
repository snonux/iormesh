# Rocky 9.9 re-measure

Part of [S3 — overhead and volume budget](../s3-overhead.md).

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
on a cheap xfs syscall. Decision #25 records it; the owner
accepted the amendment (2026-11-26): ≤ 1.25× stays the gate where
mitigations are the hardware's own, el9 in a fully software-mitigated
guest takes 1.25× CPU / 1.27× wall, guarded by ≤ 0.35 µs added CPU
per bystander read or write.

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
