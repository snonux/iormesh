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
root-owned directory for it.

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
on two real hosts, which only the owner can make. **Status: not
run on real hosts yet** — nothing below counts as accepted until
the owner's run prints every criterion PASS.

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

- A signed build for each host's arch in `dist/`:

  ```sh
  mage buildPodman      # or buildDocker; dist/iormesh-linux-*
  IORMESH_MINISIGN_KEY=~/.minisign/iormesh.key mage sign
  ```

Each host (two machines, Linux, x86_64 or aarch64):

- Kernel with BTF (`/sys/kernel/btf/vmlinux`) and the classic
  `raw_syscalls` tracepoints. 6.2 or newer is needed for the
  clock bound (M4-10: `tcp_seq` reads use `bpf_rdonly_cast`).
  The Java mark uprobes need 6.10 (or 6.6–6.9 with commit
  46ba0e49), though this run loads no agent. The suite has only
  run on Linux 7.2, where the dispatcher findings (PLAN.md,
  "Syscall handlers run behind `raw_syscalls` dispatchers") were
  measured; Rocky 9's 5.14, the first target platform, has never
  run the tracer, so a 7.x kernel is the safe choice.
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
| M4-0 | — preflight | ssh `pwd`; arch, kernel, machine ids (distinct, neither the controller's); `test -f` of the binary and key; `sudo -n -l /opt/iormesh/bin/iormesh`; the tools; no unit or trace; signed build per arch; local rsync; jar pushed | PASS, arch and kernel per host printed |
| M4-1 | `add` → `deploy`, zero manual steps on the servers | `add -project P -pattern 'java -jar */.iormesh-m4-run/workload.jar <scenario> --role <role> *' HOST -duration 1h` per role, `deploy HOST1 HOST2`, then `status -json` per project | every project `running` |
| M4-2 | streaming collect | per round: `file-basic` on HOST1, 65 s, list every `ready/`, `collect`, list again | every listed file gone from the server and in the store; the project dir holds only `ready/`, `state.json`, `.lock` and `.active-*`; ≥ 1 file moved |
| M4-3 | `collect` alone changes nothing but the local store | hash every file of the work dir outside the store before and after each `collect` | unchanged |
| M4-4 | `refresh` produces the merged graph export | `refresh` (no host, no project) | exit 0; every project and both hosts in the model; no damaged segment |
| M4-5 | merged mesh equals the single-host result | `build -project m4single`, `query -project m4single`, tcp-loopback's golden (`internal/normalize`) on the rebuilt graph and events | no violation (the rig checks graph equality on every run) |
| M4-6 | M0 client and server as two projects fuse | `build` of m4shop+m4billing (`tcp-two-host`, client HOST1, server HOST2); `build` of m4shop alone | one fused cross-host connection, none unresolved; alone, the client's end unresolved |
| M4-7 | NAT'ed pair fuses on different 4-tuples | `tcp-nat` through the DNAT; `build` m4natc+m4nats; `query -conn` | one fused connection; client dialed the NAT port, server accepted on base+3 |
| M4-8 | microservices: every REST edge fused, plain and TLS | the four services on HOST1, one project each, read from the refresh's model | orders and billing `tls`, inventory `plain` (2 conns), all fused |
| M4-9 | microservices spread over both hosts (task 1f4) | gateway and inventory (`--bind ADDR1`, base+15) on HOST1, orders (base+16) and billing (base+14) on HOST2 (`--bind ADDR2`), one project each (m4sgw, m4sinv, m4sord, m4sbill), every pattern ending in its `--dir` `microservices-spread`, so M4-8's sessions never trace them; orders' public certificate copied from HOST2 to HOST1 over ssh (`base64`); `build` of the four projects | two fused cross-host connections (gateway→orders TLS, orders→inventory plain), none unresolved; orders and billing `tls`, inventory `plain` (2 conns), all fused |
| M4-10 | clock bound | the m4shop+m4billing model's clock windows | non-empty and two-sided, widths printed (real clocks; the injected skew is `TestClockSkewRecovered`'s) |
| M4-11 | `build` with the network down is byte-identical | both outputs deleted, then the same build rerun under `unshare -rn` (else with a `PATH` lacking ssh and rsync) | `graph.json` and `model.json` byte-equal |
| M4-12 | naming two services shows them by name | `services name -port 18202 ADDR2 m4-billing-svc`, `services name -port 18213 127.0.0.1 m4-orders-svc`, `build`, `services` | both names in the model's services and edges and in the listing |
| M4-13 | disk watchdog | `add` m4disk on HOST2 with `-disk-limit 0 -disk-min-free <free − 64 MiB>`, `deploy -project m4disk`, `fallocate` 128 MiB, wait | `ended`/`disk_limit`; collected segment intact; `show` names `disk_limit` |
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

The owner's run is what remains: the prerequisites above, then
`mage m4Acceptance HOST1 HOST2`, all criteria PASS.

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
seccomp and AppArmor defaults, PID namespaces (PLAN.md #13 — the
tracer targets host pids today), and a non-root tracer uid.

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
