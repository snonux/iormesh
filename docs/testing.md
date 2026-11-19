# Testing strategy

Companion to [PLAN.md](../PLAN.md) — the plan is the source of
truth.

The test workload comes *first*, not last — and it comes first
per milestone:

- The workload framework (M0) lands before any tracer code. Every
  later milestone adds its own scenarios before its tracer code,
  so each one lands against a known target. PLAN.md's scenario
  catalogue says which scenarios prove which milestone.
- A walking skeleton (M1) runs the whole chain once — probes,
  correlator, schema, normalizer, golden check, harness — on the
  smallest scenario, before the traced surface widens.
- The risky mechanisms are proven by spikes before milestones
  depend on them: the Java↔syscall join, the cross-host join,
  and the overhead budget.
- The app has client and server modes — and a microservices flavor
  where several service roles talk REST over plain HTTP and TLS —
  so cross-host fusion and mesh expansion can be rehearsed on
  loopback, between two test VMs/containers or across test hosts,
  without going near anything live. One scenario talks to a
  non-Java peer, so the degraded node shape is tested too.
- Only once a milestone passes against the workload is it considered
  ready for a live process.

## What a golden check asserts

A JVM issues thousands of syscalls nobody declared — startup,
class loading, JIT and GC threads — and PIDs, TIDs, ports and
inodes differ on every run. So validation is subset, not
equality:

