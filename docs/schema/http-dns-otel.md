# HTTP, DNS and OpenTelemetry

Part of [Trace schema (v1, frozen)](../schema.md).

## HTTP events (M3)

With `-spool`, the agent's HTTP hooks (decision #22, M3 step 7)
add one `http` event per request a JVM made or answered through
the JDK's HttpServer, HttpClient (`send`) or HttpURLConnection,
or a third-party library (task de4): a servlet container's
`HttpServlet` (Servlet API, jakarta or javax), Apache HttpClient 5's
classic client (stamped when it hands the response back, its start
when `execute` reached the client's `doExecute`; sizes from the
request's entity and the response's `Content-Length`), OkHttp
(likewise, on the thread that ran the call's interceptor chain —
OkHttp's dispatcher for `enqueue`) and Netty's HTTP/1.x codec (a
server's event when the encoder writes the answer's last content,
spanning from the request's decoding; a client's when the response's
last content is decoded, from the request's write).
It is not a syscall: it is stamped when the request completed —
the server's exchange was closed by the handler (or the handler
returned without closing it), the client's `send` returned, the
connection's response was read — on the thread that did it, with
its stack as `java` (the handler's or the caller's method; the
JDK's dispatch when the handler never closed the exchange). The
thread is named as a syscall's join names it: a virtual thread's
request has `carrier: true` and its carrier's name (task mb4
review), and a thread whose THREAD record never came has no name.
`fd`
is `-1`; `ret` and `bytes` are 0.

Since task wd4 the event spans its request like a syscall: `boot_ns`
is when the library began it and `latency_ns` the time to its
completion, so `boot_ns + latency_ns` is the completion stamp. The
start is the entry of the call that began the request on the
recording thread — HttpClient's `send`; HttpURLConnection's
response read (`getInputStream0`, outermost), so a request body the
app uploaded earlier through `getOutputStream` is not part of the
latency; for the server the start of the exchange's task
(`ServerImpl$Exchange.run`, when an executor thread took it up — the
time the task waited in the executor's queue is not included) — and
only when the hook can tie that entry to this very request;
otherwise `boot_ns` is the completion and `latency_ns` 0 (a server
exchange closed on another thread than its dispatch, and every
`sendAsync` request, below). The body sizes go into the `http`
object.

A servlet's event (task de4) is stamped when `HttpServlet.service`
returns to the container, on the request thread, and spans the
service call (the container read the request line and headers
before it; a reply it still buffers is written after, so the event
may precede that write). Its sizes are the request's
`Content-Length` and the one the servlet set on the reply (absent
otherwise — the container may add one later). A request still in
asynchronous processing at that return has no `status`, no
`resp_bytes`, no latency and no `req_seq`; one whose service threw
before the reply was committed has no `status`.

```json
{"event":"http","fd":-1,"conn_id":"cb061683df650ac2","attribution":"java",
 "java":{"thread":"main","stack":[220,221]},
 "http":{"method":"GET","scheme":"http","host":"127.0.0.1",
  "path":"/redact/login?user=***&token=***","status":200,"req_seq":1,
  "version":"HTTP/1.1","req_headers":["authorization: ***","x-api-token: ***"],
  "resp_headers":["content-length: 7","set-cookie: ***"]}}
```

| `http` field | Type | Meaning |
|---|---|---|
| `method`, `scheme` | string | as the library had them; `scheme` `https` over TLS (seen at the API boundary, never on the wire), empty when the library did not say (Netty: an unreadable pipeline) |
| `host` | string, optional | the request URI's host — client side only |
| `path` | string, optional | path and query, every query (and `;`/`#` parameter) value masked; path segments verbatim (#22's default) |
| `status` | int, optional | the response's status; absent when none came |
| `req_seq` | int, optional | the request's ordinal on its connection, 1-based, the same on both ends — see below |
| `version` | string, optional | `HTTP/1.1` or `HTTP/2` (appended in M3) |
| `req_headers`, `resp_headers` | list of string, optional | `name: value`, names lower-cased, sorted, at most 32 each, redacted (see "Redaction"); HttpURLConnection's request line is no header and never recorded (appended in M3) |
| `req_bytes`, `resp_bytes` | int, optional | the request's and the response's body size in bytes as the library knew it — absent when it did not, never estimated (appended in M3, task wd4; see below) |
| `held` | bool, optional | `true` when the recording thread worked on this request alone from its start to its completion: a synchronous library's request (the JDK's HttpServer, a servlet's service, HttpURLConnection, HttpClient `send`, Apache's classic client, OkHttp's call chain) with a start. Absent for Netty, whose event loop serves other channels inside a request's span, for an async completion and without a start. Only on a held span may a view place the thread's other calls and file work (appended in M5, task mb4 review; the agent's HTTP flag 16) |

Body sizes come from what the library itself knows and enforces:
HttpClient's request publisher length (absent for a streamed body)
and the response's `Content-Length`; HttpURLConnection's count of
the bytes the app wrote (its buffered or streaming output) and the
response's `Content-Length`; HttpServer's request `Content-Length`
as the client declared it (absent when chunked) and the length the
handler passed to
`sendResponseHeaders` (absent for a chunked reply). A reply without
a body by protocol (HEAD, 1xx, 204, 304) is 0; a response without
`Content-Length` (chunked, or read to the close) has no size.

The event goes into the stream after the events that completed
before it: it waits until an event that completed 5 ms after it
has reached the joiner, or, on a stream with nothing later, 7 s
after it completed: the correlator's 5 s fuse window, for which a
socket event whose other end is on another host holds the stream,
plus 2 s (task 1f4). So on an idle stream an http event reaches
the live output and the segment being written up to about 7 s
after its request ended. The trace's `end` stays last.
`conn_id` is the connection whose socket ends match the ones the
library still held (both, else the remote end — a server's local
end is unreadable once the close shut the connection — else the
local one; a client knows the remote end only for an IP-literal
host), and only when exactly one connection of the process is
known on those ends: a client's two connections to one server
share the remote end, so such a request keeps no `conn_id`.

Behind a proxy that names the client (task 7j4) — Tomcat's
`RemoteIpValve` (on by default in Spring Boot on Kubernetes),
Spring's `ForwardedHeaderFilter` — a servlet's remote address is
the `X-Forwarded-For` or `Forwarded` header's claim, which the
sender chooses, not the socket's peer. So a servlet request
carrying either header records no remote end, and `scheme` is as
the app had it (`X-Forwarded-Proto`'s, which may say `https` on
a plain-HTTP hop). Its local end is the listener's, shared by every
connection it accepted, so a held request with a start (`held`)
is joined by its own thread's socket I/O instead: the one
connection on the request's ends that the thread read or wrote
inside the span (the thread's outbound calls, to a database say,
have other ends, and a call to the process's own listener, whose
loopback connection shares its `conn_id` with the server end, is
skipped; two such connections answer nothing; never for a virtual
thread, whose carrier serves others in the span). A
request whose body and reply moved outside the span — a GET whose
reply the container still buffered when `service` returned —
leaves no I/O inside it and keeps no `conn_id`, nor `req_seq`. A
proxy set up with another header (`RemoteIpValve`'s
`remoteIpHeader`) is not recognised.

The tracer numbers a connection's requests per process and end
(client or server — a same-process loopback call has both on one
`conn_id`) in completion order (`req_seq`), so the client's and the
server's event of one HTTP/1 request pair by (`conn_id`,
`req_seq`). It leaves `req_seq` out

- over HTTP/2 (multiplexed, decision #22), and without a `conn_id`;
- for every request of a JVM that may have lost an HTTP record —
  dropped by the ring, abandoned, undecodable, or a hook call that
  failed or was skipped (the agent counts those as dropped);
- for every java.net.http (HttpClient) client request of a JVM that
  completes after the JVM's first `sendAsync` call: an async request's
  HTTP/1.1 connection goes back to the pool once its body is read,
  while its record waits for the agent's completion (which runs after
  the app's own callbacks), so another request reusing that connection
  can be recorded first and would take its ordinal. The agent writes a
  marker at the `sendAsync` call, before the request can reach a
  connection; from its stamp on the tracer numbers no java.net.http
  client end of that JVM, on any connection (requests completed before
  it keep their numbers; HttpURLConnection and server ends are
  unaffected). A java.net.http request is numbered only once the JVM's
  watermark passed its stamp — proof that no earlier marker is still
  unread — so one released by the timeout gets none;
- from a servlet request still in asynchronous processing when its
  service returned on (task de4): its reply comes later, out of its
  connection's order, so its server end is numbered no further;
- from the first request on a connection that took several HTTP
  exchanges on: HttpClient following a redirect or answering a
  401/407 through its authenticator, HttpURLConnection doing the
  same (an interim `100 Continue` counts too), Apache HttpClient
  doing either (every exchange its executor ran), OkHttp likewise
  (every network exchange of the call) — the server saw
  each exchange as a request, the caller one, and the ends' counts
  no longer agree.

The limit: completion order is the agent's stamp at the hook, so two
requests of one connection that complete within the stamps'
resolution on different threads (pipelining clients answered out of
the scheduler's order) could swap ordinals. HTTP/1.1 clients send the
next request on a connection only after the previous response, which
rules that out for the JDK's synchronous calls (`send`,
HttpURLConnection, the server's exchanges) — but not for `sendAsync`,
whose record is not stamped at its response: hence the rule above
that stops a JVM's java.net.http client numbering at its first
`sendAsync`.

The summary line counts the http events and those without a
connection. `HttpClient.sendAsync` (task wd4) is recorded when the
agent's completion of its future runs — on the client's executor —
but under the sending thread: its `tid`, `java.thread` and stack
(the method that called `sendAsync`) and the OTEL span current at
the call; a failed or cancelled future records nothing, like a
`send` that throws. A future runs its callbacks last-registered
first, so the agent's (registered first) runs after the app's own
callbacks and `get()`'s waiter: its stamp is not when the response
arrived. Such an event therefore has no latency and no `req_seq`,
and its connection's client end numbers no more requests from it on
(the server end still does). Not recorded in v1: the other libraries of #22's
set.

The agent writes each request as an HTTP record of the ring
(format version 8: exit stamp, stack id, Java thread id, kernel
tid, status, the string and request-header counts, flags — server
or client, unordered (a `sendAsync` completion), java.net.http, held
(`held`, task mb4 review), or
a `sendAsync` marker that is no request — the exchange count, the
OTEL span ids, the start stamp and the two body sizes, then
method, scheme, host, path, version, local and remote socket
address, and the headers), redacted before it is written; a record
that would pass 16 KiB keeps the request and drops its headers.
The tracer skips any ring record over 64 KiB as malformed, and
reads a ring only once its process is traced
([security.md](../security.md), "The agent spool is untrusted
input").


## DNS events (M3)

With `-spool`, the agent's DNS hook (decision #8's default, task
wd4) adds a `dns` event when a JVM resolves a host name to a set
of addresses it had not mapped that name to before: the first
lookup of a name, and again whenever its address set changes
(compared as a set, so a resolver rotating the order is no
change). The agent remembers 4096 names per JVM; past that it
starts afresh, and a mapping it had recorded before is recorded
once more. The hook is `InetAddress.getAllByName(String)`, through
which `getByName`, `InetSocketAddress(host, port)` and so the
JDK's sockets, HttpClient and HttpURLConnection resolve. IP
literals (parsed, never looked up), the empty host and failed
lookups are not recorded; reverse lookups are not hooked.

```json
{"event":"dns","fd":-1,"attribution":"java",
 "java":{"thread":"HttpClient-1-Worker-0","stack":[139,140]},
 "dns":{"host":"localhost","addrs":["127.0.0.1","0:0:0:0:0:0:0:1"]}}
```

| `dns` field | Type | Meaning |
|---|---|---|
| `host` | string | the name as the app passed it |
| `addrs` | list of string | the addresses, in the resolver's order (the JDK connects to the first), textual — IPv6 in the JDK's full form; at most 32 |

The event is stamped when the lookup returned, on the thread and
stack that asked (`latency_ns` 0), and goes into the stream like
an `http` event, after the events that completed before it. It
names no connection: a view pairs a connection's remote address
with the latest mapping of the same process that contains it. In
the ring a mapping is a DNS record (format 5 on: exit stamp, stack
id, Java thread id, kernel tid, string count, OTEL span ids, then
host and addresses); one the ring dropped is counted (`agent
lost`) and not sent again. The summary line counts the dns
events.


## OpenTelemetry span context (M3)

When a JVM traced with `-spool` runs the OpenTelemetry API (with
or without an SDK, decision #20's default), each event joined to
the agent gets the span that was current on its Java thread:

```json
{"event":"write","fd":6,"attribution":"java",
 "java":{"thread":"main","stack":[31,32]},
 "otel":{"trace_id":"a3ce929d0e0e47364bf92f3577b34d01",
  "span_id":"c11e0000000000a1"}}
```

| `otel` field | Type | Meaning |
|---|---|---|
| `trace_id` | string | the W3C trace id, 32 lowercase hex digits, never all zero |
| `span_id` | string | the W3C span id, 16 lowercase hex digits, never all zero |

- A syscall gets the span current when its bracket opened (the
  hooked JDK method's entry); an `http` event the span current
  when the request was recorded (the server's exchange closed,
  the client's response read). Events outside any span, events
  that stay `none`, and every event of an app without OTEL carry
  no `otel`.
- The ids are what the app's `Span.current().getSpanContext()`
  returns, read reflectively — the agent has no OTEL dependency —
  so iormesh events join the app's distributed traces in any OTEL
  backend by these two ids. They are span metadata, not payload;
  spans themselves are not ingested (#20's optional half).
- The agent starts reading a copy of OTEL (one per class loader)
  only after the app initialized OTEL's context storage, and does
  not initialize it itself (one exception: a live attach that
  catches a copy between loading and initializing it initializes
  it on the attaching thread); I/O before the app's first use of an
  OTEL context carries no ids, which is right — no span can be
  current then.
- In the ring (format 4, and 5 since) a bracket inside a span is 72 bytes
  instead of 48 (trace id as two u64 halves, span id as one u64);
  the HTTP record always carries the three words, zero for none.
