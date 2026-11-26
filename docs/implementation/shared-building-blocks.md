# Shared building blocks

Part of [Implementation guide](../implementation.md).

These types are used by every package after M1. They live in
`internal/schema` (cgo-free, landed with M1 as the v1 *draft*) and
are the Go form of output-contract.md. The package
is the reference; the sketches below show its shape.

## The event envelope

```go
// Package schema is the trace output contract: one Event type,
// two serializations (NDJSON and parquet). Fields are only ever
// appended — a reader of an older file sees the new ones as
// absent, never as a different meaning (ior's parquet rule).
package schema

const Version = 1

type Event struct {
    SchemaVersion uint32 `json:"schema_version" parquet:"schema_version"`
    Project       string `json:"project" parquet:"project,dict"`
    Seq           uint64 `json:"seq" parquet:"seq"`
    BootNS        uint64 `json:"boot_ns" parquet:"boot_ns"`
    WallNS        uint64 `json:"wall_ns" parquet:"wall_ns"`
    HostID        string `json:"host_id" parquet:"host_id,dict"`
    PID           uint32 `json:"pid" parquet:"pid"`
    PIDInc        uint64 `json:"pid_incarnation" parquet:"pid_incarnation"`
    TID           uint32 `json:"tid" parquet:"tid"`
    Comm          string `json:"comm" parquet:"comm,dict"`
    Event         string `json:"event" parquet:"event,dict"`
    FD            int32  `json:"fd" parquet:"fd"`
    FDGen         uint32 `json:"fd_gen" parquet:"fd_gen"`
    Ret           int64  `json:"ret" parquet:"ret"`
    Err           string `json:"err,omitempty" parquet:"err,dict"`
    LatencyNS     uint64 `json:"latency_ns" parquet:"latency_ns"`
    Bytes         uint64 `json:"bytes" parquet:"bytes"`
    Args          Args   `json:"args" parquet:"args"`
    ConnID        string `json:"conn_id,omitempty" parquet:"conn_id,optional"`
    Attribution   string `json:"attribution" parquet:"attribution,dict"`
    Java          *Java  `json:"java,omitempty" parquet:"java,optional"`
    HTTP          *HTTP  `json:"http,omitempty" parquet:"http,optional"`
    Session       *Session `json:"session,omitempty" parquet:"session,optional"`
}

// Args holds every syscall-specific argument as an optional
// field: one nullable parquet column per field, never a JSON
// blob, so queries can filter on any of them.
type Args struct {
    Path       *string `json:"path,omitempty" parquet:"path,optional"`
    NewPath    *string `json:"new_path,omitempty" parquet:"new_path,optional"`
    Flags      *uint32 `json:"flags,omitempty" parquet:"flags,optional"`
    Offset     *int64  `json:"offset,omitempty" parquet:"offset,optional"`
    LocalAddr  *string `json:"local_addr,omitempty" parquet:"local_addr,optional"`
    LocalPort  *uint16 `json:"local_port,omitempty" parquet:"local_port,optional"`
    RemoteAddr *string `json:"remote_addr,omitempty" parquet:"remote_addr,optional"`
    RemotePort *uint16 `json:"remote_port,omitempty" parquet:"remote_port,optional"`
    SockState  *string `json:"sock_state,omitempty" parquet:"sock_state,optional"`
    Marker     *string `json:"marker,omitempty" parquet:"marker,optional"`
    Lost       *uint64 `json:"lost,omitempty" parquet:"lost,optional"`
    EndReason  *string `json:"end_reason,omitempty" parquet:"end_reason,optional"`
}
```

Field notes:

- `host_id` is `/etc/machine-id` (in a pod, since M7, `pod-<uid>`:
  M7 step 6); the session header's optional
  `session` object (`schema.Session`) adds boot id, FQDN, kernel
  release and arch.
- `pid_incarnation` is the task's start time on the boot clock
  (`/proc/<pid>/stat` starttime, clock-tick resolution) — unique
  per PID reuse more than one clock tick apart. It is read when the session takes the process
  on (see "Envelope" below), not at its first record.
- `seq` is per host, starts at 1 and has no gaps but explicit
  `drop` events; 0 means "never assigned" and is rejected.
- `fd` is the fd the event acts on: the argument, or for open*
  and accept* the returned one; `-1` (`schema.NoFD`) when there
  is none — synthetic kinds, a failed open or accept.
