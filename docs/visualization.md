# Visualization — exploring a consolidated mesh

How the result of a consolidation run is shown. Companion to
[PLAN.md](../PLAN.md) (M4, M5, decision #11) — the plan is the
source of truth; this doc is the design.

A *consolidation run* is `iormeshctl build`: it merges the
collected trace segments and rebuilds the graph. (`collect` only
syncs segments from the servers; `refresh` runs collect, then
build.) Every build ends with three things to look at, all made
from the same data:

| Surface | Command | For |
|---------|---------|-----|
| Local site (HTML/JS/SVG) | `iormeshctl open` | exploring with a mouse: zoom, click, follow |
| Terminal UI (Bubble Tea) | `iormeshctl tui` | exploring over SSH or without a browser |
| Plain text | `iormeshctl show …` | reading, grepping, piping, pasting into an LLM |

`build` itself prints the plain-text summary and the path of the
site, so there is always something on screen when it finishes.
With `refresh`, the first line comes from the collect half:

```
$ iormeshctl refresh --project shop
collected  3 hosts · 41 segments · 1.2M events · 12:00:00–12:07:31
merged     4 services · 7 edges (6 fused, 1 unresolved)
health     0 drops · 2.1% unattributed · clock bounds ≤ 180µs
unresolved orders → 10.20.3.7:5432  (try: iormeshctl expand)

site  ~/.local/share/iormesh/sites/shop/latest/index.html
      iormeshctl open   ·   iormeshctl tui   ·   iormeshctl show
```

## One view model, three renderers

The three surfaces must never disagree. So none of them reads the
merged graph directly: the consolidation run builds one **view
model**, and each surface only renders it.

```
merged graph + events ──► view model ──┬─► site  (data files + JS)
(internal/federate)   (internal/viewmodel) ├─► TUI   (Bubble Tea)
                                          └─► text  (show, build)
```

```go
// Package viewmodel is everything a human-facing surface may
// show. It is computed once per consolidation run, is fully
// sorted, and holds no reference back into the raw trace —
// detail beyond it is fetched through internal/query.
package viewmodel

type Model struct {
    Run           Run            // project, hosts, time range, counts
    Health        Health         // drops, unattributed share, clock
                                 //   bounds, how each session ended
    Services      []Service      // named or host:port, sorted
    Edges         []Edge         // service → service, with stats
    Conversations []Conversation // one per traced request chain
    Processes     []Process      // per host: threads, files, sockets
}

type Edge struct {
    From, To    string
    State       string // fused | unresolved
    TLS         bool
    Protocol    string // http/1.1 | http/2 | tcp
    Connections int
    Requests    int
    Bytes       uint64
    P50, P99    time.Duration
    TopPaths    []PathStat // HTTP method + path template, by count
}

// Conversation is one line of the end-goal picture: a request
// entering the mesh and everything it caused, in happens-before
// order.
type Conversation struct {
    ID    string
    Start time.Time
    Steps []Step
}

type Step struct {
    Kind     string // request | response | file | note
    Service  string
    Thread   string
    To       string        // peer service, for request/response
    Label    string        // "GET /orders/{id}", "read orders.conf"
    Bytes    uint64
    Latency  time.Duration
    Ordering string        // causal | time-ordered
    Stack    []string      // class#method(file:line), top first
    Ref      string        // query handle for the raw events
}
```

Rules:

- **Sorted and deterministic.** The same merged graph yields the
  same model, byte for byte when serialized. Golden tests sit on
  the model, so all three surfaces are covered by one set.
- **Bounded.** The model is a summary: every list has a cap (top
  N paths per edge, at most N conversations per edge, newest
  first) and says so when it cuts (`truncated: 412 more`). Raw
  events never go into it.
- **Detail on demand.** Each step carries a `Ref`. The TUI and
  `show` resolve it through `internal/query`; the static site
  cannot run a query, so it shows the ready-made command
  (`iormeshctl query --ref …`) with a copy button.
- **Honest.** `unresolved`, `time-ordered`, `attribution: none`
  and `carrier` are fields of the model, so every surface has to
  show them — a renderer cannot quietly draw a guess.

## The local site

A folder of static files, written at the end of every `build`:

```
~/.local/share/iormesh/sites/<project>/
  latest -> 2026-11-18T12-07-31Z/
  2026-11-18T12-07-31Z/
    index.html          # shell, inline CSS
    app.js              # the explorer, one file, no dependencies
    data/model.js       # window.IORMESH.model = {…}
    data/conv-0001.js   # conversation chunks, loaded on demand
    graph.svg           # deterministic overview (also standalone)
    sequence/…svg       # deterministic sequence diagrams
```

Design decisions:

- **No server, opens from `file://`.** That rules out `fetch()`
  of JSON, so data ships as `.js` files that assign into
  `window.IORMESH` and are loaded with script tags —
  conversations lazily, one chunk at a time.
- **No dependencies, no network.** Hand-written JavaScript and
  SVG, embedded in the `iormeshctl` binary with `go:embed`; no
  CDN, no build step, no npm. A `Content-Security-Policy` meta
  tag forbids every remote origin: the site contains production
  metadata and must not be able to leak it.
- **Kept, not overwritten.** Each run gets its own folder, so two
  runs can be opened side by side; `latest` points at the newest.
  Like the traces, old sites stay until the operator deletes
  them.
- **Deep links.** The URL hash holds the whole view state
  (`#/conv/0042/step/7`), so a link to a specific step can be
  pasted to a colleague who has the same folder.

Screens:

1. **Mesh** — the service graph: services as nodes, edges
   weighted by request count, TLS marked, unresolved edges
   dashed and ending in a host:port stub. Pan, zoom, click.
   A toggle drops one level down to hosts and processes.
2. **Edge panel** — click an edge: connections, requests,
   bytes, latency, top HTTP paths, and its conversations.
3. **Sequence** — one conversation as a sequence diagram:
   lifelines per service, arrows per request and response, each
   thread's file operations as notes on its lifeline.
   Time-ordered (non-causal) arrows are drawn dashed. Click
   anything for the detail panel: Java stack, thread, sizes,
   latency, OTEL ids, the `query` command for the raw events.
4. **Process** — one process: threads, open files, sockets,
   restarts (PID incarnations).
5. **Health** — what the run could not see: ring-buffer drops,
   attach gaps, unattributed and carrier-flagged I/O, clock
   bounds per host pair, how each tracer session ended.

Everywhere: a filter bar (service, host, thread, path, time
range) and text search.

The static `graph.svg` and `sequence/*.svg` are the deterministic
exports PLAN.md already requires; the interactive screens draw
the same model with the same layout rules, so a screenshot and
the export match.

## The terminal UI

`iormeshctl tui` — Bubble Tea, with the same libraries and
versions ior uses (`charm.land/bubbletea/v2`, `lipgloss/v2`,
`bubbles/v2`), all pure Go, so the controller stays cgo-free.
ior's TUI rules apply as written in its AGENTS.md: all-pointer
model methods, side-effect-free `Init`, never render wider or
taller than the terminal, tests through `teatest`.

```
 iormesh · shop · 12:00:00–12:07:31 · 4 services · 7 edges        ? help
┌ Services ─────────────┐┌ gateway → orders ──────────────────────────┐
│ ▸ gateway        3 ▸  ││ fused · TLS · http/1.1                     │
│   orders         2 ▸  ││ 1 204 requests · p50 3.1ms · p99 41ms      │
│   inventory      1 ▸  ││                                            │
│   billing        0    ││ GET  /orders/{id}            1 010         │
│   10.20.3.7:5432 ?    ││ POST /orders                   194         │
│                       ││                                            │
│                       ││ Conversations (newest first)               │
│                       ││ ▸ 12:07:29.114  GET /checkout   4 hops     │
│                       ││   12:07:28.902  GET /checkout   4 hops     │
└───────────────────────┘└────────────────────────────────────────────┘
 ↑↓ move · ⏎ open · tab pane · / filter · s sequence · h health · q quit
```

The sequence screen draws lifelines in text; a thread's file work
sits on its own lifeline:

```
 gateway            orders             inventory
 http-1             worker-3           worker-1
   │── GET /orders/42 ─▶│                  │
   │                  │ read orders.conf   │
   │                  │── GET /stock/7 ───▶│
   │                  │◀── 200 · 1.2ms ────│
   │                  │ create order-42.json.tmp
   │                  │ rename → order-42.json
   │◀── 200 · 9.8ms ──│                    │
   ┆┄┄ async ┄┄┄┄┄┄┄┄▶┆   (time-ordered, not causal)
```

Screens mirror the site: Mesh (as the table above — a graph does
not fit a terminal well), Edge, Sequence, Process, Health.
`⏎` on a step opens the detail pane with the Java stack; `e`
writes the current view as text to a file.

## Plain text

`iormeshctl show` prints one view and exits. It is what `build`
prints, what gets piped, and what the LLM eval feeds on.

```
iormeshctl show                       # the summary build prints
iormeshctl show services
iormeshctl show edges [--unresolved]
iormeshctl show sequence [CONV-ID]    # the text diagram above
iormeshctl show thread SERVICE/THREAD # one thread's story, one line
iormeshctl show processes             # per host: threads, files, sockets
iormeshctl show health
iormeshctl show report                # the summary, then the rollup
```

- Colors and box characters only when stdout is a terminal;
  plain ASCII with `--ascii` or when piped.
- `--json` prints the view model's JSON for the same view.
- The output is deterministic, so text goldens work.

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
[implementation.md](implementation.md), M4 "Build, refresh and
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

Differences from the sketch above, until M5's renderers land:
the text is plain (no colors, `->` for arrows); an edge between
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
requests in order, top methods ([schema.md](schema.md#summary-rollup-m5)).
The rollup is no fourth reading of the data for the three
surfaces: it is the export an LLM reads first (PLAN.md M5). (Lines cut
with … here; `internal/ctl/testdata/build_summary.golden` has
them whole.) Every string from the traces is printed
terminal-safe: one holding a control character comes out
Go-quoted, never as an escape sequence.

V2 as built (task mb4; files and passes in
[implementation.md](implementation.md), M5 "Conversations and
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
(`more_steps`), 2¹⁸ http events read (the newest by completion),
2²⁰ followed fds.

`show conversations` lists them; `show sequence [ID]` (the newest
without an ID) prints one as numbered steps — ordering, actor
(`service/thread`), arrow, label, bytes, latency, a hop that is not
`paired` — each with its top Java frame and its ref, every string
terminal-safe; `-json` prints the conversation. The summary gains a
`sequence` line counting conversations and their time-ordered and
unordered steps. `build` also writes the deterministic SVG exports
(`internal/render`): `graph.svg` (apps, one column per host — host,
processes, files — and the connections; unresolved connections
dashed, 200 nodes a column, the rest counted) and
`sequence/<ID>.svg` per conversation (lifelines in first-appearance
order; causal arrows solid, time-ordered dashed, unordered dotted
and marked `?`; file work as notes; every CSS class from a fixed
list, so no data writes attribute text).

Per-thread lifelines, thread stories and processes (task 0f4; files
in [implementation.md](implementation.md), M5 "Threads and
processes"):

- **Lifelines**: a conversation's `lifelines` are its threads in
  first-appearance order — service, host, pid, tid, thread name —
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
  `(virtual)`).
- **Thread stories** (`threads`): per lifeline with a thread,
  what it did across the kept conversations — `receive`, `send`,
  `response`, `reply`, `file` — each entry a conversation step
  (ID and number) with its ref. All of a lifeline's entries
  happened on one kernel thread of one host, so they are ordered
  by that host's clock only: `causal` between two entries of one
  request span the thread held alone (the `owned` rule above),
  `time-ordered` when the clock moved on, `unordered` on a tie. A
  carrier's lifeline has no owned span, so its entries are never
  causal. The newest 200 entries are kept (`more_entries`).
  `show thread` lists the stories; `show thread SERVICE/THREAD`
  prints every lifeline of that name (one per host, process and
  kernel thread, a carrier's virtual-thread lifeline beside its
  own) — the story as one line (`received GET /orders/42 from
  gateway => read /etc/orders.conf => … => replied 200 to
  gateway`), then entry by entry; `-json` prints the stories.
- **Processes** (`processes`, `more_processes`): projected from the
  run's rollup (`report.json`, task qb4), not a reading of their
  own — per process incarnation its host, pid, comm, app, the name
  it carries as a client lifeline, the services it serves, how
  many incarnations of its pid the run saw (a restart), its
  threads (events, bytes, calls, and `carried` / `carried_calls`:
  the virtual threads' I/O a carrier ran, counted apart), files
  (bytes, ops) and sockets (merged conn id, fd, role, addresses,
  service, peers), with the rollup's caps. `show processes` prints
  them, `-json` the list.

Tests: golden files on the serialized model and on every `show`
view; the site's `data/*.js` are goldens too, and one headless
browser test loads `index.html` from `file://` and checks that
each screen renders without a console error or a network
request; the TUI through `teatest` at several terminal sizes.

## See also

- [Output contract](output-contract.md) — the data underneath.
- [Implementation guide](implementation.md) — M4 and M5.
