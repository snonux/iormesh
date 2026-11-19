# S1 — Java↔syscall join: result

Companion to [PLAN.md](../../PLAN.md) — the plan is the source of
truth; this is spike S1's written result. Raw notes, per-run
numbers and the throwaway code are in
[spikes/s1/](../../spikes/s1/NOTES.md). Kernel 7.2.15-200.fc44,
bpftrace 0.24.2, JDK 25.0.4.1 (Red Hat build; 27-ea for the
uprobe join), November 2026. No JDK 21 runtime was installed:
JDK 21 was checked against its sources only — which missed one
signature: 21 has `Net.serverSocket(ProtocolFamily, boolean)`, not
25's `serverSocket(ProtocolFamily)`. M3 found it on real 21
runtimes (task sc4) and split the hook table (PLAN.md #29).

**Verdict: works.** Every declared I/O op of `file-basic` (6/6)
and `tcp-loopback` (server 11/11, client 10/10) is attributed to
its declared method, on platform threads, in every run — through
the in-kernel uprobe mark for the native call and the agent's
bracket for the Java method and stack. The join needs no clock
conversion if BPF stamps the native's entry with
CLOCK_MONOTONIC; the agent talks to the tracer through an mmap
ring with no syscall per record; `-javaagent` stays primary and
live attach works with a warning. The largest cost is the
agent's stack walk (≈8 µs per I/O call on a loaded host), then the
uprobe pair (≈3.5 µs); the rest is small.

## The prototype

All of it is under `spikes/s1/` and throwaway:

- `join.bt.in` (bpftrace, rendered by `run.sh`): a uprobe and a
  uretprobe on each native of a hook set push and pop a per-TID
  mark; `raw_syscalls:sys_enter` of the target logs the innermost
  mark. Probes are attached by library path (inode), so libraries
  the JVM maps later (`libnet`, `libextnet`) are covered.
- `agent/`: a minimal Java agent. It rewrites the bodies of 17
  JDK wrapper methods (`Hooks.enter(id)` at entry, `Hooks.exit(id)`
  before each return and in a rethrowing catch-all) and records
  one *bracket* per call: kernel TID, hook, enter/exit
  `System.nanoTime()`, stack id.
- `agentjoin.py`: a marked syscall belongs to the innermost agent
  bracket on the same TID whose window contains it; each join is
  checked against the hook's native (`HOOK_NATIVES`).
- The workload: M0's `file-basic` and `tcp-loopback`, gated, so
  the probes are live before the scenario starts.

## Experiment 1 — hook set and join

With a curated set of leaf I/O natives (`hooks-v1.txt`: the
`java.io` stream/RAF/descriptor natives of `libjava`, every
`Java_sun_nio_*` of `libnio`, all of `libnet` and `libextnet`;
584 probes), every scenario syscall on the scenario thread carries
the right native's mark, in 3 runs each on JDK 25 and 27-ea. The
agent join on top attributes every `expect.json` event to its
declared method — `file-basic` 6/6, `tcp-loopback` server 11/11
and client 10/10, 0 hook/mark mismatches — in 8 runs with the
in-memory recorder and again through the mmap ring (below).

| Process | Declared ops attributed | Declared methods |
|---|---|---|
| file-basic | 6/6 (+ unlink) | `FileWork.write:60/61/62`, `FileWork.read:69/70/74`, `FileWork.run:44` |
| tcp server | 11/11 (+ unlink, listener close) | `TcpLoopback.serve:128/131/132/135`, `expect:148`, `server:103` |
| tcp client | 10/10 (+ unlink) | `TcpLoopback.client:140/141/143`, `expect:148` |

Four rules the first, two-function experiment did not show:

1. **Leaf natives only.** Hooking every `Java_*` makes non-I/O
   natives that run Java code (`Class.forName0`,
   `ClassLoader.defineClass1`) outer brackets; I/O by unhooked
   code below them inherits their mark.
2. **A syscall allowlist per native.** Even leaf brackets contain
   JVM work: loading `FileNotFoundException` inside
   `FileInputStream.open0` reads the runtime image (`pread`, fd 3)
   under the `open0` mark; `Inet6Address.init` marked two
   `mprotect`s. A syscall whose kind the native does not issue is
   "JVM work inside a bracket", not the native's I/O.
3. **Drop syscall 335.** On x86_64 with kernel ≥ 6.11 every
   uretprobe return enters `uretprobe(2)` (nr 335) on the target
   thread — the most frequent "syscall" of a traced JVM.
4. **Probe ids, not symbols.** `libextnet.so` is mapped after
   attach; its probes fired but printed as raw addresses. The
   production mark is a per-attach-point id (BPF cookie). And the
   per-TID stack needs a reset path (thread exit, depth cap): a
   missed uretprobe would leave a sticky mark.

The Java-level hook table (body rewrites only — retransformation
cannot add methods, so the native-prefix trick is out) is in the
[implementation guide](../implementation.md#s1--javasyscall-join),
corrected by S1: `UnixFileDispatcherImpl` since JDK 21, the
callers of the native `Net.accept`/`Net.listen`, socket close via
`UnixDispatcher.close0`. JDK 21 and 25 agree on every native the
set uses; 17 differs (no `UnixFileDispatcherImpl`/`UnixDispatcher`,
RAF `readBytes` without the `0`) — decision #29's table variants.

Two agent mechanics: `appendToBootstrapClassLoaderSearch` is not
enough on its own — `java.base` must be made to read the bridge's
module (`Instrumentation.redefineModule`), or the rewritten JDK
methods fail to link. And appending to the bootstrap path prints
a CDS note on stderr ("Sharing is only supported for boot loader
classes…"); stdout is untouched.

**What stays unbracketed**: JVM class loading from the runtime
image, the JVM's own `dlopen` of JDK libraries (7 declared-kind
syscalls per tcp process for `libextnet.so`), `libextnet`'s
feature probes and `libnio`'s init (marked at native level, no
agent hook), JIT/GC threads, and by construction third-party JNI,
FFM downcalls, JVM-internal I/O (`-Xlog`, hsperfdata, JFR, the
attach listener) and `MappedByteBuffer` page faults. All of it
stays in the trace as `attribution: none`.

## Experiment 2 — clocks

`System.nanoTime()` is CLOCK_MONOTONIC; ior's records (and
bpftrace's `nsecs`) are CLOCK_BOOTTIME. On the test laptop they
differed by 307 592 s (85 h suspended). The first half of S1
converted with a (boot, mono) pair read in bpftrace's `BEGIN` and
saw it "jitter" by up to ~180 ns. `spikes/s1/clock/` measured
where that comes from:

- In user space (`offset.c`), sampling mono–boot–mono and taking
  the midpoint, the offset is a **constant** — the same p50 to the
  nanosecond in every run of the session, p1–p99 spread 2 ns at
  loadavg ~3 and 10 ns at ~25. BOOTTIME is MONOTONIC plus
  accumulated suspend time; it moves only across a suspend.
- `ClockProbe` brackets 20 000 writes to `/dev/null` with
  `nanoTime()` (bracket p50 1.2–8 µs, min 310 ns–1 µs,
  load-dependent); `clock.bt` stamps each `sys_enter` with both
  kernel clocks. 8 runs at loadavg 3–27
  (`spikes/s1/results/clock.txt`; the midpoint row exists for the 6
  runs whose raw logs survived):

| kernel stamp, converted how | outside bracket | min margin |
|---|---|---|
| `bpf_ktime_get_ns` (MONOTONIC), none | 0 / 160 000 | 146–453 ns |
| boot, minus the `BEGIN` pair | **14 / 20 000** in one run, 0 in the other 7 | −14 ns |
| boot, minus the user-space midpoint | 0 / 120 000 | 132–419 ns |

The `BEGIN` pair is off by roughly 70–230 ns, run-dependent: it
is two helper calls in sequence, not a moving offset. The
midpoint conversion lands within 16–41 ns (p50) of the in-probe
monotonic stamp — the time between its two helper calls. The
"jitter" was measurement error, and it is enough to push a stamp
out of a tight bracket. In the scenario joins
the brackets are wider (smallest window-to-syscall gap 1.6–9.6 µs,
load-dependent), so wa4's joins were right, but not by design.

**Result**: the join needs no clock correlation at all if BPF stamps with
`bpf_ktime_get_ns` — the same clock as `nanoTime()`. The prototype compared the
`sys_enter` stamp with the agent bracket; stamping the native's *entry* (the
uprobe) instead, as M3 plans, is the same clock one step earlier and was not
run. Events keep their boot-clock timestamp (ior's convention, safe across
suspend); only the join field is monotonic. Where an agent-only record (an HTTP
event) must be placed on the boot timeline, the tracer converts with a midpoint
offset sampled in user space and re-sampled per segment, so a suspend shows up
as a step.

