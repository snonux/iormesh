# M3 steps 8–10: OTEL, scenarios, live attach

Part of [M3 — Java agent](../m3.md), in [Implementation
guide](../../implementation.md).

8. OTEL (done, task 4b4): read `Span.current().getSpanContext()`
   reflectively in the app's class loader at `enter` and add the
   ids to the bracket. No OTEL dependency in the agent. The `otel`
   scenario (task za4, below) is the target: run it as `java -jar
   workload-otel.jar otel --role server|client` (`mage
   workloadOtelBuild`). Its golden (`expect-pending.json` until
   this step promoted it to `expect.json`) puts an
   `otel` object (`trace_id`, `span_id`, the declared ids of
   `OtelScenario`) on every event inside a span: each client
   event of a call (connect, write, read, the last call's
   close — `HttpURLConnection` does them on the calling
   thread), and each server reply write and http event (the
   span is current from the start of `HttpServe#handle` until
   after `ex.close()`). The server's accept and request reads
   happen before `handle`, outside any span, and declare no
   ids. The client's http events assume an
   `HttpURLConnection` hook (decision #22's v1 set) that
   records the response inside `OtelFetch#call` once
   `getResponseCode` returns — built by step 7.

   As built:
   - **When to read**: only once the app has initialized OTEL's
     context storage. `OtelTransformer` rewrites the static
     initializer of `io.opentelemetry.context.LazyStorage` (any
     app class loader) to end with `Hooks.otel(LazyStorage.class)`
     inside a catch-all — a loader that cannot see the bridge
     gets a linkage error there, which must not fail OTEL's
     initializer. `Otel.arm` then resolves `Span` and
     `SpanContext` in that loader through the public lookup and
     reads once, so the classes a read touches load inside the
     app's own OTEL start. The agent never initializes OTEL
     itself: `LazyStorage`'s initializer runs a ServiceLoader
     (jar I/O, jar locks) and freezes the storage wrappers, so
     doing it from a hooked I/O call could deadlock or change
     the app's OTEL. A live attach arms each `LazyStorage`
     already loaded — one caught between loading and
     initializing (OTEL loads it only to call into it, so a
     window, not a state) is then initialized on the attaching
     thread. Arming locks only to dedupe and publish, never
     across the resolve and first read: on attach that read may
     wait for an app thread's `LazyStorage` initializer, which
     itself ends in arming (review of 4b4; `OtelAttachRaceTest`
     forces that order). Up to eight copies (class loaders) are
     read, a ninth is refused and counted as a fail; readers are
     held weakly — a `ClassValue` on `LazyStorage` keeps a
     reader alive exactly as long as its class, so the agent
     pins no undeployed web app.
   - **Hot path**: one volatile read without OTEL; with it, one
     handle call (`Span.current().getSpanContext()` composed),
     after the stack walk and before the enter stamp (outside the
     bracket's window). The span context is compared by identity
     with the thread's last one (held weakly), so `getTraceId`,
     `getSpanId` and the hex parse run once per span and thread.
     Ids are numbers in the ring: trace id as two longs, span id
     as one, all zero for no span. Cost (an `enter`/`exit` loop
     without ring or stack walk, JDK 25, a loaded host): ≈90 ns
     per bracket without OTEL, ≈145–155 ns armed, in a span or
     not — some 55–65 ns, against the ≈3 µs stack walk every
     bracket pays (S1: ≈8 µs), so inside decision #25's budget.
   - **Ring v4**: a BRACKET inside a span is 72 bytes instead of
     48 (the three id words appended), so brackets of an app
     without OTEL cost what they did; HTTP's fixed part gains the
     three words (zeros for none). v3 was never released; the
     bump makes a mismatched agent and tracer fail loudly.
   - **Join**: a syscall joined to a bracket, and an http event
     with a stack, get `otel` (`trace_id` 32, `span_id` 16
     lowercase hex digits) when the record had a span;
     `schema.Event.Validate` allows `otel` with attribution
     java only.

   Tests: `mage agentTest` — `OtelTest` (bridge: hex ids, an
   armed copy's span read and closed, nested spans, an OTEL
   without its API jar arms nothing and throws nothing, BRACKET
   and HTTP records carry the ids, a throwing reader is no span
   and drops the thread's cache, a ninth copy is refused and
   counted), `OtelTransformerTest` (the real `LazyStorage`
   rewritten, verified and run: armed at the first context use,
   not at class load; a loader without the bridge in sight
   keeps a working OTEL) and `OtelAttachRaceTest` (an attach
   arming a copy whose initializer runs on an app thread: both
   finish, one copy armed — it hung before the fix), all on the
   workload's vendored OTEL 1.67.0 jars in loaders of their own;
   Go — `internal/agentspool` (long BRACKET, HTTP ids, half-set
   ids malformed), `internal/correlate` (`javaotel_test.go`),
   `internal/schema` (`TestOtelRoundTrip`, rejects),
   `internal/normalize` (`otel_test.go`); integration —
   `TestOtelHTTP` against the promoted golden, with mutated
   spans caught, and `TestHTTPPlain` asserts no `otel` without
   OTEL. Not yet: an OTEL distribution whose context storage
   never initializes `LazyStorage` is not seen (untested with
   the OpenTelemetry Java agent).
9. Scenarios first, as always: `method-flow`,
   `http-plain`, `http-tls`, `http-redaction`, `otel`.
   Done for the first four (task ya4), ahead of steps 5–7, and
   for `otel` (task za4), ahead of step 8. `method-flow`
   (solo, `--dir`) issues
   each I/O op from its own method — `MethodFlow$Journal#create`,
   `#append`, `#seal` on `main` at recursion depth 24,
   `MethodFlow$Reader#readBack` on `method-flow-worker`,
   `MethodFlow$Recorder#record` on `method-flow-sink`,
   `MethodFlow$Sweeper#sweep` back on `main` — so each declared
   op names its causing method. The HTTP scenarios run
   `HttpFetch#call` (JDK HttpClient, HTTP/1.1) against
   `HttpServe#handle` (JDK HttpServer, handler on the dispatcher
   thread) on 127.0.0.1: GET, POST and a 404 for `http-plain`
   and `http-tls`, two calls carrying planted secrets for
   `http-redaction`. `http-tls`'s certificate is an EC P-256
   self-signed one made in memory (`SelfSignedCert`, with a
   small DER encoder, `Der`, since the JDK has no public
   certificate builder); the public half goes to the client
   through `<dir>/http-tls.crt`. Each golden is split: the
   loadable `expect.json`, checked against real traced runs
   (JDK 25), and `expect-pending.json` with the `attribution`,
   `java` and `http` declarations (format below and in
   [testing.md](../../testing/integration/m3.md#m3-scenarios-ahead-of-the-agent)).
   Steps 5 and 7 add those fields to `normalize.ExpectedEvent`,
   then replace each `expect.json` with its pending file and add
   the integration tests. Step 5 did its part (task 2b4):
   `ExpectedEvent.java` (`thread`, `method`) loads, and the rule
   is settled as the goldens assumed — `java.method` is the
   innermost frame outside the JDK's own packages (`java.`,
   `javax.`, `jdk.`, `sun.`, `com.sun.`), as `class#method`
   (`normalize.CausingMethod`); `TestJavaMethodFlow` checks
   `method-flow`'s pending file directly. Step 7 (task 3b4) added
   `ExpectedHTTP` and promoted `method-flow`'s and the HTTP
   scenarios' pending files to `expect.json`; step 8 (task 4b4)
   promoted `otel`'s, the last one.
10. Live attach (done, task ge4; decision #2). Until then the
    agent's `agentmain` existed but nothing loaded it: S1 used a
    throwaway `Attach.java`. `internal/javaattach` speaks
    HotSpot's attach protocol from Go — no JDK on the tracer
    host, every step under a context deadline. It opens the
    JVM's cwd and its root's `/tmp` as directory fds (`/tmp` by
    `openat2` with `RESOLVE_IN_ROOT|RESOLVE_NO_SYMLINKS`) and
    works relative to them only; if `.java_pid<nspid>` is
    missing there, it creates `.attach_pid<nspid>` in the cwd
    (else `/tmp`; `O_EXCL|O_NOFOLLOW`, an existing one accepted
    only as a regular file of root or the JVM's user), sends one
    SIGQUIT through a pidfd and polls for the socket; checks it
    is a socket, connects through `/proc/self/fd/<tmp>/…` and
    refuses unless `SO_PEERCRED` names the target's pid and euid
    (the security review's confused-deputy fixes); sends protocol
    1's `load instrument false <jar>=spool=<dir>` and judges the
    reply (status line, then `return code: 0`, or the JVM's
    message — `Dynamic agent loading is not enabled` becomes
    `ErrDynamicLoadingDisabled`). SIGQUIT kills what does not
    handle it, so a process without `libjvm.so` mapped, without
    SIGQUIT in `SigCgt` (a JVM still starting), or with
    `-XX:+DisableAttachMechanism` in its command line or
    `JAVA_TOOL_OPTIONS`/`JDK_JAVA_OPTIONS` is refused unsignalled
    (`-Xrs` JVMs start their listener at startup and attach
    without a signal). `internal/cli/attach.go` adds `iormesh
    attach -pid -agent -spool [-timeout]` and `run -agent JAR`
    (needs `-spool`): the latter loads into the session's
    starting processes from a goroutine while the main one keeps
    handling records, prints one line per pid, then polls the
    spool so the new rings are marked before `-ready`. A load
    the JVM confirmed but that left no `<spool>/<pid>[.<hex>].ring` (the
    agent disabled itself; checked only for a JVM in the
    tracer's mount namespace) is reported as such. The tracer
    now prints `java agent of pid N joined, K natives probed`
    when it marks a JVM — what `iormesh attach`'s user and
    `TestJavaLiveAttach` wait for. It marks the JVM only once a
    record after the hook table (warmUp's watermark) shows the
    table read whole: probing at the first HOOK of a drain that
    raced the agent's writes left later hooks' natives unprobed
    (task tf4). Scenario `live-attach` and
    the three tests: [testing.md](../../testing/integration/m3.md#live-attach-task-ge4);
    user-facing caveats: [java-attach.md](../../java-attach.md).
    Checked on 17.0.19 (by hand), 21.0.12, 25.0.4 and 27-ea+35
    (`IORMESH_ATTACH_JDKS`). Across mount namespaces: M7, #14
    (task cf4). Late attach (task lh4, `internal/cli/
    lateattach.go`): `session.handle` (a traced process's exec),
    `judgeExec` (an adopted pattern match) and `adoptOwner` (a
    restarted port owner) hand the pid to a `lateAttacher`,
    which tries it on a goroutine of its own every 250 ms for
    30 s (5 s while it maps no `libjvm.so`), through the
    start-time `loadAgent` and stages, at most 2 handshakes at a
    time and 64 pids pending (each overflow named once, `full`
    cleared when a slot frees); the trace loop's `poll` collects
    its lines and new stages, and `run` stops it (cancel, wait,
    collect) before `finish` closes the Java join. Unit tests in
    `lateattach_test.go`, end to end `TestJavaLateAttach`.

**Done when**: milestones.md's M3 acceptance holds.
