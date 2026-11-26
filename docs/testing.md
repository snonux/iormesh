# Testing strategy

How iormesh is tested. The milestones whose acceptance it records
are in [milestones.md](milestones.md), the decisions it cites in
[decisions.md](decisions.md).

The test workload comes *first*, not last — and it comes first
per milestone:

- The workload framework (M0) lands before any tracer code. Every
  later milestone adds its own scenarios before its tracer code,
  so each one lands against a known target. milestones.md's scenario
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
  a test-only knob (`IORMESH_TEST_CLOCK_SKEW`, below) injects a
  clock offset per tracer, and the merge must recover it; NAT is
  emulated with a DNAT hop.
- The k8s mode rehearses M0's client/server and ingress
  scenarios as pods in kind (milestones.md M7); the microservices
  scenario as pods is planned (task uh4).
- The LLM claim is tested: a fixed question set over workload
  traces, with answers known from the scenario declarations.
  Built (M5), but not yet run with a real model, so M5 is not
  accepted; once it has run, it is to be rerun on every schema
  change.

## Race detector gate (task ij4)

`mage testRace` runs the Go suite once more under `-race`
(`go test -race -count=1 -failfast -timeout=15m`). It belongs
to the gate beside `mage test`; `mage test` itself stays
without `-race`, since the detector multiplies the cost of the
two slowest packages many times over. The package set is
every package `go list ./...` names minus a deny-list,
`gatecmd.RaceExcluded`, so a new package is race-checked from
its first commit; `internal/buildgate` pins the argv and the
list.

Measured on 2026-11-22 (`go test -race -p 2 ./internal/...`,
GOMAXPROCS=4, nice 15, a loaded host):

| package | under `-race` | in the race gate |
|---|---|---|
| `internal/generate` | 2099 s | no: its tests compile and run C harness programs, and its Go code starts no goroutine |
| `internal/ctl` | 947 s | no: its code starts no goroutine and no test calls `t.Parallel` (the tests' one goroutine is `runBounded`'s watchdog, synchronised by a channel); what it drives (segment, federate, k8s) is race-checked in those packages |
| every other package | under 60 s each | yes, among them all that start goroutines: acceptance, bpf, cli, stacks |

So the gate costs a few minutes, not the 50 the whole module
would. The 15-minute timeout is per test binary and stops a
hung test long before `mage test`'s 90. A package that turns
out slow under `-race` goes into `RaceExcluded` with its
measured cost and a row here. `IORMESH_RACE_PKGS="./internal/a
./internal/b"` replaces the set for a run on one or two
packages while working on them.

The two failures the first run found are fixed: the acceptance
runner's log writer is locked (background workload roles log
while the run does), and `internal/segment`'s
`TestReadHostileFile` skips under `-race` only (a `race`
build-tagged constant): there parquet-go's `unsafe.Slice(nil,
n)` on a flipped footer is a fatal runtime throw, not the
panic the reader recovers, so it would kill the test binary.
It still runs in `mage test`. The root suites
(`mage bpfRootTest`, `mage integrationTest`) are not run under
`-race`.

## CI

CI (GitHub Actions, task ck4) runs the gates that need neither
root nor a particular kernel: `mage fmtCheck`, `vet`, `lint`,
`test`, `testRace`, `workloadTest` and `agentReproTest` (two
clean agent jar builds must match, task bm4), plus the generate diff
check, on an Ubuntu runner ([build.md](build.md), "CI", has the
steps). What stays manual is everything that needs root, el9 or
a cluster: `mage bpfRootTest` and `mage integrationTest` (run as
root on Rocky 9.9, the main target, and on the dev host), the
el9 runbook runs below, and the kind and existing-cluster
acceptance (`mage k8sAcceptance`). A green CI run therefore says
nothing about the BPF object loading on el9's 5.14 kernel.

## Integration tests follow ior

iormesh does not invent its end-to-end test machinery: the
suite lifts ior's `integrationtests/` package (in `../ior`, its
README first) with the workload swapped for M0's. Before
changing the suite, re-read ior; this section is the map, not
the replacement:

- **Suite layout**: a dedicated `integrationtests/` package and
  `mage integrationTest` (plus a serial variant) that builds
  the tracer, the M0 workload and the compiled test binary,
  then runs it as root — skipping with an explanation when
  root or BPF is unavailable. The test binary is compiled to
  `bin/` (ior uses the repo root; decision #30) so its absolute
  path matches a scoped sudoers rule.
- **Harness** (ior's `harness.go`): start the tracer against
  a known M0 workload process — or the whole microservices
  topology — collect the trace output (NDJSON live, parquet
  segments), and expose parsed rows for assertions; ior's
  readiness wait, timeouts and BPF-object override env apply
  as-is.
- **Two expectation layers** (ior's `expectations.go`):
  aggregated event assertions (path contains, event, comm,
  min count, flag bits) over the recording, and exact
  per-event fields (fd, fd_gen, ret, err, bytes, latency)
  over the parquet rows — aggregate where exactness doesn't
  matter, row-exact where it does.
- **Per-family test files**: one `*_test.go` per trace family,
  each running its M0 scenario and asserting the declared
  events — file ops (data + meta: rename, symlink, touch,
  xattr, chmod/chown…), sockets (IPv4/IPv6, TCP/UDP
  lifecycle), local IPC, signals, process/thread/fd
  lifecycle, and with M3: Java stacks,
  class loads, HTTP events (plain + TLS — including the
  redaction expectations: the planted Authorization header,
  query secret and argv secret must be absent), OTEL ids.
- **The golden check is the integration-level assertion**:
  the normalized trace, cut to the scenario's marker bracket,
  must contain everything the scenario declares and nothing
  on its must-not-appear list ("What a golden check asserts"
  above) — subset semantics, the same spirit as ior's min-count
  and row-presence assertions. The per-family expectations are
  the fine-grained complement, not a replacement.
- **Multi-host tests**: the microservices topology across two
  test hosts (or containers in distinct network namespaces)
  drives deploy → collect → merge (and expand, M6) and asserts
  the fused cross-host edges and the sequence view's thread
  causality.
- **Emulated hosts share a kernel — compensate**: tracers in
  network namespaces on one machine share one clock, so
  reconciliation would never be exercised. A test-only flag
  injects a clock offset per tracer; the merge must recover
  it within its recorded bound. NAT is emulated the same way
  (a DNAT hop between namespaces). Built (task ye4): the knob
  is the environment variable `IORMESH_TEST_CLOCK_SKEW` on
  `run` — not a flag, so no usage text shows it and `start`
  cannot forward it; refused through the sudoers rule and by
  `start` — and shifts `wall_ns` only, the clock the fusion
  window and the bounds use; `boot_ns`, shared by the emulated
  hosts, is the truth the tests order against. Skews of
  −0.25 s and +1.25 s fuse and are recovered inside two-sided
  bounds ≈250–800 µs wide, with no cross-host order
  contradicting the truth; +3 min fuses nothing (`no_match`,
  no bound) — see "M4 two-host and NAT'ed pairs" below.
- **Kernel-support probes** (ior's `kernel_support_test.go`):
  probe for the newer syscalls/tracepoints a scenario needs
  and skip with the reason — never infer support from kernel
  version.
- **Loss honesty** (ior's `kernelloss.go` + `unexercised.go`):
  a run missing events that kernel-side loss could explain
  is re-run; a test none of whose runs exercised its case
  skips with a marker instead of passing — a silent green is
  not a green.
- **Gates, not exit codes** (ior's `gatecmd`): the runner
  verifies the required tests actually reported passed and
  prints a skip summary — exit 0 alone is not believed.
- **Parallelism**: `INTEGRATION_PARALLEL`-style knob with
  `-test.failfast` by default, serial mode for debugging.

### The suite today

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

### M2 acceptance (2026-11-19)

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

### M3 scenarios ahead of the agent

`method-flow`, `http-plain`, `http-tls` and `http-redaction`
(milestones.md scenario catalogue, M3) landed before the Java agent
(task ya4), the OTEL flavor `otel` after them (task za4), and
`http-async` with the agent's `sendAsync` hook (task wd4).
`mage workloadTest` runs each one untraced: the self-test, the
HTTP pairs as two processes (`http-tls` with a shared `--dir`
for the pinned certificate, which the server must remove
again), and their usage and run-failure exit codes. `otel`
lives in its own module and jar (`mage workloadOtelBuild`
builds `workload-otel.jar`), so the check runs it from there:
`--self-test otel`, `--list` and `--expect otel` through the
core `Main`, the two-process pair — whose server fails the
run unless the client's `traceparent` and `tracestate` arrive
as declared — and its usage errors; `workload.jar` alone must
not know the scenario. The third-party HTTP libraries' scenarios
(task de4: `http-servlet`, `http-apache`, `http-okhttp`,
`http-netty`) live in `workload/libs`
(`mage workloadLibsBuild` builds `workload-libs.jar` against
the jars vendored under `workload/libs/third_party`) and get the
same checks from there.

Their goldens came in two parts: `expect.json` with what the
tracer records without an agent, and `expect-pending.json`
adding what needs it. The agent's join (task 2b4), HTTP hooks
(task 3b4) and span-context read (task 4b4) taught
`normalize.ExpectedEvent` the `attribution`, `java` (thread,
causing method as `class#method`), `http` (`ExpectedHTTP`:
method, scheme, host, path, status, `req_seq`, version, and
header lines that must be among the event's) and `otel`
(`trace_id`, `span_id`; compared when absent too, so an event
declared without it must be outside any span) fields, so all
five scenarios have one `expect.json` again —
`TestNoPendingGoldens` fails on a new pending file — and the
integration tests run them under the agent:

| Test | What it checks |
|---|---|
| `TestJavaMethodFlow` | method-flow golden under the agent with `-spool`: every declared op on its declared Java thread and causing method; one ring read; every stack id in `methods.ndjson`; every method's class in `classes.ndjson`, the scenario's frames from the workload jar (app loader), the JDK's from `jrt:/` (task fe4); mutated goldens fail |
| `TestHTTPAsync` | http-async golden (task wd4): the client's three requests through `sendAsync` to `localhost`, each recorded at completion yet on the calling thread with `HttpFetch#callAsync` as causing method, on the server's `conn_id`, with neither `req_seq` nor latency (their stamp follows the app's callbacks) while the server numbers 1–3; the client's lookup of `localhost` as one `dns` event (127.0.0.1 among its addresses) on the thread that connects; every http event has a latency; mutated goldens fail |
| `TestHTTPPlain` | http-plain golden: one `http` event per request on each end (method, scheme, path, status, the client's host), after the I/O that completed it, caused by `HttpServe#handle` and `HttpFetch#call`; both ends on one `conn_id`, `req_seq` 1–3, each pair agreeing on method, path and status; every request found its connection; no `otel` on any event (the workload has no OTEL); mutated goldens fail |
| `TestHTTPTLS` | the same over TLS, scheme `https`, plus the certificate file I/O caused by `HttpScenario#serverContext` and `#client` |
| `TestHTTPRedaction` | http-redaction golden: the query keeps its names with every value masked, the planted `Authorization`, `X-Api-Token`, `Cookie` and `Set-Cookie` headers are recorded as `name: ***`; no planted secret in `events.ndjson`, `graph.json` or the Java tables, and no agent ring left in the spool (task 2j4); a golden forbidding the masked header fails |
| `TestHTTPServlet` | http-servlet golden (task de4, `workload-libs.jar`: the JDK HttpClient against a servlet in an embedded Tomcat): the server's `http` events come from the agent's Servlet hook in the app's class loader — one per request on Tomcat's one request thread, after the reply's write, caused by `jakarta.servlet.http.HttpServlet#service`, with the declared sizes and a latency, `req_seq` 1–3 on the client's `conn_id`, both ends pairing; mutated goldens fail |
| `TestHTTPApache` | http-apache golden (task de4, `workload-libs.jar`: Apache HttpClient 5's classic client against the JDK's HttpServer): the client's `http` events come from the agent's Apache hooks — one per request on the calling thread after the response's read, caused by `InternalHttpClient#doExecute`, on the connection its exchange used, with sizes and a latency, `req_seq` 1–3, both ends pairing; mutated goldens fail |
| `TestHTTPOkHttp` | http-okhttp golden (task de4: OkHttp 5 against the JDK's HttpServer): the client's `http` events from the agent's OkHttp hooks (Kotlin code in the app's class loader), one per call on the calling thread, caused by `RealCall#getResponseWithInterceptorChain$okhttp`, on the exchange's connection, sizes and latency, `req_seq` 1–3; mutated goldens fail |
| `TestHTTPNetty` | http-netty golden (task de4: the JDK HttpClient against a Netty HTTP/1.1 server): the server's `http` events from the agent's Netty codec hooks, one per request on the worker event loop at the encoder's write (before the flush's write), caused by `HttpObjectEncoder#write`, with sizes, a latency from the request's decoding and `req_seq` 1–3; mutated goldens fail |
| `TestOtelHTTP` | the OTEL flavor (`workload-otel.jar`, HttpURLConnection against HttpServer) satisfies its `expect.json`, the HttpURLConnection hook's `http` events included: every client event of a call and the server's reply write and `http` event carry the call's declared span ids, the server's accept and request reads none; a golden with another span, a span ending early or starting early fails |

`TestOtelGoldenIDsDeclaredByScenario` (`internal/normalize`)
checks the golden's ids against `OtelScenario`'s declaration.

### Live attach (task ge4)

Decision #2's live-attach path ([java-attach.md](java-attach.md))
has its own scenario, `live-attach`: one JVM writes a file, prints
`awaiting attach` and waits for its `--attach-gate` file, then
writes a second one and removes both. Its golden declares the
first half `attribution: none` and the rest `java` with the
causing method. Without the option it runs straight through, so
`mage workloadTest` self-tests it like the others. The harness
starts these JVMs without `-javaagent` (`Harness.AttachAgent`);
the tests need root like the rest of the suite.

| Test | What it checks |
|---|---|
| `TestJavaLiveAttach` | live-attach under `run -spool`: between the halves `iormesh attach` loads the agent, the test waits for the tracer's `java agent of pid N joined` line and opens the gate; the golden holds (first half none, second half and sweep java); no event of the JVM that ended before the attach began is attributed, and there are such events; JEP 451's warning on the JVM's stderr; mutated goldens fail |
| `TestJavaLiveAttachAtStart` | `run -agent` on a gated method-flow JVM: the tracer reports the load and the method-flow golden holds as under `-javaagent`; the JVM printed the warning |
| `TestJavaLateAttach` | late attach (task lh4): a `-pattern -agent` session starts with no JVM; http-async's server and client are exec'd afterwards (the pattern names the attempt's port) and wait at their startup gates; the tracer reports both loads and both `java agent of pid N joined` lines before the gates open; both ends' `http` events and the client's `dns` event for `localhost` are attribution java; only the pair's pids are traced |
| `TestJavaAttachMountNS` | `run -agent` on http-plain's pair (the server as nobody through `setpriv`, the client as root), each JVM in its own mount namespace with a private tmpfs `/tmp` (`unshare --mount`, `Harness.JVMWrap`; task cf4, decision #14): the tracer stages the jar and spool inside each JVM's root; both loads reported, two rings read, every request on its connection, the client's `http` events carry their host, the http-plain golden holds; no ring in the tracer's own spool, and no `iormesh-*` stage left in either JVM's `/tmp` (held open past the JVM's exit) |
| `TestJavaAttachDynamicLoading` | no tracer, per JDK (the `java` on PATH plus each JAVA_HOME in `IORMESH_ATTACH_JDKS`): a default JVM loads and warns, `-XX:+EnableDynamicAgentLoading` loads silently, `-XX:-EnableDynamicAgentLoading` refuses with `dynamic agent loading is disabled` and no ring, the JVM finishing cleanly every time and a loaded agent removing its ring at exit (task 2j4); and a root attacher loads into a JVM running as nobody, whose ring is then nobody's |

`internal/javaattach` unit-tests the handshake against a fake
`/proc` and listener: the trigger file and the one SIGQUIT, no
signal to a running listener, a process that is no JVM, has no
SIGQUIT handler or disables the attach mechanism, a planted
non-socket, and both timeouts; and the confused-deputy cases
(`safety_test.go`): a peer of another pid or uid
(`SO_PEERCRED`), a symlink at the socket path, a symlinked
`/tmp` in the JVM's root, an existing trigger file (the JVM
user's: used and left alone; a planted symlink: refused
unsignalled). `internal/cli` checks the agent-jar trust rule
(`TestTrustedAgent`). The integration tests copy the jar into a
root-owned directory for it. The staging inside a JVM's root
(task cf4) is unit-tested against a hostile fake root
(`internal/javaattach/stage_test.go`: planted stage names, a
symlinked, missing, read-only, non-sticky, group-writable or
foreign `/tmp`, a world-writable `/`, a planted jar, removal
past a planted symlink, another PID namespace, a pid gone, stage
reuse only for the same euid and `/tmp` object, a swapped stage
directory), with `TestMakeSpoolAt` in
`internal/agentspool` and `internal/cli/stage_test.go`.

### M4 microservices scenario (task jb4)

`microservices` (milestones.md scenario notes, "Microservices
end-to-end scenario") landed ahead of M4's multi-host work: four
service roles, one process each, on loopback.

| Hop | Call | Link |
|---|---|---|
| gateway → orders | `POST /orders` (201) | TLS |
| orders → inventory | `POST /inventory/reserve` | plain |
| orders → billing | `POST /billing/charge` | TLS |
| gateway → inventory | `GET /inventory/stock/widget` | plain |

Orders makes its two calls while it handles the gateway's
order, so the hops always run in table order: a chain
(gateway → orders → inventory), a fan-out from orders and from
the gateway, and a fan-in at inventory. Each hop is its own
HTTP/1.1 connection with one request (`req_seq` 1). Each
service boots in `<role>.d` in the shared `--dir`: it writes
its config, reads it back, and saves one record as `.tmp` and
then `rename`. A TLS service (orders, billing) publishes its
in-memory certificate as `<role>.crt` before it listens and
removes it once it has served. Its callers pin exactly that
certificate.

`mage workloadTest` (`magefile_m4workload.go`) runs the
self-test (the whole topology in one JVM). It then runs the
four processes the way a harness does: billing, inventory and
orders each start only after the previous one printed its
`listening <port>` line, then the gateway. All four must end
their bracket and exit 0, and the `--dir` must be empty
afterwards. Usage errors exit 2: no or a non-service role, a
missing or unconnectable callee port, `--port` on the gateway,
an option the role does not take. Run-time failures exit 1: a
caller whose `--dir` holds no certificate for its TLS callee, a
missing `--dir`, a `billing.d` the run did not create (which
must survive). A broken hop (orders pointed at a closed
inventory port) must fail orders and the gateway with 1, never
pass, and orders must still remove its certificate.

| Test | What it checks |
|---|---|
| `TestMicroservices` | microservices golden under the agent, the four services released leaves first on free ports in one `--dir`: every hop one fused connection with an `http` event on each end (`https` on the TLS hops) pairing by `(conn_id, req_seq)`; each server's request handled by `HttpServe#handle`, each call made by `HttpFetch#call`, orders' calls on its dispatcher thread inside the gateway's request; config, certificate and record I/O with their causing methods (`ServiceConfig#provision`/`#load`, `ServiceNode#publish`/`#readCert`/`#withdraw`, `DataStore#save`; the services' mkdir and rmdir stay unattributed, as no hook issues them); four rings; mutated goldens fail |
| `TestMicroservicesAcrossHosts` | the services on three emulated hosts (task 1f4), each traced as its own project under the agent: each host's trace holds only its own processes, three distinct host ids; the merge fuses exactly the three cross-host hops, each across two projects with one 4-tuple mirrored, and leaves nothing unresolved; the merged trace satisfies `expect-spread.json` and its requests pair; a service on the wrong host, a hop declared at loopback and the cross-host TLS hop declared plain each fail it — `internal/meshcheck.Check`, shared with the kind pod run (task uh4) |

The golden was written from strace runs of the four processes
(JDK 25) and confirmed against a traced run (iormesh run +
normalize -check as root, 5214 events): the one correction was the
services' mkdir and rmdir, which stay attribution none because no
agent hook issues them.

**One service per host (task 1f4).** A server takes `--bind A`
and a caller `--<callee>-host A` next to `--<callee>-port N`,
both IPv4 literals defaulting to 127.0.0.1, so the loopback run
and `expect.json` are unchanged. A TLS service's certificate
names its `--bind` address as its only IP SAN, so a caller
verifies it only at that address. The key stays in memory. A
caller still pins `<callee>.crt` from its own `--dir`. Across
hosts, putting the public certificate there is the deployment's
job: the rig's namespaces share one `--dir`, and the acceptance
run copies the file over ssh. `0.0.0.0` is a usage error
because a wildcard names no address a certificate or a caller
could use.

`mage workloadTest` (`magefile_m4spread.go`) runs the topology
a second time with each server on its own loopback address
(billing 127.0.0.2, inventory .3, orders .4), every caller
dialing it there. Address usage errors exit 2: a name, a
wildcard, an out-of-range or IPv6 literal, `--bind` on the
gateway, `--host`, or a host option for a service the role does
not call. A `--bind` address the host lacks (192.0.2.1) exits
1. The pin mismatch also exits 1: the gateway's `--dir` holds
billing's real certificate as `orders.crt`, valid for the same
address but not orders' own. The gateway must fail the
handshake (`PKIX` on stderr) and must not fall back to trusting
the server.

`TestMicroservicesAcrossHosts` spreads the services over the
rig's three namespaces, each an emulated host with its own
project, tracer and agent spool:

| Host | Address | Services | Project |
|---|---|---|---|
| hosta | 10.231.0.1 | gateway, inventory `:80` | edge |
| hostb | 10.231.0.2 | orders `:443` (TLS) | orders |
| hostc | 172.31.0.2 | billing `:443` (TLS) | billing |