## Experiment 3 — transport (decision #27)

Two candidates, both in a tracer-owned directory:

- **Append-only spool file**: per-thread 64 KiB buffers flushed
  with `write(2)`. Each flush is a syscall on a target thread
  that the tracer must recognise (by fd/path) and drop; the file
  grows without bound when the tracer falls behind — no natural
  back-pressure point except the disk.
- **Memory-mapped ring** (`Ring.java`): one file per JVM, mapped
  `MAP_SHARED` by agent and tracer. A header (magic, capacity,
  pid; `head` and `tail` on their own cache lines; drop counters)
  and variable-length records framed by one 8-byte word
  `len | type << 32`, published with a release store. Writers
  reserve with a CAS on `head`; the reader copies a record out,
  zeroes it and advances `tail`. When the reader is a whole ring
  behind, the writer **drops and counts** — the target never
  blocks.

Measured on `syscall-burst` (400 120 records per run):

| setup | delivered | dropped (counted) |
|---|---|---|
| 256 MiB ring, read after exit | 400 120 | 0 |
| 64 KiB ring, no reader | 1 149 | 398 977 |
| concurrent reader, 64 KiB / 1 MiB / 16 MiB | 398 968 / 398 236 / 399 979 | 1 152 / 1 884 / 141 |

Every record is either delivered or counted — the loss
accounting the trace contract needs. After setup the ring costs
no syscall: in the scenario runs no agent syscall appeared inside
the traced window at all. The agent's own syscalls are its ring
setup in `premain`/`agentmain` (open, ftruncate, mmap of a path in
the spool directory) and one `readlink("/proc/thread-self")` per
thread for the kernel TID; the tracer tags those by the spool path
and as the first such readlink of a TID before its `THREAD`
record. Warming the hot path up in `premain` keeps the agent's own
class loading out of the target's I/O later.

