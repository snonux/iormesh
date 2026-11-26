# The local site, the terminal UI and plain text

Part of [Visualization — exploring a consolidated mesh](../visualization.md).

## The local site

A folder of static files, written at the end of every `build`
(as built by task nb4, `internal/site`):

```
~/.local/share/iormesh/builds/<projects>/sites/
  latest -> 20261118T120731Z-3f9c0a1b2c4d/
  20261118T120731Z-3f9c0a1b2c4d/
    index.html          # the explorer: CSS, script and model inline
    graph.svg           # deterministic overview (also standalone)
    sequence/<ID>.svg   # deterministic sequence diagrams
```

Design decisions:

- **No server, opens from `file://`, loads nothing.** That rules
  out `fetch()`, and from `file://` a script or stylesheet file
  next to the page is not reliably "same origin" either (Chrome
  treats `file:` as an opaque origin, so CSP `'self'` would not
  match it). So `index.html` is one self-contained file: the
  stylesheet and the script inline, the model as an inert
  `<script type="application/json">` block the script reads with
  `textContent` and `JSON.parse`. The earlier sketch's
  `data/*.js` chunks loaded on demand were dropped: the model is
  capped (100 conversations, 200 steps each, 12 frames a step),
  so it parses in one go.
- **No dependencies, no network.** Hand-written JavaScript and
  CSS (`internal/site/assets`), embedded in the `iormeshctl`
  binary with `go:embed`; no CDN, no build step, no npm, nothing
  vendored. A `Content-Security-Policy` meta tag says
  `default-src 'none'` and allows only the page's own inline
  script and stylesheet, by their sha256: no remote origin, no
  `'self'`, no `'unsafe-inline'`, no image, font, frame or
  connection. The site contains production metadata and must not
  be able to leak it.
- **Trace text is data, never markup.** Every string in the model
  came from a traced server (docs/security.md). The JSON block
  cannot close early — `encoding/json` writes `<`, `>`, `&`,
  U+2028 and U+2029 as `\u` escapes, and the page is refused if
  one slipped through — and the script puts strings on the page
  only through `textContent`, text nodes and SVG attributes it
  chooses, never `innerHTML` or string-built markup; control and
  bidi characters are shown as `\uXXXX`; a route built from an
  ID is `#/…` with each part URI-encoded; the `query -ref`
  command offered for copying is shell-quoted.
- **Deterministic.** Same model, same bytes: the folder is named
  after the run's last traced instant (`Run.Last`, a trace time —
  never the build machine's clock) and a hash of its files, so a
  rebuild over the same store rewrites the same folder byte for
  byte and new data makes a new one.
- **Kept, not overwritten.** Each run gets its own folder, so two
  runs can be opened side by side; `latest` points at the newest
  (swapped by rename, so it always names a whole run). Like the
  traces, old sites stay until the operator deletes them; a
  build never removes one. `iormeshctl open` prints the newest
  page's path and hands it to `xdg-open` (`open` on macOS); with
  no opener — over SSH, a browser-less host — the path is the
  answer.
- **Deep links.** The URL hash holds the view
  (`#/conv/0042/step/7`, `#/mesh/edge/3`, `#/thread/5`,
  `#/proc/2`, `#/health`), so a link to a specific step can be
  pasted to a colleague who has the same folder. The filter is
  not in the hash.

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
exports milestones.md M5 already requires; the interactive screens draw
the same model with the same layout rules, so a screenshot and
the export match.


## The terminal UI

`iormeshctl tui` — Bubble Tea, with the same libraries and
versions ior uses (`charm.land/bubbletea/v2` and `lipgloss/v2`;
`bubbles/v2` turned out unneeded, see below), all pure Go, so
the controller stays cgo-free.
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

V4 as built (task ob4; files in [implementation.md](../implementation.md),
M5 "Terminal UI"): `iormeshctl tui [-project NAME]... [-builds
DIR]` reads the build's `model.json` through the loader `show`
uses — no second reading of the data — and needs a terminal on
stdin and stdout; without one it exits 1 and points at `show`.
Seven screens, each a list and a detail pane (side by side from
100 columns, stacked below):

| Key | Screen | List | Detail |
|-----|--------|------|--------|
| `1` | services | the mesh as a table: conns, requests, TLS, unresolved | endpoints, top paths, edges in and out, newest conversations |
| `2` | edges | client -> server, state, conns | counts, first/last, conversations along it |
| `3` | conversations | ID, start, entry, request, hops | `show sequence`'s text |
| `4`/`s` | sequence | one step per row (`show sequence`'s step line) | the step: lifelines, ordering explained, hop, sizes, the whole Java stack, its ref and `iormeshctl query -ref REF` |
| `5` | threads | lifelines (a carrier's marked `virtual`) | `show thread`'s story |
| `6` | processes | host, comm[pid], service, restarts | `show processes`' block |
| `7`/`h` | health | — | the build summary, then `show health` |

`⏎` walks down: a service or an edge opens the conversations
scoped to it, a conversation its sequence, a step its thread's
story, a story its newest step's conversation; `esc` walks back
and drops the scope. `tab` moves the focus to the detail (to
scroll a long stack), `/` filters the current list
(case-insensitive substring; `⏎` keeps, `esc` clears), `?` help,
`q` quits. Differences from the sketch: the sequence is the
numbered step list `show sequence` prints, not a lifeline
drawing (it fits any width, and the arrows' names are the
lifelines'); the raw event is one `query -ref` away (the TUI
reads only the model, never the store); `e` is not built —
`iormeshctl show VIEW` writes any view as text; `bubbles` is not
needed (the list and the filter are a few lines each). The whole model
goes through `viewmodel.Safe` (termsafe) once on load, so a
server's escape sequence prints Go-quoted on every screen (a
reflection test fills every string of the model with one and
checks `Safe` left none). From the 20×6 minimum up a frame is
exactly the terminal's size — each line cut to its width (ANSI-
and wide-rune-aware) and padded, exactly its height in lines;
below the minimum it is one line saying the terminal is too
small. Frames are a pure function of model, size and keys. When
`⏎` has nothing to open (a step whose thread has no story, a
story whose conversation the model's cap dropped) the footer
says why until the next key.


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

- As built the text is always plain ASCII (`->` for arrows),
  terminal or pipe. Colors and box characters on a terminal,
  and an `--ascii` switch, were sketched but are not built.
- `--json` prints the view model's JSON for the same view.
- The output is deterministic, so text goldens work.
