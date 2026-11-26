# M3 step 7: HTTP hooks

Part of [M3 — Java agent](../m3.md), in [Implementation
guide](../../implementation.md).

7. HTTP hooks: one transformer per library, each emitting an
   `http` event at request completion with `conn_id` (via the
   socket's fd) and `req_seq` (per-connection counter). Start
   with the JDK's `HttpClient` and built-in `HttpServer` — the
   pair the workload uses — then widen per decision #22.
   Headers pass through `redact.Policy` *in the agent*, before
   the spool.
   The workload's pair landed with step 9's scenarios: the
   `http` events their `expect-pending.json` declares (one per
   request, after the I/O that completes it; `req_seq` 1, 2, 3
   on both ends of one connection; query values masked) are
   this step's target. They assume the server's event is
   recorded inside `HttpServe#handle` when the exchange closes
   (`ex.close()`, after the reply is written) and the client's
   when the response returns inside `HttpFetch#call`.

   Done (task 3b4) for the JDK's three, as built:

   ```
   agent/core/src/iormesh/agent/HttpHooks.java       # libraries → ids, probes
   agent/core/src/iormesh/agent/HttpTransformer.java # exit-only rewrite
   agent/core/src/iormesh/agent/http/Library.java    # one class per library:
   agent/core/src/iormesh/agent/http/JdkHttpServer.java
   agent/core/src/iormesh/agent/http/JdkHttpClient.java
   agent/core/src/iormesh/agent/http/JdkUrlConnection.java
   agent/core/src/iormesh/agent/http/Redaction.java  # the agent's policy copy
   internal/agentspool/http.go                       # HTTP record (ring v3)
   internal/correlate/javahttp.go                    # http events
   ```

   - **One library per class, one rewriter.** A `Library` lists
     its `Target`s — (class, method, exact descriptor, which
     argument to pass) — each with a probe, and the packages its
     probes reflect on. `HttpTransformer` rewrites every target
     the same way: before each return, and in a catch-all for a
     method (a constructor only at its returns), it calls
     `Hooks.http(ret, this, arg, id)`. Exit only: an http event
     is stamped when the request is done and joins no syscall.
     Platform-loaded classes are candidates (java.net.http and
     jdk.httpserver live there); each library's module is made to
     read the bridge and to open its probes' packages to the
     agent (`Instrumentation.redefineModule`). A library whose
     module or internals are missing is skipped with a note; a
     JDK generation that changed a signature gets a second row
     (`parseHTTP` lost its `ProgressSource` after 17), reported
     `alternative` in the debug dump.
   - **Hooks.http** hands the three objects to the id's probe
     (a `BiFunction` of JDK types — the core is on another
     loader), which returns the request's strings, already
     redacted, or null (a binding hook, a request already
     recorded). Only then does it walk the stack and write an
     HTTP record; the thread is busy throughout, and marked
     closing from before its stamp until the record is published,
     so the watermark promise covers HTTP records like brackets.
   - **HttpServer**: `HttpExchangeImpl.close`/`HttpsExchangeImpl
     .close` (the handler's explicit close — the golden's
     `HttpServe#handle`) and `ServerImpl$Exchange.run` (the
     handler returned; one that ended its exchange by closing the
     body alone is recorded there, with the JDK's stack); first
     wins per `ExchangeImpl`, whose package-private getters the
     probe calls by reflection.
   - **HttpClient**: `HttpClientImpl.send`'s return, on the
     caller's thread (`HttpFetch#call`). The response does not
     know its connection; `HttpResponseImpl`'s constructor gets
     the JDK's `Response`, whose `localAddress` a binding hook
     ties to the response. `sendAsync` (task wd4): the public
     three-argument overload (the two-argument one and the facade
     reach it, `send` does not) is hooked; its probe takes the
     caller's identity — stack id (walked past the probe's own
     frames, `StackIds.belowAgent`), thread, kernel tid, OTEL span —
     through `Hooks.httpCaller()` and registers a completion on the
     returned future, which hands it with a describer to
     `Hooks.httpLate()`: the describer runs like a probe (busy
     thread, meta) on the completing thread and the record goes out
     under the caller's identity. A future already done at the
     return, or completing inline while the completion is
     registered (same thread), is recorded right there; one that
     fails records nothing; a completion that lands on a busy thread
     counts as a lost HTTP record (numbering stops, never shifts).
     Review of wd4: the future runs dependents last-registered
     first, so the agent's completion runs after the app's callbacks
     and `get()`'s waiter — its stamp is not the response's arrival.
     Every `sendAsync` record is therefore flagged
     `HTTP_UNORDERED` (flags bit 2) with no start: the joiner gives
     it no `req_seq` and stops numbering its connection's client
     end. Review round 2: that is not enough — the connection goes
     back to the pool when the async body is read, so a sync `send`
     reusing it can be recorded before the async record and take its
     ordinal. `sendAsync` is therefore also `marked()`: its entry
     calls `Hooks.httpMark`, which writes an HTTP record flagged
     `HTTP_ASYNC_MARK` (bit 4, empty strings, under the watermark
     promise) before the request can reach a connection, and every
     java.net.http record carries `HTTP_NET_HTTP` (bit 8; ring
     format 6). The joiner keeps the JVM's earliest marker stamp and
     numbers no java.net.http client request completing after it
     (`netHTTPNumbered`); such a request is also released only once
     the JVM's watermark passed its stamp (or by the timeout, then
     unnumbered), so no earlier marker can still be unread.
     HttpURLConnection and server ends are unaffected.
   - **HttpURLConnection**: `getInputStream0`'s first exit with a
     response code (`getResponseCode` and the header getters go
     through it; a 404 throws after reading the response), reading
     `responseCode` and the request and response `MessageHeader`s
     by reflection (calling `getResponseCode` would reconnect when
     no response came); `HttpClient.parseHTTP` binds the socket's
     ends, which `getInputStream0` drops after a body-less reply.
   - **Connection by socket ends, not fd**: the libraries hand out
     addresses through public API, a descriptor only through deep
     internals. The joiner indexes every socket event's ends under
     its `conn_id` and looks a request up by both ends, else the
     remote, else the local one — answering only when one
     connection is known there; `req_seq` is counted per process,
     connection and end (client or server: a same-JVM loopback
     call has both) in completion order, and left out over HTTP/2,
     without a connection, once the JVM may have lost an HTTP
     record (dropped, abandoned, malformed, or a hook call that
     failed — the agent counts it as dropped), and from a request
     that took several exchanges on (review of 3b4: a client
     library's own redirect or 401/407 retry — HttpClient's
     `previousResponse` chain, HttpURLConnection's `parseHTTP`
     count — is one request to the caller and several to the
     server). HttpURLConnection reaches `getInputStream0` again
     from inside it while following, so only the outermost exit
     records (a stack walk counts the frames). The joiner inserts
     the http events by time (each waits until an event that
     completed 5 ms after it arrived, or, on an idle stream, the
     correlator's 5 s fuse window plus 2 s: task 1f4,
     `UpstreamHold`) and therefore numbers its output `seq`
     itself.
   - **Redaction twice**: `Redaction` (agent) masks denied headers,
     URI-valued headers (Location, Content-Location, Referer,
     Origin, X-Original-URI and the other forward-auth ones) as
     URLs even when relative, every query value, and URL
     userinfo with `internal/redact`'s authority heuristic; the
     joiner applies `internal/redact` again.
     `TestAgentPolicyMatchesDefault` reads the agent's lists
     from `Redaction.java` and fails when they drift.

   Tests: `mage agentTest` — `HttpTransformerTest` (a fixture's
   ctor, return, last-argument-after-a-long and throw paths hand
   over the right objects; static and primitive targets refused),
   `HttpProbeTest` (redaction, addresses, the HttpClient probe on
   a fake response), `HttpHooksTest` (the HTTP record, quiet and
   throwing probes, oversized headers), and `AgentSmoke` now does
   one request through each library with a spool, so every JDK in
   `IORMESH_AGENT_JDKS` must record four (checked on 17, 25 and
   27-ea); Go — `internal/agentspool` (HTTP decode, malformed,
   drops), `internal/correlate` (`javahttp_test.go`: order,
   waiting, conn index, numbering rules, end last), `internal/
   schema` (`kinds_http_test.go`), `internal/normalize`
   (`ExpectedHTTP`); integration — `TestHTTPPlain`,
   `TestHTTPTLS`, `TestHTTPRedaction`, `TestOtelHTTP`
   ([testing.md](../../testing/integration/m3.md#m3-scenarios-ahead-of-the-agent)).

   Latency and sizes (task wd4): a `Target` marked `stamped()` —
   the method that begins a request: `HttpClientImpl.send`,
   `getInputStream0`, `ServerImpl$Exchange.run` — also calls
   `Hooks.httpEnter(this, id)` first thing, and its exits call
   `Hooks.httpExit`, which pops that stamp (a per-thread stack in
   `Brackets`, 8 deep, balanced past it; constructors are never
   stamped). Every probe gets the innermost open stamp and its
   `this` (`a[3]`) and adopts the start only when it is provably
   its request's: the stamped method's own exit
   (`Request.ownStart`), or for `HttpExchangeImpl.close` a dispatch
   whose `tx` is the closing exchange — never a stamp of another
   call. The probe's `long[]` meta (`Ring.M_*`) carries status,
   counts, flags, exchanges, start and the body sizes; ring format
   5 adds the start and both sizes to HTTP's fixed part. Sizes are
   the libraries' own numbers: HttpClient's publisher length and
   the response's Content-Length, HttpURLConnection's poster size
   or streaming count (read at `parseHTTP`, before
   `getInputStream0` lets go of them), HttpServer's
   `reqContentLen` and `rspContentLen` (0 means chunked or none —
   the reply headers tell which). The joiner turns the start into
   `boot_ns`/`latency_ns` and the sizes into `http.req_bytes` and
   `resp_bytes`. Tests: `HttpHooksTest` (record words, entry
   stamps and their nesting), `HttpTransformerTest` (stamped
   exits, the throw path, constructors never stamped),
   `HttpProbeTest` (declared sizes, adoption), Go decode and join
   tests, goldens `http-plain`/`http-tls` declare the sizes and
   every integration HTTP run checks each event has a latency.

   `sendAsync` is covered by the `http-async` scenario (task wd4:
   http-plain's calls through `sendAsync`, to `localhost`),
   `TestHTTPAsync`, `HttpProbeTest`'s completion case and the
   smoke's extra request (six HTTP records per JDK).

   DNS hooks (#8, task wd4): `JdkDns` is a library of one target,
   the public static `InetAddress.getAllByName(String)`, marked
   `lookup()`: `HttpTransformer` now takes static methods (null as
   `this`, arguments from slot 0) and calls `Hooks.dns` at a
   lookup's exits. Its probe returns `{host, address…}` only for a
   mapping new to the JVM (a bounded map of host → sorted address
   set), never for literals, the empty host or a failed lookup;
   `Hooks.dns` writes a DNS record (ring type 7) with the stack, and
   a lookup on a busy thread is the agent's own (an HTTP probe
   parsing a literal) and records nothing — no HTTP loss either.
   The joiner queues DNS records with the HTTP ones and emits `dns`
   events (`internal/correlate/javadns.go`; schema `KindDNS`,
   `Event.DNS`; normalize `ExpectedDNS`). Tests: `HttpProbeTest`
   (mappings, rotation, literals), `HttpHooksTest` (record, busy),
   `HttpTransformerTest` (static target, throw path), agentspool
   DNS decode, the join's order, the `http-async` golden's lookup
   of `localhost`, and the smoke's `dns` kind on every JDK.

   Third-party libraries (task de4, #22's order). The JDK's
   libraries live in the bootstrap and platform loaders; these
   live in whatever application loader the app (or its
   container, per web app) defines them in, so:

   ```
   agent/core/src/iormesh/agent/BridgeSight.java   # loader sees bridge?
   agent/core/src/iormesh/agent/http/Handles.java  # API per class
   agent/core/src/iormesh/agent/http/Servlet.java  # HttpServlet
   agent/core/src/iormesh/agent/http/ApacheHttpClient.java
   agent/core/src/iormesh/agent/http/Slots.java    # exchanges
   agent/core/src/iormesh/agent/http/OkHttp.java   # OkHttp 4/5
   agent/core/src/iormesh/agent/http/Netty.java    # Netty's HTTP/1.x codec
   workload/libs/                                  # module, jar, vendored libs
   ```

   - **A `Library` is `appLoaded()`**: no module to open, nothing
     resolved at install — its targets are listed whether or not
     the app has it. `HttpTransformer` rewrites a third-party
     class only in a non-JDK loader (a JDK class only in the
     bootstrap or platform loader), and only when `BridgeSight`
     says the loader resolves `iormesh.bridge.Hooks` to the
     bridge's own class (an OSGi framework without boot
     delegation would otherwise fail the app's method at its
     first return); a named module gets the read of the bridge
     added, or is skipped. The answer is kept per loader in a
     weak map and computed with no lock of the agent held
     (`Class.forName` may load through the app's loader — 4b4's
     live-attach lesson). A live attach retransforms the
     third-party classes already loaded, through the same check.
   - **Store scan**: before rewriting a third-party class, a first
     ASM pass refuses every listed method whose code stores into
     `this` or a passed argument's slot — the catch-all's
     hand-written frame declares those with their declared types,
     which javac never contradicts but another compiler might, and
     a VerifyError would fail the app's class. (The JDK's targets
     are not scanned: `getAllByName` reassigns its `host` with a
     `String`, which verifies.)
   - **Two arguments**: a `Target` may name a second argument
     (`withSecond`); the pair goes to the probe as one `Object[]`.
     The throw path now hands over the throwable as `ret` (it was
     null), so a void method's probe tells a throw from a return.
   - **Probes** find the library's API in the class of the object
     they are handed (`Handles`: the public lookup, one
     `MethodHandle` per getter — `getMethod` would resolve every
     public method's types and load classes the app never loads),
     cached in a `ClassValue` (as long as the class lives, never
     pinning its loader). They call nothing that changes the
     request. A probe that cannot read a request throws, which
     `Hooks` counts as a lost record (numbering stops, never
     shifts).
   - **Servlet**: `HttpServlet.service(ServletRequest,
     ServletResponse)`, jakarta and javax, entry-stamped, request
     and response as the pair. Only the original dispatch
     (`getDispatcherType()` REQUEST; Servlet 2.5 has none)
     records — a forward, include, error page or async dispatch
     is no new request — and of it only the outermost `service` on
     the thread (review of de4: a JSP under `JspServlet`, Spring's
     `ServletWrappingController`, a Servlet 2.5 forward, which has
     no dispatcher type): `Hooks.httpExit` tells a probe (`a[5]`)
     whether a stamp of the same hook is still open below. Status, headers and the reply's declared
     `Content-Length` (asked for by name: containers keep it
     outside the header map) at service's return; the request's
     `getContentLengthLong`. A request still in async processing
     is recorded then without status or reply size, unordered; a
     service that threw before the reply was committed leaves the
     status unknown. Not seen: a servlet overriding the public
     `service` without calling it (Jersey's `ServletContainer`),
     a filter answering without a servlet. The causing method is
     `jakarta.servlet.http.HttpServlet#service` (`javax.` is a JDK
     prefix to `CausingMethod`, so a javax servlet's event names
     the container's frame below it).

   Tests: `mage agentTest` — `HttpTransformerTest` (an app loader
   that sees the bridge is rewritten, the bootstrap loader, a
   blind loader and no bridge are not; the pair and the throwable
   on both paths; a reassigning method refused),
   `ServletHookTest` (the real targets rewriting a stand-in
   `HttpServlet` from `agent/test/src/jakarta/servlet`: a redacted
   request with addresses, sizes and its own start; a forward
   records nothing; async and throws as above); `mage
   workloadTest` — `workload-libs.jar`'s self-tests, listing,
   two-process pairs and failures; integration —
   `TestHTTPServlet` (`http-servlet`: the JDK HttpClient against a
   servlet in an embedded Tomcat 10.1, one request thread).

   - **Apache HttpClient 5** (classic): `InternalHttpClient` and
     `MinimalHttpClient.doExecute(HttpHost, ClassicHttpRequest,
     HttpContext)` — what every `execute` overload reaches —
     entry-stamped, target and request as the pair, record the
     request on the calling thread when the response is handed
     back (body unread). httpcore5's `HttpRequestExecutor.execute`
     (one exchange on one connection) records nothing: its probe
     finds the request's `doExecute` as the open hook and fills a
     per-thread `Slots.Slot` — exchanges counted (a followed
     redirect, an answered 401/407: the server saw each), the
     connection's socket ends, the version spoken — which the
     request's exit takes and clears both ways out. A request is its
     client and its start stamp together (review of de4): calls nest
     on one thread — an interceptor calling the same client, an
     OkHttp authenticator's own call — and each open one keeps its
     own slot, found by the stamp Hooks offers with the open hook;
     an outer exit clears any slot left above it. A request that
     failed records nothing, unless an exchange had happened: then
     the probe throws, a lost record (numbering stops, never
     shifts). The causing method is the library's
     `InternalHttpClient#doExecute`; the app's call is below it on
     the stack. HttpClient 4.x (`org.apache.http`) is not hooked.

   Tests (Apache): `AgentSmoke` executes one GET through the
   vendored client in a `URLClassLoader` of its own, so the dump
   must show `doExecute` and `execute` applied and recording on
   every JDK of `IORMESH_AGENT_JDKS` (17, 21, 25, 27 checked);
   `TestHTTPApache` (`http-apache`: the client against the JDK's
   HttpServer).

   - **OkHttp 4/5**: `RealCall.getResponseWithInterceptorChain$okhttp()`
     — what `execute()` and the dispatcher's `AsyncCall` both run —
     entry-stamped, records the call's original request and the
     chain's response on that thread (the caller's for `execute`,
     the dispatcher's for `enqueue`, before the app's callback);
     `CallServerInterceptor.intercept(Chain)` fills the call's slot
     like httpcore5's executor (exchanges, `Chain.connection()
     .socket()`'s ends). OkHttp's protocol names map to versions
     (`h2` is HTTP/2, not numbered). A cached response has no
     exchange and no connection. Kotlin-compiled; the store scan
     refuses neither method.
   - **Netty** (4.2, HTTP/1.x codec): no call spans a request, so
     `HttpObjectDecoder.decode` (its output list at the exit, with
     the context) and `HttpObjectEncoder.write` (the message) feed a
     per-channel FIFO (weak map; its lock held only to find or
     change a queue): a request opens an exchange — a server's,
     decoded, stamps the start —, the answer's head (status,
     headers; an interim 1xx is skipped) goes to the oldest one,
     and the answer's last content completes it, recorded with
     `Channel.localAddress()/remoteAddress()`. Server: at the
     encoder's write, before the flush; client: at the response's
     decoding; a close-delimited response's last content comes from
     `decodeLast`, hooked too (only its list's last element, and only
     to complete: its own `decode` ran inside it). Pairing in order
     holds only while nothing of the channel went unseen (review of
     de4), so the channel is poisoned — nothing more recorded on it
     — when its codec predates the agent (a live attach: the codecs'
     constructors, hooked, mark the ones made since) or its codec's
     loader did not get both `decode` and `write` rewritten (task
     6j4: the transformer tells the library each method it rewrote,
     per loader; older Netty 4.1 has no encoder `write`), and, counting
     a lost record, when a probe fails, a write throws, an answer
     finds no request of its own, 64 requests wait unanswered, or one
     call completes two exchanges (one record per call). A decode
     that throws still passes its output on, which counts. Netty's
     HTTP/2 codecs are not seen.

   Tests (OkHttp, Netty): `TestHTTPOkHttp` (`http-okhttp`: OkHttp 5
   against the JDK's HttpServer) and `TestHTTPNetty` (`http-netty`:
   the JDK HttpClient against a Netty server, one acceptor and one
   worker event loop), `NettyPairingTest` (stand-ins of Netty's types
   in `agent/test/src/io/netty`: pipelining, 1xx, a throwing decode,
   decodeLast, each poisoning case, and a half-hooked loader),
   `HttpTransformerTest` (the transformer tells Netty what it
   rewrote in which loader), `SlotsTest` (nested requests
   on one client and across calls), `ServletHookTest` (a nested
   service records once; the javax target on a Servlet 2.5-shaped
   stand-in in `agent/test/src/javax/servlet`); `AgentSmoke` also runs one OkHttp call from
   the vendored jars in a loader of their own, so the Kotlin targets
   are rewritten, verified and recording on every agent-test JDK (17,
   21, 25, 27 checked). The servlet and Netty hooks run only under
   the integration suite's JDK (25).

   Not hooked: Apache HttpClient 4 (`org.apache.http`) and its async
   client, OkHttp 3, Netty 4.1 before its `HttpObjectEncoder` had
   a `write` of its own (4.1.79 and older checked: only the decoder
   is rewritten, so its channels record nothing and never count a
   lost record — task 6j4; 4.1.110 has `write` and is hooked,
   untested; a constructor the list misses leaves its codecs
   unmarked, likewise silent), Jersey's `ServletContainer`
   (overrides `service`).