- `fd_gen` counts how often this fd number was (re)created in this
  process incarnation: +1 on *every* fd-creating syscall landing
  on it — open*, accept*, socket, dup*, pipe, … — so the
  correlator must see those syscalls even where it emits no
  event for them. Otherwise a library's open and a later
  `socket()` on the same number (tcp-loopback's client `$fd6`)
  would share one (pid, fd, fd_gen). `0` means the fd predates
  the trace, or there is no fd.
- `err` is the errno name and is only set when `ret < 0`.
- `event` is the syscall name, or one of the synthetic kinds:
  `tcp_state`, `marker`, `drop` (ring-buffer loss), `session`
  (header), `end` (why the trace ended).
- `attribution` is `"java"` or `"none"` — never empty. A
  `carrier` flag (added with M3) marks I/O that ran for a
  virtual thread, which v1 attributes to the carrier.
- `Java` (`thread`, `stack` of method ids; set exactly when
  `attribution` is `java`) is settled by S1 (thread name from the
  agent's `THREAD` record, stack ids from its `STACK` records);
  `HTTP` was provisional until M3's acceptance froze it. M1
  assigns `conn_id` locally to fused loopback pairs;
  across hosts the merge assigns it from the ISN-pair join (S2,
  decision #26). The Java fields are nested
  (`java.thread`, `java.stack`) — settled by S1; the envelope sketch's flat
  `java_thread`/`java_stack` were a sketch. `HTTP` was
  provisional until M3's acceptance froze it.
- `schema.Sink` (`Event(*Event) error`, `Close() error`) is what
  the correlator writes to; the NDJSON and parquet writers
  implement it. `Event` borrows the event for the call — a sink
  that keeps it copies it (task ud4: the ~400-byte event was
  copied once per pipeline hop by value).

Reading is strict: `schema.DecodeEvent` rejects unknown keys at
any depth — matched case-sensitively, since encoding/json alone
would let `"Comm"` overwrite `"comm"` — required keys (every
field without `omitempty`) that are missing or `null`, trailing
data, a `schema_version` other than `Version`, an unknown
`event` kind, a bad `attribution` (`java` needs a non-empty
`java.stack`), and a kind missing the argument it cannot be read
without (open* → non-empty `path`, `marker` → `marker`,
`tcp_state` → `sock_state`, `drop` → `lost` > 0, `end` → a known
`end_reason`, `session` → a complete `session` object, which no
other kind may carry). The kind list only grows: M2
appends its syscalls there.

One NDJSON line, as M1 emits it:

```json
{"schema_version":1,"project":"demo","seq":412,"boot_ns":91822003417,
 "wall_ns":1794953012000112233,"host_id":"3f2a…","pid":4711,
 "pid_incarnation":91700000000,"tid":4719,"comm":"java",
 "event":"openat","fd":7,"fd_gen":1,"ret":7,"latency_ns":18211,
 "bytes":0,"args":{"path":"/etc/orders.conf","flags":0},
 "attribution":"none"}
```

## The graph

```go
// Graph is the merged, queryable form: built per host by the
// correlator (M1), fused across hosts by the merge (M4).
type Graph struct {
    SchemaVersion uint32 `json:"schema_version"`
    Nodes         []Node `json:"nodes"` // sorted by (Type, ID)
    Edges         []Edge `json:"edges"` // sorted by (Type, From, To)
}

type Node struct {
    Type  string            `json:"type"` // host|process|file|socket, +later
    ID    string            `json:"id"`   // stable, see below
    Attrs map[string]string `json:"attrs,omitempty"`
}

type Edge struct {
    Type  string            `json:"type"` // runs_on|opened|connected|…
    From  string            `json:"from"`
    To    string            `json:"to"`
    State string            `json:"state,omitempty"` // fused|unresolved
    Attrs map[string]string `json:"attrs,omitempty"`
}
```

Node ids are derived, never counters: `host:<host_id>`,
`proc:<host_id>/<pid>@<pid_incarnation>`, `file:<host_id>:<path>`
(a colon, so an absolute path never makes a double slash),
`conn:<conn_id>`. A `socket` node is one connection — the socket
pair, not one end — and carries the `conn:` id, so both
processes of a fused pair point at the same node. Edge shapes:
`runs_on` process → host, `opened` process → file, `connected`
process → socket with `state` `fused` (both ends traced and
joined) or `unresolved` (one end seen), and from M2 `incarnation`
app → process and `forked` process → process; no other edge
carries a state. Slices are sorted (`Graph.Sort`) before every export; Go
map iteration order must never reach an output file.
`nodes` and `edges` are always arrays, never `null` (the encoder
writes `[]` for an empty graph). `schema.DecodeGraph` and
`Graph.Validate` reject unsorted or duplicate nodes and edges,
ids without their type's prefix (or a file id without
`<host>:<path>`), and edges to missing nodes or nodes of the
wrong type.

## Expectations (the golden format)

A scenario's expectation file sits next to its source in
`workload/scenarios/<name>/expect.json`. Symbolic ids (`$p1`,
`$t1`, `$fd1`, `$port1`, `$h1` for hosts, `$c1` for
connections) bind on first match;
the checker then requires consistency.

The shipped `file-basic` golden (write, close, reopen, read):

```json
{"scenario": "file-basic",
 "schema_version": 1,
 "events": [
   {"thread": "$t1", "event": "openat",
    "args": {"path": "$tmp/file-basic.dat", "flags": 577}, "fd": "$fd1"},
   {"thread": "$t1", "event": "write", "fd": "$fd1", "bytes": 4096},
   {"thread": "$t1", "event": "close", "fd": "$fd1"},
   {"thread": "$t1", "event": "openat",
    "args": {"path": "$tmp/file-basic.dat", "flags": 0}, "fd": "$fd2"},
   {"thread": "$t1", "event": "read", "fd": "$fd2", "bytes": 4096},
   {"thread": "$t1", "event": "close", "fd": "$fd2"}
 ],
 "edges": [
   {"type": "opened", "from": "$p1", "to": "file:$h1:$tmp/file-basic.dat"}
 ],
 "must_not_appear": []}
```

Events of one `thread` must appear in the listed order; events of
different threads are unordered against each other.

Clarifications the M0 goldens rely on (file-basic and
tcp-loopback ship real ones):

- `fd` is the fd the event acts on: the argument, or for
  `openat` and `accept` the returned one. An fd symbol binds to
  the fd *instance* (pid, fd, `fd_gen`), so two opens that reuse
  one number — as FileOutputStream then FileInputStream do —
  take two symbols (`$fd1`, `$fd2`).
- Multi-process scenarios add `"process": "$p1"` next to
  `thread`, binding the same `$p` symbols the edges use.
- Declare only syscalls the target milestone traces (M1: open,
  read, write, close, connect, accept) and ports rather than
  addresses: the JVM's dual-stack sockets show 127.0.0.1 as
  `::ffff:127.0.0.1` unless the host lacks IPv6 (the normalizer
  unmaps it to `127.0.0.1` anyway, see M1 step 8). M2's
  `tcp-ipv4`, `tcp-ipv6` and `udp-loopback` do declare
  addresses: their NIO channels are opened with an explicit
  protocol family, so each socket has exactly one.
- A kind's second fd (`pipe2`'s other end, `epoll_ctl`'s
  target) is `args.fd2`, an `$fd` symbol bound to the instance
  like `fd` (M2 step 7); a `dup2`'s returned fd is still declared
  through the events that use it, as `fd-lifecycle` does for its
  `dup`. `args.signal` compares a `kill`'s or `signal`'s number.
- `flags` are compared by access mode plus the declared bits, so
  577 means "write-only, create, truncate" and tolerates an extra
  `O_CLOEXEC`; `0` matches any read-only open (M1 step 9).
- `attribution` (`"java"` or `"none"`), when declared, must equal
  the event's; unset, it is not compared. `non-java-peer`
  declares `none` on every event of its Go peer.
- M3's agent fields: `"attribution": "java"`, `"java":
  {"thread": "<Java thread name>", "method": "<class>#<method>"}`
  (the causing method: the innermost workload frame),
  `{"event": "http", "conn_id": "$c1", "http": {"method",
  "scheme", "host", "path", "status", "req_seq"}}` events, and
  `"otel": {"trace_id", "span_id"}` (with attribution java).
  `otel` is compared when absent too, like `args.count`: an
  event declared without it must be outside any span. The M3
  scenarios first carried these in a second file,
  `expect-pending.json`, until the agent could produce them
  (M3 step 9); task 4b4 promoted the last one, and a test now
  fails on any new pending file.
- `$a1` names an app node — the one node every incarnation of a
  pattern-matched app links to (M2 step 3). Only edges use it;
  it binds the whole node id, so `restart`'s two `incarnation`
  edges from one `$a1` assert one app identity, whatever the
  id's format turns out to be.