1. **Cut to the marker bracket**: every scenario emits a begin
   and an end marker; nothing outside them is asserted on. A
   child the scenario forks inside its bracket shares it (the
   graph's `forked` edge says whose child it is).
2. **Normalize** what is left: run-specific values become
   symbolic ids, in order of first appearance inside the
   bracket, so startup noise never shifts them.
3. **Declared ⊆ collected**: every declared event and edge is
   present, in the declared per-thread order.
4. **Must-not-appear**: the scenario's forbidden list holds —
   e.g. the planted `Authorization` header, query secret and argv
   secret never show up.

Expectations are declared in the same trace schema as real
output, so they double as few-shot examples for LLM
introspection.

## Beyond single-host

- Deterministic SVG gives the scenarios visual goldens (rendered
  from normalized graphs).
- The controller flow (add → deploy → refresh → rm) is rehearsed
  on two test hosts; its merged mesh must equal the single-host
  result for single-host scenarios.
- Mesh expansion is rehearsed on the microservices topology with
  only the gateway enrolled; the expanded mesh must equal the one
  obtained by enrolling every host by hand.
- Hosts emulated as network namespaces share one kernel clock, so
  a test-only flag injects a clock offset per tracer, and the
  merge must recover it; NAT is emulated with a DNAT hop.
- The k8s mode rehearses the same scenarios as pods in kind.
- The LLM claim is tested: a fixed question set over workload
  traces, with answers known from the scenario declarations,
  rerun on every schema change.

## Integration tests follow ior

The end-to-end suite lifts ior's `integrationtests/` pattern
(in `../ior`): a dedicated package with a harness that runs the
tracer against the known workload, two expectation layers
(aggregated events vs exact per-event rows), one test file per
trace family, kernel-support probes with explained skips,
loss-honest re-runs, unexercised-skip markers, and pass gates
that do not trust exit codes. See PLAN.md, "Integration tests —
follow ior" for the full map.

### The suite today (M1)

`integrationtests/` drives the real tracer binary (`./iormesh
run`) against the real workload (`java -jar workload.jar`) and
reads the trace directory it writes. Each lifted file names its
ior source and what changed:

- `harness.go` + `process.go` — start workloads behind their
  startup gate (PID first on stdout), start `iormesh run -pid
  … -ready FILE`, wait for the ready file, release the
  workloads in order (the tcp-loopback client only once the
  server printed `listening`), wait for all of them and for the
  tracer's drain, and read the trace. Every wait is bounded.
  With `Harness.Pattern` set the tracer runs `-pattern GLOB`
  instead (M2); such a session never ends by itself, so the
  harness stops it with SIGINT once the workloads exited
  (`TestRestartPattern`: the `restart` scenario's two
  incarnations under one app node, the launcher never traced).
- `parse.go` — `events.ndjson` + `graph.json`, read strictly.
- `kernelloss.go` — parses the tracer's exit summary line; a run
  whose kernel lost records is re-run (three attempts), and a
  test whose every run lost records skips with the
  unexercised marker (`unexercised.go`) instead of passing.
  Undecodable records fail at once: they are a decoder bug.
- `kernel_support_test.go` — the tests skip unless tracefs shows
  every tracepoint M1 attaches to; a probe that cannot find them
  as root fails, so a blind probe never turns the suite green.

The tests:

| Test | Asserts |
|---|---|
| `TestWalkingSkeleton` | tcp-loopback golden; exactly one fused connection (two `connected` edges, both fused, to one socket node, one from each PID); mutated goldens fail |
| `TestFileBasic` | file-basic golden; mutated goldens fail |
| `TestFileLifecycle` | file-lifecycle golden, incl. `ftruncate` on the renamed path (rename-aware fd paths); mutated goldens fail |
| `TestFileLinks` | file-links golden (symlink text unresolved, no-follow `statx`, hard link); mutated goldens fail |
| `TestFileAttrs` | file-attrs golden (chmod/chown by path and fd, dirfd-resolved `fchmodat`, `dup`, f*xattr); mutated goldens fail |
| `TestFdLifecycle` | fd-lifecycle golden: fd reuse, the `dup`'s copy as its own instance, the child JVM kept through its `forked` edge with its `dup2`s and stdio read/write; only the target and its forked children appear; mutated goldens fail |
| `TestArgvSecretRedacted` | argv-secret golden: the child JVM's `exec` event and `forked` edge, the planted secret in `must_not_appear`; the raw `events.ndjson` and `graph.json` never contain it, and every planted shape is in the exec's argv, masked; a golden forbidding the masked form fails |
| `TestForkedChildrenKept` | pipe-ipc's and signals' marker-less child JVMs survive the bracket cut on the default families |
| `TestPipeIPC` | pipe-ipc golden with `-trace-families ipc`: each `pipe2` binds both ends, three fused pipe connections, two shared with the child JVM's stdio; mutated goldens fail |
| `TestSignals` | signals golden with `-trace-families signal`: `kill` with signal 15 then 9, each child's `signal` event, `signaled` edges parent → children and none back; mutated goldens fail |
| `TestUnixSocket` | unix-socket golden: the socket path on `bind` and `connect`, one fused connection of server and client; mutated goldens fail |
| `TestUDPLoopback` | udp-loopback golden: per-datagram addresses (the wildcard client by port only), one fused flow; mutated goldens fail |
| `TestTCPFamilies` | tcp-ipv4 and tcp-ipv6 goldens: full 4-tuples per family; tcp-ipv6 skips on a host without an IPv6 loopback (the workload's exit 3) |
| `TestNonJavaPeer` | non-java-peer golden: the JVM's connect/echo/log read, the Go peer's `accept4`, echo, EOF read and log write, one fused connection; the peer is followed as the JVM's child (its `exec` of `echopeer`); every event of the raw trace is `attribution: none` with no `java` or `http`; a default run has no `stack_id` and no `stacks.ndjson`; mutated goldens fail |
| `TestNativeStacks` | non-java-peer with `-native-stacks`: the golden still passes, every `stack_id` names a `stacks.ndjson` entry of its own process, and the peer's `accept4` and log `openat` stacks reach a `main.*` frame in the echopeer binary |
| `TestSyscallBurstFoldsEveryCall` | a traced 2-thread `syscall-burst` (20 000 ops) loses no record, is folded (events with `args.count`, M2 step 11), and the counts of its write and read events on its own files add up to exactly its ops, each call one byte |
| `TestSkipCodeIsSkip` | (no root) a workload exit 3 becomes `ErrWorkloadSkipped`, which the tests turn into `t.Skip`; exit 1 stays a failure |
| `TestUntracedProcessStaysOut` | a released but untraced twin leaves no event and no node |
| `TestSecondRunNormalizesIdentically` | two runs of each scenario, normalized and bracketed, are equal (tcp_state: see below) |

The repeat check is exact per process: two processes running at
once interleave in the host-wide sequence as the scheduler
decides, and seq and the clocks are that rank after
normalization, so the comparison drops those three fields and
keeps each process's events in its own order. Everything else
must match — which is why tcp-loopback's server waits for the
client's EOF before it closes: closing right after its reply
raced the client's close, and the traced TCP teardown (a
simultaneous close through `CLOSING`, or not) differed between
runs.

tcp_state events are the one exception. They are the kernel's
socket transitions (`sock:inet_sock_set_state`), mostly fired
in softirq as the peer's segments arrive, not the process's
syscalls. Where they fall between the process's syscalls, and
whether the tail lands before the end marker, depends on
scheduling. Under CPU load, about half of the tcp-loopback
runs failed: the client's `FIN_WAIT2` and `CLOSE` came before
its `close` event in one run and after it in the next, or after
the end marker. Also, Java's close runs `shutdown(2)` first,
and `close(2)` then stores the socket's state again. The kernel
reports that as `FIN_WAIT1` → `FIN_WAIT1` if the server's ACK
has not arrived yet; if it has, there is no record. So each
process's tcp_state events are compared apart from its
syscalls, in their own order, with an immediate repeat
collapsed. The shorter run's sequence only has to be a prefix
of the longer one's, and that prefix must still reach teardown
(`FIN_WAIT1`, `CLOSE_WAIT`, …) whenever the longer one does. A
process's first teardown state is set before its own close or
EOF read returns, so it is always inside the bracket. Without
that rule, an empty sequence, or one cut after `SYN_SENT`,
would match anything. Syscall events and the graph are still
compared exactly.

`mage integrationTest` builds the tracer, the workload jar and
the test binary (`integrationtests.test` at the repo root, for a
scoped sudoers rule, as in ior), runs it as root with `sudo -n
-E` from `integrationtests/`, in parallel
(`INTEGRATION_PARALLEL`, default min(NumCPU, 2)) with
`-test.failfast`; `mage integrationTestSerial` runs one test at
a time. The binary runs with `-test.v`, and the target fails
unless every test in `gatecmd.IntegrationTests()` printed
`--- PASS` and no test or subtest printed `--- SKIP` — Go reports
a parent whose subtests all skipped as passed, so the gate reads
skips at every level. Skips caused by kernel loss are summarized
before the gate fails. `internal/buildgate` keeps that list
equal to the tests the package declares. Set `IORMESH_IT_KEEP=DIR`
to keep a failed test's trace directories.

### M2 acceptance (2026-11-19)

`mage integrationTest`: 25 tests, two consecutive full runs green
on HEAD, no skips, zero lost records. PLAN.md's M2 edge cases map
to the suite as follows:

| Edge case | Proven by |
|-----------|-----------|
| fd reuse and aliasing | `TestFdLifecycle` (golden: reuse, dup, inherited stdio) |
| exec | `TestFdLifecycle` (child exec, CLOEXEC), `TestArgvSecretRedacted` |
| pattern restarts (new PID, same app) | `TestRestartPattern` (two incarnations, one app node) |
| threaded I/O | `TestSyscallBurstFoldsEveryCall` (2 threads, folded counts exact) |
| loopback pairs | `TestWalkingSkeleton`, `TestTCPFamilies` (v4, v6), `TestUnixSocket`, `TestUDPLoopback`, `TestPipeIPC` |
| non-Java peer | `TestNonJavaPeer`, `TestNativeStacks` |
| PID reuse | root/unit level only: exit cleanup of `target_map` (`TestFollowForkExec`), `TestExecWatchExitedBeforeAdopt`, `internal/target` `TestAlive` |
| socket races | root level only: `TestSockResetBeforeAcceptNotRegistered`, `TestSockFailedConnectUnregisters` |

The last two rows are not yet integration tests — forcing a PID
reuse or a reset race end to end needs a dedicated harness; a
follow-up task tracks it. The decision #25 budget run is recorded
in docs/spikes/s3-overhead.md ("M2 acceptance measurement").

## Performance baselines

S3 (PLAN.md, decision #25) measures overhead against a load the
workload can state in advance: `syscall-burst`, a tight loop of
1-byte `write` + `read` pairs on per-thread temp files — the
most syscalls per unit of real work, so the tracer's per-event
cost is at its largest. Run it standalone:

```sh
mage workloadBuild
java -jar workload.jar syscall-burst --ops 1000000 --threads 4
```

After the PID it prints one result line,
`syscall-burst ops=N threads=T syscalls=2N elapsed_ns=E`; E
times the loops only, not JVM startup or the file opens. Its
expect.json declares only the marker bracket: the event count
scales with `--ops` and the threads interleave freely, so the
spike asserts counts, not a golden.

`scripts/perf-baseline.sh` (lifted from ior) records and
compares baselines under `perf/`:

```sh
scripts/perf-baseline.sh record [label]   # default label: short HEAD
scripts/perf-baseline.sh compare OLD NEW  # benchstat, labels or paths
```

`record` builds the jar, runs the burst untraced `PERF_COUNT`
times (default 6, the fewest benchstat builds a confidence
interval from) at each thread count in `PERF_THREADS` (default
`1 4 16`) with `PERF_OPS` ops (default 1,000,000), and writes
`perf/burst-<label>.txt`: a provenance header (commit, kernel,
CPU, JDK, load average) and one benchstat line per sample,
`BenchmarkSyscallBurst/mode=untraced/threads=T` with ns/op and
ns/syscall. As in ior, a tree with uncommitted changes gets a
`-dirty` label, an existing baseline is never overwritten
unless `PERF_FORCE=1`, and a tree that changes while recording
writes nothing — so while other work edits the checkout, record
from a clean one (`git worktree add --detach DIR` and run DIR's
script, then copy the file into `perf/`). Record on an idle
host; the header's load average says whether it was. Baselines
are committed so a later change is judged against the numbers
that motivated it. S3's traced and bystander runs add their own
`mode=` lines.