Three hops cross hosts: gateway → orders (TLS), orders →
inventory (plain) and orders → billing (TLS, through hostb's
bridge veth). Gateway → inventory stays on hosta. The merged
golden `expect-spread.json` is `expect.json` with four changes:
every process `runs_on` its host, each file edge on its host's
`$h` (a certificate is one file node on its publisher's host and
another on its reader's), the servers' accept and the callers'
connect at the literal addresses and ports, and each client
http event's `host` set to the address it dialed.
`TestSpreadGoldenAgrees` (`internal/normalize`) keeps it in step
with `expect.json`.

Host traces are merged by wall clock, each host's events kept in
the order its tracer wrote them (`mergeByWall`): an http event
is stamped at its request's start but written at its end. The
hosts' Java method ids are moved into ranges of their own, since
ids are per trace. This test found a tracer bug. A cross-host
connection holds the correlator's stream back for its whole
fuse window, because its far end never shows up locally. The
correlator's clock moved only with records, so on an idle
stream the hold lasted until the next record, however late.
The Java join's 2 s http timeout ran on the clock, so it
released http records ahead of their own connect or accept,
and they lost their `conn_id` (about one run in eight). Two
fixes (task 1f4 and its review). First, the run loop now ticks
the correlator (`Correlator.Tick`) while no records are
waiting, so the hold really ends 5 s after the socket was first
seen. Second, the join's timeout counts from the end of that
hold (`JavaJoinConfig.UpstreamHold`). An http event on an idle
stream therefore goes out up to 7 s after its request ended.
`TestJavaJoinHTTPWaits` and `TestIdleStreamHeldAcceptKeepsConn`
pin this. The second test uses an idle stream behind a held
accept: with the tick the http event keeps its `conn_id`, and
without it the event loses it.

### M4 two-host and NAT'ed pairs (task kb4)

`tcp-two-host` and `tcp-nat` (milestones.md scenario notes, "Two-host
scenarios") are one Java scenario (`TcpHosts`) under two names:
M0's client and server with explicit IPv4 literals, the server
`--bind`ing an address and the client dialing `--host`. Only the
network between the hosts differs, so only the goldens do. Each
scenario has a golden per host and a merged one:

| Golden | What it declares |
|---|---|
| `expect-client.json` | the client host's trace: file work, connect, 5 bytes each way, close; its end `unresolved`, on `$h1` |
| `expect-server.json` | the server host's trace: file work, bind, accept, read 5, write 5, read EOF, close; its end `unresolved` |
| `expect.json` | the merged mesh: both processes, `runs_on` two distinct hosts (`$h1`, `$h2`), one connection `fused`, both ends' events on it |

The integration rig (`integrationtests/multihost.go`,
`multihost_test.go`) emulates the hosts on one machine:

```
client host 10.231.0.1 --veth-- 10.231.0.2 server host
                                172.31.0.1 --veth-- 172.31.0.2 container
```

Three network namespaces, every interface created inside its
own. Each host's tracer runs under `unshare --mount --uts` with
an `/etc/machine-id` of its own bind-mounted over the real one
(random per harness, kept across its runs, as a real host keeps
it across sessions), so it stamps its own host id, as a second
host would; the workloads
and tracers enter the host's network namespace with `nsenter`
(`ip netns exec` would remount `/sys` and lose tracefs). The NAT
is what `docker run -p 880:80` installs: a DNAT rule in the
server host's prerouting chain from 10.231.0.2:880 to the
container's 172.31.0.2:80. In `tcp-two-host` the server runs on
the server host itself at 10.231.0.2:80. The goldens declare
these addresses and the literal ports 80 and 880 (the
normalizer keeps ports below 1024), so the NAT'ed client must
show 10.231.0.2:880 and its server 172.31.0.2:80.

`mage workloadTest` runs both scenarios' roles on loopback
with explicit addresses, and their usage errors (exit 2: a
role without its address option, the other role's option, a
name or a malformed or IPv6 address, client port 0) and run
failures (exit 1: nobody listening, a `--bind` address the host
lacks, a missing `--dir`). `TestMeshGoldensAgree`
(`internal/normalize`) keeps the three goldens in step: the
merged one declares exactly each host's events and edges, the
connection fused instead of unresolved.

| Test | What it checks |
|---|---|
| `TestTwoHostsTwoProjects` | `tcp-two-host` on the rig, the two hosts traced as projects `shop` and `billing`: each host's trace satisfies its golden and holds only its own process, the two host ids differ, and a golden naming the other end's address is caught. Subtest `merged`: `federate.Merge` of the two traces fuses exactly one cross-project connection with nothing unresolved, whose ends saw one 4-tuple, mirrored; the merged trace (events renamed to the merged conn id) satisfies `expect.json`, which fails with both ends on one host or on two connections; the client's trace merged alone stays `no_match` |
| `TestNATPair` | the same for `tcp-nat`, the server in the container: the fused connection's client end saw 10.231.0.2:880 and its server end 172.31.0.2:80 |
| `TestClockSkewRecovered` | `tcp-two-host` with the client's clock skewed −0.25 s and the server's +1.25 s: the offset the tracers actually stamped (the difference of their `wall_ns − boot_ns`, one value per trace) is 1.5 s within 1 ms; the merge fuses the connection, and every clock bound for the pair is two-sided (tcp_seq, task xe4), contains that offset and excludes 0; `Timeline.Order` over every client/server pair of event starts and ends never contradicts the boot-clock order, orders some pairs, and the raw wall stamps misorder some (else the skew tested nothing) |
| `TestClockSkewBeyondWindow` | the server's clock +3 min, beyond the 2-minute fusion window: nothing fuses, both ends stay `no_match`, and the merge reports no clock bound |

Both pass as root on real traces, `merged` subtests included,
now that the tracer records the four `tcp_sock` fields on
`tcp_state` (task ve4): the merge keys both ends and fuses them,
across the DNAT hop too. A trace without those fields fails
`merged` up front, naming the cause. A run with a real docker
daemon (the rig's DNAT rule is docker's) and on two real hosts is
still the owner's.

The injected clock skew of milestones.md's M4 acceptance (task ye4)
is the environment variable `IORMESH_TEST_CLOCK_SKEW`, a Go
duration (`1.5s`, `-3m`, at most a day either way) that `iormesh
run` adds to the boot-to-wall offset it measures at start.
Every `wall_ns` the run stamps derives from that offset (the
Java events' too), so the whole host's wall clock moves; `boot_ns`
does not, and since the emulated hosts share one boot clock it
is the true order the tests check the merge against — the
fusion window and the clock bounds work on `wall_ns` only. It is
**test-only**: an environment variable rather than a flag, so it
appears in no usage text and `start` cannot forward it; a run
through the sudoers rule refuses it, as does `start` (its unit
would never see it); a skewed run says so on stderr (`TEST
ONLY: wall clock skewed by …`). The rig passes it per host
(`Host.ClockSkew`). On veth one exchange each way gives bounds
≈250–800 µs wide including the 60 µs drift allowance per side.


The rig leaves the machine as it found it, with two side
effects of `ip netns` and nft themselves: the first `ip netns
add` turns `/run/netns` into a shared bind mount, and the DNAT
rule autoloads the `nf_nat` and `nf_conntrack` modules, which
stay loaded. Its namespaces are named `iorm<pid>-<n>-{a,b,c}`
after the test binary's PID. `t.Cleanup` deletes them, but a
killed binary (Ctrl-C, SIGKILL, a `-timeout` panic) never runs
it, so every new rig first deletes the namespaces of binaries no
longer alive, and their veths and nft table go with them.
Emulated hosts take no tracer options and no `Credential`
(`TraceHosts` and `StartWorkloadIn` refuse them): the host
tracer is the default one, bound by its workloads' PIDs or, with
`Host.ListenPort`, by listening port (M6 below).

### M6 on the rig (task tb4)

`TestMeshExpansion` (`integrationtests/expansion_test.go`) is
milestones.md's M6 acceptance on the rig: the spread of
`TestMicroservicesAcrossHosts` with only hosta (the gateway's
host, project `edge`) registered and the allowlist covering
10.231.0.0/24 and 172.31.0.0/24. Each round re-runs the whole
scenario — every service on its host, only registered hosts
traced, the others' services running untraced — and adds the
round's traces to a store that is merged whole, as the
controller's is. `expand.Plan` and `expand.Enroll`, the code
`iormeshctl expand -apply` runs, decide the rounds; what the rig
does not do is the SSH deploy, so an enrolled host's tracer is
started as `iormesh run -listen-port PORT` (`Host.ListenPort`),
the target the registry entry names, once its server listens
and before the gateway is released (`TraceHosts`). The deploy
argv itself is `internal/ctl`'s `TestExpandApply`, over a fake
transport. The rig needs no ssh, so it never meets the rule a
real round does: each enrolled host's ssh host key must already
be in `known_hosts` (`StrictHostKeyChecking=yes`, `BatchMode`;
`ssh-keyscan` it and verify the fingerprint out of band before
`-apply`), and `TestExpandApplyFailedDeploy` checks the hint
`-apply` prints when ssh refuses one.

| Round | Traced | Asserted |
|---|---|---|
| 1 | hosta | the plan is exactly `deploy 10.231.0.2:443 hop 1`; enrolled as `10.231.0.2`, hop 1, `edge`, listen port 443 |
| 2 | hosta, hostb by port 443 | round 1's gateway → orders connection is still `no_match` in the store's merge, fused with nothing; at `-hops 1` billing is `hop_limit` and orders `enrolled`; with an allowlist of 10.231.0.0/24 alone billing is `not_allowed` and `Enroll` enrolls nothing; at `-hops 2` billing is enrolled at hop 2. The cycle orders → inventory (back to hosta) fuses and proposes nothing |
| 3 | all three | nothing to deploy, at `-hops 5`: both earlier peers are `enrolled`; the round's own merge fuses exactly the three cross-host hops with nothing unresolved (`assertSpreadFused`, within project `edge`), and the network part of `expect-spread.json` holds — accepts, connects and requests at their addresses and schemes, every process on its host, every connection fused. A TLS hop declared plain, a hop at loopback or billing on the gateway's host each fail it |

Inbound skips are left out of the compared plans: inventory's
side of orders' call in round 1 is a server half whose peer
port is ephemeral, never a candidate. The golden's file and
JVM-startup events are not compared: a tracer bound by
listening port attaches to a server already running. Each round
gives every host a fresh Java agent spool, so an untraced JVM's
rings never wait for a later round's tracer.

### M5 on the rig (task rb4)

`TestViewsAcceptance` (`integrationtests/m5*_test.go`) is milestones.md's
M5 acceptance on the rig. It runs the spread of
`TestMicroservicesAcrossHosts`, copies each host's ready segments
into `<store>/<project>/<host>/` as collect leaves them, names
the three servers (`iormeshctl services name`) and builds with
the real `../bin/iormeshctl` (`mage build`), as root, HOME in a
scratch directory. The SSH deploy and collect are what the rig
does not do.

| Surface | Asserted |
|---|---|
| model | orders, inventory and billing named with their scheme, every edge fused, nothing unresolved, no bad segment |
| sequence | the checkout (`POST /orders`) is exactly the declared story: gateway `main` → orders' serving thread → inventory, then billing, each writing its record as create-tmp, write, rename before its `200`, then orders' own record and the `201` — lanes checked by host, role (PID) and thread; every step `causal`, every hop `paired`; the stack of each hop and record write holds the method that caused it (`ServiceNode#checkout`, `ServiceNode#handle`, `DataStore#save`) |
| `show` | services, edges, conversations; `show sequence ID` in order with the rename's top frame and ref; `-json` its whole stack; `query -ref` its raw event; `show thread orders/HTTP-Dispatcher`; `show processes`: orders' config, record, and server socket with the gateway as peer |
| TUI | ob4's key walk on the model: filter to orders, its conversation, down to the rename — the detail names `DataStore#save` and the `query -ref` command — then the thread's story |
| site | `sites/latest`'s embedded model equals `model.json`; CSP `default-src 'none'`, no remote reference; with `IORMESH_SITE_BROWSER=1`, headless Chrome (`--no-sandbox` as root) passes `#/selftest` with an empty console and shows the rename step's stack at its route (set without a browser, it fails; the browser process runs with background networking, component updates and sync off) |
| SVG | the shipped exports are held by a rebuild writing the same bytes into the same site folder, `graph.svg` naming hosts and servers, and each `sequence/ID.svg` being its conversation; the goldens `workload/scenarios/microservices/graph-spread.svg` and `sequence-checkout-spread.svg` are re-rendered by the test from `graph.json` and the model, without the run's ids and the machine's own file work (connections named by role pair, a second one of a pair `#2`; `IORMESH_UPDATE_SVG=1` rewrites them) |
| eval | `eval.Main` over the store with a scripted stand-in model — the test binary itself (`TestMain`, `-iormesh.eval-stand-in`) — scores 10/10, answering two questions only once its `QUERY` slice and `SOURCE` file showed the evidence; a wrong stand-in scores 0 and is refused |

The stand-in knows the declared answers: it proves the pipeline,
not a model. The eval criterion itself waits for the owner's
model command (`mage eval`, below). The scenario's clients are
synchronous, so it has no async hop: that every async or unpaired
hop is drawn time-ordered or unordered is held by
`internal/viewmodel`'s tests. Passed four times on 2026-11-20
(Chrome 144).

## M4 acceptance on two real hosts (task lb4)

The rig above proves M4's criteria on network namespaces; the
acceptance itself (milestones.md M4, "Acceptance") also needs one run
on two real hosts. **Status (2026-11-21): accepted** (milestones.md
M4, an owner-delegated advisor decision): run six times on two
Rocky 9.9 VMs the owner provided; the sixth (qf4, the build of
35c7de7) printed every criterion PASS, and so did a seventh on
a HEAD build (task 3f4, the build of 5eedf77). Both VMs share
one physical host; the two kf4 runs before the sixth passed 14
of 15 each, the fourth failing the clock bound (before jf4),
the fifth the spread M4-9 (skipped runs, qf4) (see "Run on
Rocky 9.9" below). The M5 and M6 re-checks on the same VMs are
"M5 on Rocky 9.9" and "M6 on Rocky 9.9" below. A rerun at
9bca1b2 (task 3k4, 2026-11-26) passed 14 of 15: the runner's
M4-8 check missed the `@host` loopback labels (task 7m4). With
7m4's fix, a rerun at c10b0a3 (task 4k4, 2026-11-26) passed all
15: **M4 is accepted on el9** (see "Guide walk on Rocky 9.9"
below).

`mage m4Acceptance HOST1 HOST2` (`magefile_m4acceptance.go`,
`internal/acceptance`) runs the whole sequence and checks each
criterion itself. It contacts exactly the two hosts named
(any other name is refused in code), and refuses to start
without both, with the same host twice, with two names for
one machine (same `/etc/machine-id`), or with the controller
itself as a host. iormeshctl runs with
`HOME` set to a private work directory, so its registry holds
only the two hosts and the operator's own registry, store and
service database are never touched. On the servers the tracer
is started, stopped, upgraded and uninstalled only by
iormeshctl; the runner itself runs the workloads (each under
`timeout 300`, killed with `pkill` when its scenario fails),
read-only probes, one fill file, and one privileged command —
the read-only `sudo -n /opt/iormesh/bin/iormesh status -json`,
through the same sudoers rule. The operator's own sudo is never
used on a server. HOST1 gets the clients, the single-host
scenarios and the microservices; HOST2 the servers and the
watchdog project.

### Prerequisites

The controller (Linux or macOS):

- The repo with Go, a JDK (`mage workloadBuild`) and mage; the
  target builds the workload jar and a cgo-free iormeshctl
  itself.
- rsync 3.4.0 or newer first in `PATH` (the preflight refuses
  older ones and openrsync; [security.md](security.md)).
- Key-based SSH to both hosts through the agent (`BatchMode`),
  each host key already in `~/.ssh/known_hosts`
  (`StrictHostKeyChecking=yes`; compare the fingerprint out of
  band, never accept it blind).
- A release key the operator makes once with minisign, outside
  the tree and never committed (`mage sign` refuses a key
  inside it):

  ```sh
  minisign -G -p ~/.config/iormesh/minisign.pub \
              -s ~/.minisign/iormesh.key
  ```

- A signed build for each host's arch and the signed agent jar
  in `dist/` (deploy ships the jar to both hosts: the
  microservices projects trace with the agent, task kf4):

  ```sh
  mage buildPodman      # or buildDocker; dist/iormesh-linux-*
  mage agentBuild       # dist/iormesh-agent.jar
  IORMESH_MINISIGN_KEY=~/.minisign/iormesh.key mage sign
  ```

Each host (two machines, Linux, x86_64 or aarch64):

- Kernel with BTF (`/sys/kernel/btf/vmlinux`) and the classic
  `raw_syscalls` tracepoints. 6.2 or newer is needed for the
  clock bound (M4-10: `tcp_seq` reads use `bpf_rdonly_cast`).
  The Java mark uprobes need 6.10 (or 6.6–6.9 with commit
  46ba0e49); the microservices projects load the agent (task
  kf4). The dispatcher findings (output-contract.md, "Syscall handlers run
  behind `raw_syscalls` dispatchers") were measured on Linux 7.2.
  Rocky 9.9's 5.14.0-741.22.1.el9_9 has backported the BPF
  features these floors name (`bpf_rdonly_cast`, uprobe_multi
  links with 46ba0e49's process-wide pid filter) and runs the
  tracer since tasks gf4 and hf4, and the root BPF and
  integration suites since task nf4 (see "Run on Rocky 9.9" and
  "Root suites on Rocky 9.9" below); the version floors above do
  not describe el9.
- systemd, rsync, Java 21 or newer on the SSH user's `PATH` (the
  goldens were confirmed on JDK 25), `ss`, GNU `df` and `stat`,
  `fallocate`, `timeout`, `pkill`, a POSIX login shell.
- HOST2 reachable from HOST1 on TCP `PortBase+2` (18202) and,
  with NAT, `PortBase+3` (18203): the prerouting redirect
  rewrites the port before firewalld's input filter sees the
  packet, so it is 18203 that must be open, not the DNAT'ed
  port. Open them for this run only — `firewall-cmd
  --add-port=18202/tcp --add-port=18203/tcp` without
  `--permanent` is runtime-only, gone at the next reload, and
  `--remove-port` with the same arguments closes them at once.
  The microservices spread (M4-9) also needs HOST2's orders on
  `PortBase+16` (18216) open to HOST1 and HOST1's inventory on
  `PortBase+15` (18215) open to HOST2; `base64` on both hosts
  copies orders' certificate.
- At least 1 GiB free on the partition of `/opt/iormesh` (HOST2
  fills 128 MiB of it for the watchdog trip).
- Idle: no `iormesh-*` unit loaded and nothing under
  `/opt/iormesh/traces` — the run ends with `rm`, which
  uninstalls `/opt/iormesh`, so it refuses a host whose traces
  are not its own.
- Bootstrapped (decision #1: the first install is the
  operator's own sudo) with `iormeshctl bootstrap` (task qh4),
  `ops` standing for the SSH user and `ARCH` for the host's
  `amd64` or `arm64`. It verifies the build against the pinned
  key before anything leaves the controller — the first
  install has no installed `upgrade` to check it — and checks
  every installed file's SHA-256 before it is renamed into
  place ([security.md](security.md)):

  ```sh
  # bin/iormeshctl from `mage build`, or
  # CGO_ENABLED=0 go build -o bin/iormeshctl ./cmd/iormeshctl
  # bootstrap contacts only registered hosts; a scratch registry
  # keeps the operator's own clean (pass it to bootstrap too)
  r=~/iormesh-boot/servers.yaml
  bin/iormeshctl add -registry "$r" -project boot -listen-port 1 HOST
  # print the exact steps and run them on HOST by hand (sudo may
  # ask for the password there):
  bin/iormeshctl bootstrap -registry "$r" -arch ARCH -user ops HOST
  # or, when ops has sudo without a password, run them over ssh:
  bin/iormeshctl bootstrap -registry "$r" -apply HOST
  ```

  Both install the rule
  `ops ALL=(root) NOPASSWD: /opt/iormesh/bin/iormesh` as
  `/etc/sudoers.d/iormesh`, under bootstrap's marker line.
  `/opt` itself must be root-owned and not writable by others
  (the steps' `upgrade` check refuses otherwise, before the
  rule goes in). The run's `rm` uninstalls `/opt/iormesh` and,
  with it, the rule bootstrap installed (a rule without the
  marker, laid down by hand before task qh4, stays: `sudo rm
  /etc/sudoers.d/iormesh` removes it), so the next run
  bootstraps the hosts again.
- For the NAT criterion, a DNAT on HOST2 from a port of the
  operator's choice to the NAT server's `PortBase+3`, the way
  `docker -p` would do it (removed again with `sudo nft delete
  table ip iormesh_m4`). `redirect` rewrites the destination to
  the primary address of the interface the packet came in on,
  so `IORMESH_M4_ADDR2` (where the server binds and the client
  dials) must be that address; for a secondary address use
  `dnat to ADDR2:18203` instead:

  ```sh
  sudo nft add table ip iormesh_m4
  sudo nft 'add chain ip iormesh_m4 pre { type nat hook prerouting priority dstnat; }'
  sudo nft add rule ip iormesh_m4 pre tcp dport 18880 \
       redirect to :18203
  ```

### Running it

```sh
export IORMESH_M4_NAT_PORT=18880
LIBBPFGO=../libbpfgo mage m4Acceptance app1.example.org app2.example.org
```

| Variable | Default | Meaning |
|---|---|---|
| `IORMESH_M4_ADDR1`, `_ADDR2` | `getent ahostsv4` on the other host | each host's IPv4 as the other reaches it (the servers bind `_ADDR2`, and the spread's inventory `_ADDR1`, so both must be addresses of the hosts' own interfaces) |
| `IORMESH_M4_NAT_PORT` | unset: M4-7 SKIP | HOST2's DNAT'ed port |
| `IORMESH_M4_DIST` | `dist` | signed builds |
| `IORMESH_M4_KEY` | `~/.config/iormesh/minisign.pub` | the pinned release key, copied into the work directory |
| `IORMESH_M4_WORKDIR` | `~/.iormesh-m4` | iormeshctl's `HOME`; must be empty or absent; kept afterwards (store, builds, service database) |
| `IORMESH_M4_PORT_BASE` | 18200 | workload ports are base+1 … base+16 |
| `IORMESH_M4_ROUNDS` | 3 | streaming collect rounds (2–10), each after a 65 s rotation |
| `IORMESH_M4_FILL_DIR` | the run's dir or `/var/tmp` | watchdog fill file's directory on HOST2; must be on `/opt/iormesh`'s filesystem |

A run takes about ten minutes and prints every command it
issues, then one result line per criterion and a verdict. Exit
status 0 only when every criterion passed; M4-9, the
microservices spread over both hosts, runs since task 1f4. Every
workload role
runs under `timeout 300`, and a failed scenario's servers are
killed, so a broken client cannot hang the run. The run's own
files on the servers (`~/.iormesh-m4-run`: the jar and the
scenarios' `--dir` trees) are removed at the end; deploy's
staging directory (`~/.iormesh/staging`) stays, as after any
deploy. **Because `rm` is a criterion, a finished run leaves
the hosts without `/opt/iormesh`: bootstrap them again before a
rerun**, and move the old work directory away.

The first ^C cancels the current step and still runs the
teardown (`rm` of both hosts, leftover roles killed, the run's
directory and fill file removed; at most 5 minutes); a second
^C ends the run at once (exit 130). mage's own handler would
exit 5 seconds after the first ^C, so the target takes SIGINT
away from it (`acceptance.InterruptContext`, task mi4) and the
teardown runs to the end in the same process, results table
included. If the teardown did not finish, clean up by hand —
with an iormeshctl built as above, and the operator's XDG
variables cleared so it reads the run's registry:

```sh
env -u XDG_CONFIG_HOME -u XDG_DATA_HOME HOME=$HOME/.iormesh-m4 \
    bin/iormeshctl rm HOST                  # per host
ssh HOST pkill -f .iormesh-m4-run/workload.jar
ssh HOST rm -rf .iormesh-m4-run            # HOST2 also: rm -f /var/tmp/.iormesh-m4-fill
```

Every session also ends by itself after its `-duration 1h`.

Send back the results block and the work directory's path.

### What the run checks

| Id | Criterion (milestones.md M4) | How | Expected |
|---|---|---|---|
| M4-0 | — preflight | ssh `pwd`; arch, kernel, machine ids (distinct, neither the controller's); `test -f` of the binary and key; `sudo -n -l /opt/iormesh/bin/iormesh`; the tools; no unit or trace; signed build per arch and signed `dist/iormesh-agent.jar`; local rsync; jar pushed | PASS, arch and kernel per host printed |
| M4-1 | `add` → `deploy`, zero manual steps on the servers | `add -project P -pattern 'java -jar */.iormesh-m4-run/workload.jar <scenario> --role <role> *' HOST -duration 1h` per role, `deploy HOST1 HOST2`, then `status -json` per project | every project `running` |
| M4-2 | streaming collect | per round: `file-basic` on HOST1, 65 s, list every `ready/`, `collect`, list again | every listed file gone from the server and in the store; the project dir holds only `ready/`, `state.json`, `.lock` and `.active-*`; ≥ 1 file moved |
| M4-3 | `collect` alone changes nothing but the local store | hash every file of the work dir outside the store before and after each `collect` | unchanged |
| M4-4 | `refresh` produces the merged graph export | `refresh` (no host, no project) | exit 0; every project and both hosts in the model; no damaged segment |
| M4-5 | merged mesh equals the single-host result | `build -project m4single`, `query -project m4single`, tcp-loopback's golden (`internal/normalize`) on the rebuilt graph and events | no violation (the rig checks graph equality on every run) |
| M4-6 | M0 client and server as two projects fuse | `build` of m4shop+m4billing (`tcp-two-host`, client HOST1, server HOST2); `build` of m4shop alone | one fused cross-host connection, none unresolved; alone, the client's end unresolved |
| M4-7 | NAT'ed pair fuses on different 4-tuples | `tcp-nat` through the DNAT; `build` m4natc+m4nats; `query -conn` | one fused connection; client dialed the NAT port, server accepted on base+3 |
| M4-8 | microservices: every REST edge fused, plain and TLS | the four services on HOST1, one project each, read from the refresh's model; each project added with `-spool /run/iormesh/spool/P -agent /opt/iormesh/lib/iormesh-agent.jar` (so deploy installs the signed jar), each JVM started with `JAVA_TOOL_OPTIONS=-javaagent:/opt/iormesh/lib/iormesh-agent.jar=spool=/run/iormesh/spool/P` — the TLS label comes from the agent's http records (task kf4) | orders and billing `tls`, inventory `plain` (2 conns), all fused |
| M4-9 | microservices spread over both hosts (task 1f4) | with the agent as in M4-8 (task kf4); gateway and inventory (`--bind ADDR1`, base+15) on HOST1, orders (base+16) and billing (base+14) on HOST2 (`--bind ADDR2`), one project each (m4sgw, m4sinv, m4sord, m4sbill), every pattern ending in its `--dir` `microservices-spread`, so M4-8's sessions never trace them; orders' public certificate copied from HOST2 to HOST1 over ssh (`base64`); `build` of the four projects | two fused cross-host connections (gateway→orders TLS, orders→inventory plain), none unresolved; orders and billing `tls`, inventory `plain` (2 conns), all fused |
| M4-10 | clock bound | the m4shop+m4billing model's clock windows | non-empty and two-sided, widths printed (real clocks; the injected skew is `TestClockSkewRecovered`'s) |
| M4-11 | `build` with the network down is byte-identical | both outputs deleted, then the same build rerun under `unshare -rn` (else with a `PATH` lacking ssh and rsync) | `graph.json` and `model.json` byte-equal |
| M4-12 | naming two services shows them by name | `services name -port 18202 ADDR2 m4-billing-svc`, `services name -port 18213 127.0.0.1 m4-orders-svc`, `build`, `services` | both names in the model's services and edges and in the listing |
| M4-13 | disk watchdog | `add` m4disk on HOST2 with `-disk-limit 0 -disk-min-free <free − 64 MiB>`, `deploy -project m4disk`, `fallocate` 128 MiB, wait | `ended`/`disk_limit`; collected segment intact; `show`'s summary prints `ended disk_limit` |
| M4-14 | `rm` leaves nothing under `/opt/iormesh` | `rm HOST1`, `rm HOST2`, `test -e /opt/iormesh`, units, registry, `build` of every project | no `/opt/iormesh`, no unit, empty registry; every session's end in the store |

Not run by the target: decision #25's tracer-CPU item (met,
task ee4, measured locally) and the injected clock skew (the
rig's, task ye4).

### Local verification (2026-11-20)

This machine has no second host and no sshd, so the target has
not run against real hosts. What ran here:

- `internal/acceptance`'s unit tests (`mage test`):
  `TestDryRun` drives every phase against fake hosts and a fake
  iormeshctl — each criterion the fakes satisfy passes, the
  single-host golden fails on the fakes' empty trace (the check
  is not vacuous), the microservices spread (M4-9, task 1f4)
  passes with orders' certificate copied to HOST1, and the
  verdict is "not accepted"; `TestDryRunSpreadNoCertificate`
  fails M4-9 when the certificate cannot be read and never runs
  the gateway; `TestDryRunTouchesOnlyTheTwoHosts` finds every
  remote command on the two hosts and no tracer lifecycle
  command outside iormeshctl; `TestPreflightRefusesBusyHost`
  stops at a host with someone else's traces before iormeshctl
  runs; the host gate, the config's refusals and the rsync
  version check have their own tests. Added in the review
  round: `TestInterruptStillTearsDown` (a ^C during the
  streaming rounds still runs `rm` on both hosts, `pkill`s the
  roles and removes the run directories),
  `TestInterruptOutlivesMageGrace` and
  `TestSecondInterruptKills` (a child process playing mage's
  generated main, its 5-second grace shortened: a teardown
  longer than the grace still finishes after one SIGINT, and a
  second SIGINT exits at once with 130),
  `TestFailedClientStopsServer` (a failed two-host client gets
  its server killed and M4-6 reports the workload's failure;
  the fakes also fail any role started without `timeout 300`),
  `TestPreflightRefusesController` and `TestNewExecCtlEnv`.
  Task kf4 added `TestDryRunMicroservicesWithAgent` (exactly the
  eight microservices projects are added with their spool and
  the installed jar, every microservices JVM starts with that
  jar as `-javaagent` into its own project's spool, no other one
  does, M4-8 and M4-9 pass) and
  `TestPreflightRefusesMissingAgentJar`; the deploy and upgrade
  halves are unit-tested in `internal/ctl`
  (`TestDeployShipsAgent`, `TestDeployRefusesBadAgent` — a
  tampered, unsigned, foreign-signed, renamed or other-version
  jar pushes nothing — `TestDeployAgentUpgradeRefused`) and
  `internal/cli` (`TestUpgradeInstallsAgentJar`,
  `TestUpgradeRefusesAgentJar`, `TestUpgradeRefusesAgentLayout`,
  the confined `-agent` in `TestRunConfinement`); 21 mutants of
  the new checks were each killed by a test. The review round
  added `TestStartForwardsResolvedAgent` (a symlink to the jar
  passes, the unit argv carries the resolved path, never the
  symlink), `TestStartForwardsCleanSpool`,
  `TestStartRefusesOtherAgent`, `TestConfinementSymlinkedAgent`,
  `TestUnitRunIsConfined`, `TestUpgradeTracerDropsAgent` and the
  0644 check; 7 more mutants, each killed.
- `mage m4Acceptance m4-host1.invalid m4-host2.invalid` (real
  SSH transport, a throwaway key and dist): the preflight fails
  on the first `ssh … pwd`, every other criterion is NOT RUN,
  nothing is added or deployed, exit status 1.
- `mage test`: every package passes (30). `mage build`, `vet`,
  `lint` (0 issues) and `fmtCheck` pass.
- `mage integrationTest` as root: all 47 tests pass, no skip,
  the rig's `TestTwoHostsTwoProjects`, `TestNATPair`,
  `TestClockSkewRecovered`, `TestClockSkewBeyondWindow`,
  `TestMicroservices`, `TestParquetSegments` and
  `TestSystemdUnit` included. A first run under load (≈ 5,
  other workers' suites beside it) failed `TestJavaLiveAttach`
  once on an attribution (`ambiguous 12`); the rerun passed,
  and this task changes nothing that test runs.

### Run on Rocky 9.9 (2026-11-21)

Two test VMs on the owner's homelab, `iom1` (10.20.0.140, HOST1)
and `iom2` (10.20.0.141, HOST2): Rocky Linux 9.9 minimal, 2 vCPUs,
2 GiB, kernel 5.14.0-741.22.1.el9_9.x86_64 with BTF, JDK 25.0.5,
rsync 3.2.7 (the controller's is 3.5.1). Bootstrapped as above
with `ops` = the SSH user and a test release key; ports 18202,
18203 and 18216 opened runtime-only on HOST2 and 18215 on
HOST1; the DNAT 18880 → 18203 on HOST2;
`IORMESH_M4_ADDR1/2` and `IORMESH_M4_NAT_PORT=18880` set.

The el9 kernel has every BPF feature the floors above ask of
6.2–6.10: `bpftool feature probe` and the BTF list
`bpf_rdonly_cast`, `bpf_cast_to_kern_ctx`, uprobe_multi and
kprobe_multi links, ringbuf, `bpf_get_current_task_btf`,
`bpf_ktime_get_boot_ns`; the 704 `syscalls` tracepoints and
`raw_syscalls` are there. Two el9 defects stopped the tracer and
were fixed in this task (each loaded per program on el9 — all 744
— and checked on 7.2 with `mage test`, `mage bpfRootTest` and
`mage integrationTest`, 51 passed, none skipped):

- **gf4** — the verifier refused `handle_inet_sock_set_state`
  ("dereference of modified ctx ptr R3 off=48"): clang lowered a
  16-byte memcpy from the tracepoint context to loads off an
  advanced ctx pointer. The addresses are now read with
  `bpf_probe_read_kernel`. It was the only program el9 refused.
- **hf4** — el9's `trace_entry` is 12 bytes (the rt-merge
  `preempt_lazy_count`), so `raw_syscalls` carries `id` at 16
  and `args`/`ret` at 24 while the per-syscall records keep
  `args` at 16. The dispatchers read `id` at a fixed 8 (nearly
  every syscall went to the read handler) and the handlers read
  `args`/`ret` through the per-syscall struct. Both now read the
  raw record through CO-RE flavors (dispatch.c, generator's
  `handlerCtxStruct`).

Beyond M4's run, on el9: the Java agent live-loaded into a traced
JVM and the uprobe_multi Java marks attached ("37 natives
probed", brackets and attributed calls), and `tcp_seq` is read
on both ends of a connection.

| Id | Run 1 (before gf4/hf4 were in) | Run 2 (after) |
|---|---|---|
| M4-0 | PASS | PASS |
| M4-1 | PASS | PASS |
| M4-2 | PASS | PASS (3 rounds, 20 files) |
| M4-3 | PASS | PASS |
| M4-4 | PASS | PASS |
| M4-5 | FAIL (no begin marker: syscalls lost, hf4) | PASS (1428 events) |
| M4-6 | FAIL (0 fused, hf4) | PASS |
| M4-7 | FAIL (hf4) | PASS (client :18880, server :18203) |
| M4-8 | FAIL (hf4) | FAIL: no TLS label — task kf4 |
| M4-9 | FAIL (hf4) | FAIL: 1 of 2 hops unresolved — task if4 |
| M4-10 | FAIL (hf4) | FAIL: one-sided bound — task jf4 |
| M4-11 | FAIL (no earlier build) | PASS (`unshare -rn`) |
| M4-12 | FAIL (hf4) | PASS |
| M4-13 | FAIL | FAIL: summary lacks the reason — task lf4 |
| M4-14 | PASS | PASS (15 sessions collected) |

Run 1 had the tracer fix gf4 but not hf4, so the traced
processes recorded little more than exec and exit. The four
run-2 failures are not the kernel's:

- **M4-8 (kf4)**: the TLS label comes only from the Java
  agent's http records, and the runner deploys the services
  without `-spool`/`-agent` (deploy has no way to ship the agent
  jar). The rig's `TestMicroservices` loads the agent. Fixed in
  kf4: deploy ships the signed jar to `/opt/iormesh/lib`
  (decisions.md #28, provisional), the microservices projects carry
  `-spool`/`-agent`, and their JVMs start with the installed jar
  as `-javaagent` (see "Run with the agent (kf4)" below).
- **M4-9 (if4)**: a skipped `inet_sock_set_state` run (a
  `skipped_runs` drop, many tracers on a 2-vCPU host) missed
  inventory's final CLOSE, its `sock_owner_map` entry stayed,
  and the reused `struct sock` of another process's socket was
  recorded under inventory's fd and conn id — whose half then
  had a connect and an accept and was dropped as a loopback
  pair. Fixed in if4: every state record now checks the entry
  against the socket's current life (a new-life transition, or
  the socket file's inode) and drops a stale one
  (`internal/sockreuse_root_test.go` reproduces the reuse, on
  7.2 and on el9). A third run with if4 fused orders→inventory
  (and M4-13 passed with lf4), but M4-9 still failed: a skipped
  run lost the gateway's `SYN_SENT → ESTABLISHED`, and the merge
  could not pair gateway→orders from its `CLOSE` alone — task
  qf4.
- **M4-10 (jf4)**: `tcp_seq` pairs exist in both directions,
  but any drop on a host — every pattern session has an
  `attach_gap` at the JVM's exec — disables the data messages
  of all its connections, leaving only the handshake.
- **M4-13 (lf4)**: the watchdog worked (`end_reason`
  `disk_limit`, `show health` prints it), but the plain
  `iormeshctl show` summary never named a recorded end reason:
  it listed only sessions with no end. Fixed in lf4 — the
  summary now names every session that ended abnormally
  (`ended disk_limit`, `ended error`), and the runner checks
  for `: ended disk_limit`.

### Run with the agent (kf4, 2026-11-21)

A fourth run, the build of 1080cfd (if4 and lf4 in, jf4 not
yet), both VMs re-bootstrapped with it, work directory
`~/.iormesh-m4-kf4-1`: deploy verified and installed
`dist/iormesh-agent.jar` on both hosts (the installed
`/opt/iormesh/lib/iormesh-agent.jar` byte-identical to the
signed one, root's 0644 in a root 0755 `lib/`), the
microservices' JVMs joined ("java agent of pid N joined, 37
natives probed"), and **14 of 15 criteria passed**:

- M4-8 PASS — `127.0.0.1:18213 tls x1, 127.0.0.1:18211 tls x1,
  127.0.0.1:18212 plain x2`, all fused;
- M4-9 PASS — the two cross-host hops fused across iom1 and
  iom2; `10.20.0.141:18216 tls x1, 10.20.0.141:18214 tls x1,
  10.20.0.140:18215 plain x2` (the qf4 loss did not recur in
  this run; qf4 stays open);
- M4-10 FAIL — `iom1~iom2: 1 windows (1 one-sided, 0 empty)`,
  jf4's defect, whose fix landed after this build;
- every other criterion PASS, M4-14 with 15 sessions collected
  and nothing left under `/opt/iormesh`.

The ports and the DNAT were removed after the run and both VMs
bootstrapped again with the same build.

A fifth run after kf4's review round (the build of 22c3ad9, jf4
in; `~/.iormesh-m4-kf4-2`) checked the hardened start on el9: the
units ran with `IORMESH_CONFINED_UNIT=1` and `-agent` forwarded as
`/opt/iormesh/lib/iormesh-agent.jar`, every confined unit run
started, and the agents joined. 14 of 15 passed — M4-8 (TLS
labels as above) and now M4-10 (`width 646µs`, two-sided) among
them. M4-9 failed: gateway→orders (TLS) and orders→billing (TLS)
fused, but orders→inventory and gateway→inventory stayed
unresolved on both ends (4 `skipped_runs` in the spread's
sessions) — the lost-transition class of if4/qf4, not the agent.
Ports, DNAT and bootstrap as after the fourth run.

Task qf4 fixed both spread losses on the controller and replayed
every stored run through the merge (no VM needed; the el9 rerun
waits for the VMs):

- **Third run** (`~/.iormesh-m4-if4-20261121123004`): the gateway
  half `3c19fcadb1b49857` had only `SYN_SENT` and `CLOSE`. The
  merge now keys a client from a `CLOSE` whose `bytes_acked`
  counts its SYN (implementation.md M4, "The merge"); replayed,
  that el9 `CLOSE` rebuilds orders' key, and the spread fuses
  4 of 4 with none unresolved (before: 3 fused, 2 unresolved).
  `TestSockSeqAbortClose` checks the `CLOSE` rebuild on 7.2 after
  RST aborts (client, client with data queued, server);
  `TestSockSeqFields` covers the FIN close.
- **Fifth run** (`~/.iormesh-m4-kf4-2`): all three unresolved
  halves had their keyed records. Inventory's server conn
  `b727ffb216e83ea3` held two connections: orders→inventory,
  whose final `CLOSE` a skipped run lost, then gateway→inventory,
  accepted at the same `struct sock` address, which the
  correlator kept under the old socket — a `key_conflict`, and
  both clients `no_match`. The correlator now starts a new socket
  when a record's tuple contradicts the socket's, both complete
  (`sockTable.observe`, `TestLostCloseThenAddressReuse`;
  `TestSockDualStackBoundConnect` keeps a dual-stack bound
  connect on one conn). The
  stored trace is already correlated, so only a rerun shows it.

The other stored runs replay unchanged.

A sixth run (qf4, the build of 35c7de7: both fixes plus the
review's — an unspecified address never contradicts a tuple, a
transition out of `CLOSE` or `LISTEN` starts a socket, a stale
record names no process; both VMs bootstrapped again with it,
work directory `~/.iormesh-m4-qf4-1`) **passed all 15
criteria**: M4-8 `127.0.0.1:18213 tls x1, 127.0.0.1:18211 tls
x1, 127.0.0.1:18212 plain x2`; M4-9 two conns fused across iom1
and iom2, `10.20.0.141:18216 tls x1, 10.20.0.141:18214 tls x1,
10.20.0.140:18215 plain x2`; M4-10 one window, two-sided, width
667µs; M4-2 3 rounds and 40 files; M4-14 15 sessions collected
and nothing under `/opt/iormesh`. Before it, on `iom1`,
`TestSockSeqAbortClose` (the three RST aborts), the new
`TestSockDualStackBoundConnect`, `TestSockSeqFields` and
`TestSockRefusedConnectNoSeq` passed as root on el9. Neither
kernel reported the unspecified address at `SYN_SENT` for that
loopback connect; the unit test covers the sequence. Ports and
DNAT were removed after the run, and both VMs bootstrapped again
with the same build.

Also found on el9, outside M4's sequence: a session dies with
"schema: invalid: fd_gen 1 without an fd" when the pattern also
matches a `su` that runs the JVM (task mf4).

A seventh run (task 3f4, the build of 5eedf77: pf4's BPF object
changes and uf4's stamped, signed artifacts; both VMs
bootstrapped again with it as root, work directory
`~/.iormesh-m4-3f4-1`) **passed all 15 criteria** on el9: M4-2
3 rounds and 44 files; M4-5 the golden on 1358 events; M4-8
`127.0.0.1:18213 tls x1, 127.0.0.1:18211 tls x1,
127.0.0.1:18212 plain x2`; M4-9 two conns fused across iom1
and iom2, `10.20.0.141:18216 tls x1, 10.20.0.141:18214 tls x1,
10.20.0.140:18215 plain x2`; M4-10 one window, two-sided, width
749µs; M4-14 15 sessions collected and nothing under
`/opt/iormesh`. Ports and DNAT as before, removed after the M6
rounds below.

Both VMs were bootstrapped again after the kf4 run with the build
of 1080cfd (the run's `rm` removes `/opt/iormesh`; the agent jar
needs no bootstrap, deploy installs it), so a rerun with that
build needs only the firewall ports, the DNAT and an empty work
directory; a rerun with a newer build of the same version needs
the tracer bootstrapped again, since `upgrade` through the rule
refuses other bytes under one version (run 2's work directory is
`~/.iormesh-m4`, run 1's `~/.iormesh-m4-run1-20261121`, kf4's
`~/.iormesh-m4-kf4-1` on the controller).

### Root suites on Rocky 9.9 (task nf4, 2026-11-21)

`mage bpfRootTest`'s test binary and the integration test binary
ran as root on `iom1` (the host above). Both were built on the
7.2 laptop with `mage`'s cgo flags plus `-tags netgo,osusergo
-ldflags '-w -extldflags "-static"'` (as `ReleaseTracer` links
the tracer: Fedora's glibc is newer than el9's 2.34), and the
integration inputs (static tracer, `iormeshctl`, the four jars,
`workload/{scenarios,src,libs,otel}`, `edgepeer`, `echopeer`)
were staged under `/opt/nf4`, run serially, and removed again.

**bpfRootTest: 27 of 27 pass** (first run: 17 of 24;
`TestProcessExitGroupDead` is new, the two `sockreuse` tests came
with if4). They cover the paths no earlier el9 run had taken:

- **Sock tests**: all seven `TestSock*` failed to attach
  — `failed to create BPF link for perf_event: -13 (Permission
  denied)` on `syscalls:sys_enter_connect` and friends. The tests
  attached the generated handlers to their own per-syscall
  tracepoints, but since hf4 the handlers read the raw_syscalls
  record, which on el9 is longer (`args` at 24, while
  `sys_enter_connect` ends at 40), and the kernel refuses a
  program reading past its tracepoint's record. Only the tests
  did that; the loader tail-calls the handlers from the
  `raw_syscalls` dispatchers. The tests now fill the dispatch
  slots and attach the dispatchers as the loader does (the same
  fix landed with if4 while this ran).
- **Java mark (`TestJavaMarkNamesTheNative`)**: passes. It now
  also requires a mark from a thread whose tid is not the pid (a
  JVM's `main` never runs on the launcher thread): el9's
  uprobe_multi filters by process, so commit 46ba0e49 is
  backported.
- **PID namespace (`TestPidNSTracerConfined`)**: passes
  (`handle_task_newtask_ns` and the namespace dispatch).
- **Follow and fork, process exit**: `TestFollowForkExec` and the
  exec-watch tests pass. el9's `sched_process_exit` has no
  `group_dead` (comm at 12, pid at 28, prio at 32), so
  `ior_exit_group_dead` takes the `signal->live` fallback. The
  new `TestProcessExitGroupDead` runs it: a Go child ends one
  locked thread (record `group_dead` 0, the process still in
  `target_map`), then exits (a `group_dead` 1 record, the entry
  gone). In ten runs every exit had exactly one `group_dead` 1
  record, on the last thread. On 7.2 the same test runs the
  field path.
- **Target hint**: the harness assumes `pid_max` ≤
  `PID_MAX_LIMIT` (el9: 4194304, the limit), BPF_XADD (present)
  and that the fallback may report a dead group twice, which
  `iormesh_forget_target`'s single successful delete absorbs; no
  duplicate was seen on el9.

**Syscall formats**: el9's `events/syscalls/*/format` against
the 7.2 formats `mage generate` reads. The handlers read every
argument by index from the raw_syscalls record (CO-RE, hf4), so
per-syscall offsets do not matter; the argument order does, and
it matches for every syscall both kernels have. Differences: 7.2
appends `__data_loc` copies of string arguments (never read);
`bpf` gained a fourth argument `size_common` (the handler reads
only `cmd`, args[0]); `getcpu`, `name_to_handle_at` and `semop`
differ in a pointer or integer type name only. 16 syscalls (32
tracepoints) are generated but absent on el9 (`file_getattr`,
`file_setattr`, `getxattrat`, `setxattrat`, `listxattrat`,
`removexattrat`, `listmount`, `statmount`, `listns`, the three
`lsm_*`, `mseal`, `open_tree_attr`, `uprobe`, `uretprobe`); the
loader skips their slots (`tracepointExists`) and reports "0
tracepoints missing" for the default families.

**Integration suite: 51 of 51 passed**, 44 in the first full run and
the other seven after the staging and golden fixes below. That
serial run (about 10 minutes; 2 GiB was enough) failed seven:

- `TestHTTPServlet`, `TestHTTPApache`, `TestHTTPOkHttp`,
  `TestHTTPNetty`, `TestOtelHTTP`: staging — the third-party jars
  under `workload/libs` and `workload/otel` were not copied.
- `TestSystemdUnit`: staging — under SELinux (enforcing on el9)
  systemd may not exec a binary in `/root` (`admin_home_t`,
  status 203/EXEC), and `iormesh start` said only "exit status
  1". Passes from `/opt`. The bare error is task rf4.

Task rf4 made a failed start name its cause. systemd-run
(`--quiet --service-type=exec`) exits 1 with nothing on stderr,
and `--collect` has already dropped the unit, so start now
appends the unit's journal since the launch (`journalctl
--since=@<launch> --unit=…`, falling back to `systemctl status
--lines=0`), at most 20 lines of 300 bytes and 4 KiB, oldest
lines dropped first, each line through `termsafe`; systemd-run's
own stderr is bounded the same way. Task 8g4 tightened it: the
300 bytes are counted after the quoting (a line of control bytes
quotes to four bytes each) and the cut keeps whole runes; each
diagnostic command has a 5 s timeout (`TestUnitLogTimeouts`);
and the status fallback shows the unit's state without journal
lines, which for a unit that survived `--collect` are most
likely a racing start's (the fake systemctl adds such a line
unless given `--lines=0`). Mutation-checked: `--lines=20`, no
timeout, cutting before quoting and cutting inside a rune each
fail a test. When SELinux enforces and the binary's
type is not `bin_t` or `usr_t`, or the log shows a permission or
AVC denial, a hint names the label and points to `/usr/local/bin`
or `/opt`. The hint does not wait for an AVC record: el9 does not
audit this denial (`ausearch -m avc` shows none). The unit tests
(`internal/cli/startdiag_test.go`) put shell stand-ins for
systemd-run, systemctl and journalctl first on `PATH`, so the
real exec path (argv, stderr capture) runs: the el9 journal and
label, the status fallback, the hint's cases, and a 200-line log
with escape sequences in it and in systemd-run's stderr. On iom1
(enforcing), a start from `/root/rf4` now reports:

```
iormesh: systemd-run --unit=iormesh-rf4probe.service … -- /root/rf4/iormesh run …: exit status 1: (no stderr)
iormesh-rf4probe.service logged:
  … iormesh-rf4probe.service: Failed to locate executable /root/rf4/iormesh: Permission denied
  … iormesh-rf4probe.service: Main process exited, code=exited, status=203/EXEC
  …
SELinux is enforcing and /root/rf4/iormesh is labelled admin_home_t: systemd may not exec a binary outside a bin_t or usr_t path (e.g. under /root, admin_home_t); install it under /usr/local/bin or /opt (restorecon relabels it)
```
- `TestPipeIPC` (fixed): el9's glibc 2.34 implements `pipe()` as
  `pipe(2)` on x86_64, Fedora's 2.43 as `pipe2(fds, 0)`; the
  tracer recorded `pipe` with both ends, the golden declared
  `pipe2`. The test now declares the kind the trace's libc issued
  (`pipeKindOfThisLibc`); both ends and every edge are still
  checked.

A second full run, on the tree rebased onto if4, kf4 and jf4,
passed 50 of 51: `TestJavaLiveAttach` failed its golden, a call
after the live attach left unattributed (`ambiguous 14`). Alone
it failed 1 of 8 serial runs on the idle host. 7.2 showed the
same symptom once under load; task tf4. The root suite on that
tree: 27 of 27.

Task tf4 found the cause in the tracer, not the test. The test
already waited for the tracer's `java agent of pid N joined`
line, but that line could lie. The joiner probed a JVM's natives
at the first drain that saw any HOOK record. The agent writes
its hook table into the ring one row at a time, and a drain that
landed mid-table probed only the first rows' natives. Every
failing run printed `joined, 8|15|16|21 natives probed`, every
passing run 37, and the later hooks' I/O (`unlink0`, `open0`)
stayed `none`. The joiner now probes only after a record that
follows the table (the agent's warm-up THREAD and WATERMARK)
shows the table read whole
(`TestJavaJoinWaitsForWholeTable`). `TestJavaLiveAttach`
serially, before → after:

| Host | Before | After |
|---|---|---|
| 7.2 laptop, load ≈ 12 (other workers' suites) | 3 of 110 failed | 0 of 100 failed |
| Rocky 9.9 `iom1`, idle | 1 of 40 failed | 0 of 40 failed |

Outside the suites: a hand-run pattern session over pipe-ipc
(`-pattern '*workload.jar*pipe-ipc*' -trace-families ipc`) died
with "fd_gen 1 without an fd". That is task mf4 again, this time
without `su`; the `-pid` run of the same scenario passes.

**mf4, fixed**: the rejected event was the spawned child's
`newfstat` on fd -1. el9's NSS lookups `fstat(-1)` (EBADF) in the
JVM and again in the child it spawns through jspawnhelper (su and
bash do NSS lookups too). The correlator registered fd -1 as an
fd instance. The exec closed it but kept its generation, so the
child's call got gen 1. A negative fd now names no instance: the
event keeps fd -1 and gen 0. The tracer is wrong on any kernel
for this sequence. Only el9's libc makes it, so 7.2 never
showed it. `TestNegativeFdAcrossExec` (root) replays it with a Go
child on 7.2 and fails without the fix; correlate's
`TestNegativeFdNamesNoInstance` holds the record shape. On iom1
the pipe-ipc pattern session (three runs) and lb4's
`su paul -c java … tcp-loopback` session (`-out` and
`-trace-dir`) now end `stop`. The old binary still dies on the
su run. A writer's error now names the event it rejects.

### M5 on Rocky 9.9 (task 3f4, 2026-11-21)

milestones.md M5's re-check on the real hosts. The input is the store
of the seventh M4 run above — the spread's four projects
(m4sgw, m4sinv, m4sord, m4sbill), traced with the agent,
deployed and collected over SSH — copied under a scratch `HOME`
(`~/.iom5`), the three servers named by address and port
(`services name -port 18216 10.20.0.141 orders`, 18214 billing,
18215 on 10.20.0.140 inventory) and built with `iormeshctl`.
**Not accepted: the sequence criterion fails (task vg4).**

| Surface | Result |
|---|---|
| model | PASS — orders, inventory and billing named, orders and billing `tls`, inventory `plain` (2 conns), 4 edges fused, nothing unresolved |
| sequence | FAIL — the checkout (0002, `POST /orders`, 3 hops) has 1696 steps; orders' serving thread (iom2 tid 9041) loads classes from `/usr/lib/jvm/java-25-openjdk/lib/modules` on its first request (744 `pread64`) and resolves names (`/etc/host.conf`, `resolv.conf`, `hosts`, `libnss_myhostname`), and the 200-step cap (`viewmodel.MaxSteps`) cuts the conversation before the inventory hop: its services and lifelines name only the gateway and orders, and no create-tmp, write or rename is shown. The kept steps are all `causal`, the first hop `paired` with its stack (`HttpClientImpl#send` … `ServiceNode#checkout`) |
| `show` | PASS for the walk to a stack: `show sequence 0002 -json` has step 1's whole stack, `query -ref` its raw `http` event, `show thread orders/HTTP-Dispatcher` the thread's story (cut like the sequence). `show processes` lists orders' server socket with the gateway as peer, but cuts its files at 32 (90 more, the JVM's library probes first, as the pattern session traces from exec), so its config and record are not listed (vg4) |
| TUI | PASS — in tmux, `/orders`, ⏎ ⏎ ⏎ reaches the checkout with step 1's detail (its Java stack and `iormeshctl query -ref`), ⏎ its thread's story |
| site | PASS — the embedded model equals `model.json`, CSP `default-src 'none'`, no remote reference; headless Chrome 144 prints `selftest: ok 228 routes` from `file://` with an empty console |
| SVG | rebuild PASS (same bytes in the same site folder); `graph.svg` names the hosts and the services. The goldens are the rig's three-host layout: the checkout's export holds the cut sequence above, not the golden's story (vg4) |
| eval | pipeline PASS — `iormesheval` with the scripted stand-in scores 10/10 on this store, the wrong one 0/10 and is refused; the criterion still awaits the owner's model command |

The rig's run did not meet the cap because it attaches by PID
after the JVMs started (task vg4): the open of `lib/modules` (and
of the jars) at JVM start is not in its trace, so the class
loading's later `pread64` calls are on an fd whose open the trace
never showed, which the sequence view cannot name and leaves out.
The real hosts' pattern sessions trace from exec, see that open
and name every read. The agent's bootstrap append (class sharing
for boot classes only, as the JVM warns) adds to the reads but
is the same on both.

**Rebuilt with folded runs (task vg4).** The same store, built by
an iormeshctl with milestones.md M5's folded runs (`iormeshctl build
-store … -db … -builds …` on a copy, nothing sent anywhere):

| Surface | Result |
|---|---|
| sequence | PASS — the checkout is 100 steps, none cut, holding all 1696 calls (`read …/lib/modules (420 calls)`, `(134 calls)`, …); all `causal`, three hops `paired`: `POST /orders` gateway main → orders HTTP-Dispatcher, `POST /inventory/reserve` → inventory HTTP-Dispatcher with `create`/`write`/`rename` of `reservation-42.json(.tmp)`, `200`, `POST /billing/charge` → billing with `invoice-42.json`, `200`, orders' own `order-42.json`, `201`; services gateway, orders, inventory, billing |
| `show processes` | PASS — orders keeps 32 files (90 more): its `orders.conf` (39 B read and written), `orders.crt`, `order-42.json.tmp` (36 B, rename) and `order-42.json`, the library probes cut |

The rig's acceptance (`TestViewsAcceptance`) checks only the
story's hops and the scenario's own files, which fold never
joins, so its story and SVG goldens stand;
`internal/viewmodel` `TestNoisyCheckoutFolds` holds the real
noise pattern, trimmed, as a unit test.

### M6 on Rocky 9.9 (task 3f4, 2026-11-21)

milestones.md M6's re-check on the real hosts, by hand with the
`iormeshctl` of the build above, `HOME` a scratch directory
(`~/.iormesh-m6-3f4`). Two hosts cannot give every service a
host of its own, so the layout differs from M4-9's: iom1 runs
the gateway, inventory (`--bind 10.20.0.140 --port 18215`) and
billing (`--bind 10.20.0.140 --port 18214`), each with the agent;
iom2 runs orders (`--bind 10.20.0.141 --port 18216`, calling
both on iom1), without it. Only iom1 is registered, as project
`edge` with one pattern for every role
(`java -jar */.iormesh-m6-run/workload.jar microservices --role *
--dir */.iormesh-m6-run/m6`, `-duration 1h -spool
/run/iormesh/spool/edge -agent /opt/iormesh/lib/iormesh-agent.jar`),
and the registry's `expand:` allows 10.20.0.140/32 and
10.20.0.141/32. Firewall: 18214 and 18215 on iom1, 18216 on iom2,
runtime only.

Each round: `deploy -restart iom1`; start billing and inventory,
copy billing's certificate to iom2 (`base64`), start orders; once
it listens, deploy any enrolled host (bound to orders' port,
resolved at start); copy orders' certificate to iom1, run the
gateway; the servers retire after their calls; after 70 s,
`collect`. Round 2 deploys through `expand -apply` at that
point.

| Criterion | Result |
|---|---|
| only the gateway host enrolled, rounds discover and deploy every service host | PASS — round 1's `expand` proposes exactly `10.20.0.141:18216 hop 1` (the two inbound skips aside); round 2's `expand -apply` enrolls `10.20.0.141`, hop 1, project `edge`, `listen_port: 18216`, verifies the signed build and starts the session on iom2 over SSH, bound to orders' pid |
| after the scenario re-runs, the merge equals the one of all hosts added by hand | PASS — round 3's own sessions merge to 4 edges fused, tcp 3 fused and 0 unresolved, orders and billing `tls`, inventory `plain` x2; a by-hand run (`~/.iom6h`: iom1 as above plus `add -project edge -listen-port 18216 10.20.0.141`) builds the same, and both graphs' connection shapes (each socket's ends by host, process and state) are identical. Each side has two one-ended UDP sockets, DNS to the gateway's resolver |
| convergence honesty | PASS — the store's merge after round 2 keeps round 1's gateway → orders end unresolved, and round 1's server halves of orders' untraced calls |
| a peer outside the allowlist is listed, never contacted | PASS — with 10.20.0.140/32 alone, the peer is `not_allowed`, `expand -apply` prints "nothing to enroll", the registry's hash is unchanged, and iom2's sshd logged no login meanwhile |
| a cyclic topology terminates | PASS — rounds 2 and 3 (`-hops 1`, `-hops 5`) propose nothing: orders' calls back to iom1 reach a registered host |
| a hop limit of 1 stops at the first ring | NOT RUN — needs a hop-2 peer, a third host reachable only through a service on iom2; every peer of iom2 is iom1 (run by task wg4 below) |

Afterwards both hosts were removed with `rm` (every session
collected), bootstrapped again with the same build, and the
ports closed.

### M6 on three Rocky 9.9 hosts (task wg4, 2026-11-21)

The hop limit, and every criterion again, with a third VM, iom3
(10.20.0.142, same kernel), as the hop-2 peer: the build of
5eedf77 (3f4's, signed; HEAD's code when the run started), its
`iormeshctl`, `HOME` a scratch directory (`~/.iormesh-m6-wg4`),
the registry and the round procedure of 3f4's above, with
billing moved to iom3:

- iom1: the gateway and inventory (`--bind 10.20.0.140 --port
  18215`), with the agent; the only registered host.
- iom2: orders (`--bind 10.20.0.141 --port 18216`, calling
  inventory on iom1 and billing on iom3), without it.
- iom3: billing (`--bind 10.20.0.142 --port 18214`), without it;
  its certificate copied to iom2.

The gateway never calls billing, so billing is two hops from
iom1 and shows only once iom2 is traced. iom3's firewall admits
18214 from iom2 alone (`firewall-cmd --add-rich-rule='rule
family=ipv4 source address=10.20.0.141/32 port port=18214
protocol=tcp accept'`, runtime only): from iom1 a connect gets
"No route to host", from iom2 it opens. 18215 on iom1 and 18216
on iom2 are open as before. The allowlist holds all three /32s.
Round 3 deploys iom2 again (`deploy -restart 10.20.0.141`, bound
to the new orders) and then runs `expand -hops 2 -apply` once
orders listens.

| Criterion | Result |
|---|---|
| only the gateway host enrolled, rounds discover and deploy every service host | PASS — round 1 proposes exactly `10.20.0.141:18216 hop 1` (one inbound skip aside); round 2's `-apply` enrolls and deploys iom2 at hop 1, bound to orders' pid; round 3's `expand -hops 2 -apply` proposes exactly `10.20.0.142:18214 hop 2` via iom2's `java`, enrolls `10.20.0.142` (`hop: 2`, `listen_port: 18214`) and starts the session on iom3 over SSH, bound to billing's pid |
| a hop limit of 1 stops at the first ring | PASS — after round 2, `expand -hops 1` lists `10.20.0.142:18214 hop_limit` ("hop 2, limit 1", via 10.20.0.141 java); `-apply` prints "nothing to enroll" and the registry's hash is unchanged |
| a peer outside the allowlist is listed, never contacted | PASS — with 10.20.0.142/32 dropped from the allowlist, `expand -hops 2` lists billing `not_allowed`, `-apply` enrolls nothing, the hash is unchanged. iom3's sshd logged no login from the end of round 2's own steps (16:38:29) to the check (16:40:01), across `collect`, both `-hops 1` and both narrowed `-hops 2` runs |
| after the scenario re-runs, the merge equals the one of all hosts added by hand | PASS — round 3's own sessions merge to 4 edges fused, tcp 3 fused, 0 unresolved, the three cross-host hops (iom1 → iom2, iom2 → iom1, iom2 → iom3) among them; a by-hand run (`~/.iormesh-m6-wg4h`: iom1 plus `add -project edge -listen-port 18216 10.20.0.141` and `-listen-port 18214 10.20.0.142`) builds the same, and both graphs' connection shapes are identical. Each side has one one-ended UDP socket on iom2 and on iom3, DNS to 10.20.0.1:53. Orders is `tls`, inventory `plain`; billing's TLS shows `-` in both, as neither end of that hop runs the agent |
| convergence honesty | PASS — the store's merge over all three rounds keeps round 1's gateway → orders end and round 2's orders → billing end unresolved (`mixed`, 1 of 3 and 1 of 2 connections), and round 1's server half of orders' untraced call to inventory |
| a cyclic topology terminates | PASS — orders → inventory goes back to iom1, a registered host; after round 3, `expand -hops 2`, `-hops 5` and `-hops 5 -apply` propose nothing (both peers `enrolled`) |

M6 is accepted on this run (milestones.md M6). All three VMs share one
physical host. Afterwards every host was removed with `rm` from
both scratch homes (every session collected), bootstrapped again
with the same build, and the ports and the rich rule removed.

### M4 and M6 rerun at 9bca1b2 (task 3k4, 2026-11-26)

Both acceptances again on iom1, iom2 and iom3 (each Rocky 9.9,
5.14.0-741.22.1.el9_9.x86_64, JDK 25.0.5.1), with builds of
9bca1b2 (main's head): `mage containerBuild podman
tracer-amd64,ctl` and `mage agentBuild` into `dist/`, signed with
the test key (`mage sign`), `bin/iormeshctl` and `bin/workload.jar`
from the same tree. Since the last runs the controller gained
bootstrap, ensure, status/list, prune and windowed builds, and
deploy, collect, rm and federate changed in the audit fixes.

At the start every host had the 5eedf77 tracer installed (with its
key), a hand-laid sudoers rule without bootstrap's marker (so `rm`
leaves it) and `~/.iormesh/staging`; no unit, no trace, no
firewall port. Each install of the new build ran as root over SSH
(the printed bootstrap steps without the sudoers rule, which was
there), then `sudo -n iormesh upgrade` of the staged build through
the rule. The VMs were started one at a time; f3 never used swap.

**M4** (`mage m4Acceptance iom1 iom2`, the variables of "Run on
Rocky 9.9" above, work directory `~/.iormesh-m4-3k4-1`; 07:21:17
to 07:26:36 UTC, 5 min 19 s): **14 of 15 PASS, not accepted** —
M4-8's check fails, though the model meets the criterion (task
7m4).

| Id | Result |
|---|---|
| M4-0 | PASS — both amd64, 5.14.0-741.22.1.el9_9.x86_64 |
| M4-1 | PASS — 14 projects running on iom1 and iom2 |
| M4-2 | PASS — 3 rounds, 48 files collected and deleted on the servers |
| M4-3 | PASS |
| M4-4 | PASS |
| M4-5 | PASS — the tcp-loopback golden on 1475 events |
| M4-6 | PASS — one conn fused across iom1 and iom2; the client alone unresolved |
| M4-7 | PASS — client :18880, server :18203 |
| M4-8 | FAIL — "orders (:18213) not among the services" (task 7m4) |
| M4-9 | PASS — two conns fused across iom1 and iom2; `10.20.0.141:18216 tls x1, 10.20.0.141:18214 tls x1, 10.20.0.140:18215 plain x2` |
| M4-10 | PASS — one window, two-sided, width 947µs |
| M4-11 | PASS — byte-identical under `unshare -rn` |
| M4-12 | PASS |
| M4-13 | PASS — `ended disk_limit`, the segment intact |
| M4-14 | PASS — 15 sessions collected, nothing under `/opt/iormesh` |

M4-8 is a regression of the runner, not of the tracer or the
merge: since task ai4 (b496ff9) a loopback endpoint's label ends in
`@host` (`127.0.0.1:18213@a425…`), and the runner's
`serviceByPort` still matches the label's `:PORT` suffix, so it
finds none of the loopback services. The refresh's model holds the
criterion: `m4-orders-svc` (127.0.0.1:18213) `tls` x1,
127.0.0.1:18211 `tls` x1, 127.0.0.1:18212 `plain` x2, every edge
to them fused, 0 unresolved. Task 7m4 fixes the check.

**M6** on three hosts, wg4's procedure and layout above
(gateway and inventory with the agent on iom1, orders on iom2,
billing on iom3, iom3's 18214 open to iom2 alone), scratch home
`~/.iormesh-m6-3k4`; rounds 07:28:33 to 07:35:15 UTC, the
by-hand run (`~/.iormesh-m6-3k4h`) 07:35:42 to 07:37:29. **Every
criterion PASS.**

| Criterion | Result |
|---|---|
| only the gateway host enrolled, rounds discover and deploy every service host | PASS — round 1 proposes exactly `10.20.0.141:18216 hop 1` (one inbound skip aside); round 2's `expand -apply` enrolls and deploys iom2 at hop 1, bound to orders' pid; round 3's `expand -hops 2 -apply` enrolls `10.20.0.142` (`hop 2`, `listen-port 18214`) and starts the session on iom3 over SSH, bound to billing's pid |
| a hop limit of 1 stops at the first ring | PASS — after round 2, `expand -hops 1` lists `10.20.0.142:18214 hop_limit` ("hop 2, limit 1", via 10.20.0.141 java); `-apply` prints "nothing to enroll", the registry's hash unchanged |
| a peer outside the allowlist is listed, never contacted | PASS — with 10.20.0.142/32 dropped, `expand -hops 2` lists billing `not_allowed`, `-apply` enrolls nothing, the hash unchanged; iom3's sshd logged no login from round 2's last step (07:30:44) to the check (07:32:44), across `collect`, `refresh`, both `-hops 1` and both narrowed `-hops 2` runs |
| after the scenario re-runs, the merge equals the one of all hosts added by hand | PASS — round 3's own sessions merge to 4 edges fused, tcp 3 fused, 0 unresolved, orders `tls`, inventory `plain` x2, billing `-` (no agent on that hop); the by-hand run builds the same, and both graphs' connection shapes are identical (8 sockets; the one-ended ones a DNS socket to 10.20.0.1:53 on iom2 and on iom3 and an address-less accept in each agent JVM on iom1, in both) |
| convergence honesty | PASS — the store's merge over the three rounds keeps round 1's gateway → orders end and round 2's orders → billing end unresolved (`mixed`, 1 of 3 and 1 of 2), and round 1's server half of orders' untraced call to inventory (`(untraced)` → 18215) |
| a cyclic topology terminates | PASS — after round 3, `expand -hops 2`, `-hops 5` and `-hops 5 -apply` propose nothing (both peers `enrolled`), the hash unchanged |

The two-host M6 layout (3f4) was not run again: the three-host
run covers each of its criteria.

Afterwards every host was removed with `rm` from both scratch
homes (every session collected), the build of 9bca1b2 installed
again as at the start (the 5eedf77 tracer is not restored), the
run directories, ports, rich rule and DNAT removed, and the three
VMs stopped.

### Guide walk on Rocky 9.9 (task 4k4, 2026-11-26)

The user guide ([guide/](guide/README.md), chapters 2–10) walked
end to end on `iom1` and `iom2` (as above; 08:19 to 08:53 UTC),
with the commands as the chapters write them, then M4 once more.
Builds of c10b0a3: `mage containerBuild podman tracer-amd64,ctl`
and `mage agentBuild` into `dist/`, signed with a throwaway key
made by `minisign -G` (`aead.dev/minisign/cmd/minisign@v0.3.0`;
the laptop has no minisign package) through
`dist/iormeshctl-linux-amd64 sign -key … .`; the controller
installed from `dist/` as `~/bin/iormeshctl`. iormeshctl ran with
`HOME` set to a scratch directory, so the key, registry, store
and service database were the walk's own; the operator's were
not touched.

**Start and end state.** Both hosts had 3k4's state: the 9bca1b2
tracer (sha256 6674249a…) and the 3k4 test key in
`/opt/iormesh`, the hand-laid rule `paul ALL=(root) NOPASSWD:
/opt/iormesh/bin/iormesh` without bootstrap's marker, the staged
9bca1b2 builds in `~paul/.iormesh/staging`; no unit, no port. To
start from the state chapter 4 assumes, `sudo -n iormesh
uninstall` (it left the unmarked rule, as documented), then the
rule and `~/.iormesh` removed as root. At the end the same bytes,
modes and rule were put back as root, the hashes compared, and
the VMs stopped.

**Workload.** A 1.9 KiB `shop.jar` (a JDK HttpClient calling
billing every 3 s over keep-alive and writing an order record
through a temporary file and a rename) on `iom1`, and billing as
`java -m jdk.httpserver -b 10.20.0.141 -p 8443` on `iom2`, 8443
opened runtime-only for the walk.

**Bootstrap.** The SSH user `paul` has no password and no sudo of
its own, so the by-hand path on `iom1` ran the printed controller
lines on the laptop, the `printf` lines as `paul`, and the `sudo`
lines as root over SSH with `HOME=/home/paul`; the staged
`upgrade` needs the caller to own the staged file, so that one
line ran with `SUDO_UID=1000` — in a root shell it refuses
(`unsafe file: … is owned by uid 1000, not by the caller`, now in
the guide and troubleshooting.md). The checkpoint, `sudo -n
/opt/iormesh/bin/iormesh status` as `paul`, printed `iormesh: no
sessions`. On `iom2`, `bootstrap -apply` without sudo refused as
documented; with the temporary rule `/etc/sudoers.d/zz-4k4-temp`
(`paul ALL=(ALL) NOPASSWD: ALL`, 0440, added at 08:30:15 UTC and
removed at 08:30:43 right after the bootstrap; `sudo -n true` as
`paul` then failed again) it ran every step and ended
`bootstrapped; iormeshctl deploy iom2 starts its projects`; a
rerun refused with `/etc/sudoers.d/iormesh already exists`.

**Walked**, each as the chapter shows it: `add` (and its two
refusals), `list`, `bootstrap`, `deploy` (a `-listen-port` with
nothing listening, both hosts, a no-op redeploy), `status`,
`collect`, `build`, `services`/`services name`/`rules`/`unname`,
every `show` view, `show -json`, `tui` without a terminal, the
site folder (CSP `default-src 'none'`, no remote reference; no
browser was opened), `query` (`-event`, `-host`, `-file`, `-from`/
`-to`, `-conn`, `-errno`), the agent added to `shop` (deploy
pushed the jar, the running JVM got it by live attach, "37
natives probed", conversations appeared), `ensure` twice
(`shortshop`, `-duration 1m`, ended `duration` and was restarted;
`billing`, stopped with `sudo iormesh stop`, was left stopped and
marked `stopped by operator: ensure leaves it`; the second run
only collected; `deploy` started billing again), `build -last 2m`
and `-from`/`-to`, `show -windowed`, the two `-last` refusals,
`prune -older-than` listing then `-apply`, flags changed in the
registry by hand and `deploy` (it restarted the session),
`expand` and `expand -apply` (iom2 bootstrapped again, untraced,
allowed as `10.20.0.141/32`: enrolled at hop 1 and deployed bound
to billing's pid; the next `expand` lists it `enrolled`), and
`rm` (`rm iom1`, `rm -project billing iom2`; both removed
`/opt/iormesh` and the bootstrap rule).

**Findings, fixed in the guide and troubleshooting.md:**

- A connection already open when its session starts makes no
  connection and no edge (only reads and writes on its socket):
  the first build after deploying over the running keep-alive
  client was `0 services, 0 edges` from 726 events; with the
  client restarted it fused at once. A product gap, task cm4
  (priority H); chapter 5 said so, chapter 10's run shows the
  resulting `mixed` edge. Task cm4 closed it (decision #35,
  "Connections open before the session" below).
- `rm -project` of a host's last project uninstalls the host, so
  chapter 8's "rm -project, then add again" to add `-agent` left
  `iom1` without a tracer (deploy: `/opt/iormesh/bin/iormesh is
  not installed`). Chapter 8 now edits the registry's flags and
  deploys again; chapter 9 and troubleshooting.md say what rm
  does to a host's last project.
- `services name 'iom2*'` named nothing: the glob matches the
  name the client asked for or the IP, never the registry's host
  name; a client dialing an IP needs an IP glob (chapter 6,
  troubleshooting.md).
- Without the agent there are no http events, so no
  conversations (`show sequence`: `no such conversation: the
  build holds none (no http events)`), chapters 6 and 7.
- `mkdir -p ~/.config/iormesh` (chapter 3) made the registry's
  directory 0755, not the 0700 chapter 4 promised; chapter 3 now
  uses `mkdir -p -m 0700`.
- A long `HOME` breaks every ssh with `control socket path under
  … would be 191 bytes, over the 104 a unix socket allows`
  (`XDG_CACHE_HOME` moves it), chapter 4 and troubleshooting.md.
- `sudo -v` (chapter 2) succeeds for a user whose only rule is
  iormesh's narrow one; the check is `sudo -l` now.
- Chapter 5's deploy and status output, chapter 6's collect,
  chapter 9's ensure, prune and rm, and chapter 10's `expand
  -apply` are now captured from this run. rm leaves the SSH
  user's `~/.iormesh/staging` and an empty `/run/iormesh/spool`
  (chapter 9).
- deploy prints a failed unit's journal tail Go-quoted on one
  line (as troubleshooting.md said); readability task dm4.

**M4** (`mage m4Acceptance iom1 iom2`, the procedure and variables
of "Run on Rocky 9.9" above, `IORMESH_M4_KEY` the walk's
throwaway key, work directory `~/.iormesh-m4-4k4`; both hosts
bootstrapped again by hand as above; 08:42:41 to 08:48:12 UTC):
**every criterion PASS — M4 is accepted on el9.**

| Id | Result |
|---|---|
| M4-0 | PASS — both amd64, 5.14.0-741.22.1.el9_9.x86_64 |
| M4-1 | PASS — 14 projects running on iom1 and iom2 |
| M4-2 | PASS — 3 rounds, 43 files collected and deleted on the servers |
| M4-3 | PASS |
| M4-4 | PASS |
| M4-5 | PASS — the tcp-loopback golden on 1485 events |
| M4-6 | PASS — one conn fused across iom1 and iom2; the client alone unresolved |
| M4-7 | PASS — client :18880, server :18203 |
| M4-8 | PASS — all hops fused: `127.0.0.1:18213@a425… tls x1, 127.0.0.1:18211@a425… tls x1, 127.0.0.1:18212@a425… plain x2` (task 7m4's fix) |
| M4-9 | PASS — two conns fused across iom1 and iom2; `10.20.0.141:18216 tls x1, 10.20.0.141:18214 tls x1, 10.20.0.140:18215 plain x2` |
| M4-10 | PASS — one window, two-sided, width 750µs |
| M4-11 | PASS — byte-identical under `unshare -rn` |
| M4-12 | PASS |
| M4-13 | PASS — `ended disk_limit`, the segment intact |
| M4-14 | PASS — 15 sessions collected, nothing under `/opt/iormesh` |

Afterwards the ports and the DNAT were removed, the scratch homes
and the run directory deleted on the laptop, the 3k4 state put
back on both hosts, and both VMs stopped (`doas vm list`: `iom1`,
`iom2`, `iom3` Stopped; only `rocky` running).

## msghdr data calls (task oh4, 2026-11-22)

`TestMsgCallsTCP` and `TestMsgCallsUDP`
(`internal/msgcalls_root_test.go`, in `mage bpfRootTest`) run
through the seq child of the tcp_seq tests. On TCP: a `sendmsg`
of two buffers and a `sendmmsg` of two messages, then a
`recvmsg` and a `recvmmsg` of two messages, each pinned to its
bytes and to `tcp_seq` from the handshake's `snd_una`/`rcv_nxt`;
the mmsg calls' `ret` stays their message count. Between two
UDP sockets (unconnected since task nk4): a `sendmmsg` of three
datagrams and the `recvmmsg` taking them carry the datagrams'
byte sum, the
single `sendmsg`/`recvmsg` their size, and no record carries
`tcp_seq`. Red first on the 7.2 laptop: before the change both
failed (`sends: calls moved [] bytes, want [7 11]`, `udp
sendmmsg moved 0 bytes, want 60`). The incomplete sums no
kernel produces — the 1024-message cap, a faulting read, a lost
park — are pinned by `TestMmsgHarness`, the real
`internal/c/mmsg.c` compiled against simulated maps, ring and
user memory with the cap lowered to 4, plus its mutations
(`TestMmsgHarnessCatchesMutations`). `TestMmsgVerifierHeadroom`
loads the four mmsg handlers on the running kernel and logs the
instructions its verifier processed (`verified_insns`): on the
7.2 laptop 821 for each enter and about 42k for each exit (the
loop), below its bound of 100k, a tenth of the 1M limit. That
is 7.2's verifier; on el9 (task rj4) the count is the same,
42,078 — see "el9 runbook and runs" below.

## MSG_PEEK receives (task ok4, 2026-11-24)

`TestPeekCountedOnce` (`internal/peek_root_test.go`, in `mage
bpfRootTest`) runs through the seq child too: the parent sends
one 12-byte chunk, the child peeks at it with `recvfrom`,
`recvmsg` and `recvmmsg` (each `MSG_PEEK`) and then reads it.
Each peek keeps its 12 bytes, is marked `args.peek` and carries
no `tcp_seq`; the read carries the chunk's range; the receives'
`StreamBytes` sum is 12. Red first on the 7.2 laptop: before the
change the peeks were unmarked and the receives counted 48
bytes. `recvmmsg`'s enter moved from `fd_event` to
`fd_size_event` to carry its flags; no CO-RE relocation was
added (`TestEl9RecordLayout` already relocates the handler).
Not yet run on el9.

## The msghdr calls' datagram peer (task nk4, 2026-11-24)

`TestMsgCallsUDP` now runs its UDP sockets unconnected: every
send names the receiving socket in `msg_name` (each `sendmmsg`
message too), every receive asks for the sender there, so the
peer is only in the msghdr. Each of the four events must name
the other socket as `remote_addr:remote_port`. Red first on the
7.2 laptop: before the change all four failed (`udp sendmmsg
names no peer, want 127.0.0.1:46226`, likewise sendmsg, recvmmsg
and recvmsg). `TestMsghdrAddrHooks` (internal/generate) pins
that the four calls park their msghdr and read it as one, and
that bind, connect, sendto and recvfrom do not. The mmsg exits'
verifier count on 7.2 grew from 41,943/41,964 (sendmmsg/recvmmsg)
to 42,554/42,579; `TestEl9RecordLayout` still relocates them
clean against the el9 BTF. Not yet run on el9.

## arm64 on an emulated el9 kernel (task 8k4, 2026-11-24)

**Emulated CPU.** The guest ran in QEMU 10.2.2 with TCG on the
x86 laptop (`-machine virt -cpu max`, 4 vCPUs, 4 GiB, UEFI from
edk2's aarch64 pflash, user-mode networking with SSH forwarded
on 127.0.0.1 only). The kernel is a real el9 aarch64 one —
Rocky 9.9 GenericCloud aarch64, image
`Rocky-9-GenericCloud-Base-9.9-20261112.0` checked against its
CHECKSUM, kernel `5.14.0-741.22.1.el9_9.aarch64` with BTF,
SELinux enforcing, cloud-init seeding a throwaway key. So this
proves load, verifier and JIT acceptance and event flow on an
arm64 el9 kernel; it says nothing about timing or overhead
(a passing root test took 4–106 s there).
The VM booted to SSH in 7.5 min and ran for 55 min in all
(09:12–10:08 UTC), then was powered off; image, overlay and keys stayed in the
session's scratch directory.

What ran:

- `dist/iormesh-linux-arm64` from `mage containerBuild podman
  tracer-arm64` (the release build, clang 19.1.7 from Debian
  trixie, compiled against the laptop's x86 `vmlinux.h`).
- `./internal`'s test binary, cross-built static in the same
  builder image (`go test -c -tags netgo,osusergo -ldflags
  '-extldflags -static'` with `IORMESH_TARGET_ARCH=arm64`'s
  environment), run as root from a copy of `internal/` with
  `testdata/btf` beside it, with `gatecmd.RootBPFTestArgs`.
- `iormesh run -project arm64 -pid <shell>` on a shell that
  runs two `dd`s (4 MiB to a file, then back) and a Python
  loopback TCP exchange (4096 bytes one way, 2 back).

Results:

- **The release object does not load.** The verifier refuses
  `handle_sys_exit_read` ("R8 unbounded memory access",
  `rwagg.c` `iormesh_rw_on_exit`): clang 19 checks
  `idx >= IORMESH_RW_SLOTS` on a zero-extended copy and indexes
  `st->slot[idx]` with the unchecked register, which the 5.14
  verifier cannot follow. It is not arm64's doing: clang 19's
  bytecode for that program is identical with
  `-D__TARGET_ARCH_x86`, and that x86 object is refused on the
  same kernel the same way. The amd64 release, built by the
  Rocky builder's clang, is unaffected. Every root test that
  loads the object fails the same way with the embedded one.
  Filed as task il4, fixed in the source (decision #31): the
  builder's clang 19 objects, arm64 and x86, then loaded and
  traced on the same guest ([build](build.md), "arm64
  status").
- **With the same source built by clang 22.1.1**
  (`-D__TARGET_ARCH_arm64`, laptop `vmlinux.h`) and loaded
  through `IORMESH_BPF_OBJECT`, the tracer attaches ("38
  tracepoints missing on this kernel" — the legacy syscalls
  arm64 never had), and the trace holds 861 events: `dd`'s
  writes summing to 8 MiB (8388608 bytes) and its reads to
  8 MiB plus its library loads, `connect` and
  `accept4` with the loopback 4-tuple and one `conn_id`,
  `sendto`/`recvfrom` with 4098 bytes each way, `tcp_state`
  transitions, exec/fork of the children; the session record
  names `arch: aarch64`. No lost records, no unpaired exits.
- **Root BPF gate with that object**: 38 of 42 pass. Two fail
  on x86 assumptions in the tests, filed as task jl4:
  `TestLoaderAttachesEveryFamily` wants no tracepoint skipped,
  but arm64 has no `open`, `mkdir`, `rename`, `dup2`, `pipe`,
  `poll`, `epoll_wait` and the like (46 tracepoints);
  `TestEl9RecordLayout` loads the arm64 object against the x86
  el9 BTF fixture, where `handle_raw_sys_enter`'s `pt_regs`
  relocation finds nothing. `TestJavaMarkNamesTheNative`
  and `TestJavaMarkOnHeldRead` first failed only for want of
  `java`; with the guest's `java-21-openjdk-headless` both
  pass (160 s and 111 s), so `javamark.c`'s uprobes on
  libjava's natives, `ctx->sp` included, work on arm64 — 40 of
  42 in all. Task jl4 (next section) made the two arm64-aware.

The x86 `vmlinux.h` was enough for the clang-22 object:
CO-RE relocated every field the attached programs use on the
arm64 kernel, `dispatch.c`'s guarded `thread_info.status`
included.

## The root BPF gate on arm64 (task jl4, 2026-11-24)

The two tests 8k4 found x86-bound now know the arch, and stay
exactly as strict on amd64:

- **`TestLoaderAttachesEveryFamily`** lets the loader skip
  only the probes of `tracepoints.ArchAbsentSyscalls(GOARCH)`
  and fails on any other skip. The list is empty for amd64.
  For arm64 it is the 23 family syscalls the kernel's generic
  syscall table (`include/uapi/asm-generic/unistd.h`) leaves
  out: `open`, `creat`, `mkdir`, `mknod`, `rmdir`, `unlink`,
  `rename`, `link`, `symlink`, `readlink`, `newstat`,
  `newlstat`, `utime`, `utimes`, `futimesat`, `chmod`,
  `chown`, `lchown`, `dup2`, `pipe`, `epoll_create`,
  `epoll_wait` and `poll` (46 tracepoints, 8k4's skip list).
  `TestArchAbsentSyscallsMatchTable` (internal/tracepoints,
  no root) pins the list to the family syscalls without an
  entry in `internal/bpf/sysnum_<arch>.go`, generated from
  x/sys's `zsysnum_linux_arm64.go`, which is that generic
  table: no name more, none less, and none for amd64.
  `TestArchAbsentSyscallsReplacements` checks that each has
  its replacement in the families (`openat`, `mkdirat`,
  `unlinkat`, `renameat2`, `newfstatat`, `utimensat`,
  `fchmodat`, `fchownat`, `dup3`, `pipe2`, `epoll_create1`,
  `epoll_pwait`, `ppoll`, ...), and since any other skip
  fails the root test, those replacements must attach on
  arm64. `TestUnexpectedSkips` (internal/archskips_test.go,
  no root) pins the filter. Red first: the list was empty, and
  `TestArchAbsentSyscallsMatchTable` failed naming the 23.
- **`TestEl9RecordLayout`** relocates the object against the
  trimmed el9 BTF of its own arch. The new
  `internal/testdata/btf/rocky9.9-5.14.0-741.22.1.el9_9.aarch64.min.btf`
  (10,747 bytes) is `bpftool gen min_core_btf` of the 8k4
  guest's `/sys/kernel/btf/vmlinux` (sha256 `1167cf6c…`,
  kernel `5.14.0-741.22.1.el9_9.aarch64`) for the arm64 object
  (`IORMESH_TARGET_ARCH=arm64 mage bpfBuild`, clang 22.1.1);
  adding 8k4's clang-19 arm64 object to the trim gives the same
  file. el9's aarch64 `raw_syscalls` records have the same
  layout as x86's (id 16, args and ret 24, tracefs format). On
  an arch without a file the test skips naming it; a listed
  file that is missing still fails, as on amd64 before.

Runs: on the x86 7.2 laptop both root tests pass unchanged
(`sudo -n -E` on `./internal`'s test binary, `-test.run` for
the two). In the 8k4 aarch64 guest (booted once for this, 2
vCPUs under TCG, 10:27–10:39 UTC, then powered off), the
static arm64 test binary with the clang-22 object embedded
(cross-built in the `tracer-arm64` builder image) passes
both: 46 probes skipped as expected, every other one attached;
record loads at 16/24 against the trimmed aarch64 BTF and
against the guest's own BTF. Not covered: that the arm64
replacements deliver records (only that they attach), and the
gate with the clang-19 object (task il4).

## Builder clang pin (task ll4, 2026-11-24)

Decision #31 puts every variable array index's bound on the
indexing register (`iormesh_index_mask`, and
`iormesh_index_clamp` where the bound is no power of two), so
the source should load whatever clang builds it. The release
builders still check their clang: `ARG CLANG_VERSION` in
`scripts/container/tracer-amd64.Containerfile` (22.1.8, Rocky
9.9) and `tracer-arm64.Containerfile` (19.1.7, Debian trixie)
fails the image build when the distro's `clang --version`
differs, and `TestBuilderClangPinned` (internal/buildgate, no
root, CI-safe) fails when a Containerfile's value is not the
one recorded in its `builderClang`. Red first: with the arm64
entry recorded as 22.1.1 it failed with
`scripts/container/tracer-arm64.Containerfile installs clang
19.1.7, recorded "22.1.1": re-run the el9 verifier check ...`.
`TestContainerfileClangRejects` covers a missing, a doubled and
an unchecked `ARG`.

**The el9 verifier check**, to run before changing either
value (the image build has already failed, or a newer clang is
wanted):

1. Compile the object with the new clang, for the builder's
   arch — `mage containerBuild podman tracer-<arch>` with the
   new `CLANG_VERSION`, or the distro image with only clang
   installed (`clang -g -O2 -Wall -fpie -target bpf
   -D__TARGET_ARCH_<x86|arm64> -I<libbpfgo>/output -c
   internal/c/iormesh.bpf.c`).
2. Load it on el9: on an x86 el9 host, or in the emulated el9
   aarch64 guest of "arm64 on an emulated el9 kernel" above,
   run `gatecmd.RootBPFTests` as root with the release's static
   test binary, or at least `TestLoaderAttachesEveryFamily`
   and `TestEl9RecordLayout` with `IORMESH_BPF_OBJECT` set to
   the new object. `TestLoaderAttachesEveryFamily` loads every
   program, so a verifier refusal fails it.
3. Only when that passes, update `CLANG_VERSION` and
   `builderClang` together, and record the run here.

**The two remaining clamp-then-index sites** (both loaded
before, but only because of how clang 19 and 22 happened to
compile them; `llvm-objdump -d -l` of the clang-19.1.7 object
before and after):

- `filter.c` `ior_update_syscall_aggregate`: the
  `bucket_idx >= IOR_HISTOGRAM_BUCKETS` clamp emitted no
  instruction at all — the index was a constant per branch of
  `ior_histogram_bucket_index`. Now `r7 &= 0x7`
  (`iormesh_index_mask`) on the register that indexes
  `duration_histogram` in the map value and in `fresh` on the
  stack.
- `fdname.c` `ior_copy_leaf_name`: `IOR_FD_NAME_LENGTH` is 68,
  no power of two, and a mask with 127 would not keep `name[len]`
  inside the field. `iormesh_index_clamp` emits
  `if r7 <= 0x43 goto +1; r7 = 0x43` as inline asm on the
  register that is both `bpf_probe_read_kernel`'s size and the
  terminator's index.

**Runs.** `mage containerBuild podman tracer-arm64` rebuilt
the release from this source in 61 s (the new
`CLANG_VERSION` step printed "Debian clang version 19.1.7
(3+b1)"; the embedded object's DWARF names that clang), and
`./internal`'s static arm64 test binary was cross-built in
the same image as in 8k4. The 8k4 aarch64 el9 guest
(`5.14.0-741.22.1.el9_9.aarch64`, SELinux enforcing) was
booted once for both, 4 vCPUs under TCG at `nice 10`, 4 GiB,
SSH on 127.0.0.1 only: up 11:02–11:20 UTC (17 min), then
powered off, and no `qemu-system-aarch64` process remained.

- The release `iormesh-linux-arm64` traced 8k4's target (two
  `dd`s and a loopback TCP exchange): attached with the 38
  tracepoints arm64 lacks missing, 860 events, `arch:
  aarch64`, no lost records, no unpaired exits.
- **Root BPF gate with the release's clang-19 object: 43 of
  43 pass** (`gatecmd.RootBPFTests` as root, from a copy of
  `internal/testdata/btf`, 11:05–11:18 UTC; `-test.timeout=3h`
  instead of 5m for the emulated CPU).
- Rocky 9.9's clang 22.1.8 (the amd64 builder's) objects,
  `-D__TARGET_ARCH_arm64` and `_x86`, pass
  `TestLoaderAttachesEveryFamily` and `TestEl9RecordLayout`
  through `IORMESH_BPF_OBJECT` on the same guest; the x86 one
  passes both on the 7.2 laptop too. That is the el9 check
  behind the amd64 value; the amd64 release itself was not
  rebuilt or run on an x86 el9 host.
- `mage bpfRootTest` on the 7.2 laptop passes.

## Release dry run v0.1.0 (task 7k4, 2026-11-26)

The first release candidate, cut as a dry run and checked as an
operator would, following [build.md](build.md#releases) and
[guide chapter 3](guide/03-release.md) literally. Nothing was
published: the `v0.1.0` tag was local only, on `c10b0a3` (commit
time 2026-11-26 09:58:16 +0200) in a git worktree, never
pushed, and deleted again (`git tag -d v0.1.0`) after the runs;
no upload, no GitHub release. Releases ship unsigned
(decisions.md #32), so the run needed no key or password.

- **Release**: `LIBBPFGO=… nice -n 10 mage release podman`
  exited 0 in about three minutes on the Fedora laptop (builder
  images cached from earlier work). Every Linux binary passed
  its `--help` smoke run on Rocky 9 of its own arch (arm64
  emulated), none skipped. Output, `dist/release/0.1.0/`:

  | File | Bytes | SHA-256 |
  |---|---|---|
  | `iormesh-0.1.0-amd64.tar.gz` | 26626747 | `0c9ec424…71f3c` |
  | `iormesh-0.1.0-arm64.tar.gz` | 24621304 | `2d90a316…772c6` |
  | `SHA256SUMS` | 186 | `106ad907…ddbdb7` |

  Each archive holds, under `iormesh-0.1.0-<arch>/`, root 0/0,
  every mtime the commit time: `CHANGELOG.md` (5524),
  `LICENSE` (1068), `iormesh-agent.jar` (291845, 0644), and
  0755 `iormesh-linux-<arch>` (amd64 32260664, arm64 30229584),
  `iormeshctl-darwin-<arch>` (23586176, 22130898),
  `iormeshctl-linux-<arch>` (23404150, 21898733) — what the
  guide's checkpoint lists.
- **Checksums**: in a fresh scratch directory, `sha256sum -c
  --ignore-missing SHA256SUMS` and plain `sha256sum -c` printed
  `OK` for both archives.
- **Sign**: with a throwaway key pair (below),
  `./iormeshctl-linux-amd64 sign -key KEY .` in the unpacked
  amd64 archive (password from `$IORMESH_MINISIGN_PASSWORD`)
  signed the four builds at 0.1.0, `4 files signed by key
  E2864DAA8B31E755`; `CHANGELOG.md` and `LICENSE` got no
  `.minisig`. The same controller signed the unpacked arm64
  archive the same way.
- **Verify**: `verify -key PUB` printed `OK, signed <file>
  0.1.0` for all four amd64 builds and for the arm64 tracer
  copied with its `.minisig` next to them (the guide's
  both-arches step). Refusals, exit 1 each: `LICENSE` (`open
  LICENSE.minisig: no such file or directory`), the jar with
  one byte appended (`the file does not match its signature`),
  and the tracer against the laptop's real pinned key (`signed
  by key E2864DAA8B31E755, the pinned key is …`). `deploy` was
  not run: no hosts in a dry run.
- **Determinism**: a second `mage release podman` rebuilt
  every container binary and gave byte-identical archives and
  `SHA256SUMS` (`cmp`). The jar was **not** rebuilt in that
  run (`agentBuild` found it fresh); removing
  `bin/iormesh-agent.jar` and running `mage agentBuild` again
  (OpenJDK 25.0.5) gave other bytes (`fdc86361…` against
  `5779b517…`): each zip entry carries its build time and the
  nested bridge jar differs with it. Filed as task bm4, since
  fixed ("Reproducible agent jar" below).
- **Refusal**: with the tag deleted, `mage release podman`
  stopped up front: `release: no v0.1.0 tag reachable from
  HEAD`.

Where the docs and reality differed (fixed in this commit):

- The guide's key step needs the `minisign` CLI, and nothing
  said that iormeshctl cannot make a key or where minisign
  comes from; it is not installed on this laptop, so the
  throwaway key came from a ten-line Go helper outside the
  repository (`aead.dev/minisign` v0.3.0 `GenerateKey` and
  `EncryptKey`, the library iormeshctl uses), and `minisign
  -Vm` was not run. build.md "Signing", guide chapters 2 and 3
  now say so.
- Guide chapter 2 listed the repository, Go, mage and Podman as
  laptop prerequisites, though chapter 3 lets an operator
  download a release; they are marked as needed only to build
  one.
- build.md and chapter 3 offered "a rebuild of the tag compared
  byte for byte" as a trust check, which the jar defeated (task
  bm4); they said binary by binary until bm4 restored the
  byte-for-byte claim.

Everything else ran as written: the `ls` of
`dist/release/0.1.0/`, the archive contents, the `sign` output
format, `verify`'s pinned key and `-key`, and the both-arches
step.

## Reproducible agent jar (task bm4, 2026-11-26)

The dry run above found the agent jar rebuilt to other bytes.
The magefiles now pack every jar with `jar --create --date`
(`jarCreate`), dated `$SOURCE_DATE_EPOCH` or else the commit
time, the date the release archives use
([build.md](build.md#releases), "Reproducible").

- **Red first**: `mage agentReproTest`, which builds the agent
  jar from scratch twice and compares SHA-256s, failed without
  `--date` (`1847d87f…` against `ebeec05e…`, OpenJDK 25.0.5);
  so did `rm -rf agent/build bin/iormesh-agent.jar; mage
  agentBuild`, twice (`9c5e564d…` against `7d276ba0…`).
- **After**: both gave `4b46f8ef…` twice, and a copy of the
  tree at another path, without `.git` and with
  `SOURCE_DATE_EPOCH` set to the commit time, the same bytes.
  `mage workloadBuild workloadOtelBuild workloadLibsBuild` run
  twice gave identical workload jars too.
- **Freshness** (task yk4): a second `mage agentBuild` kept the
  jar (its mtime unchanged); a touched input rebuilt it, and so
  did another `SOURCE_DATE_EPOCH`, whose jar's entries then
  carried that date (`internal/dist` `JarDated`).
- **Entry order**: the jar tool of JDK 17.0.9 and of 25.0.5
  both sort a directory's entries by name whatever order the
  file system lists them in. `internal/dist`'s
  `TestJarToolReproducible` packs one tree written in two orders
  with the `jar` on `PATH` (skipped without one) and wants the
  same bytes.
- **Gap**: the builder's JDK is not pinned; javac's output and
  the manifest's `Created-By` follow it, so a rebuild matches
  only with the JDK release the published jar names.

## el9 runbook and runs

Rocky 9.9 is the main target, and some evidence exists only as
far as the 7.2 laptop and the trimmed el9 BTF can give it. To
run the root suites on el9, build `mage bpfRootTest`'s test
binary (and the integration binary) statically on the laptop,
stage it on `iom1` and run it serially as root, then remove it
— the procedure of "Root suites on Rocky 9.9 (task nf4)" above.
The VMs are started by the owner only.

Stage `internal/testdata/btf/` next to the binary too
(`<dir>/internal/testdata/btf/`): `TestEl9RecordLayout` reads
the trimmed el9 BTF from there and fails without it.

**Root BPF gate on el9 (task rj4, 2026-11-23): 41 of 41
pass.** On `iom1` (5.14.0-741.22.1.el9_9), the static
`./internal` test binary built on the 7.2 laptop (as in nf4,
with the BPF object embedded) ran every test of
`gatecmd.RootBPFTests` as root, staged under `/root/rj4` and
removed afterwards; iom1 was started for the run and stopped
again. It covers oh4's mmsg loop, which until then was only
relocated against the el9 BTF:

- `TestMsgCallsTCP` and `TestMsgCallsUDP` pass: sendmsg,
  recvmsg, sendmmsg and recvmmsg move the expected bytes on el9.
- `TestMmsgVerifierHeadroom` passes: el9 reports
  `verified_insns` (backported; upstream has it from 5.16), and
  its 5.14 verifier processes 1,002 instructions for each mmsg
  enter handler and 42,078 for the sendmmsg exit, against the
  1M limit — the same as 7.2.
- `TestEl9RecordLayout` passes against the host's own BTF as
  well as the trimmed one (both give the same record offsets).

Nothing is pending on el9 for the root BPF gate.

**Both root gates on el9 (task 2k4, 2026-11-26): 43 of 43 and
54 of 54 pass.** On `iom1` (5.14.0-741.22.1.el9_9.x86_64), at
6597008, with no test or subtest skipped. The first integration
run on el9 since nf4 (2026-11-21); it adds
`TestListenPortFollowsRestart`, `TestJavaLateAttach`, the Java
attach tests after 7l4/9l4, and every correlator, agent and BPF
fix since. The binaries were built on the 7.2 laptop as in nf4
(the static tracer, `internal.test` and `integrationtests.test`
with the cgo env plus `-tags netgo,osusergo -ldflags '-w
-extldflags "-static"'`; `iormeshctl`, `edgepeer`, `echopeer`
and the four jars from `mage build agentBuild workloadBuild
workloadOtelBuild workloadLibsBuild`), staged under `/opt/2k4`
with `bin/`, `workload/{scenarios,src,libs,otel}`,
`eval/questions` and `internal/testdata/btf` (iom1 has no `tar`;
`rsync -aR` copies the tree), run serially as root with
`IORMESH_IT_KEEP` set, removed afterwards; iom1 was started for
the run and stopped again.

| Gate | Result | Wall time |
|---|---|---|
| Root BPF (`gatecmd.RootBPFTests`) | 43 of 43 pass | 81 s |
| Integration (`gatecmd.IntegrationTests`, serial) | 54 of 54 pass | 218 s |

The integration binary ran with `gatecmd.IntegrationTestArgs(0)`
minus `-test.failfast`, so a failure would not have hidden the
tests after it. Newly covered on el9: `TestMsgCallsPeers` (hl4)
and `TestPeekCountedOnce` (ok4) pass. `TestMmsgVerifierHeadroom`
reports 993 and 992 instructions for the sendmmsg and recvmmsg
enter handlers and 42,659 and 42,684 for their exits (rj4:
1,002 and 42,078), still far below the 1M limit.

## Final socket snapshot (task sf4, 2026-11-21)

`TestSockFinalSnapshot` (`internal/sockfinal_root_test.go`, in
`mage bpfRootTest`) opens a loopback connection in the test
process, exchanges 5 and 7 bytes and leaves it open. It walks
`sock_owner_map` and test-runs `iormesh_sock_final` for each
entry, as `bpf.Tracer.FinalSockets` does. Each end must give one
`TCP_STATE_FINAL` record with its owner, its tuple and the
counters the exchange implies: client 6 acked (its SYN too) and
7 received, server 7 and 5. Both ends' fields must rebuild one
ISN pair. Once both sockets closed, neither gives a record, nor
does an address nobody registered. It passes on the 7.2 laptop
and, with the sock root tests, on `iom1` (5.14.0-741.22.1.el9_9;
static test binary as in nf4, staged under `/root/sf4` and
removed). Mutations caught: no counter read (all zero) and the
owner's inode check inverted (no record).

The sf4 review added the two guards' own cases to the test: an
owner entry planted for the live client socket with its inode plus
one, and one for a socket a refused connect left in `TCP_CLOSE`
with its fd still open, must give no record. Deleting the inode
check, or the `TCP_CLOSE` skip, now fails the test on 7.2. The
final snapshot now reads the four fields once instead of
re-reading them until two reads agree. The merge uses its counters
only when they rebuild the connection's key, which drops a torn
read as well as another connection's record (`keyedCounts`).
`ackedCovered` takes a final snapshot only from a session whose
targets exited (`TestClockAckedPastSum`'s duration, stop and
no-end cases). The walk over `sock_owner_map` takes each key once
and is capped (`TestOwnerKeysDedupesAndStops`).

End to end, `iormesh run -pid` traced a Python process that opens
a loopback connection after the attach, exchanges 5 and 7 bytes
and keeps it open. On both kernels the trace ends with two
`tcp_state` records marked `"sock_snapshot":"final"`, on the
connection's conn id and fd instances: `bytes_acked` 6 and
`bytes_received` 7 on the client, 7 and 5 on the server.

The unit tests: `internal/federate` (`TestClockFinalSnapshot`,
`TestFinalSnapshotDatesNothing`, `TestClockAckedPastSum`),
`internal/correlate` (`TestFinalSnapshotEvent`), `internal/cli`
(`TestFinishWritesFinalSnapshots`), `internal/bpf`
(`TestFinalRecords*`, `TestDecodeFinalRejects`), `internal/schema`
(`TestSockSnapshotFinal`) and `internal/normalize`
(`TestNormalizeDropsFinalSnapshots`). Each guard was mutated out
once and a test failed every time.

## Connections open before the session (task cm4, 2026-11-26)

`TestSeedPreopenedConnection` (`internal/preopen_root_test.go`,
in `mage bpfRootTest`) starts two children of the test binary
(`internal/preopen_child_test.go`). Each listens on loopback,
connects to itself and accepts, all before the tracer loads, and
keeps the listener open. The tracer traces the first child only,
with the state tracepoint and the data calls, and runs
`bpf.Tracer.SeedSockets` twice. Then both children move 5 bytes
one way and 7 back. Run through the correlator, the target's two
ends must give exactly one initial `tcp_state` event each:
`initial_client` on the connecting fd, `initial_server` on the
accepted one (its port the listener's), `ESTABLISHED`, the
loopback tuple and the four fields. Both ends and their reads and
writes must carry one conn id. Split onto two hosts, the ends
must give a client and a server half with one key, which the
merge fuses. The bystander child gets no event. It failed first
(red) with `fd 6: 0 initial tcp_state events, want 1`. Mutations
caught: the listener lookup dropped (both ends read as clients;
the keys then differ by one and both halves stay `no_match`), the
target check dropped (bystander events), and the
already-registered check dropped (two snapshots per end after
the second seeding). `TestEl9RecordLayout` relocates the two
iterators against the trimmed el9 BTF too; their context struct
is sock.c's own, so they need no type the file lacks. Passed on
the 7.3 laptop; the pod-mode loads (`TestPidNS*`) load the
iterators too.

The unit tests: `internal/correlate`
(`TestInitialSnapshotFusion`), `internal/federate`
(`TestHalvesFromEvents`' initial cases, `TestFuse`'s open-before
cases, `TestFuseFoldsSessions`, and `TestFuseMatchesReference`,
whose reference now knows `Before`), `internal/schema`
(`TestSockSnapshotInitial`), `internal/servicedb`
(`TestInitialSnapshotLocalPair`), `internal/report`
(`TestReportInitialSnapshotRoles`), `internal/cli`
(`TestSessionSeedsOpenSockets`, `TestPortSessionFollowsRestart`)
and `internal/bpf` (`TestDrainIter`).

**On el9 (2026-11-26): 44 of 44 pass.** On `iom1`
(5.14.0-741.22.1.el9_9.x86_64), at dc76ca6, the static
`internal.test` built on the 7.3 laptop ran every test of
`gatecmd.RootBPFTests` as root (staged under `/opt/cm4` with
`internal/testdata/btf`, removed afterwards), none skipped.
`TestSeedPreopenedConnection` passes, so el9's 5.14 loads and runs
both `iter/task_file` programs; `TestEl9RecordLayout` passes
against the host's own BTF. iom1 was started for the run and
stopped again.

The cm4 review found that a host with a million open files in
view would not have been seeded at all. The kernel's
`bpf_seq_read` returns `EAGAIN` once one read() visits
`MAX_ITER_OBJECTS` (1M) objects without output, and the seed
programs write none; `drainIter` ended there. It now reads on,
and the walk resumes. `TestDrainIter`'s EAGAIN case failed first
(`resource temporarily unavailable after 1 reads`). The listener
map is emptied after the walk too.

## el9 record layout (task of4)

lb4's dispatch bug, the raw_syscalls record read at upstream's
offsets on el9, showed only on el9. Two root tests in `mage
bpfRootTest` now hold the fix on a 7.x host:

- **`TestEl9RecordLayout`** (`internal/btf_root_test.go`)
  relocates the dispatchers and close's two handlers against a
  committed el9 BTF, set as libbpf's `btf_custom_path`. Their
  record loads must sit at id 16 and args/ret 24. Relocated
  against the host BTF, they must match the host's tracefs
  format file (8 and 16 on 7.2). The host kernel refuses the
  el9-relocated programs: their BTF-typed `task_struct` loads
  carry el9 offsets. So the test reads libbpf's relocated
  instructions (`bpf_program__insns`, `internal/corereloc`), not
  the loaded ones. With `preserve_access_index` dropped from the
  enter struct it fails: id is then read at 0, the pt_regs slot,
  and args at 8. Each program must read its field at the wanted
  offset and nothing of the record below it; a later load is no
  regression (task bg4). A relocation the trimmed BTF cannot
  satisfy makes libbpf poison the instruction (a call with imm
  `0xbad2310`); the test then fails with a hint to regenerate
  the BTF instead of reporting wrong offsets. The data calls'
  enter and exit handlers (tail-call targets the dispatchers
  do not cover, reading `tcp_sock` fields for `tcp_seq`) are
  relocated against the same BTF and checked for poison only
  (task wi4; the four msghdr calls' handlers too since task
  oh4). A `mage test`
  gate (`internal/buildgate/testonly_test.go`) keeps
  `internal/corereloc` out of every non-test file.
- **`TestDispatchSeesSeveralKinds`** traces an open/read/close
  loop and requires more than one syscall kind, and all three
  enters. The el9 bug sent nearly every call to the read
  handler.

The BTF is
`internal/testdata/btf/rocky9.9-5.14.0-741.22.1.el9_9.x86_64.min.btf`
(11 KB), trimmed to what the object's CO-RE relocations name. It
was extracted from the Rocky kernel-core RPM
(`BaseOS/x86_64/os/Packages/k/kernel-core-5.14.0-741.22.1.el9_9.x86_64.rpm`
on the Rocky 9.9 mirror, sha256
`8a339817a1d896907da0a776a00f5a022bf73aa84f1e8983f1da2c314fe81294`,
as the repository's `primary.xml` lists it); a trim of iom1's
live BTF is byte-identical. To regenerate it after `mage
bpfBuild` (needed when the four programs gain relocations
against types the file lacks):

```sh
sha256sum kernel-core-5.14.0-741.22.1.el9_9.x86_64.rpm  # 8a339817…
rpm2cpio kernel-core-5.14.0-741.22.1.el9_9.x86_64.rpm | cpio -idm
scripts/extract-vmlinux lib/modules/*/vmlinuz > vmlinux  # kernel tree
bpftool gen min_core_btf vmlinux el9.min.btf internal/c/iormesh.bpf.o
# or: ssh root@iom1 cat /sys/kernel/btf/vmlinux > el9.btf
```

The test picks the file by `GOARCH` (`el9BTFs` in
`internal/btf_root_test.go`): the object's `pt_regs` accesses
follow `__TARGET_ARCH`, so an arm64 object finds no field in
x86's BTF. The aarch64 one,
`rocky9.9-5.14.0-741.22.1.el9_9.aarch64.min.btf`, is trimmed
the same way from the emulated el9 aarch64 guest's
`/sys/kernel/btf/vmlinux` for the object `IORMESH_TARGET_ARCH=arm64
mage bpfBuild` builds (task jl4, previous sections).

The nf4 review changed `TestProcessExitGroupDead` in the same
change-set. It waits for the record it needs, then for a 200 ms
quiet period, instead of a fixed second. Where
`sched_process_exit` has `group_dead`, read from the format file,
it requires exactly one `group_dead` record; the el9 fallback
keeps "at least one". The child locks its main thread in
`init()`. `TestJavaMarkNamesTheNative` reports a missing
off-launcher-thread mark in an error of its own, which names
commit 46ba0e49.

## Comm renames (task 0g4)

exec.c's `task_rename` program was compiled and loaded but never
attached, so a process renaming itself kept its old `comm` in the
trace and the graph. It is now a control probe (raw tracepoint,
every family), and the correlator applies the main thread's
rename; a worker thread's is ignored (docs/schema.md, `comm`).
`TestRenameReflected` (root, `internal/rename_root_test.go`) runs
a Go child that names a worker thread and then its main thread
with `prctl(PR_SET_NAME)` between two `fstat(-1)` calls. Both
renames must arrive as records, the first call must keep the old
comm and the second carry the new one. correlate's
`TestRenameRefreshesComm` holds the leader/worker rule.
`TestEveryHandWrittenProgramIsAttached` now lists the programs
from the compiled object's ELF symbols rather than a regex over
the C, and requires every attached program to be in the object
(task bg4).

## Command-line help (task vh4)

milestones.md M4 ("Command-line conventions") makes `SUBCMD -h` a
request: exit 0, only that subcommand's flags. Both binaries pin
it with `TestSubcommandHelpExitsZero` (`internal/ctl/usage_test.go`
over every subcommand, services verb and show view;
`internal/cli/cli_test.go` over every tracer subcommand), each with
the negative case that an unknown flag still exits 2 with the full
usage. `TestUsageNamesEverySubcommandAndFlag` runs `-h` on every
entry of `iormeshctl`'s dispatch tables (`commands`,
`servicesVerbs`, `showViews`) and fails when the usage text lacks an
entry, a view or a flag the flag set registers (`[K8S]` stands for
`-context` and `-namespace`). Add a subcommand or a flag and the
test asks for its usage line.

## Local site (task nb4)

`mage test` covers the generated site without a browser
(`internal/site`, `internal/ctl/open_test.go`: determinism, the
CSP, no remote URL, hostile strings escaped, runs kept). The one
browser check is opt-in, because a test run must not need a
browser:

```sh
CGO_ENABLED=0 IORMESH_SITE_BROWSER=1 go test -run Browser ./internal/site/
# or IORMESH_SITE_BROWSER=/path/to/chromium
```

It writes a site of a model full of hostile strings, loads it in
headless Chrome or Chromium from `file://` at `#/selftest` (the
page renders every route in turn) and fails unless the DOM says
`selftest: ok` and the browser logged no console message — an
exception, or a CSP refusal, which is how a request or an
injected script would show. It passed with Chrome 144 on
2026-11-20. Without Chrome, the same check by hand: build, run
`iormeshctl open`, append `#/selftest` to the URL and see
`selftest: ok N routes` with an empty console (Firefox included).

## LLM introspection eval (task qb4)

milestones.md M5's eval is no part of `mage test` — it needs a model —
but everything around the model is: `internal/eval`'s tests run
the harness against a scripted model and a stub iormeshctl. The
eval itself is two opt-in steps:

```sh
mage evalTrace   # root: one judged TestMicroservices run -> eval/store
IORMESH_EVAL_MODEL_CMD='ollama run MODEL' \
IORMESH_EVAL_MODEL_NAME=MODEL mage eval
```

The model command reads the prompt on stdin and writes its reply
on stdout. The prompt carries the trace's rollup and query
slices, so pick a local model unless the trace is one you may
send elsewhere; the workload's traces hold nothing but the
scenario's own files and requests. The question set and its
pinned threshold (0.8) are in `eval/questions/microservices.yaml`;
results land in `eval/results/<date>-<scenario>.json`. Rerun it on
every schema change (milestones.md M5).

## K8s capability set (task ub4)

decisions.md #13 (provisional) names the capabilities the
tracer needs without `privileged`. They were checked on the host,
not in a cluster (2026-11-20, Fedora kernel 7.2.15, SELinux off,
JDK 25.0.4, libbpfgo v0.9.2): a container's root is uid 0 with a
reduced capability set, which `setpriv` reproduces by shrinking
the bounding set before exec. The tracer and the agent jar were
installed root-owned in a root-owned directory (the jar trust
rule; a binary under a 0700 home is not even executable without
`DAC_OVERRIDE`), and a fresh JVM running `Files.readAllBytes` in a
loop as an ordinary user was traced with live attach:

```
sudo -n setpriv --bounding-set=-all,+bpf,+perfmon,+sys_resource,\
+sys_ptrace,+dac_override,+kill,+checkpoint_restore --inh-caps=-all -- \
  /run/iormesh-x/iormesh run -pid JVM -project x -out OUT -duration 6s \
  -spool SPOOL -agent /run/iormesh-x/iormesh-agent.jar
```

With that set the run matched an unrestricted root run: the
agent loaded, `java agent of pid N joined, 37 natives probed`, and
≈310 of ≈360 brackets attributed in both. Each capability left
out alone (one fresh JVM per run):

| Left out | What broke |
|---|---|
| `BPF` | libbpf's probe load: `Couldn't load trivial BPF program` |
| `PERFMON` | loading the first tracing program (`handle_inet_sock_set_state`): EPERM |
| `SYS_RESOURCE` | `load embedded module: error setting rlimit` — libbpfgo's 512 MiB memlock bump; the same run under `prlimit --memlock=unlimited` traces fine without it |
| `SYS_PTRACE` | kernel tracing works; attach fails: `JVM root: permission denied` (`/proc/<pid>/root`) |
| `DAC_OVERRIDE` | kernel tracing works; attach fails: `connect .java_pid<N>: permission denied` (the JVM's 0600 socket) |
| `KILL` | kernel tracing works; attach fails: `SIGQUIT: operation not permitted` |
| `CHECKPOINT_RESTORE` | the agent loads, but `java mark: none of the 37 natives found` and nothing is attributed (`/proc/<pid>/map_files`) |

Kernel-only tracing (`run -pid` of a shell loop, no `-spool`)
needs `BPF`, `PERFMON` and `SYS_RESOURCE` — or just the first two
with the memlock limit raised. Not covered here, and left for the
kind run: tracefs and BTF inside a container, the runtime's
seccomp and AppArmor defaults, and a non-root tracer uid. PID
namespaces are covered on the host (task 4f4, below).

## PID namespaces (task 4f4)

An in-pod tracer numbers every process as its PID namespace does
(decisions.md #13, docs/implementation.md M7 step 1). Checked on the
host, without a cluster, with `unshare --pid --fork --mount-proc`
(a new PID namespace and a `/proc` mounted for it — what a pod's
container has):

- `TestPidNSTracerConfined` (in `mage bpfRootTest`): the test
  binary re-runs itself as such a tracer. Inside, a forking bash
  is traced by its namespace pid and its forked child follows by
  the namespace pid its `$!` reports; a bash in a namespace nested
  one level deeper (a second `unshare`) is traced by its number in
  the tracer's namespace, its child followed; and the tracer is
  told to target the host pid of a host bash that keeps opening
  the probe file outside — no record of it may appear; nor of a
  bash in a *sibling* namespace (a second `unshare --pid` beside
  the tracer's) that it is told to target by its number there.
  Only the expected children may open the probe file, and no
  record may carry pid 0. With the namespace switch forced off
  (the tracer reading host pids, as before task 4f4) the three
  positive checks fail; with the namespace-inode check at the
  tracer's level removed, the sibling check fails (its loop is
  traced as pid 203).
- The root test binary run whole inside a namespace
  (`sudo -n unshare --pid --fork --mount-proc ./iormesh-internal.test
  -test.run …`): the loader, follow, exec-watch, native-stack,
  signal, Java-mark (uprobe_multi attached by the JVM's namespace
  pid) and stream-sequence tests pass. The `TestSock*` tests do
  not apply: they load the object without the loader, so it stays
  in host mode while their pids are the namespace's.
- On the host, the same tests — and the integration suite — are
  the check that host mode did not change: the walk is pruned at
  load there.

The tracer refuses a namespace whose `/proc` is the host's
(`/proc/self` naming another pid than its own), since every
`/proc/<pid>` read would then name another process.

## K8s transport (task wb4)

The controller's kubectl transport (`internal/k8s`, decisions.md #12,
#18; docs/implementation.md M7 step 9) is tested without a
cluster: `fake_test.go` puts a fake `kubectl` first on `PATH`, a
shell script that logs its argv (NUL-separated, one record per
call) and serves canned output — pod JSON for `get`, a tar for
`exec … iormesh ready`, a copy of the `--custom` file for `debug`.
The real `execRunner` runs it, so the tests see exactly the argv
kubectl would get. They run with the package's unit tests
(`CGO_ENABLED=0 go test ./internal/k8s/`, and in `mage test`):

- status: a failed remote command (stderr ending "command
  terminated with exit code N", N kubectl's status — also behind
  10 KB of stderr) is `*exec.ExitError`; kubectl failing on its
  own, before or after a complete stream, is `ErrUnreachable`
  without one (both fail when `explain` stops telling them
  apart);
- argv: `--context=` and `--namespace=` on every call, `exec POD
  --container=iormesh -- argv…` with shell metacharacters arriving
  as single, verbatim elements; `debug POD --container=iormesh
  --target=APP --image=<digest> --attach=false --profile=general
  --custom=FILE -- argv…` (`sysadmin` for the privileged profile),
  and the `--custom` spec's security context and two mounts;
- refusals before any kubectl runs: an unregistered or malformed
  context, an image not pinned by `@sha256:<64 hex>` (none is
  refused by `Debug` and `Deploy` alone, since task af4 — exec and
  fetch need no image; the built-in default is empty until task
  yb4 publishes one),
  namespace, pod or container names that are not Kubernetes names
  (`$(id)`, `x;touch …`, `--all-namespaces`), a remote dir other
  than `/opt/iormesh/data/traces/<project>/ready`, and `Push`;
- refusals after the `get` alone: a pod without the `tracefs`
  hostPath or a size-bounded `iormesh-traces` emptyDir, with
  `hostUsers: false`, without the app container, or already
  carrying an `iormesh` ephemeral container; a sidecar target
  without its container or `shareProcessNamespace`;
- the fetch stream: `WriteReady` sends only regular `*.parquet`
  files of a `ready/` planted with a symlink, a FIFO, a
  subdirectory, a dot file and a text file; `Unpack` refuses
  traversal, directory parts, absolute and dot names, links,
  devices, directories, oversize and duplicate entries, and leaves
  no partial file from a truncated stream, nor more than the
  per-fetch total; `WriteReady` notes an oversize file on stderr;
  `Fetch` refuses more than 64 KiB after the tar's end and removes
  on the tracer exactly the files that arrived, also when the
  stream is refused part-way; through an injected file interface
  (`ready_sync_test.go`, task ii4), `Unpack` fsyncs each temporary
  before its rename and the directory after the last, and a failed
  sync keeps `Fetch` from removing what is not durable (nothing at
  all for the directory sync); `TestReadyRoundTrip`
  (internal/cli) runs `iormesh ready` and `-remove` around
  `Unpack`.

What only kind can show is checked by task yb4's run
([below](#m7-acceptance-on-kind-task-yb4)).

## K8s controller wiring (task af4)

iormeshctl's pod path (decisions.md #18's "Wired" note;
docs/implementation.md M7 step 10) is tested in `mage test`
without a cluster, against fake kubectls on `PATH`:

- `internal/k8s` (`pods_test.go`, `deploy_test.go`): `Select`
  passes the selector as one `--selector=` element, keeps running
  pods only, sorted; no running match is `ErrNoPod`, more than 32
  `ErrTooManyPods`; empty, negative-only, set-based and
  option-like selectors never reach kubectl; a pod name or UID
  not in the API server's form fails; kubectl failing is
  `ErrUnreachable`; a tracer container terminated or without a
  status is `Stopped`, with why. `Deploy` starts a debug container on a
  prepared pod, leaves an ephemeral tracer running the pinned
  digest alone (containerd's and cri-dockerd's `imageID` forms),
  verifies a sidecar, and — after the `get` alone — refuses
  another digest, an earlier debug container whose spec image is
  another digest or a tag, a bare `sha256:` `imageID`, a tag, a
  tracer not
  running, a missing sidecar and an unprepared pod. No image:
  `Resolve` passes, `Debug` and `Deploy` refuse before kubectl
  runs. A held fetch lock refuses a second `Fetch` (`ErrBusy`)
  without running kubectl.
- `internal/federate` (`registry_k8s_test.go`): both sections
  round-trip through `Save`; references, the namespace and
  project defaults; `RemovePod`; refusals at load — unpinned
  image, unknown mode, bad context or cluster name, unknown
  cluster, `pod:` and `selector:` both or neither, a negative or
  empty selector, no namespace anywhere, the tracer's name as the
  app container, two projects in a debug entry, a duplicate
  entry, an unknown key, and a server host shaped like
  `pod-<uid>`.
- `internal/cli` (`linger_test.go`, review B1): a whole run —
  pattern target, short `-duration`, a temp trace dir — on a fake
  tracer (`openTracer`): with `-linger`, once the session ended the
  tracer is closed (in `bpf.Tracer`, every program detached and
  every map freed) and a segment is in `ready/`, yet the run has
  not returned; cancelling (SIGTERM) ends it. Without `-linger` it
  returns. Mutation-checked: skipping the linger fails the test
  ("session returned before it ended"), and so does a `finish`
  that no longer closes the tracer. `ready_test.go`: a project with
  no trace dir fails, one without `ready/` yet is an empty stream.
- `internal/ctl` (`pods_test.go`, its own fake answering per pod
  from files): collect of a pod entry runs `get pod`, then `exec
  … iormesh ready` and the remove — never `true`, `test` or
  `find` — into `<store>/shop/pod-<uid>`, and pins that directory
  to `pod-<uid>` and its FQDN in `hosts.json`; a pod kubectl
  cannot exec into, or that is gone, fails the collect and is
  never reported empty (checked by mutation: dropping the fetch
  error fails the test); a pod without a tracer container is
  "nothing to collect" without an exec; a selector collects each
  match into its own directory, and one matching nothing fails;
  `collect` alone includes pod entries; a tracer container that
  terminated (OOM-killed) fails the collect as "session ended",
  pointing at deploy for a fresh debug tracer (task ah4), with no
  exec tried (mutation-checked); an `-context`
  that is not the entry's cluster's, a malformed one, an unknown
  entry and an unpinned `-image` are refused before kubectl runs,
  and so are `-context`/`-namespace` on rm and on a collect or
  refresh of every target; deploy runs `debug … -- iormesh run
  -project shop -linger -pattern=… -duration 1h` (mutation-checked),
  refuses an unprepared pod naming the prepare patch and a
  registry without an image (collect still works there); rm
  keeps the registry when the last collect fails, drops the entry
  with `-force` or after a good collect, and says the tracer is
  not stopped.
- Debug tracers after an app container restart (task ah4).
  These tests use the fake kubectl and never run kind or Docker.
  `internal/k8s/tracers_test.go`:
  - `TracerName` and `tracerIndex` round-trip `iormesh` through
    `iormesh-8` and refuse look-alikes such as `iormesh-1`,
    `iormesh-02` and `iormesh-9`.
  - A pod whose tracers all terminated (exit 137) gets one
    `debug` call for the next name, with the pinned `--image`,
    `--target` and the disk bounds. This holds with containers
    listed out of order and with look-alike names beside them.
  - A newest tracer that runs is left alone, with no `debug`
    call, and the transport names it. It must carry the pinned
    spec image and `imageID` and the project.
  - Refused, with only the `get` run: a newest tracer still
    waiting or without a status, an earlier one running or
    without a status while the newest ended, and eight ended
    tracers (the cap).
  - After a replacement, the attach probes go to `iormesh-2`.
    The predecessor's `dead` or `ended duration` is waited past
    until `running`. A `dead` that never gives way times out,
    naming it, the chance that the run failed before attaching,
    and `kubectl logs … --container=iormesh-2`. An answer that
    changes from the first one seen (`dead` to `ended`, `ended`
    with a new reason, `ended` to `dead`) fails at the change. A
    replacement whose container exits fails at once. A deploy
    rerun that leaves `iormesh-2` running waits past the
    predecessor's `dead` the same way, while a first tracer's
    `dead` still fails at once.
  - `started=` times (task eh4, `attach_started_test.go`), with
    `iormesh-2` running since 00:00:00 in the pod read.
    `TestWaitAttachedOlderState`: a `dead`, `ended` or
    (reused-pid) `running` state started an hour earlier, or
    1 µs past the 2 s skew window, is waited past until the new
    run's `running`, with one pod read for the whole wait. Its
    timeout names both instants and the container's logs.
    `TestWaitAttachedNewState`: a new run's `ended` (with or
    without a reason) or `dead`, started after the container
    or exactly 2 s before it, fails at the first probe and names
    the logs. So does a new `dead` after the predecessor's
    `dead`, which the state-change check alone would miss. A new
    `running` attaches. `TestWaitAttachedNoContainerStart`: no
    status, or a waiting one, falls back to the state-change
    check and reads the pod again each probe.
    `TestWaitAttachedTimelessAfterOlder`: an answer without a
    time after a timed predecessor's fails as a change.
    `TestWaitAttachedMalformedStarted` fails at once on a time
    that is not one. `TestParseStarted` takes RFC3339 in any
    zone and unix ns, and refuses dates, missing zones, years
    before 2020 or from 2200, signs, overflow, over-long
    fractions, escape bytes and a time after `pending`.
    `TestReadyState` (`internal/cli`) pins the `started=` word
    in UTC with nanoseconds.
  - The `Lister` reports the newest tracer and its own status.
  - `Run` and `Fetch` exec into `Target.Tracer`. `New` refuses a
    malformed tracer name, a sidecar tracer other than
    `iormesh`, and an app container named like a tracer.

  `internal/ctl/pods_test.go`:
  - Collect of a pod with a killed `iormesh` and a running
    `iormesh-2` execs both the fetch and the remove into
    `iormesh-2`. It gets both sessions' segments into the one
    `pod-<uid>` and pins one identity.
  - Deploy starts `iormesh-2` and waits on it, or leaves a
    running `iormesh-2` alone, or fails at the cap without a
    `debug`.
  - The stopped-tracer error tells the operator to run deploy,
    except at the cap (`iormesh-8` stopped), where it says to
    restart the pod or use sidecar mode and never mentions
    deploy.

  `internal/segment` (`TestNextRunKeepsKilledRunsSegments`): a
  writer whose flock is gone without `Close` (a kill) is followed
  by another session's writer in the same dir. The new writer
  sweeps the unsealed files and publishes beside the killed run's
  sealed segment.

  Mutation checks: each of 13 single-line mutations fails at least
  one of these tests. The mutations: the next name repeated, the cap
  off by one, no all-ended check, an ended tracer kept as running,
  `superseded` unset or ignored, the `Lister` picking the oldest
  tracer, `exec` or `debug` naming `iormesh`, tracer-name
  validation dropped, a leading zero accepted, collect ignoring
  `PodState.Container`, and deploy skipping the attach wait after
  a replacement. The review fixes added six more, each failing a
  test too: a rerun not marked superseded, a first tracer marked
  superseded, no state-change check, the first answer not
  remembered, that memory reset per wait, and the cap message
  dropped.

## K8s manifests and pod identity (task vb4)

Unit tests in `mage test`, no cluster and no container engine
(milestones.md M7; docs/implementation.md M7 steps 5, 6 and 8):

- `internal/k8s/manifest_test.go`: `deploy/` must equal what
  `mage k8sManifests` writes (golden; edit the generator, never
  the files). Each file is then decoded as untyped YAML or JSON,
  the way the API server reads it, and the fields asserted:
  `hostUsers: true` in both patches, `shareProcessNamespace:
  true` only in the sidecar's; tracefs as a read-only `hostPath`
  `Directory` and the trace dir as an `emptyDir` with a
  `sizeLimit`; uid 0, `allowPrivilegeEscalation: false`, no
  `privileged`, `RuntimeDefault` seccomp, `drop: [ALL]` and
  exactly #13's seven capabilities, in order;
  `seLinuxOptions` with only `type: spc_t` and `level: s0` on
  the tracer container, and no pod-level `securityContext` in either patch
  (task 5h4); the downward-API
  env (`POD_NAME`, `POD_NAMESPACE`, `POD_UID`); the sidecar's
  image pinned by digest, its args ending in the ¾ byte cap and
  its small requests; and no field in the `--custom` profile
  that kubectl debug's own flags set. `Validate` refuses a tag,
  a malformed digest, an odd size and a command line that is
  not `run`.
- SELinux type (task 5h4): `k8sname.TestSELinuxType` takes a
  bare type name and refuses a full context, a level, upper
  case, a dash and over 128 bytes; `TestResolveSELinuxType`
  pins the precedence — an unset type defers, an empty one set
  higher up wins and omits the field, `spc_t` is the fallback, a
  malformed one is `ErrTransport`; `TestDebugSELinuxType` and
  `assertCustom` check the `--custom` file (the type for the
  minimal profile, none for an empty type or the privileged
  profile); `TestPodDeploySELinuxType` runs `iormeshctl deploy`
  against the fake kubectl with the default, a registry
  `selinux_type`, `-selinux-type` over it and `-selinux-type=`,
  and refuses `spc_t:s0` before any kubectl call; the registry
  tests round-trip an explicit `selinux_type: ""` and refuse a
  malformed one at load.
- SELinux level (task 8h4): `k8sname.TestSELinuxLevel` takes
  `s0` and `s0:cN,cM` (distinct N, M ≤ 1023) and refuses empty,
  a range, `s1`, a lone, a third or a repeated category,
  `c1024`, a leading zero, a full context and stray whitespace;
  `TestResolveSELinuxLevel` pins the same precedence as the
  type (unset defers, empty wins and omits the level, `s0` the
  fallback) and `ErrTransport` for a bad level from a layer or
  a `Target`; `TestDebugSELinuxType` checks the `--custom` file
  for a pinned pair, no `level` key for an empty level and no
  `seLinuxOptions` for an empty type; `TestPodDeploySELinuxLevel`
  runs `iormeshctl deploy` with the default, a `k8s:`
  `selinux_level`, a pod entry's over it, `-selinux-level` over
  both and `-selinux-level=`, and refuses three malformed levels
  before any kubectl call; the registry tests round-trip a
  pinned pair and an explicit `selinux_level: ""` and refuse a
  range, `c1024`, `s1` and a full context at load. Each check
  was mutation-checked.
- Pod byte cap and attach wait (task 5g4):
  `internal/k8s/quantity_test.go` reads every byte-size form
  (`2Gi`, `2G`, `2000M`, a byte count, `2e9`, `1.5Gi`, `.5Ki`)
  to its bytes and refuses a sign, a milli suffix, wrong case,
  spaces, a three-digit exponent, a value over 4 EiB and text
  over 40 bytes; `TestTraceCap` pins min(¾, limit − 96 MiB) and
  no cap under 128Mi; `TestCheckTraceLimit` and
  `TestDeployRefusesUnreadableLimit` refuse such a pod in debug
  mode before any `kubectl debug` and accept it in sidecar
  mode. `TestParseAttach` refuses every `ready -state` answer
  iormesh never writes (an upper-case, short, long or trailing
  session id, escape bytes, an end reason outside `[a-z_]{1,32}`,
  a word after `pending` or `dead` other than `dead`'s
  `started=` time), with no raw control byte in
  the error; `TestWaitAttachedProbeFails` (a remote exit status
  fails after one exec), `TestWaitAttachedInterrupted` (a
  cancel says "interrupted" and wraps `context.Canceled`),
  `TestWaitAttachedSlowProbe` (a 300 ms answer still counts
  after a 50 ms wait; a probe over its own bound is named) and
  `TestDeployRunningTracerProject` (an earlier tracer of
  another project, by `-project` or the namespace default, is
  refused) cover the wait; the timeout tests are back to 150
  and 100 ms. Mutation-checked: loosening the session or reason
  pattern, letting `pending`/`dead` take a word, dropping the
  length cap, the milli refusal, the exponent bound, the
  4 EiB bound, the 96 MiB reserve, the 128Mi floor, or the
  debug-mode check in `checkPrepared` each fails a test.
- `internal/k8s/cgroup_test.go`: `/proc/<pid>/cgroup` paths in
  the formats the kubelet and runtimes write — the systemd driver
  (`kubepods-<qos>-pod<uid_escaped>.slice/cri-containerd-<id>.scope`,
  `crio-<id>.scope`, CRI-O's `…/container` sub-cgroup,
  cri-dockerd's `docker-<id>.scope`) and the cgroupfs driver
  (`kubepods/<qos>/pod<uid>/<id>`, `crio-<id>`), every QoS class,
  a static pod's 32-hex UID, kind's `--cgroup-root=/kubelet`
  nesting inside the node and as seen from the host under the
  node container's scope; whole v1, v2 and hybrid files. Negative
  cases: `0::/` (a private cgroup namespace) and host or plain
  Docker cgroups give `ErrNotInPod`; bad or escaped UIDs, short
  or non-hex container ids, unknown runtimes, CRI-O's conmon, v1
  controllers naming different pods and text not in the kernel's
  format are errors. `cgroup_forged_test.go`: pod-named cgroups
  under `user.slice` or a `user@<uid>.service`, inexact slice
  prefixes and unknown classes give `ErrNotInPod`; a pod slice
  missing its systemd parents, under the wrong parent or a
  foreign root is an error (each case fails with the parent
  check disabled); the in-pod `/../<container>` form yields the
  container id. The strings are built to the formats;
  `TestParseProcCgroupKindCaptures` adds the files captured on
  kind (task yb4): from the host, from the node, the tracer's
  own, and the tracer seen from the app container's private
  cgroup namespace.
- `internal/segment/emptydir_test.go`: the trace dir one level
  inside a 0777 setgid directory (an emptyDir root) is accepted
  and created 0755, while the volume root as the trace dir is
  still refused. Mutation-checked: dropping `trustedDir`'s
  write-bit check fails the refusal, and checking the volume
  root too fails the acceptance. The watchdog's byte cap trips
  at the cap, counts regular files only, and is off at 0.
- `internal/cli/podproject_test.go`: with all three downward-API
  variables set `run`'s project defaults to the namespace
  (decision #21) and `-trace-dir` to `k8s.TraceRoot`; `-project`
  and `-trace-dir` still win, `-out` alone writes NDJSON only;
  none set leaves `-project` required; one or two set, or a
  malformed value (a namespace that is no valid label, a UID
  that is no UID), is refused even with `-project` (task 7f4);
  `-disk-max-bytes` reaches the watchdog.
- Pod identity (task 7f4): `internal/k8s/podidentity_test.go`
  checks `PodFromEnv` and `PodRef.Validate` (none set is no
  pod; partial, forged or malformed values are `ErrPodEnv`;
  RFC 1123 names, the API server's UID form only — a static
  pod's hash UID is refused — and the 190-byte FQDN bound;
  the refusal names the variables to unset),
  `HostID` and `FQDN` equal to the controller's `PodHostID` and
  `PodFQDN` pin, and end to end two pods whose app is pid 7
  with one start tick: their segments, one with an FQDN of
  exactly `MaxFQDN`, pass the controller's segment-name pattern
  and the name parser, go through the controller's `Unpack`
  (as do table names with a 10-digit seq), load under the
  controller's pins (directory `pod-<uid>`), and merge into two
  process nodes; that pod's name is one 117-byte part.
  `TestSegmentNameFitsUnpack`: a 249-byte name unpacks, a
  250-byte one is `ErrStream`. `Check` and `Deploy` refuse a
  static pod's UID, a missing identity and an FQDN over
  `MaxFQDN` (`ErrName`), and a debug deploy beside a sidecar.
  `internal/k8sname`: pod names are strict RFC 1123 subdomains
  (`a..b`, `a-.b` refused) with no per-part cap (a 64-byte part
  and a 253-byte name pass, 254 bytes fail), as Kubernetes'
  `IsDNS1123Subdomain`.
  `internal/cli/hostinfo_test.go`: in a pod the host id and FQDN
  are the pod's with or without a machine-id in the image,
  outside a pod the machine-id's, and a partial or malformed pod
  environment fails even with a machine-id present. Mutations
  (each test fails): host id without the UID, a wider
  `MaxFQDN`, the UID, name or FQDN check dropped, the partial
  set read as no pod, a shorter UID prefix, the pod branch
  skipped, and either one-tracer-per-pod refusal in
  `checkContainers` removed.

Checked on kind by task yb4
([below](#m7-acceptance-on-kind-task-yb4)): the image builds and
runs, pods on one node traced side by side, `kubectl patch` and
`kubectl debug --custom` carry the capabilities, mounts and env,
and the trace dir stays 0755 under `fsGroup`. Not checked: the
oldest runtime whose `RuntimeDefault` lets `bpf` through (only
containerd 2.2.0 was run).

## Route map and ClusterIP join (task xb4)

Unit tests in `mage test`, no cluster (milestones.md M7 "Service-NAT
correlation", "Ingress route mapping", #15;
docs/implementation.md M7 steps 11 and 12):

- `internal/routemap/join_test.go`: two synthetic pods' traces —
  the client dials ClusterIPs, the server sees its own pod
  address and the client's (kube-proxy's DNAT, no SNAT) — go
  through the unchanged M4 merge (`federate.Halves`, `Fuse`): the
  two ClusterIP connections to the traced pod fuse by the ISN
  pair. The joined result matches `testdata/clusterip.golden`:
  the fused edge names the Service and the pod (matched by UID);
  an untraced Service with two endpoints stops at its
  StatefulSet with no pod, one with a sole endpoint names it; the
  FQDN is routed through Traefik (`ingress_test.go`
  `TestRouteNamesSoleTracedPod`: the route's edge names the
  Service's one endpoint when that pod is traced, not for two
  endpoints, an untraced pod or an endpoint without a UID;
  `viewmodel` `TestRoutesNameTracedPod` the same from
  observations); an address in the Service CIDR
  no Service holds is `unknown_service`, a fused pod missing from
  a Service's endpoints `stale_service`, an HTTP host whose route
  names another Service than the one reached
  `route_contradicted`; an address no map names yields nothing.
  `table_test.go`: static over discovered for one FQDN, Service
  name or ClusterIP (each shadowing flagged), wildcards of one
  label only, a nil table, an endpoint-less Service never stale,
  a fused direct-to-pod flow contradicting its HTTP host's
  route, a Host with a port. `ingress_test.go`: a reused pod IP
  (the map's endpoint has another UID than the server end's
  `pod-<uid>`) names no pod and flags `stale_service`; in-cluster
  clients asking for the FQDN through Traefik get the route's
  edge and no contradiction — by Traefik's ClusterIP, by an
  address in the Service CIDR with Traefik's Service unmapped
  (`unknown_service` plus the route), fused to a Traefik pod
  through its ClusterIP or directly — while another pod still
  contradicts. Mutation-checked (each fails a test): the UID
  guard on the address fallback, the ingress short-circuit, the
  route after `unknown_service`, the fused-pod ingress check.
- `internal/routemap/load_test.go`: a hostile `routes.yaml` is
  refused whole — group- or world-writable file or directory,
  over 4 MiB, an unknown key, a second document, an escape
  sequence in an FQDN, a single-label FQDN, a space in an
  ingress, a Service without a namespace, a DaemonSet or
  foreign-namespace workload, a duplicate FQDN, a bad or
  unspecified ClusterIP, no ports, a port past 65535, an
  endpoint pod of another namespace, a forged UID, two Services
  claiming one ClusterIP and port, a non-canonical CIDR, and past
  1024 endpoints. `paths_test.go`: a bad `path_templates` entry
  refuses the file too, and only the static map's templates
  reach the table.
- HTTP path templates (task xh4): `internal/pathtmpl` folds
  numeric, canonical-UUID and 16+-hex segments to `{id}` and
  keeps words and versions (`/api/v2/users`, `cafe`, 15 hex
  digits, near-UUIDs, `report-2024`, the query) verbatim;
  operator templates win, a literal one pins a path.
  `internal/servicedb/paths_test.go`: Observe counts one row per
  template; a source imported again under other keys replaces
  its rows (never counted twice), also rows a fused connection
  took over from a server row; raw rows stored before the
  templating (schema version 3 marks them) fold when read,
  while a stored template — a literal `/items/1` rule,
  `/api/2024/{x}` — is shown as stored. `TestBuildPathTemplates`
  (internal/ctl): the model's top paths show `/orders/{id}`,
  then the route map's `/orders/{order}` over the same
  database, while report.json keeps `/orders/1`.
- `internal/routemap/discover_test.go`: a namespace as kubectl
  lists it becomes `testdata/discovered.golden` — a Deployment's
  Service with its named target port resolved and its not-ready
  endpoint left out, a StatefulSet's, routes from an Ingress and
  a Traefik IngressRoute naming two hosts — while headless,
  ExternalName and UDP-only Services are left out and a host
  split across two Services (`ambiguous_route`), a Service over
  two workloads (`ambiguous_workload`), a forged endpoint
  address, a host with an escape sequence, a TraefikService
  backend, a negated matcher, a 65-port Service and an endpoint
  pod of another namespace, UID dropped (`skipped`), and endpoints
  naming no pod (`ambiguous_workload`, its own text) are flags;
  an Ingress's status address names Traefik's Service as the
  route's `ingress_service`, `-ingress-service` the
  IngressRoutes'; JSON that is not kubectl's fails
  whole. `MatchHosts` takes `Host` only (not `HostSNI`,
  `HostRegexp` or a longer name), in v2's list form too. A
  snapshot round-trips 0600; an unknown field, data after the
  JSON value, a forged context and a writable file are refused.
- `internal/k8s/routes_test.go`: a route get's exact argv
  (context, namespace, `get`, the resource, `--output=json`); an
  unserved resource is `ErrNoResource`; `secrets`, an option-like
  context and a bad namespace never reach kubectl; output past
  the cap is refused.
- `internal/ctl/routes_discover_test.go`, a fake kubectl on
  `PATH`: `routes discover` makes only read-only gets in the
  registered context and namespace, survives the missing Traefik
  CRDs and writes the snapshot; the next build names the Service,
  pod and route from it without running kubectl; a static route
  for the same FQDN wins and is flagged `shadowed`; a snapshot
  whose context the registry no longer lists fails the build; no
  subcommand, an unregistered cluster, a bad namespace, a bad
  `-ingress-service` and a failing kubectl write no snapshot; a
  missing `-routes` file fails the build.
- `internal/ctl/routes_test.go`: a build over a ClusterIP mesh
  with the map beside the registry prints the routed edges
  (`testdata/routes_summary.golden`: the fused edge with its
  pod, the database's StatefulSet with "2 endpoints, pod
  unknown", and checkout → FQDN → Traefik → Service →
  Deployment) and models them; a stale endpoint list flags the
  Service and keeps the fused edge on the server's host; a
  writable or forged map fails the build; `-routes` reads
  another file. The same build's `graph.svg` draws the routed
  edges (`testdata/routes_graph_svg.golden`, task bf4); the
  renderers' own route tests are in `internal/render`,
  `internal/tui` and `internal/site`
  ([visualization.md](visualization.md), "Routed edges").

Checked on kind by task yb4
([below](#m7-acceptance-on-kind-task-yb4)): a ClusterIP connection
between two traced pods fusing (iptables and nftables), the
hairpin, the M0 ingress scenario through a real Traefik, and
`routes discover` against a real API server as a list-only
identity (Traefik's CRDs under `traefik.io`; the
`traefik.containo.us` group was not installed, so only its
absence was exercised).

## M7 acceptance on kind (task yb4)

milestones.md M7's acceptance runs on one local kind cluster:
`mage k8sAcceptance` (`magefile_k8sacceptance.go`,
`magefile_k8simages.go`) and the tests in `integrationtests/kind`.
The target says SKIP and succeeds when `kind`, `kubectl`, `helm`
or a usable `sudo -n docker` is missing; starting and stopping
the Docker daemon is the operator's. Every docker and kind call
goes through `sudo -n`. It:

1. builds the controller, the static release tracer (`mage
   releaseTracer`, which ran on the Fedora host directly), the
   agent jar and the workload jar;
2. refuses to go on when Docker's data root (`docker info`'s
   `DockerRootDir`, where the node, images and registry volume
   land) has under 20 GiB free, or on btrfs with metadata within
   0.5 GiB of its allocation and under 2 GiB unallocated — before
   the run and before each image build; it never balances, and
   says so (a NOTICE) when the `sudo -n btrfs` check cannot run;
3. records which base images (`kindest/node`, `registry`,
   `eclipse-temurin`) are present, in a state file beside the
   kubeconfig (the first run's; a rerun over a kept cluster keeps
   it), and, once the setup below is done or has failed, the
   exact image IDs this run's steps used: the registry
   container's if it created that container, the node
   container's if it created the cluster, the Containerfile's
   pinned JRE if its workload build ran — each only when this
   run's own snapshot, taken before its setup, did not hold it
   (`kindrun.Ran`, `UsedImages`, `RecordUsed`);
4. starts a local registry (`registry:2`, pinned) on
   `localhost:5001` with a named volume `iormesh-registry-data`
   (a stopped container of that name is removed first), creates
   the cluster `iormesh-m7` without `--kubeconfig` — root never
   writes a path the operator chose — and writes `kind get
   kubeconfig`'s output as the operator to
   `.k8s-acceptance/kubeconfig` at the repository root (or
   `$IORMESH_KIND_KUBECONFIG`; 0600, ignored by git), then wires
   the node's containerd to the registry; its kind and kube-proxy
   configs are `os.CreateTemp` files;
5. builds and pushes the tracer image
   (`scripts/container/tracer-image.Containerfile`) and a workload
   image (`scripts/container/kind-workload.Containerfile`: a JRE,
   `workload.jar`, and, test-only, the agent jar), both labelled
   `org.iormesh.acceptance=m7`, so each pod's `imageID` carries the
   registry digest — a `kind load`ed image reports a bare config
   digest, which deploy refuses;
6. installs Traefik (chart 42.4.1, Traefik 3.7, a ClusterIP
   Service: kind has no load balancer) from a helm repository
   config and cache in a temporary directory, so the operator's
   helm repositories stay as they were;
7. runs `TestKindAcceptance` and `TestKindMicroservices` (below,
   task uh4) with kube-proxy in iptables mode,
   switches kube-proxy to nftables and runs `TestKindClusterIP`,
   each with `go test -v`, judged as `mage integrationTest` is
   (`gatecmd.KindTestsVerdict`, task si4): the target fails when
   the test does not report `--- PASS`, or when it or any subtest
   reports `--- SKIP` (go test exits 0 for both); `mage
   k8sDebugRestart` judges `TestKindDebugRestart` the same way,
   and `mage k8sMicroservices` `TestKindMicroservices`, each
   alone;
8. tears down exactly what it made (`internal/kindrun`
   `TeardownPlan`): the cluster, the registry container with its
   volumes and the named volume, the run's two tags, any image
   left with the run's label (`docker image prune --all
   --filter=label=org.iormesh.acceptance=m7`), the base images
   the run pulled (by ID: those it recorded as used that no base
   repository held before; with no state none — an
   `eclipse-temurin` or `registry` image the operator pulls for
   other work between a kept run and `mage k8sTeardown` stays,
   task oi4; a state file written before that change carries no
   `used_images`, so its base images are left for the operator to
   prune by hand), the kubeconfig and the state file. Every step
   but helm's uninstall exits 0 when its object is already gone,
   so a failing step is a real failure (sudo or the daemon
   refusing): the rest still run, the teardown fails and the
   state file stays for the next attempt (task qi4).
   `IORMESH_KIND_KEEP=1` keeps everything; `mage k8sTeardown`
   runs the same steps on its own and is safe to repeat. Never
   `docker system prune` or an unfiltered prune: the daemon holds
   the operator's own images, volumes and build cache (the first
   manual run's system prune removed unrelated images). BuildKit's
   build cache cannot be labelled, so the two builds' cache — about
   0.6 GB with the JRE base's layers, measured after a run — stays;
   it is the operator's to prune.

Re-run on 2026-11-21 on cf4's tracer, without the workaround and
with the scoped teardown: `TestKindAcceptance` (all six subtests,
104 s) and `TestKindClusterIP` (83 s, nftables) passed, the
ingress subtest's routed edge `→ shop.iormesh.test →
ingress/iormesh-m7/shop → iormesh-m7/inventory →
iormesh-m7/deployment/inventory (route, 1 connections)` among
them. An interrupted run (SIGINT to mage after the first image
push) printed `interrupt: teardown running as pid …`, and the
detached `k8sTeardown` removed the cluster, the registry and its
volume with the images back to the state before.

Re-run on 2026-11-21 on the df4–ff4 tracer and controller,
without either compensation (no disk flags in the registry, no
wait for the `attached` log line; the sidecar's own start, which
no deploy makes, is still awaited by its log line), on a host disk
97 % full (33 GiB free): `TestKindAcceptance` (all six subtests,
185 s) and `TestKindClusterIP` (103 s, nftables) passed. Deploy
reported every debug tracer `attached, session …` before the test
started a workload, each debug container's command ended with
`-disk-limit 0 -disk-max-bytes 1536MiB`, and the routed edge read
`→ shop.iormesh.test → ingress/iormesh-m7/shop →
iormesh-m7/inventory → iormesh-m7/deployment/inventory →
iormesh-m7/inventory-c78778bcd-wqscn (route, 1 connections)`.
Docker was started for the run and stopped after it; `docker
images -a` and `docker volume ls` were identical before and after,
with no container or kind cluster left.

Checked on 2026-11-21 with a real run over a daemon holding
unrelated images: `docker images -a` was identical before and
after (the three pulled base images removed by ID, the operator's
eight tags untouched), no volume or container left, no kind
cluster, no kubeconfig or state file.

Ctrl-C or SIGTERM during the run starts the teardown as `mage
k8sTeardown` in a session of its own (log in
`.k8s-acceptance/teardown.log`),
which outlives mage's 5-second grace for cancelled targets and the
terminal's process-group SIGINT; if the daemon or the binary is
gone by then, run `mage k8sTeardown` by hand — the recovery step
for any interrupted run. `internal/kindrun`'s tests drive the
plans with a fake runner recording every argv: teardown names only
the run's objects (no `system prune`, no prune without the label
filter), removes only base images the run recorded as used and
that were absent before (never an unrelated ID new in a base
repository, nor one the operator pulled between a kept run and a
rerun or between the state's save and a failed step), keeps the
first run's state across a kept cluster's rerun while used IDs
accumulate, keeps `JREImage` equal to the
workload Containerfile's pin, replaces a
stopped registry, never passes `--kubeconfig` to `kind create`,
writes the kubeconfig without following a planted symlink, and
the disk guard reads the data root and refuses a full disk or
tight btrfs metadata.

The tests read the cluster from `IORMESH_KIND_*` (kubeconfig,
context, the two images, the iormeshctl binary, the node
container for `crictl`) and skip without them. Each makes a
fresh namespace, applies `testdata/workloads.yaml`, and applies
deploy/'s patches as an operator would — rendered from
`k8s.Manifests` for the test's image, the sidecar's target set
to the workload's pattern. Every app container sleeps; the test
runs the scenarios with `kubectl exec` once the tracers are
attached, through iormeshctl's real k8s path: a registry with
`k8s:` and `pods:` entries, `deploy`, `collect`, `routes
discover`, `build -registry`.

| Check | What it asserts |
|---|---|
| tracer spec | each debug container as stored: exactly #13's seven capabilities with `drop: [ALL]` after `--profile=general` and `--custom` merged, nothing privileged, `RuntimeDefault` seccomp, uid 0, the three downward-API env vars, both mounts, `command` starting with `iormesh` (the image's `PATH`) and ending with the disk bounds deploy took from the pod (`-disk-limit 0 -disk-max-bytes 1536MiB`, task df4); the tracer's status `imageID` names the pushed digest (the sidecar's too) |
| `clusterip` | tcp-nat's client dials the server Service's ClusterIP (880, DNAT to the pod's 80): the build prints the routed edge `→ Service → deployment → pod (fused, 1 connections)`; each pod's trace matches `expect-client.json` / `expect-server.json` and the merge `expect.json`, the rig's addresses replaced by the client pod's, the ClusterIP and the server pod's (no SNAT between pods); a golden with the client dialing the pod fails |
| `hairpin` | tcp-nat's two roles in one pod, the client dialing the pod's own Service: one fused connection, the server end seeing the node's masquerade address (10.244.0.1), the Service and pod named |
| `identity` | every pod is its own store directory and host, `pod-<uid>`, with the FQDN `<name>.<ns>.<uid[:8]>`; all share one `boot_id` |
| `ingress` | the gateway (debug container with the agent) asks for `shop.iormesh.test` through Traefik (hostAliases to Traefik's ClusterIP); the backend runs under a sidecar. The build prints one routed edge `→ shop.iormesh.test → ingress/<ns>/shop → <ns>/inventory → <ns>/deployment/inventory → <ns>/<inventory pod> (route, 1 connections)` — the pod named as the Service's sole endpoint, traced by the sidecar (task ff4) — no route flag; `model.json` and `graph.svg` carry it; the discovered route's FQDN, ingress, Service and ingress Service equal the scenario's declared `routes.yaml` (discovery leaves the workload to the Service, so it is checked in the build's line), and the snapshot holds the IngressRoute's host too (`traefik.io`); the merged trace matches `workload/scenarios/ingress/expect.json` (both ends unresolved: Traefik splits the call; the gateway's `dns` and `http` events carry the FQDN) |
| `rbac` | `routes discover` as a ServiceAccount allowed only `list` on services, pods, endpointslices, replicasets, ingresses and ingressroutes (both Traefik groups), not secrets: same two routes |
| `sigterm` | deleting a pod ends its debug tracer on the kubelet's SIGTERM: exit 0, about 0.6 s after the delete, inside the 5 s grace |

`TestKindClusterIP` is the `clusterip` row alone, in a namespace
of its own, under nftables.

The gateway's tracer runs `-agent` with the tracer image's jar
and its spool on the tracer's emptyDir; the app container sees
neither, so the tracer stages both inside the JVM's root (M7
step 3, decision #14, task cf4). Until cf4 the run below used a
test-only workaround — the app container mounted the trace
emptyDir and the workload image carried the jar at the tracer's
paths — which is now gone from `testdata/workloads.yaml` and
`kind-workload.Containerfile`. The re-run without it (below)
passed, the ingress routed edge included: the agent the tracer
staged reported the gateway's FQDN as its HTTP host.
The gateway starts behind the workload's startup gate, so the
debug tracer finds the JVM at start and live-loads the agent
before the bracket.

Run on 2026-11-21 (kind 0.31.0, Kubernetes 1.35.0, containerd
2.2.0, Docker 29.9.2, Fedora kernel 7.2.15, one node):
`mage k8sAcceptance` from scratch passed: `TestKindAcceptance`
(every subtest, 107 s; iptables) and `TestKindClusterIP` (84 s;
nftables), then tore the cluster and registry down. Two pods
happened to share pod-local pid 47 and still gave two process
ids. The first from-scratch run had failed only the ingress
golden, which had pinned the backend's `close` to its main
thread — the JDK HttpServer closes on whichever thread finishes
the exchange, so the golden no longer declares it.

Run on 2026-11-24 at 4ca2f82 (task 5k4; kind 0.31.0, Kubernetes
1.35.0 from `kindest/node:v1.35.0`, its containerd 2.2.0, Docker
29.9.2, Fedora kernel 7.2.15, one node): `mage k8sAcceptance`
from scratch, Docker freshly started, passed in 7 min 40 s wall
time, the first full run since sh4, ki4, ii4, the kindrun
claim/record/teardown changes and `TestKindMicroservices` joined
the suite. `TestKindAcceptance` passed every subtest (133 s;
iptables), `TestKindMicroservices` its `mesh`, `pod_fields` and
`identity` subtests (122 s), and `TestKindClusterIP` passed
(95 s; nftables), none skipped. The scoped teardown left `kind
get clusters` empty and no `iormesh-registry` container; it
removed the registry's volume, the two pushed images and the base
images the run had pulled.

Checked by hand on the same cluster and recorded here, not
automated:

- **kubectl floor**: `kubectl debug --custom` is unknown to
  1.29.15, and to 1.30.14 unless `KUBECTL_DEBUG_CUSTOM_PROFILE=true`
  (the controller now sets it on every kubectl); 1.31.12 and
  1.35.9 take it by default. Every version that took it stored
  exactly the custom capabilities with `--profile=general`.
- **Readiness**: a tracer failing at once, as a regular container
  and as a native sidecar (`initContainers`, `restartPolicy:
  Always`), made the pod NotReady and its endpoint not ready
  either way; a healthy sidecar kept both ready
  (docs/implementation.md M7 step 8).
- **`-linger`**: a session ended by `-duration` left the debug
  container running and collectable (its last segment fetched),
  and the pod's deletion ended it with exit 0.
- **`fsGroup`**: under `fsGroup: 2000` the emptyDir root was
  `drwxrwsrwx root:2000` and `traces/` stayed `drwxr-xr-x` 20
  minutes on.
- **Ingress Service**: discovering Traefik's namespace too
  without `-ingress-service` (kind's ClusterIP Service gives the
  Ingress no status address) named the gateway's edge as the
  Traefik Service with `route_contradicted`; with
  `-ingress-service traefik/traefik` it is the route's edge.
- **RBAC**: a forbidden `ingressroutes` list fails discover whole
  and writes no snapshot.

Found and fixed: `build` had no `-registry`, so the snapshot's
context was checked against the default registry and refused
(`build` takes it now, `refresh` shares its own;
`TestRoutesRegistryFlag`); a selector matched a terminating pod
right after a rollout and deploy failed on it (`Select` skips
`deletionTimestamp`; `TestSelectArgvAndFilter` — so deploy,
collect and rm (its final collect) of a selector entry now skip
pods in their grace period, whose tracer is ending on SIGTERM
anyway); kubectl 1.30's gate (`TestDebugArgv`). Follow-ups: cf4
(agent staging in the JVM's root; built since), df4 (debug
deploy without a byte cap: the 90 % `-disk-limit` stopped every
debug tracer on a node fuller than that, so the test passed
`-disk-limit=0 -disk-max-bytes=1536MiB`; built since: deploy
passes the pod's bounds itself, the tracer spec check asserts
them, and the registry carries only the gateway's agent flags),
ef4 (deploy returned before the tracer was attached; the test
waited for its `attached` log line; built since: deploy waits
for the tracer's attach state and the test only checks deploy's
`attached, session` line — the sidecar's own start, which no
deploy makes, is still awaited by its log line), ff4 (a route
edge never named a sole endpoint's pod; built since: a traced
sole endpoint is named, and the `ingress` check expects the
inventory pod at the end of the routed edge, in the summary and
`model.json`).

### Microservices as pods (task uh4)

milestones.md M0 runs the microservices scenario "on plain hosts
(M4/M6) and as pods (M7)"; `TestKindMicroservices` is the pod
run (milestones.md M7 "Microservices as pods"). `mage
k8sMicroservices` runs it alone, with the same setup, disk
guard, judging and scoped teardown as `mage k8sAcceptance`,
which runs it too, after `TestKindAcceptance`; a failure there
does not skip `TestKindClusterIP`.

In namespace `iormesh-m7-micro` it applies
`testdata/microservices.yaml`: one Deployment per service
(billing, inventory, orders, gateway), each app container
sleeping, the prepare patch on each. The registry gives every
pod a debug entry with the agent flags, all in one project.
Then, leaves first, for each service it:

1. ships in the certificate the service pins (billing's into
   orders' `/tmp/w`, orders' into the gateway's), `cat` out of
   one pod and into the other through `kubectl exec`, so the
   copy matches no tracer's pattern;
2. starts the JVM behind the startup gate, deploys the pod's
   debug tracer, waits for the agent's `joined` line, and opens
   the gate;
3. waits for a server's `listening` line, which comes after its
   certificate is published.

The servers bind their pod addresses at the rig spread's ports
(billing and orders 443 with TLS, inventory 80), and the callers
dial those addresses: a TLS certificate names the one address
its server binds, so a ClusterIP would fail the pinning. Then
it collects and builds.

| Check | What it asserts |
|---|---|
| summary | the build saw 4 hosts and 4 sessions, `tcp 4 fused, 0 unresolved` |
| `mesh` | `internal/meshcheck.Check`, the function `TestMicroservicesAcrossHosts` calls too: the four pods' traces, merged with their method tables, fuse the four hops at their server pods' addresses, inventory's end twice (orders and the gateway each call it from their own pod), every 4-tuple mirrored, all in the test's project, nothing unresolved; the merged trace holds `expect-pods.json`, the spread golden with the gateway on a host of its own, at the pods' addresses; every request's two http events agree and are timed; a wrong golden (orders on another host, orders dialing inventory on loopback, the TLS hop to orders declared plain) fails |
| `pod fields` | every event names its pod's namespace and name (task sh4); a process's events also carry the app container's id from the pod status, host-level events none |
| `identity` | each pod is its own `pod-<uid>` host with its FQDN, all on one `boot_id` |

`internal/meshcheck`'s unit tests pin `expect-pods.json` to
`expect-spread.json` (only the gateway's four edges move to
`$h4`). They also check that two hops may share a server end,
which the rig's spread never needed, that each mutation changes
both goldens, and the request pairing. `TestMicroTemplate` and
`TestMicroAddresses` check the template, each service's options
and the address map.

Status: green on kind, 2026-11-22. The first `mage
k8sMicroservices` run stopped at the disk guard (`/var/lib/docker
has 16 GiB free, need 20`) before it created anything; after
space was freed, the rerun passed: `TestKindMicroservices` with
its `mesh`, `pod_fields` and `identity` subtests in 152 s, and
the teardown removed exactly the cluster, the registry, its
volume and the images it had made. Green on el9 k3s under both
kube-proxy modes, 2026-11-26 ("Rerun with pod fields (task
6k4)" below).

### M7 on an existing cluster (task zg4)

kind's node runs in a privileged container, so a kind run says
nothing about a real node's SELinux. For a node of the main target
platform — el9, SELinux enforcing — `mage k8sAcceptance` also
targets an existing cluster: set `IORMESH_K8S_KUBECONFIG` to its
kubeconfig (and `IORMESH_K8S_CONTEXT`, else the kubeconfig's
current context). The run then needs no kind, creates no cluster,
leaves kube-proxy's mode alone (k3s embeds kube-proxy; there is no
daemonset to switch) and runs `TestKindAcceptance` and
`TestKindClusterIP` in the cluster's own mode. It still starts the
laptop's registry on `localhost:5001`, builds and pushes both
images, and installs Traefik with helm. The cluster is the
operator's, and a release `traefik` in namespace `traefik` is the
chart's documented install of a cluster's own ingress controller
(task li4). So before touching the cluster the run claims its
namespaces (`kindrun.ClaimExisting`): it refuses when `traefik`,
`iormesh-m7`, `iormesh-m7-clusterip` or `iormesh-m7-micro` is
already there and not recorded as its own, or when it cannot list
them, and otherwise
records the kubeconfig, the context, the Traefik release and the
namespaces in `.k8s-acceptance/existing.state.json` at the
repository root, beside the base-image state. The teardown
removes only what that record names, in the recorded kubeconfig
and context (not the kubeconfig's current one, which may point
elsewhere by then):
it uninstalls the release and deletes the namespaces (Traefik's
CRDs, which helm never removes, stay). Without a record — a run
refused or failed before its claim, a bare `mage k8sTeardown` —
it leaves the cluster alone; when the namespace deletion fails,
the state file stays for the next `mage k8sTeardown`. It removes
the registry and images as the kind path does and keeps the
operator's kubeconfig. A record of another context makes the next
run refuse until that one is torn down; the refused run's own
teardown leaves that record and its objects alone for an explicit
`mage k8sTeardown`. A record naming no kubeconfig or context (a
hand-edited state file) is not acted on in the cluster: the
teardown says so, keeps the state file and fails. The SIGTERM
check reads the tracer's exit through `IORMESH_K8S_CRICTL`, the
argv prefix that runs crictl on the node (`ssh root@<node> k3s
crictl`), and `mage k8sDebugRestart` kills the app container's
init through it (`crictl stop --timeout 0`, a SIGKILL without
grace: the prefix runs nothing but crictl). Without it both
checks skip, and a skip fails the target (task si4), so a run on
an existing cluster needs the prefix. Without
`IORMESH_K8S_KUBECONFIG` nothing changes.

The cluster must pull `localhost:5001/…` under that name: the
images are pinned by the registry's digest, and that reference is
what the pods run. `scripts/k8s/el9-k3s.sh` sets up such a node:
run on it as root, `up` installs a single-node k3s (pinned, see
below) with containerd's SELinux support on, `k3s-selinux` from
the k3s installer, no bundled Traefik, and a containerd mirror of
`localhost:5001` to `http://localhost:5001`, and adds to
firewalld's trusted zone the pod and Service CIDRs it lacks;
`proxy nftables` (or `iptables`) restarts k3s in that kube-proxy
mode for the second pass; `down` uninstalls k3s with its images
and data and removes the `k3s-selinux` package and repository
(and `container-selinux` and the firewalld sources only if `up`
added them); `status` shows SELinux, k3s and the disk. `down`
refuses to run without the state file `up` writes
(`/root/.iormesh-k3s-state`), so it cannot wipe a k3s node it did
not set up (task ni4).

The script pins k3s v1.35.10+k3s1 and the sha256 of that tag's
`install.sh` (`8598e002…6d91`) instead of piping get.k3s.io into a
root shell; the installer checks the k3s binary against the
release's `sha256sum` file. To bump, set `K3S_VERSION` in the
script to the new tag and `K3S_INSTALLER_SHA256` to the output of
`curl -sfL https://raw.githubusercontent.com/k3s-io/k3s/<tag>/install.sh
| sha256sum`, the tag's `+` written as `%2B`. From the laptop, one
tunnel carries both directions:

```
scp root@node:/etc/rancher/k3s/k3s.yaml "$scratch/k3s.yaml"
ssh -N -L 6443:127.0.0.1:6443 -R 5001:127.0.0.1:5001 root@node &
IORMESH_K8S_KUBECONFIG="$scratch/k3s.yaml" \
  IORMESH_K8S_CRICTL='ssh root@node k3s crictl' mage k8sAcceptance
```

k3s's kubeconfig names `https://127.0.0.1:6443`, which the `-L`
forward serves; the `-R` forward gives the node the laptop's
registry. Trace data stays on the laptop and the node. Delete the
copied kubeconfig after the run.

The harness option and the script are unit-tested
(`TestTeardownExisting`, `TestExistingConfig`, `TestCrictlPrefix`,
the claim and record in `TestClaimExistingRefusesTaken`,
`TestClaimExistingUnreachable`, `TestClaimExistingRecords`,
`TestTeardownExistingWithoutRecord`,
`TestTeardownExistingKeepsRecord`, `TestRefusedRunLeavesForeignRecord`
and `TestTeardownRecordWithoutContext`; the script with shellcheck,
and its `down` against recording fakes in `TestDownRefusesWithoutState`
and `TestDownFirewalld`).

#### Run on Rocky 9.9 (task zg4, 2026-11-21)

One VM (2 vCPU, 4 GB for the run, 20 GB disk): Rocky 9.9, el9
5.14.0-741.22.1, SELinux enforcing, k3s v1.35.10+k3s1 with
containerd 2.2.10, container-selinux 2.250 and k3s-selinux 1.6,
firewalld active; the laptop built and pushed the images.
**M7 does not pass as shipped** (milestones.md M7, reopened):

| Criterion | #13 spec as shipped | With the diagnostic module |
|---|---|---|
| tracer spec (#13's capabilities, `RuntimeDefault`, mounts, image digest) | spec stored as asked, tracer dies on load | PASS |
| BPF load under SELinux | FAIL: `avc: denied { prog_load }` `container_t` `tclass=bpf` | PASS, no AVC |
| tracefs hostPath | not reached (denied in permissive: `tracefs_t` `file { open read }`) | PASS |
| BTF from the container's sysfs | not reached | PASS, nothing needed |
| `clusterip` (fused, goldens), iptables | FAIL (no trace) | PASS |
| `clusterip`, nftables (`el9-k3s.sh proxy nftables`) | not run | PASS |
| `hairpin` | FAIL (no trace) | PASS (server saw 10.42.0.1) |
| `identity` (5 pods, one `boot_id`) | FAIL | PASS |
| debug container with `-agent` (Java live-attach) | FAIL | PASS |
| `ingress` routed edge to the inventory pod | FAIL (sidecar never Ready) | PASS |
| `rbac` (list-only discovery) | not reached | PASS |
| `sigterm` (crictl over ssh) | not reached | PASS: exit 0, 624 ms |

The first column is the run as shipped: the sidecar's rollout timed
out (the tracer crash-looped on `load BPF object: permission
denied`), the debug tracers ended `session failed` and lingered,
and libbpf's message blames `RLIMIT_MEMLOCK` (task 6h4). `ausearch
-m avc` printed no match; the raw `/var/log/audit/audit.log` held
the denial. Six probe pods, one tracer each with `-duration 15s`,
isolated the cause:

| Probe pod | Result |
|---|---|
| #13 as shipped | EPERM |
| `seccompProfile: Unconfined` | EPERM |
| `+SYS_ADMIN` | EPERM |
| `seLinuxOptions.type: spc_t` (#13's capabilities, `RuntimeDefault`) | attached |
| `spc_t` and Unconfined | attached |
| `privileged: true` | attached |

`container_t` permissive (a one-line CIL module, removed again)
logged the rest of what the tracer does: `perf_event { open cpu
kernel read write }` and `tracefs_t` `file { open read }`. The
diagnostic module that made the second column pass, loaded with
`semodule -i` and removed after:

```
(allow container_t self (bpf (map_create map_read map_write prog_load prog_run)))
(allow container_t self (perf_event (open cpu kernel read write tracepoint)))
(allow container_t tracefs_t (dir (search read open getattr)))
(allow container_t tracefs_t (file (open read getattr)))
```

It grants every container on the node BPF and is no deployment
answer; decision #13 records the choices (task 5h4). Two node
findings, fixed in the script: el9's firewalld rejected
forwarded pod traffic (`reject with icmpx admin-prohibited`, so
Traefik's pod got "Host is unreachable" to the backend and the
gateway a 502), so `up` trusts the pod and Service CIDRs at
runtime; k3s's metrics-server never became Ready and its stale
API stalled every namespace deletion, so `up` disables it. The
ingress pass came after both fixes; ClusterIP passed before them.
After the run the cluster, the module, the packages, the
registry and the images were removed: the laptop's `docker
images -a` and volumes matched the state before, and the VM had
the 371 packages and the `/opt/iormesh` it started with.

#### Load refusal hint (task 6h4)

Since that run a BPF load refused with EPERM or EACCES no longer
leaves libbpf's `RLIMIT_MEMLOCK` line as the only clue: the
tracer's `load BPF object` error names the likelier cause
(`internal/bpf/lsmhint.go`). SELinux enforcing with the process
in a confined domain (not `spc_t`, `unconfined_t`, `kernel_t`)
names the context from `/proc/self/attr/current`, decision #13's
el9 profile and the raw audit.log; otherwise an active AppArmor,
BPF LSM or lockdown (`/sys/kernel/security/lsm`); the memlock
hint stays only for a low limit on a kernel before 5.11, else
the message says libbpf's hint does not apply. The error, and
whether the tracer fails, is otherwise unchanged. Unit-tested
with injected file reads and limit (`TestLoadHint`: enforcing
`container_t`, enforcing `spc_t`, permissive, no LSM, low and
high memlock on old and new kernels; `TestLoadHintContextIsSafe`,
`TestWithLoadHint`), each branch mutation-checked.

#### Re-run with `spc_t` (task 5h4, 2026-11-21)

The same VM and versions (4 GB for the run), the tracer container
as shipped by task 5h4 — #13's seven capabilities,
`RuntimeDefault`, `allowPrivilegeEscalation: false` and
`seLinuxOptions: {type: spc_t, level: s0}` — and **no node-local
policy**: `semodule --list-modules=full` listed only the packaged
`container` and `k3s` modules (priority 200), nothing at the
local priority 400. **M7 passes on el9** (milestones.md M7):

| Criterion | iptables | nftables |
|---|---|---|
| tracer spec as stored (`spc_t`/`s0` on the tracer only, pod `securityContext` empty, apps without `seLinuxOptions`) | PASS | PASS |
| BPF load under SELinux, tracefs hostPath, BTF | PASS, no AVC | PASS, no AVC |
| `clusterip` (fused, goldens), both tests | PASS | PASS |
| `hairpin` | PASS (server saw 10.42.0.1) | PASS (server saw 10.42.0.1) |
| `identity` (5 pods, one `boot_id`) | PASS | PASS |
| debug container with `-agent` (Java live-attach, container_t JVM to spc_t tracer through the emptyDir spool, ring and attach socket) | PASS | PASS |
| `ingress` routed edge to the inventory pod (the sidecar tracer) | PASS | PASS |
| `rbac` (list-only discovery) | PASS | PASS |
| `sigterm` (crictl over ssh) | PASS: exit 0, 810 ms | PASS: exit 0, 715 ms |

`kubectl debug --custom` (kubectl 1.35) took the field: every
debug container was stored with it. On the node (`ps -eZ`) the
tracers ran as `system_u:system_r:spc_t:s0`, the app containers
as `container_t` with their pods' own MCS pairs. The raw
`/var/log/audit/audit.log` and its rotations, read by
timestamp (they cover the whole session), held no AVC during
either pass — `ausearch` is not trusted here (task zg4).

The level is part of the fix. The first pass ran with
`{type: spc_t}` alone: `TestKindClusterIP` passed, but
`TestKindAcceptance` failed in the ingress subtest, the gateway's
JVM getting `java.net.ConnectException`; the audit log held
`avc: denied { read }` for `java` (`container_t:s0:c19,c249`)
on `resolv.conf` and `etc-hosts`, labelled
`container_file_t:s0:c292,c345` — the debug tracer's own pair.
Given a type without a level, containerd gives the container a
fresh MCS pair and relabels the pod's shared files (and the trace
`emptyDir`) to it, locking the app out. With `level: s0` those
files (and the `emptyDir`) were `container_file_t:s0`, readable
and writable from every pair, and both passes went green.
After the run `el9-k3s.sh down` left the VM with the same 372
packages (`rpm -qa` identical), the same `/opt/iormesh` (paths,
sizes, mtimes, modes) and no trusted firewalld source; the
laptop's `docker images -a` and volumes matched the state before.
The kind path (`mage k8sAcceptance` without
`IORMESH_K8S_KUBECONFIG`) passed with the field present too:
`TestKindAcceptance` all six subtests (119 s, SIGTERM exit 0 in
702 ms) and `TestKindClusterIP` (104 s, nftables); the kind node
has no active LSM, so there the field is inert. Docker was
started for the runs and stopped after; images and volumes
matched the state before.

#### App container restart (task 8h4, 2026-11-21)

Does an app container restarting mid-session, which relabels the
pod's shared files back to the pod's MCS pair, break the tracer or
the spool? The same VM and versions (4 GB), the tracer as shipped
(`spc_t` at `s0`), no node-local policy, and a minimal
reproduction instead of the suite: the tcp scenario's `server`
Deployment (app container `sleep infinity`, prepare patch) with a
debug tracer carrying the Java agent (the acceptance test's
`-spool`/`-agent` flags), and the ingress scenario's `inventory`
shape with the sidecar patch (`run -pattern '*workload.jar*'`).
In each pod a gated `file-basic` JVM ran and then held; the app
container's init (`sleep`) was then killed with SIGKILL from the
node, and the kubelet restarted the container (restart count 1).

**SELinux: no break.** The raw `/var/log/audit/audit.log`, which
covered the whole session, held no `AVC`, `USER_AVC` or
`SELINUX_ERR`. `ls -Z` on the node:

| File | Before the tracer | Tracer running | After the app restart |
|---|---|---|---|
| `etc-hosts`, sandbox `resolv.conf` and `hostname` | `s0:c557,c864` (the pod's) | `s0` | `s0:c557,c864` |
| trace `emptyDir`, `spool/`, `traces/` | — | `s0` | `s0` |

The restarted container relabels the files it mounts back to the
pod's pair, as expected; the `emptyDir`, which only the tracer
mounts, stays at `s0`. The `spc_t` tracer reads either label.
In the sidecar pod the app container starts after the tracer, so
there those files are at the pod's pair from the start.

**Debug mode: the tracer dies with the app container — not
SELinux.** `kubectl debug --target` joins the app container's PID
namespace; when its init died the kernel killed every process in
it, the tracer included: the ephemeral container ended `exitCode
137` at the same instant, and an ephemeral container is never
restarted. The unsealed segment (`.active-*`, 0 bytes) was lost;
`iormeshctl collect` said "session ended: tracer container
terminated (Error, exit code 137) … stranded until the pod goes",
and `iormeshctl deploy` refused a second tracer ("ephemeral
containers cannot be replaced: restart the pod"). Before the kill
the agent was live-loaded into the JVM (`java agent of pid 41
joined, 37 natives probed`). A JVM started after the tracer, as a
restarted app's always is, gets no agent from `run -agent` — as
[java-attach.md](java-attach.md) documents; it needs `-javaagent`.
Task ah4 takes the debug-mode finding: milestones.md M7 now recommends
the sidecar for pods that restart, and `iormeshctl deploy` replaces
an ended debug tracer with `iormesh-2` (up to `iormesh-8`), whose
`iormesh ready` serves the stranded sealed segments. This is checked
against the fake kubectl (the "Debug tracers after an app container
restart" item above) and on kind, in the next section.

**Sidecar mode: no break.** With `shareProcessNamespace` the pause
container is the PID namespace's init, so the tracer kept running
(restart count 0) through the app restart; the pattern re-attached
to the new JVM, and the collected segment held both incarnations
— pid 27 before the restart and pid 64 after, 644 and 647 events,
8 `file-basic` file events each — plus one `drop` record
(`skipped_runs`, 1 lost) at the restart. After the run `el9-k3s.sh
down` left the same 372 packages and the same `/opt/iormesh`; iom1
was stopped and set back to 2 GB; the laptop's images, volumes and
containers matched the state before, Docker was stopped and the
copied kubeconfig deleted.

#### Dedicated type `iormesh_container_t` (task 7h4, 2026-11-21)

The opt-in policy module (decisions.md #13, `packaging/selinux/`) on the
same VM and versions (4 GB): Rocky 9.9, container-selinux 2.250,
selinux-policy 38.1.86, k3s v1.35.10+k3s1. The RPM came from `mage
selinuxRPM podman` on the laptop and was installed with `dnf`; its
`%post` loaded the module at priority 400 beside the packaged
`container` and `k3s` ones, and a `dnf remove` (its `%postun`)
unloaded it again. (Since the policy review the spec installs and
removes at priority 200, `semodule -X 200`, leaving 400 to local
overrides; task gh4 installed that RPM, below.) These runs relied
on container-selinux's `virt_sandbox_use_all_caps` boolean, on by
default, for the tracer's capabilities; it was not turned off.
Task gh4's module grants them itself and passed with it off.
Two harness variables run the acceptance with
another type and level: `IORMESH_K8S_SELINUX_TYPE` and
`IORMESH_K8S_SELINUX_LEVEL` (default `spc_t` and `s0`). They go
into the registry's `k8s:` and the sidecar patch, the spec check
expects them, and a level other than `s0` is also pinned on every
workload pod (`securityContext.seLinuxOptions.level`), so the
tracer runs at its pod's pair as #13 requires
(`TestSELinuxOptions`). Every run used `iormesh_container_t` at
`s0:c100,c200`.

| Run | Module | Result |
|---|---|---|
| 1, iptables, enforcing | zg4's four rules only | `TestKindClusterIP` PASS; `TestKindAcceptance` FAIL: the gateway's tracer attached, but the agent never joined, and no AVC was logged |
| 2, iptables, the type permissive, dontaudit off (`semodule -DB`) | the same | PASS; the review below |
| 3, iptables, enforcing | zg4's rules plus the Java attach's five | PASS, no AVC |
| 4, nftables, enforcing | the same | PASS, no AVC |
| 5, nftables, module removed, default `spc_t` at `s0` | none (only the packaged `container` and `k3s` modules) | PASS, no AVC: tracers `spc_t:s0`, apps `container_t` at their pods' random pairs, SIGTERM exit 0 in 587 ms |

Runs 3 and 4 in detail:

| Criterion | iptables | nftables |
|---|---|---|
| tracer spec as stored (`iormesh_container_t` at `s0:c100,c200` on the tracer only, apps without `seLinuxOptions`) | PASS | PASS |
| BPF load, tracefs hostPath, BTF | PASS, no AVC | PASS, no AVC |
| `clusterip` (fused, goldens), both tests | PASS | PASS |
| `hairpin` | PASS | PASS |
| `identity` (5 pods, one `boot_id`) | PASS | PASS |
| debug container with `-agent` (Java live-attach) | PASS | PASS |
| `ingress` routed edge (the sidecar tracer) | PASS | PASS |
| `rbac` (list-only discovery) | PASS | PASS |
| `sigterm` (crictl over ssh) | PASS: exit 0, 719 ms | PASS: exit 0, 616 ms |

On the node (`ps -eZ`, sampled every 10 s) the tracers ran as
`system_u:system_r:iormesh_container_t:s0:c100,c200`, the app
containers as `container_t` at the same pair, and the cluster's own
pods (CoreDNS, Traefik, local-path) at their own pairs. The raw
`/var/log/audit/audit.log` and its rotation, read by timestamp, held
no `AVC`, `USER_AVC` or `SELINUX_ERR` during runs 3 and 4.

Run 1's silence is container-selinux's: it dontaudits a container
domain's `search` of another domain's `/proc/<pid>` directory, so
the tracer's first step towards the JVM failed without a record.
Run 2 made the type permissive with a one-line CIL module
(`(typepermissive iormesh_container_t)`) and rebuilt the policy
without dontaudit rules; the audit2allow review of its AVCs (read
by hand — the VM has no `audit2allow`, and installing it would have
changed its packages) found, for `scontext=iormesh_container_t`,
only targets of type `container_t` at the pod's pair, the JVMs of
the app containers:

| Denied | Object |
|---|---|
| `dir { search getattr }` | `/proc/1`, `/proc/7` |
| `file { open read getattr }` | `/proc/1/cmdline`; `read` also without a path, the ptrace read check `/proc/<pid>/root` takes |
| `lnk_file { read }` | `/proc/<pid>/exe` |
| `process { signal }` | the SIGQUIT to the JVM |
| `unix_stream_socket { connectto }` | `/tmp/.java_pid7`, the attach socket |

Nothing else appeared for the type — no further file, network or
capability need. zg4's diagnostic module granted `container_t`
itself, which owns these objects, so it never met them. The five
rules were added as `allow iormesh_container_t container_t:…` with
exactly these permissions; the permissive module was removed and
the dontaudit rules restored (`semodule -B`) before runs 3 to 5.
Not run: the type at `s0` (MCS refuses the Java attach there,
decisions.md #13), a multi-node cluster, a pod without a pinned level.

**Without the boolean (task gh4, 2026-11-21).** The module now
grants the tracer's seven capabilities itself
(`self:capability { dac_override kill sys_ptrace sys_resource }`,
`self:capability2 { bpf perfmon checkpoint_restore }`). Same VM
(4 GB), same versions; the RPM from `mage selinuxRPM podman`.

- *Install at priority 200:* `dnf install` put the module beside
  the packaged ones, `semodule -lfull`: `200 container`,
  `200 iormesh`, `200 k3s`.
- *MCS:* `seinfo -a mcs_constrained_type -x | grep
  iormesh_container_t` printed `iormesh_container_t` (setools
  installed for this and removed again). `sesearch` showed
  `capability2 { bpf checkpoint_restore perfmon }` for the type
  without a condition, where `container_t` has them only under
  `[ virt_sandbox_use_all_caps ]:True`.
- *The acceptance,* iptables, `iormesh_container_t` at
  `s0:c100,c200` as above:

| Run | Boolean (`setsebool`, not `-P`) | Result |
|---|---|---|
| 1 | off | FAIL, not the tracer: the app containers' JVMs (`container_t`, root) were denied `capability { dac_override }` opening `workload.jar` (2 AVCs), so no scenario started; the tracers attached |
| 2 | off, plus a temporary local CIL module at priority 400, `(allow container_t self (capability (dac_override)))` | PASS: `TestKindAcceptance` (all six subtests, SIGTERM exit 0 in 658 ms) and `TestKindClusterIP`, no AVC |
| 3 | on, the CIL module removed | PASS, both tests (SIGTERM exit 0 in 721 ms), no AVC |

`ps -eZ` showed the tracers as `iormesh_container_t:s0:c100,c200`
and the JVMs as `container_t` at the same pair. The raw
`/var/log/audit/audit.log` and its rotations, read by timestamp,
held no `AVC`, `USER_AVC` or `SELINUX_ERR` during runs 2 and 3.
Run 1 shows that the boolean is still the app containers' matter:
a node owner who turns it off must give their own containers what
they need.

- *Cross-pair containment:* one pod (`shareProcessNamespace`)
  with its level pinned at `s0:c300,c400`: the app container ran a
  gated `file-basic` JVM as `container_t:s0:c300,c400`, the tracer
  container `run -pattern '*workload.jar*' -spool … -agent …` as
  `iormesh_container_t:s0:c100,c200`, dontaudit rules off
  (`semodule -DB`) so the denials are logged. The tracer loaded
  its programs ("attached, tracing pattern"), but the pattern never
  matched the JVM, and `kubectl exec … iormesh attach -pid 8`
  (the JVM's pid in the pod) failed "no such process". The audit
  log:

  ```
  avc:  denied  { search } for  pid=27986 comm="iormesh" name="8"
  dev="proc" ino=154534
  scontext=system_u:system_r:iormesh_container_t:s0:c100,c200
  tcontext=system_u:system_r:container_t:s0:c300,c400 tclass=dir
  permissive=0
  ```

  and the same `search` denial for the pause process (`/proc/1`)
  and for the processes `kubectl exec` started in the pod. MCS
  stops the attach at its first step; the signal and socket are
  never reached. The pod was deleted and `semodule -B` restored
  the dontaudit rules.
- *Removal:* `dnf remove iormesh-selinux` unloaded it at priority
  200 ("Removing last iormesh module"); `semodule -lfull` listed
  no `iormesh` and `seinfo -t iormesh_container_t` no type.

After the run the boolean was on, `el9-k3s.sh down` left the same
372 packages and the same `/opt/iormesh`, iom1 was stopped and set
back to 2 GB, the laptop's Docker images matched the state before,
Docker was stopped, the tunnel closed and the kubeconfig deleted.

#### Debug tracer replacement on kind (task ah4, 2026-11-21)

`mage k8sDebugRestart` runs `TestKindDebugRestart`
(`integrationtests/kind/restart_test.go`) on the laptop's kind
cluster. The setup, images and teardown are `mage
k8sAcceptance`'s, and the VMs were not used. The test uses the
tcp scenario's prepared `server` Deployment, whose app container
runs `sleep infinity`, in namespace `iormesh-ah4`. Its registry
entry is a debug tracer with the Java agent (`-spool`, `-agent`).
The steps:

1. A gated `file-basic` JVM is started, then `iormeshctl deploy`.
   The agent joins the JVM, which is released and ends; the test
   then waits 75 s so the session publishes a segment.
2. The app container's init is SIGKILLed from the kind node, its
   pid taken from `crictl inspect` (on an existing cluster, since
   task si4, by `crictl stop --timeout 0` through
   `IORMESH_K8S_CRICTL`).
3. The ephemeral `iormesh` must end. A collect must fail with
   "session ended" and point to `iormeshctl deploy`.
4. Another gated JVM is started, then `deploy` again. While that
   deploy waits for the attach and `iormesh-2` already runs, a
   second `deploy` reruns (N1).
5. The agent joins the new JVM; after the run and another 75 s,
   `collect` runs.

Result: passed (181 s).

- `iormesh` ended with `Error, exit code 137` as soon as the app
  container's init died. The restart count read at that moment
  was still 0; the test then waited for the pod to be `Ready`
  again with the restarted container.
- The collect at that point failed with the deploy advice.
- The replacing deploy took 10.9 s and printed "earlier tracer
  ended (its unsealed segment is lost, its published ones stay
  for collect); started tracer iormesh-2, … attached, session
  11870b83". The agent joined the JVM running then.
- The N1 rerun started 1.7 s in, once `iormesh-2` ran and 9.2 s
  before the replacing deploy returned. So its attach probes
  read the killed tracer's `dead` `state.json`. It left
  `iormesh-2` alone and reported "tracer iormesh-2 already
  running the pinned image, attached, session 11870b83", the
  same session. Before the N1 fix it would have failed at once
  with "died without its end".
- The final collect, which execs into `iormesh-2`, brought 4
  segment files of the killed session `869f05bb` and 4 of
  `11870b83` into the one `pod-<uid>` directory.

Teardown: the cluster, the registry and its volume, the run's two
images and the two base images it pulled (`kindest/node`,
`registry`) were removed. The disk guard passed with 23 GiB free
(minimum 20). Docker was started for the run and stopped after.
`docker images -a`, `docker volume ls` and `docker ps -a` matched
the state before. The `kind` network predates the run (created
11:37 that day) and was left alone.

#### Rerun with pod fields (task 6k4, 2026-11-26)

zg4's procedure at 26da303, the first existing-cluster run since
the pod fields (task sh4) and with `TestKindMicroservices`. iom1
(2 vCPU, raised to 4 GB for the run and set back to 2 GB after,
the config diffed identical): Rocky 9.9, el9 5.14.0-741.22.1,
SELinux enforcing, firewalld active; `el9-k3s.sh up` installed
k3s v1.35.10+k3s1 (containerd 2.2.10-k3s1, systemd cgroup
driver, cgroup v2) with container-selinux 2.250 and k3s-selinux
1.6; Traefik v3.7.19 from the harness. The tracer as shipped
(`spc_t` at `s0`), no node-local policy (`semodule
--list-modules=full`: only the packaged `container` and `k3s`
at priority 200). The laptop built and pushed the images. `mage
k8sAcceptance` with `IORMESH_K8S_KUBECONFIG` and
`IORMESH_K8S_CRICTL='ssh root@iom1 k3s crictl'`, once per
kube-proxy mode (`el9-k3s.sh proxy nftables` between). **All
green:**

| Test, subtest | iptables | nftables |
|---|---|---|
| `TestKindAcceptance` | PASS (178 s) | PASS (138 s) |
| `clusterip`, `hairpin`, `ingress`, `rbac` | PASS | PASS |
| `identity` | PASS: 5 pods, 6 process ids, one `boot_id` | PASS, the same |
| `sigterm` | PASS: exit 0, 754 ms | PASS: exit 0, 848 ms |
| `TestKindMicroservices` | PASS (123 s) | PASS (124 s) |
| `mesh` | PASS: 4 hosts, 4 sessions, `tcp 4 fused, 0 unresolved` | PASS, the same |
| `pod_fields` | PASS | PASS |
| `identity` | PASS: 4 pods, one `boot_id` | PASS |
| `TestKindClusterIP` | PASS (94 s) | PASS (95 s) |

The raw `/var/log/audit/audit.log` and its four rotations, read
after each pass (they rotate every few minutes under the BPF
audit records, so each read covered its whole pass), held no
`AVC`, `USER_AVC` or `SELINUX_ERR`.

**Real cgroup files.** During both passes a sampler on the node
read, every 3 s, `/proc/<pid>/cgroup` of each process under
`kubepods` as the node sees it and, for each `iormesh` tracer,
of every process of its pod through `nsenter -C -t <tracer>`
(the tracer's private cgroup namespace), mapping ids to
containers and sandboxes with `crictl ps -a` and `crictl pods`.
They replace task sh4's constructed k3s shapes in
`internal/k8s/container_test.go` (`TestContainerOfShapes`,
`TestParseProcCgroupK3sCaptures`):

| Pod (namespace `iormesh-m7`) | Captured |
|---|---|
| `inventory`, burstable, sidecar tracer (`shareProcessNamespace`) | node view of the tracer, the app (its `sleep` and the JVM) and the sandbox's `pause`; the tracer's view of the app and of `pause` (`0::/../cri-containerd-<id>.scope`) and of itself (`0::/`) |
| `gateway`, best-effort, debug tracer (`kubectl debug --target app`) | node view of the tracer, the app and `pause`; the tracer's view of the app |
| `inventory` again (nftables pass) | the app's view of itself (`0::/`) and of the tracer (`0::/../cri-containerd-<tracer>.scope`) |

Every shape is the systemd driver's
`/kubepods.slice/kubepods-<qos>.slice/kubepods-<qos>-pod<uid_>.slice/cri-containerd-<id>.scope`,
the one kind showed without kind's `kubelet` root: the parser
read each one as the constructed fixtures expected, and needed
no change. Two things the constructed set had wrong. No process
sat in the pod slice itself (cgroup v2 keeps processes out of
an inner cgroup): the sandbox's `pause` has a scope of its own,
so `ContainerOf` names the sandbox id for it, an id no container
status lists — a sidecar tracer sees `pause` as pid 1, but
`pause` does no traced I/O. And `pause` shares the node's
cgroup namespace, so from inside it the full path shows. k3s
on el9 runs the systemd driver; the cgroupfs cases stay as
paths built from the formats.

After the run `el9-k3s.sh down` (state file present) removed
k3s, the two SELinux packages it added, the repository and the
two trusted firewalld sources: `rpm -qa` matched the 372
packages before, `/opt/iormesh` matched (paths, sizes, mtimes,
modes), the sampler and its files were removed. The laptop's
`docker images -a`, volumes, containers and networks matched
the state before; Docker was stopped, the tunnel closed, the
kubeconfig deleted. iom1 was stopped and its config restored;
`doas vm list` on f3 afterwards:

```
iom1       default    uefi       2    2G      -               No       Stopped
iom2       default    uefi       2    2G      -               No       Stopped
iom3       default    uefi       2    2G      -               No       Stopped
```

## Performance baselines

S3 (decisions.md, decision #25) measures overhead against a load the
workload can state in advance: `syscall-burst`, a tight loop of
1-byte `write` + `read` pairs on per-thread temp files — the
most syscalls per unit of real work, so the tracer's per-event
cost is at its largest. Run it standalone:

```sh
mage workloadBuild
java -jar bin/workload.jar syscall-burst --ops 1000000 --threads 4
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

## Independent parquet readers (task ee4)

The events segment file has its own encoder, so `mage test` alone
proves only that `parquet-go` reads it. `TestIndependentReaders`
(`internal/segment/indep_test.go`) also reads it with pyarrow and
DuckDB — every value against the NDJSON of the same events, and
DuckDB's `IS NULL` on every nullable column — when
`IORMESH_PARQUET_CHECK` names a Python venv holding both:

```sh
python3 -m venv /tmp/pq && /tmp/pq/bin/pip install pyarrow duckdb
IORMESH_PARQUET_CHECK=/tmp/pq mage test
```

Without it the test skips: neither reader is a build dependency.
Run it after any change to `internal/segment`'s events encoder.
