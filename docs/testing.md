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
  a test-only knob (`IORMESH_TEST_CLOCK_SKEW`, below) injects a
  clock offset per tracer, and the merge must recover it; NAT is
  emulated with a DNAT hop.
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
  `StartHelper` gates a Go helper the same way: `edgepeer`
  (`workload/edgepeer`, cgo-free, built by `mage workloadBuild`)
  forces the edge cases a JVM cannot force deterministically —
  PID reuse and socket races — and prints a facts line per
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
  counts (PLAN.md, "Syscall handlers run behind `raw_syscalls`
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

`mage integrationTest`: 27 tests (25 at acceptance, plus the PID
reuse and socket race tests of task id4), two consecutive full
runs green on HEAD, no skips, zero lost records. PLAN.md's M2
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
(PLAN.md scenario catalogue, M3) landed before the Java agent
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
| `TestHTTPRedaction` | http-redaction golden: the query keeps its names with every value masked, the planted `Authorization`, `X-Api-Token`, `Cookie` and `Set-Cookie` headers are recorded as `name: ***`; no planted secret in `events.ndjson`, `graph.json` or the Java tables; a golden forbidding the masked header fails |
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
| `TestJavaAttachMountNS` | `run -agent` on http-plain's pair (the server as nobody through `setpriv`, the client as root), each JVM in its own mount namespace with a private tmpfs `/tmp` (`unshare --mount`, `Harness.JVMWrap`; task cf4, decision #14): the tracer stages the jar and spool inside each JVM's root; both loads reported, two rings read, every request on its connection, the client's `http` events carry their host, the http-plain golden holds; no ring in the tracer's own spool, and no `iormesh-*` stage left in either JVM's `/tmp` (held open past the JVM's exit) |
| `TestJavaAttachDynamicLoading` | no tracer, per JDK (the `java` on PATH plus each JAVA_HOME in `IORMESH_ATTACH_JDKS`): a default JVM loads and warns, `-XX:+EnableDynamicAgentLoading` loads silently, `-XX:-EnableDynamicAgentLoading` refuses with `dynamic agent loading is disabled` and no ring, the JVM finishing cleanly every time; and a root attacher loads into a JVM running as nobody, whose ring is then nobody's |

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

