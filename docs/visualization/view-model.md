# One view model, three renderers

Part of [Visualization — exploring a consolidated mesh](../visualization.md).

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
