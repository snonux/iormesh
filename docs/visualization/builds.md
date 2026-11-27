# Builds and build order

Part of [Visualization — exploring a consolidated mesh](../visualization.md).

## Namespace and windowed builds

`build -namespace NS` writes `<projects>@NS/` and a windowed
build (`-from`, `-to` or `-last`) `<projects>[@NS]~window/`,
beside the full build ([kubernetes.md](../kubernetes.md#collect-and-build),
milestones.md "Build window and prune"). All three surfaces read them
with the same two flags, which pick the same directory for each
(task hk4):

```
iormeshctl open -namespace prod       # the site of <projects>@prod
iormeshctl tui -windowed              # the TUI over <projects>~window
iormeshctl show -namespace prod -windowed
```

Without them each reads the full build. A windowed build records
its window in the model (`run.window`, the `FROM .. TO` text
build echoes, `(open)` for a side not given), so the site's
header and the TUI's title say which window they show; the run's
first and last instants can lie a minute or two outside it, as
the build takes whole segments. A full build's model has no
`window`. `build` ends with the `open` and `show` commands, with
these flags, that read what it just wrote.


## Build order

| Step | Lands with | What |
|------|-----------|------|
| V1 | M4 | `internal/viewmodel` (run, health, services, edges); `show` and the `build` summary |
| V2 | M5 | conversations in the model; `show sequence`; deterministic SVG exports |
| V3 | M5 | the local site, written by every `build` |
| V4 | M5 | the TUI |

V1 goes first on purpose: from the first working `build`, a run
ends with readable output, and the model's shape is settled
before two UIs are built on it.

V1 as built (task ib4; details in
[implementation.md](../implementation.md), M4 "Build, refresh and
show"): `Model` holds `Run`, `Health`, `Services` and `Edges`;
`Conversations` and `Processes` are appended in V2. The summary
reads, for the M4 test mesh:

```
built      billing+shop: 2 hosts, 2 sessions, 8 segments, 22 events, …
merged     3 services, 2 edges (1 fused, 0 mixed, 1 unresolved); tcp …
health     0 lost, 0 skipped runs, 0 unattributed, 0 attach gap; clock …
clock      hostA.example.org ~ hostB.example.org: 1 windows, width …
session    shop/app1 a1b2c3d4: no end recorded (running, or cut short)
unresolved java -> 203.0.113.9:5432 (1 of 1 connections)
```

A `session` line names each session the operator must look at:
one that recorded no end, and one whose end reason is not a
normal one (`duration`, `stop`, `targets_exited`), so a run the
disk watchdog cut short reads `session    m4disk/iom2 …: ended
disk_limit (the disk watchdog stopped the tracer)` — milestones.md M4
puts the exit reason in the summary (task lf4). `show health`
lists every session's end.

When the merge marked connections pending (decision #43: made in
the build's last minute, their far end maybe in a segment not
collected yet), the `merged` line's tcp part ends in `, N
pending` and a `pending    N connections in the build's last
minute: …` line follows the edges; `Run.PendingConns` holds the
count. Pending connections are in no edge and in neither tcp
count; a build without any prints the lines as before.

Differences from the sketch above (V1; still so after M5's
renderers landed): the text is plain (no colors, `->` for
arrows); an edge between
two services aggregates connections, so its state is `fused`,
`unresolved` or `mixed`; request counts and top paths sit on the
service (the database counts them per endpoint, not per client),
and bytes and latency percentiles are not in the model yet.
`show` has `summary`, `services`, `edges [-unresolved]` and
`health`, each with `-json`, read from the build's
`model.json`; the site path line comes with V3. `show report`
(task qb4) prints the summary followed by the per-run rollup
from `report.json`, which build writes beside the model: one
block per process — files, connections, threads with their
requests in order, top methods ([schema.md](../schema/query-summary.md#summary-rollup-m5)).
The rollup is no fourth reading of the data for the three
surfaces: it is the export an LLM reads first (milestones.md M5). (Lines cut
with … here; `internal/ctl/testdata/build_summary.golden` has
them whole.) Every string from the traces is printed
terminal-safe: one holding a control character comes out
Go-quoted, never as an escape sequence. The `-json` views (and
`show report -json`, `expand -json`) escape DEL, the C1
controls and bidi overrides as JSON `\u` escapes, so the JSON
still decodes to the same value.

V2 as built (task mb4; files and passes in
[implementation.md](../implementation.md), M5 "Conversations and
exports"): `Model` gains `Conversations` (the newest 100, newest
first, `MoreConversations` counting the rest) and
`Health.RequestsCut`; task 0f4 added the per-thread lifelines,
`Threads` and `Processes` (below). A
conversation is built from the http events only — the correlated
trace, never re-inferred:

- **Paired hop**: a client and a server http event on the same
  merged conn id with the same `req_seq` (exactly one of each) —
  the request went from the one thread to the other, a causal
  edge (`hop: paired`).
- **Nesting**: a client request belongs to the server request on
  the same kernel thread whose span holds it — one thread, one
  clock, so the containment is a fact — and only when the thread
  worked on each request alone: `http.held` (a synchronous
  library: the JDK's HttpServer, a servlet, HttpURLConnection,
  HttpClient.send, Apache's classic client, OkHttp's call chain),
  a platform thread known by name, a start. Netty's event loop
  serves other channels inside a request's span and a virtual
  thread's carrier (`carrier`) other virtual threads; a thread
  whose record never came may be either. Such spans nest nothing
  and own no file work.
- **Clock join**: an unpaired client request (HTTP/2, a lost
  record) and an unpaired server request on the same fused
  connection are joined only when the merge's clock bounds
  (`federate.Timeline`) prove the server's span inside the
  client's, each is the other's only such match and no unpaired
  end of the connection lacks a start (an async request might be
  the true partner) — `hop: clock`, the arrows time-ordered. A
  bound too wide, no bound, two candidates or a startless end:
  the client's step ends at its peer (`hop: unpaired`) and the
  server request is a conversation of its own, named after its
  traced caller or `(untraced)`. Once records were cut (past
  2¹⁸), a request starting before the oldest kept completion plus
  the widest clock offset joins no hop: its partner may be cut.
- **File work**: a server request's thread's file operations in
  its span — open/create, read and write (path by the fd's open,
  offset when given), rename, unlink, mkdir, link/symlink,
  truncate, touch, chmod/chown, xattr writes; metadata only —
  interleaved in that thread's clock order with the calls it
  made. Another thread's I/O, socket I/O and an fd whose open the
  trace never showed are not in it.
- **Folded runs** (task vg4): back-to-back repeats of one call by
  one thread — same verb and path, the same outcome (success, or
  failure with the same errno: return values and byte counts may
  differ, so a short read or the read that hits EOF folds with the
  reads before it), only the offset differing, and no client
  request of the thread's started in between — are one step:
  `count` holds the calls, the label ends `(N calls)` and drops
  the offset, `bytes` and `latency_ns` are the run's sums, `stack`
  and `ref` its first call's, `last_ref` its last call's, and the
  step after it is judged against its last call. On Rocky 9.9 a
  JVM's serving thread read its runtime image (`lib/modules`) 744
  times while loading classes on its first request, which pushed
  the checkout to 1696 steps and its story past the cap; folded it
  is 100 steps that still count all 1696 calls. A single call has
  none of these fields. Every surface says which fields are a
  run's: the site's and the TUI's step detail label them `bytes
  (N calls)`, `latency (sum of N calls)`, the Java stack `first
  call`, the ref `first of N`, and `show sequence` marks the frame
  and ref the same way; the SVG, which cuts labels at 40
  characters, shortens a run's path and keeps its `(N calls)`.
  `run_query` is the query that prints every call of the run —
  each surface shows it ready to copy:

  ```
  iormeshctl query -host FQDN -thread TID -file PATH \
    -from FIRST -to LAST+1ns
  ```

  the run's host, its kernel thread, its file (globs escaped to
  match only themselves, every word shell-quoted; a rune that is
  not printable, or an invalid byte, becomes the wildcard `?`, so
  the command is printable as built and no surface re-quotes it)
  and its window, from the first call's instant to just past the
  last's (`-to` is exclusive). `-file` follows an fd opened from the path even
  before the window, so reads on an fd the process opened at its
  start are printed; anything else the thread did to that file
  between the calls (a close, a stat) is printed with them.
  Past a server request's 200 file steps, the calls cut fold the
  same way, so `more_steps` counts steps, not calls, and nothing
  folds into a kept step across a cut call.

Each step carries `ordering`, judged against the step before it in
the list: `causal` (thread order or a paired send→receive),
`time-ordered` (no edge, but the timestamps prove it — one host's
clock, or two hosts' through the clock bounds), or `unordered`
(neither: no bound covers the two hosts or the instant, or they lie
closer than the bound is wide). Only the step right before is
compared: a step unordered against it may still be provably after
an earlier one. The first step, with nothing before it, is `causal`
when its own hop is paired and `time-ordered` otherwise. Raw
timestamps of two hosts never order anything; the list's own order of conversations (by the
entering request's start) is presentation only. Steps differ from
the sketch above: `host`, `carrier` and `hop` are added, there is no
`note` kind, a stack is at most 12 frames, and `ref` is
`<project>/<host>/<fqdn>-<session>/<seq>` — `iormeshctl query -ref
REF` prints that one raw event. Caps: 200 steps per conversation
(`more_steps`; folded runs count as one step each, and a server
request keeps at most 200 file steps of its own), 2¹⁸ http events
read (the newest by completion), 2²⁰ followed fds. `hops` and
`services` describe the whole conversation, steps past the cap
included, so a service reached only there is still named.

`show conversations` lists them; `show sequence [ID]` (the newest
without an ID) prints one as numbered steps — ordering, actor
(`service/thread`), arrow, label, bytes, latency, a hop that is not
`paired` — each with its top Java frame and its ref, every string
terminal-safe; `-json` prints the conversation. The summary gains a
`sequence` line counting conversations and their time-ordered and
unordered steps. `build` also writes the deterministic SVG exports
(`internal/render`): `graph.svg` (apps, one column per host — host,
processes, files — and the connections; unresolved connections
dashed, 200 nodes a column, the rest counted; host columns titled
and ordered by the store's host name, a server labeled with the
service it serves, a client with its app — the model's names,
task rb4) and
`sequence/<ID>.svg` per conversation (lifelines in first-appearance
order; causal arrows solid, time-ordered dashed, unordered dotted
and marked `?`; file work as notes; every CSS class from a fixed
list, so no data writes attribute text).

Per-thread lifelines, thread stories and processes (task 0f4; files
in [implementation.md](../implementation.md), M5 "Threads and
processes"):

- **Lifelines**: a conversation's `lifelines` are its threads in
  first-appearance order — service, host, pid, pid incarnation,
  tid, thread name —
  and each step names its own and its peer's (`lane`, `to_lane`,
  `-1` for a file step): gateway thread X → orders thread Y →
  inventory thread Z. The far end of a hop no trace saw (an
  unpaired peer, an untraced caller) is a lifeline with a service
  only. A virtual thread's I/O, which the trace names by its
  carrier, is a lifeline of its own (`carrier: true`), never the
  carrier's platform-thread lifeline. `show sequence` lists the
  lifelines and names the peer's thread on each arrow;
  `sequence/<ID>.svg` draws one column per lifeline (service, and
  thread below; a carrier's lifeline dashed and marked
  `(virtual)`; a step whose lane index is out of range is not
  drawn, and the diagram counts it).
- **Thread stories** (`threads`): per lifeline with a thread,
  what it did across the kept conversations — `receive`, `send`,
  `response`, `reply`, `file` — each entry a conversation step
  (ID and number) with its ref. A platform lifeline is one kernel
  thread of one process incarnation on one host (a restarted
  process reusing pid and tid is another lifeline), so its entries
  are ordered by that host's clock only: `causal` between two
  entries of one request span the thread held alone (the `owned`
  rule above), `time-ordered` when the clock moved on, `unordered`
  on a tie. A carrier's virtual-thread lifeline is all
  `unordered`: an http event names the carrier the virtual thread
  ran on when it was emitted, but virtual threads migrate between
  carriers, so nothing proves the order of their steps on it. The
  newest 200 entries are kept (`more_entries`). `show thread`
  lists the stories; `show thread SERVICE/THREAD` prints every
  lifeline of that name (one per host, process incarnation and
  kernel thread, a carrier's virtual-thread lifeline beside its
  own) — the story as one line (`received GET /orders/42 from
  gateway => read /etc/orders.conf => … => replied 200 to
  gateway`; a carrier's says it has none), then entry by entry;
  `-json` prints the stories.
- **Processes** (`processes`, `more_processes`): projected from the
  run's rollup (`report.json`, task qb4), not a reading of their
  own — per process incarnation its host, pid, comm, app, in a pod
  its container (`container_id`, and `container`, the name from
  the pod's container map when collect recorded one, task jk4:
  shown `app (4f6c0f1e7b6c)`, the bare id without a name), the name
  it carries as a client lifeline, the services it serves, how
  many incarnations of its pid the run saw (a restart), its
  threads (events, bytes, calls, and `carried` / `carried_calls`:
  the virtual threads' I/O a carrier ran, counted apart), files
  (bytes, ops) and sockets (merged conn id, fd, role, addresses,
  service, peers), with the rollup's caps. Past 32 files the kept
  ones are chosen by the work done on them — written or changed,
  then read, then only opened, then only failed on — and listed in
  first-seen order (task vg4: a JVM traced from its exec probes and
  opens ~90 library paths first, and a first-seen cut hid the
  service's own config and records) — a ranked selection, not a
  prefix of the files in the order they were opened. `show
  processes` prints them, `-json` the list. The model's
  `untracked` is the rollup's count of events past its tracking
  cap (task 2i4): when it is not zero, `show processes`, the TUI's
  process detail and the site's processes screen say the list is
  partial.

V3 as built (task nb4; files in [implementation.md](../implementation.md),
M5 "Local site"): every `build` writes the run's site folder
(above) after the model and the exports, and prints its page as
the `site` line; `iormeshctl open` opens the newest. The screens
render the model only — the same `model.json` `show` reads,
embedded whole:

- **Mesh**: services as boxes ranked left to right by the longest
  client → server path, edges as curves (width by connections;
  unresolved dashed, mixed in the warning colour; a client-only
  service's box dashed). Drag pans, the wheel and `+`/`−` zoom,
  `fit` resets. Clicking a service lists its endpoints, TLS, top
  paths, edges, processes and conversations; clicking an edge
  shows its state, counts and the request steps that crossed it,
  and links its client and server — following a connection to its
  other side, across hosts. The host/process level the sketch
  above toggles to is the Processes screen instead.
- **Conversations** and the **sequence** view: one column per
  lifeline (service, thread; a carrier's virtual-thread lifeline
  dashed), arrows solid/dashed/dotted for causal/time-ordered/
  unordered (`?` on the last), file work as notes, pan and zoom
  as on the mesh. A step opens its detail: kind, service, host,
  thread, peer, bytes, latency, ordering, hop, the Java stack top
  first, links to both threads' stories, and `iormeshctl query
  -ref …` with a copy button (the clipboard when the browser
  grants it from `file://`, else the text selected). The static
  `sequence/<ID>.svg` is linked.
- **Threads** (the stories, ordered by the thread's own host's
  clock), **Processes** (threads with carried virtual-thread I/O
  apart, files, sockets — a socket's service links to the mesh)
  and **Health** (lost, skipped, unattributed, attach gap,
  requests cut, sessions and how they ended, clock bounds, bad
  segments, ignored entries).
- **Filter bar** on every list and the mesh: host (the run's
  hosts, i.e. the store's host directories; a conversation is the
  directory's when one of its steps' refs starts with
  `<project>/<host dir>/`, a thread when one of its own entries'
  refs does (send, reply, file: a receive or response entry
  carries the peer's ref), so two directories sharing one FQDN —
  cloned VMs, a host that only knows itself as `localhost` — never
  show each other's; one without such refs falls back to its
  FQDNs, matched through the sessions that link a directory to
  its FQDN; a mesh service is the directory's when a process of it
  runs there, when an endpoint's whole address is the directory or
  one of its FQDNs (never a prefix of it), or when a loopback or
  link-local endpoint's `@<host id>` scope is the host id
  (`/etc/machine-id`, a pod's own id) of one of the directory's
  sessions), text (service, thread, pid, path,
  method, a stack frame, a ref) and a time window on the
  conversations' start (`HH:MM[:SS]` against the time of day, or
  RFC 3339). The model has no pod yet (M7); the text filter
  matches host and service names meanwhile.
- `#/selftest` renders every route of the model in turn and
  prints `selftest: ok N routes`, or the routes that threw; it
  also selects each host of the filter and lists what it shows
  (conversations, threads, mesh services), which the browser test
  compares with lists pinned for its sample model, and requires
  nothing for a host the run lacks.

Tests: golden files on the serialized model and on every `show`
view; `internal/site` tests the page itself — byte-identical
renders and folder names, the CSP (`default-src 'none'`, exactly
the inline script's and stylesheet's hashes, nothing else), no
remote URL and no markup-writing or network API in the script,
a model of hostile strings (closing tags, `<!--`, U+2028/9, an
escape sequence, a bidi override, invalid UTF-8) kept inside its
data block and parsing back, runs kept and `latest` refusing to
point outside; `internal/ctl` that build writes a site, open
finds it, a rebuild rewrites the same folder and changed data
adds one, and that the page embeds exactly `model.json`. One
headless browser test loads that hostile site from `file://` at
`#/selftest` and fails on any console message (an exception, or a
CSP refusal — how a request or an injected script would show):
opt-in, as `mage test` must not need a browser
([testing.md](../testing.md), "Local site"). The TUI through
`teatest` at several terminal sizes (built: model-level frames at
eight sizes, an escape-injection check, the walk, and one
`teatest` program run; the microservices walk's frames are
goldens in `internal/ctl/testdata/tui_*.golden`).