`microservices` (PLAN.md scenario notes, "Microservices
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
| `TestMicroservicesAcrossHosts` | the services on three emulated hosts (task 1f4), each traced as its own project under the agent: each host's trace holds only its own processes, three distinct host ids; the merge fuses exactly the three cross-host hops, each across two projects with one 4-tuple mirrored, and leaves nothing unresolved; the merged trace satisfies `expect-spread.json` and its requests pair; a service on the wrong host, a hop declared at loopback and the cross-host TLS hop declared plain each fail it |

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

`tcp-two-host` and `tcp-nat` (PLAN.md scenario notes, "Two-host
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

The injected clock skew of PLAN.md's M4 acceptance (task ye4)
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
PLAN.md's M6 acceptance on the rig: the spread of
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

`TestViewsAcceptance` (`integrationtests/m5*_test.go`) is PLAN.md's
M5 acceptance on the rig. It runs the spread of
`TestMicroservicesAcrossHosts`, copies each host's ready segments
into `<store>/<project>/<host>/` as collect leaves them, names
the three servers (`iormeshctl services name`) and builds with
the real `../iormeshctl` (`mage build`), as root, HOME in a
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
acceptance itself (PLAN.md M4, "Acceptance") also needs one run
on two real hosts. **Status (2026-11-21): run five times on two
Rocky 9.9 VMs, not accepted** — the two kf4 runs pass 14 of 15
criteria each, but not the same 14: the fourth fails the clock
bound (before jf4), the fifth the spread M4-9 (skipped runs,
qf4) (see "Run on Rocky 9.9" below);
nothing counts as accepted until a run prints every criterion
PASS.

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
  kf4). The dispatcher findings (PLAN.md, "Syscall handlers run
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
  operator's own sudo), with `ops` standing for the SSH user
  and `ARCH` for the host's `amd64` or `arm64`. Verify the build
  against the pinned key before it leaves the controller — the
  first install has no `upgrade` to check it:

  ```sh
  # on the controller (./iormeshctl from `mage build`, or
  # CGO_ENABLED=0 go build -o iormeshctl ./cmd/iormeshctl; or instead
  # minisign -Vm dist/iormesh-linux-ARCH -p ~/.config/iormesh/minisign.pub)
  ./iormeshctl verify dist/iormesh-linux-ARCH
  scp dist/iormesh-linux-ARCH ~/.config/iormesh/minisign.pub HOST:
  # on HOST
  sudo install -d -o root -g root -m 0755 /opt/iormesh \
       /opt/iormesh/bin /opt/iormesh/etc
  sudo install -o root -g root -m 0755 iormesh-linux-ARCH \
       /opt/iormesh/bin/iormesh
  sudo install -o root -g root -m 0644 minisign.pub \
       /opt/iormesh/etc/minisign.pub
  rm iormesh-linux-ARCH minisign.pub
  # the rule: written to a temp file, checked, then installed, so a
  # typo never reaches /etc/sudoers.d (a broken file there breaks sudo)
  t=$(mktemp)
  echo 'ops ALL=(root) NOPASSWD: /opt/iormesh/bin/iormesh' > "$t"
  visudo -cf "$t" &&
       sudo install -o root -g root -m 0440 "$t" /etc/sudoers.d/iormesh
  rm "$t"
  ```

  `/opt` itself must be root-owned and not writable by others
  (`upgrade` checks the layout; [security.md](security.md)). A
  host that will not be bootstrapped again after the run should
  lose the rule too: `sudo rm /etc/sudoers.d/iormesh` (the run's
  `rm` removes `/opt/iormesh`, not the rule).
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
^C kills the run outright. If the teardown did not finish, clean
up by hand — with an iormeshctl built as above, and the
operator's XDG variables cleared so it reads the run's registry:

```sh
env -u XDG_CONFIG_HOME -u XDG_DATA_HOME HOME=$HOME/.iormesh-m4 \
    ./iormeshctl rm HOST                   # per host
ssh HOST pkill -f .iormesh-m4-run/workload.jar
ssh HOST rm -rf .iormesh-m4-run            # HOST2 also: rm -f /var/tmp/.iormesh-m4-fill
```

Every session also ends by itself after its `-duration 1h`.

Send back the results block and the work directory's path.

### What the run checks

| Id | Criterion (PLAN.md M4) | How | Expected |
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
  (PLAN.md #28, provisional), the microservices projects carry
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

Also found on el9, outside M4's sequence: a session dies with
"schema: invalid: fd_gen 1 without an fd" when the pattern also
matches a `su` that runs the JVM (task mf4).

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
  and args at 8.
- **`TestDispatchSeesSeveralKinds`** traces an open/read/close
  loop and requires more than one syscall kind, and all three
  enters. The el9 bug sent nearly every call to the read
  handler.

The BTF is
`internal/testdata/btf/rocky9.9-5.14.0-741.22.1.el9_9.x86_64.min.btf`
(11 KB), trimmed to what the object's CO-RE relocations name. It
was extracted from the Rocky kernel-core RPM; a trim of iom1's
live BTF is byte-identical. To regenerate it after `mage
bpfBuild` (needed when the four programs gain relocations
against types the file lacks):

```sh
rpm2cpio kernel-core-5.14.0-741.22.1.el9_9.x86_64.rpm | cpio -idm
scripts/extract-vmlinux lib/modules/*/vmlinuz > vmlinux  # kernel tree
bpftool gen min_core_btf vmlinux el9.min.btf internal/c/iormesh.bpf.o
# or: ssh root@iom1 cat /sys/kernel/btf/vmlinux > el9.btf
```

The nf4 review changed `TestProcessExitGroupDead` in the same
change-set. It waits for the record it needs, then for a 200 ms
quiet period, instead of a fixed second. Where
`sched_process_exit` has `group_dead`, read from the format file,
it requires exactly one `group_dead` record; the el9 fallback
keeps "at least one". The child locks its main thread in
`init()`. `TestJavaMarkNamesTheNative` reports a missing
off-launcher-thread mark in an error of its own, which names
commit 46ba0e49.

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

PLAN.md M5's eval is no part of `mage test` — it needs a model —
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
every schema change (PLAN.md M5).

## K8s capability set (task ub4)

PLAN.md decision #13 (provisional) names the capabilities the
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
(PLAN.md #13, docs/implementation.md M7 step 1). Checked on the
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

The controller's kubectl transport (`internal/k8s`, PLAN.md #12,
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
  stream is refused part-way; `TestReadyRoundTrip`
  (internal/cli) runs `iormesh ready` and `-remove` around
  `Unpack`.

What only kind can show is checked by task yb4's run
([below](#m7-acceptance-on-kind-task-yb4)).

## K8s controller wiring (task af4)

iormeshctl's pod path (PLAN.md #18's "Wired" note;
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
  stranded, with no exec tried (mutation-checked); an `-context`
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

## K8s manifests and pod identity (task vb4)

Unit tests in `mage test`, no cluster and no container engine
(PLAN.md M7; docs/implementation.md M7 steps 5, 6 and 8):

- `internal/k8s/manifest_test.go`: `deploy/` must equal what
  `mage k8sManifests` writes (golden; edit the generator, never
  the files). Each file is then decoded as untyped YAML or JSON,
  the way the API server reads it, and the fields asserted:
  `hostUsers: true` in both patches, `shareProcessNamespace:
  true` only in the sidecar's; tracefs as a read-only `hostPath`
  `Directory` and the trace dir as an `emptyDir` with a
  `sizeLimit`; uid 0, `allowPrivilegeEscalation: false`, no
  `privileged`, `RuntimeDefault` seccomp, `drop: [ALL]` and
  exactly #13's seven capabilities, in order; the downward-API
  env (`POD_NAME`, `POD_NAMESPACE`, `POD_UID`); the sidecar's
  image pinned by digest, its args ending in the ¾ byte cap and
  its small requests; and no field in the `--custom` profile
  that kubectl debug's own flags set. `Validate` refuses a tag,
  a malformed digest, an odd size and a command line that is
  not `run`.
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

Unit tests in `mage test`, no cluster (PLAN.md M7 "Service-NAT
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
  1024 endpoints.
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

PLAN.md M7's acceptance runs on one local kind cluster:
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
   kubeconfig;
4. starts a local registry (`registry:2`, pinned) on
   `localhost:5001` with a named volume `iormesh-registry-data`
   (a stopped container of that name is removed first), creates
   the cluster `iormesh-m7` without `--kubeconfig` — root never
   writes a path the operator chose — and writes `kind get
   kubeconfig`'s output as the operator to `.kind-kubeconfig` at
   the repository root (or `$IORMESH_KIND_KUBECONFIG`; 0600,
   ignored by git), then wires the node's containerd to the
   registry; its kind and kube-proxy configs are `os.CreateTemp`
   files;
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
7. runs `TestKindAcceptance` with kube-proxy in iptables mode,
   switches kube-proxy to nftables and runs `TestKindClusterIP`;
8. tears down exactly what it made (`internal/kindrun`
   `TeardownPlan`): the cluster, the registry container with its
   volumes and the named volume, the run's two tags, any image
   left with the run's label (`docker image prune --all
   --filter=label=org.iormesh.acceptance=m7`), the base images
   the run pulled (by ID, against the recorded state; with no
   state none), the kubeconfig and the state file.
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

Checked on 2026-11-21 with a real run over a daemon holding
unrelated images: `docker images -a` was identical before and
after (the three pulled base images removed by ID, the operator's
eight tags untouched), no volume or container left, no kind
cluster, no kubeconfig or state file.

Ctrl-C or SIGTERM during the run starts the teardown as `mage
k8sTeardown` in a session of its own (log in `k8s-teardown.log`),
which outlives mage's 5-second grace for cancelled targets and the
terminal's process-group SIGINT; if the daemon or the binary is
gone by then, run `mage k8sTeardown` by hand — the recovery step
for any interrupted run. `internal/kindrun`'s tests drive the
plans with a fake runner recording every argv: teardown names only
the run's objects (no `system prune`, no prune without the label
filter), removes base images only against recorded state, keeps
the first run's state across a kept cluster's rerun, replaces a
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
deploy without a byte cap: the 90 %
`-disk-limit` stopped every debug tracer on a node fuller than
that, so the test passed `-disk-limit=0 -disk-max-bytes=1536MiB`;
built since: deploy passes the pod's bounds itself, the tracer
spec check asserts them, and the registry carries only the
gateway's agent flags), ef4 (deploy returned before the tracer was attached; the test
waited for its `attached` log line; built since: deploy waits for
the tracer's attach state and the test only checks deploy's
`attached, session` line — the sidecar's own start, which no
deploy makes, is still awaited by its log line), ff4 (a route edge never named a
sole endpoint's pod; built since: a traced sole endpoint is named,
and the `ingress` check expects the inventory pod at the end of
the routed edge, in the summary and `model.json`).

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
