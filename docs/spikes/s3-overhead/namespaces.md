# PID namespaces

Part of [S3 — overhead and volume budget](../s3-overhead.md).

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
