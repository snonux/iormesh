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
| `TestParquetSegments` | non-java-peer with `-native-stacks` and `-trace-dir` beside `-out` (M4, task 8b4): no active file is left, every file in `ready/` is 0644, the events segments in name order hold exactly the NDJSON events and the union of the stacks tables exactly `stacks.ndjson` |
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
| `TestMicroservices` | microservices golden under the agent, the four services released leaves first on free ports in one `--dir`: every hop one fused connection with an `http` event on each end (`https` on the TLS hops) pairing by `(conn_id, req_seq)`; each server's request handled by `HttpServe#handle`, each call made by `HttpFetch#call`, orders' calls on its dispatcher thread inside the gateway's request; config, certificate and record I/O with their causing methods (`ServiceConfig#provision`/`#load`, `ServiceNode#publish`/`#readCert`/`#withdraw`/`#retire`, `DataStore#save`); four rings; mutated goldens fail |

The golden was written from strace runs of the four processes
(JDK 25). The agent-side fields are reasoned from the M3 goldens
and still need a traced run to confirm (see the task's notes).

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
