# The suite today

Part of [Integration tests follow ior](../integration.md), in [Testing
strategy](../../testing.md).

## The suite today

`integrationtests/` drives the real tracer binary (`bin/iormesh
run`) against the real workload (`java -jar bin/workload.jar`) and
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
  `StartHelper` gates a Go helper the same way: `edgepeer`
  (`workload/edgepeer`, cgo-free, built by `mage workloadBuild`)
  forces the edge cases a JVM cannot force deterministically —
  PID reuse, socket races and a listener restarted on its
  port (`port-owner`, task mh4) — and prints a facts line per
  case (its fds and ports) for the test to match the trace
  against.
- `parse.go` — `events.ndjson` + `graph.json`, read strictly.
- `kernelloss.go` — parses the tracer's exit summary line; a run
  whose kernel lost records, or that had skipped program runs
  (possible loss: the count is host-wide, so it cannot say
  whether a target's call was skipped), is re-run (three
  attempts), and a test whose every run lost or skipped records in
  the kernel skips with the
  unexercised marker (`unexercised.go`) instead of passing.
  Skips used to hit every run on a loaded host: Linux 7.2 let a
  task preempted inside a per-syscall tracepoint program skip
  that syscall for every other task on its CPU. Task 8e4 moved
  the syscall handlers behind classic `raw_syscalls`
  dispatchers, which run with preemption disabled; a skip left
  is a task preempted inside a `bpf()` map syscall, and still
  counts (output-contract.md, "Syscall handlers run behind `raw_syscalls`
  dispatchers").
  Undecodable records fail at once: they are a decoder bug.
- Every run also writes parquet segments (`-trace-dir` beside
  `-out`, the disk watchdog off), and every judged run checks
  M4's acceptance for single-host scenarios (task ze4): the graph
  the controller rebuilds from the segments
  (`federate.RebuildGraph`, the build's rules) is exactly the
  run's `graph.json`, node for node and edge for edge
  (`assertRebuiltGraph`; a multi-host run checks each emulated
  host's tracer run on its own), so a fork, an inherited fd, a pattern's
  app and an `F_SETFD` the rebuild could not see fail the suite.
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
| `TestTCPFamilies` | tcp-ipv4 and tcp-ipv6 goldens: full 4-tuples per family; both ends' `tcp_state` records carry the `tcp_sock` fields, and with the trace split into two pretend hosts `internal/federate` fuses the ends by ISN pair alone (M4, decision #26), and the sends and receives carry `tcp_seq`, from which `federate.ClockBounds` bounds the two pretend hosts' clock offset from both sides, around 0 (task xe4); tcp-ipv6 skips on a host without an IPv6 loopback (the workload's exit 3) |
| `TestNonJavaPeer` | non-java-peer golden: the JVM's connect/echo/log read, the Go peer's `accept4`, echo, EOF read and log write, one fused connection; the peer is followed as the JVM's child (its `exec` of `echopeer`); every event of the raw trace is `attribution: none` with no `java` or `http`; a default run has no `stack_id` and no `stacks.ndjson`; mutated goldens fail |
| `TestNativeStacks` | non-java-peer with `-native-stacks`: the golden still passes, every `stack_id` names a `stacks.ndjson` entry of its own process, and the peer's `accept4` and log `openat` stacks reach a `main.*` frame in the echopeer binary |
| `TestParquetSegments` | non-java-peer with `-native-stacks` and `-trace-dir` beside `-out` (M4, task 8b4): no active file is left, every file in `ready/` is 0644, the events segments in name order hold exactly the NDJSON events and the union of the stacks tables exactly `stacks.ndjson` |
| `TestSystemdUnit` | the M4 lifecycle on the real systemd (task 9b4, decision #19): `iormesh start` traces a sleeping process as `iormesh-<unique project>.service` with `CPUQuota` one core, `MemoryMax` 1 GiB, `RuntimeMaxSec` `-duration` + 5 min, SIGTERM and a 30 s stop timeout; `status -json` shows it running, a second start is refused, `stop` reports the end (`stop`) and `status` then the ended state, the unit gone and a published segment, no active file. A start with another `-trace-dir` is refused first (confinement); the trace goes to the pinned `/opt/iormesh/traces/<project>`, removed again with the directories above it the test created. The unit is stopped and reset in `t.Cleanup` on every path; nothing is enabled, reloaded or written to `/etc` |
| `TestPIDReuse` | a traced `edgepeer pid-target` brackets a file write and exits; at once (no settle pause: the session seeded the target's incarnation at start) an untraced launcher lands a new process on exactly its PID (`clone3` with `set_tid`, root) which does file work of its own while a traced keeper holds the session open; the keeper exits and the session ends `targets_exited` while the PID is still in use. The target's work is all under its own `pid_incarnation`; nothing of the newcomer (no event of its incarnation, no marker, no file) is traced; one process node on the PID |
| `TestListenPortFollowsRestart` | a `-listen-port` session (`-listen-grace 5s`) on an `edgepeer port-owner`, which brackets a file write and is killed; a second `port-owner` binds the same port. The session adopts it (stderr `following new owner pid N`, a second `attach` on a new process node), traces its bracketed write, and once it exits and nothing takes the port ends `targets_exited` with `no new owner within 5s`; nothing but the two incarnations is traced (task mh4, decision #23) |
| `TestSocketRaces` | `edgepeer sock-races`: a refused connect (ECONNREFUSED after `SYN_SENT`, one unresolved conn), an unreachable one (ENETUNREACH, no `tcp_state`, no conn), a reset in the accept queue (client `ESTABLISHED` → `CLOSE`; `accept(2)` returns the child as a `CLOSE` snapshot, its read sees the reset (an error or EOF, as the helper reports); two unresolved conns, never fused; a child on the client's freed fd number is a new generation of it), a reset mid-stream (one fused conn, the server's read after the reset ECONNRESET on it), a `shutdown` racing a blocked `accept4` (EINVAL on the other thread, no fd, no conn); one conn_id per socket instance, fused iff shared by two, no edge without events |
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
the test binary (`bin/integrationtests.test`, decision #30; ior
uses the repo root), for a scoped sudoers rule, runs it as root
with `sudo -n -E` from `integrationtests/`, in parallel
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


## M2 acceptance (2026-11-19)

`mage integrationTest`: 27 tests (25 at acceptance, plus the PID
reuse and socket race tests of task id4), two consecutive full
runs green on HEAD, no skips, zero lost records. milestones.md's M2
edge cases map to the suite as follows:

| Edge case | Proven by |
|-----------|-----------|
| fd reuse and aliasing | `TestFdLifecycle` (golden: reuse, dup, inherited stdio) |
| exec | `TestArgvSecretRedacted` (exec event, argv), `TestNonJavaPeer` (exec path, comm refresh), `TestFdLifecycle` (fds surviving exec) |
| pattern restarts (new PID, same app) | `TestRestartPattern` (two incarnations, one app node) |
| threaded I/O | `TestSyscallBurstFoldsEveryCall` (2 threads, folded counts exact) |
| loopback pairs | `TestWalkingSkeleton`, `TestTCPFamilies` (v4, v6), `TestUnixSocket`, `TestUDPLoopback`, `TestPipeIPC` |
| non-Java peer | `TestNonJavaPeer`, `TestNativeStacks`, `TestParquetSegments` |
| PID reuse | `TestPIDReuse`; below it, exit cleanup of `target_map` (`TestFollowForkExec`), `TestExecWatchExitedBeforeAdopt`, `internal/target` `TestAlive`, `internal/correlate` `TestNewTaskRegistersChild`, `TestSeedOutlivesPIDReuse` and `TestNewTaskChildGoneBeforeRecord` (a correlator lagging past the reuse), `internal/cli` `TestPIDTargetSeededAtStart` and `TestChildStartAfterForkIsGone` |
| socket races | `TestSocketRaces`; below it, `TestSockResetBeforeAcceptNotRegistered`, `TestSockFailedConnectUnregisters` |

The PID reuse and socket race rows were root/unit level only at
acceptance; `edgepeer` now forces both end to end. The decision
#25 budget run is recorded in docs/spikes/s3-overhead.md ("M2
acceptance measurement"); its two timing items moved to task md4
(paired CPU-time method), which gated M3's acceptance and found
both inside the budget ("Follow-up (md4)").
