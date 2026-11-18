# Visualization — exploring a consolidated mesh

How the result of a consolidation run is shown. Companion to
[PLAN.md](../PLAN.md) (M4, M5, decision #11) — the plan is the
source of truth; this doc is the design.

A *consolidation run* is `iormeshctl collect`: it syncs the trace
segments, merges them, and rebuilds the graph. Every such run ends
with three things to look at, all built from the same data:

| Surface | Command | For |
|---------|---------|-----|
| Local site (HTML/JS/SVG) | `iormeshctl open` | exploring with a mouse: zoom, click, follow |
| Terminal UI (Bubble Tea) | `iormeshctl tui` | exploring over SSH or without a browser |
| Plain text | `iormeshctl show …` | reading, grepping, piping, pasting into an LLM |

`collect` itself prints the plain-text summary and the path of the
site, so there is always something on screen when it finishes:

```
$ iormeshctl collect --project shop
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
                                          └─► text  (show, collect)
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

A folder of static files, written at the end of every `collect`:

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

`iormeshctl show` prints one view and exits. It is what `collect`
prints, what gets piped, and what the LLM eval feeds on.

```
iormeshctl show                       # the summary collect prints
iormeshctl show services
iormeshctl show edges [--unresolved]
iormeshctl show sequence [CONV-ID]    # the text diagram above
iormeshctl show thread SERVICE/THREAD # one thread's story, one line
iormeshctl show health
```

- Colors and box characters only when stdout is a terminal;
  plain ASCII with `--ascii` or when piped.
- `--json` prints the view model's JSON for the same view.
- The output is deterministic, so text goldens work.

## Build order

| Step | Lands with | What |
|------|-----------|------|
| V1 | M4 | `internal/viewmodel` (run, health, services, edges); `show` and the `collect` summary |
| V2 | M5 | conversations in the model; `show sequence`; deterministic SVG exports |
| V3 | M5 | the local site, written by every `collect` |
| V4 | M5 | the TUI |

V1 goes first on purpose: from the first working `collect`, a run
ends with readable output, and the model's shape is settled
before two UIs are built on it.

Tests: golden files on the serialized model and on every `show`
view; the site's `data/*.js` are goldens too, and one headless
browser test loads `index.html` from `file://` and checks that
each screen renders without a console error or a network
request; the TUI through `teatest` at several terminal sizes.

## See also

- [Output contract](output-contract.md) — the data underneath.
- [Implementation guide](implementation.md) — M4 and M5.