**Result**: the mmap ring.

## Experiment 4 — attach (decision #2)

`spikes/s1/attach/` loads the same agent at launch and live, into
a gated `file-basic` (JDK 25.0.4.1; live attach also on 27-ea):

| case | result |
|---|---|
| `-javaagent` | silent (only the CDS note above) |
| live attach, no flag | works; JEP 451's 4-line `WARNING: A Java agent has been loaded dynamically …` on the target's stderr — on 25 and on 27-ea |
| `-XX:+EnableDynamicAgentLoading` | works, silent |
| `-XX:-EnableDynamicAgentLoading` | `AgentLoadException: Dynamic agent loading is not enabled` |
| attacher root, target another uid | works |

Attach took ≈150–175 ms, loading the agent (retransforming the
17 hook classes) ≈90–130 ms — both on a loaded host, so
load-dependent. The attach handshake (an
`.attach_pid` file, SIGQUIT, a Unix socket `/tmp/.java_pid<pid>`
in the target's mount namespace) is a client connection to the
target's local socket — the tracer still listens on nothing. A
live-attached agent sees only what happens after the attach: the
startup I/O is gone (31 brackets vs 128 with `-javaagent`). JDK 21
was not run; JEP 451 specifies the same warning for it.

**Result**: `-javaagent` primary; live attach supported, with the
warning (or `+EnableDynamicAgentLoading`) stated, and a clear error
when the target disables it.

## Experiment 5 — cost

`spikes/s1/cost.sh` on `syscall-burst`, one thread; one op is
`FileOutputStream.write(int)` + `FileInputStream.read()`, two
hooked natives. The host was shared with other workers
(loadavg 9–34): wall time swung 1.9–12 µs per op untraced, so the
table uses JVM CPU time (user + sys) differenced between 100k and
500k ops, median of 3 — less sensitive to other tenants, but still
inflated by contention (the idle-host baseline is 0.76 µs per op
wall). **Indicative; the ratios are the finding.**

| mode | CPU ns/op | over untraced |
|---|---|---|
| untraced | 3 152 | — |
| syscall tracepoint + mark lookup | 3 775 | +622 |
| + uprobe/uretprobe marks | 10 150 | +6 998 |
| agent, empty hooks | 3 625 | +472 |
| agent, ring, no stack | 3 775 | +622 |
| agent, ring, stack (10 frames) | 19 998 | +16 845 |
| agent, append file, stack | 34 272 | +31 120 (noisy) |
| agent, ring, stack + uprobe marks | 35 325 | +32 172 |

Per hooked I/O call that is roughly: the uprobe pair ≈ 3.5 µs
(trap, uretprobe and its syscall-335 trampoline, two map
operations); an agent bracket without the stack ≈ 0.3 µs; the
stack walk ≈ 8 µs — about 95 % of the agent's cost. Two more cost
facts for decision #25: as attached here (bpftrace, by library
path, no PID filter) the uprobes trap in every process mapping
that `libjava`/`libnio`/`libnet` — all JVMs of that JDK install
on the host — and filter by PID in the program; the kernel can
also scope a uprobe to a PID (perf-event pid, `uprobe_multi`
pid), which M3/#25 must confirm for the production attach. And
syscall-burst is the worst case (a 1-byte I/O per call); real I/O
amortizes the same per-call cost over more work.

**Result for decision #6**: stacks are captured at the I/O
boundary by the agent (`StackWalker`), as planned — the uprobe
mark alone cannot name a Java method, and a plain BPF stack walk
returned only unsymbolized Java frames (a HotSpot-aware eBPF
unwinder was not tried; v1 does not need one). Sampling between I/O events is
not adopted for v1. The stack walk is the cost to engineer down in
M3: fewer frames, no `StackFrame` materialization for known
stacks, and a per-thread cache of the last stack for tight loops
— measured against #25.

## Limits — untested

- A **JDK 21 runtime** (sources only) and the agent on 21: the
  prototype uses `java.lang.classfile` (JDK 24+); M3 shades ASM.
  Since covered by M3: the ASM agent runs on 21.0.6 and 21.0.12
  under `mage agentTest`.
- JDK builds with **stripped or statically linked** native
  libraries (no `Java_*` symbols to attach to).
- Multiple threads in the cost runs, and the cost on a quiet host.
- The join on the uprobe's native-entry stamp (the prototype used
  the `sys_enter` stamp), and per-PID uprobe attachment. Since
  built by M3 (task td4): `uprobe_multi` links scoped to the
  JVM's pid, the join on the native-entry stamp — see the
  [implementation guide](../implementation.md) M3 step 5.
- The ring's reader in Go, and a ring that wraps while the target
  writes from many threads at once.
- Containers: the spool directory reached through
  `/proc/<pid>/root`, attach across mount namespaces (M7).
- The clock-free alternative (k-th agent bracket ↔ k-th uprobe
  bracket of its native per TID) — not needed after experiment 2.

## What changes in the plan

- S1 is done; its exit criterion holds (Experiment 1).
- Decision #2 decided: `-javaagent` primary, live attach supported
  with JEP 451's warning documented.
- Decision #6 decided: boundary stack capture in the agent; no
  sampling in v1.
- Decision #27 decided: the mmap ring, drop-and-count.
- M3's join: uprobe mark (curated leaf natives, per-native
  syscall allowlist, probe ids, nr 335 dropped, reset on thread
  exit) plus the agent bracket on the same TID, compared in
  CLOCK_MONOTONIC.
- The envelope's Java fields are settled: nested, as
  `internal/schema` already has them — `java.thread` (the Java
  thread name, from the agent's `THREAD` record) and `java.stack`
  (method-table ids, innermost first, from its `STACK` record),
  set exactly when `attribution` is `java`. PLAN.md's flat
  `java_thread`/`java_stack` were a sketch. A syscall the uprobe
  marked but no agent bracket contains stays `attribution: none`;
  whether to also record the native's name is M3's call (an
  appended optional field). `http` stays provisional until M3.
