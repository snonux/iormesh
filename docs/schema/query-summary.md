# Query slices and summary rollup

Part of [Trace schema (v1, frozen)](../schema.md).

## Query slices (M5)

`iormeshctl query` (task pb4; [implementation.md](../implementation.md),
M5 "Query") writes the events of the merged trace a filter selects
as NDJSON lines of this schema — each one decodes strictly as an
event — in store order, with two differences from a trace's
`events.ndjson`:

- `conn_id` is the merged trace's id (the merge's renames applied),
  so both ends of a fused connection carry the same one.
- Non-printable runes the JSON encoding leaves raw (C1 controls,
  DEL, bidi and other format runes) are written as `\u` escapes:
  the same string, safe on a terminal.

A slice is capped (`-max-bytes`, the record below included). When
the cap cuts it, the last line is the truncation record, which is
not an event:

```json
{"schema_version":1,"event":"truncated","args":{"emitted":4,"remaining":9}}
```

`emitted` counts the event lines above it, `remaining` the
matching events the cap left out. The lines written are always the
first matches in order, never a selection; without the record the
slice is complete. The order is the store's (project, host
directory, session, `seq`), not time's, so a cut keeps the hosts
that sort first. A reader checks the last line for
`"event":"truncated"` before decoding lines strictly as events: the
record lacks the envelope's required fields.

With `-frames` (decision #42, task an4), each line whose event has
a `java.stack` ends in one more member, `java_frames`: the frame
text of each id, `class#method(file:line)` in the stack's order
(innermost first), from the session's methods tables, and
`method#ID` for an id they lack (a table left out as damaged
leaves its ids so, and query exits 1 naming it). The member comes
after the event's own, so the rest of the line is byte for byte the
event's encoding; such a line is not a strict event until
`java_frames` is dropped. Lines without a stack, and every line
without the flag, are unchanged.

```json
{…,"attribution":"java","java":{"thread":"http-1","stack":[2,4]},
 "java_frames":["com.shop.Http#send(Http.java:7)","method#4"]}
```


## Summary rollup (M5)

`iormeshctl build` writes `report.json` beside `model.json` (task
qb4; milestones.md M5; output-contract.md "Summary rollup"): one
entry per traced process, the cheap context read before slicing the
events with `query`. `iormeshctl show report` prints the run summary
and then the rollup as text; `-json` prints `report.json`'s object.
The Go types are `internal/report`'s `Report`.

```json
{"processes": [{"id": "proc:h1/20@7", "project": "shop",
  "host": "app1", "fqdn": "h1.example.org", "pid": 20,
  "pid_incarnation": 7, "comm": "java", "app": "",
  "argv": ["java", "-jar", "w.jar", "--role", "orders"],
  "serves": ["orders"], "events": 17, "java_events": 16,
  "files": [{"path": "/o/order-42.json.tmp", "read_opens": 0,
    "write_opens": 1, "read_bytes": 0, "write_bytes": 36,
    "ops": ["rename -> /o/order-42.json"], "more_ops": 0,
    "methods": ["shop.DataStore#save"]}], "more_files": 0,
  "conns": [{"id": "m-c1", "fd": 6, "role": "server", "local": ":8001",
    "remote": "", "service": "orders", "peer_state": "traced",
    "peers": ["shop/app1/java[10]"], "sent_bytes": 33,
    "recv_bytes": 36, "requests": […], "more_requests": 0}],
  "more_conns": 0,
  "threads": [{"name": "http-1", "tid": 21, "events": 16,
    "bytes": 243, "calls": [{"dir": "in", "method": "POST", "scheme": "http",
    "path": "/orders", "status": 201, "conn": "m-c1",
    "service": "orders", "peers": ["shop/app1/java[10]"]}],
    "more_calls": 0}], "more_threads": 0,
  "methods": [{"method": "shop.DataStore#save", "events": 6,
    "bytes": 135}], "more_methods": 0}],
 "more_processes": 0, "untracked": 0}
```

- **Processes** are sorted by project, store host, pid and
  incarnation; host-level records (pid 0) are no process's. `app`
  is the target pattern of the session's `attach`, `argv` the last
  `exec`'s (redacted by the tracer) — the `attach`'s for a process
  running before the trace with no exec since — `serves` the endpoint labels
  it accepted connections on. `container_id`, absent outside a
  pod, is the `pod.container_id` its events carry; the view
  model names it (see [Pod fields on
  events](graph.md#pod-fields-on-events-m7)).
- **Files** are listed in the order the process first opened or
  changed them (past the cap, which ones stay is ranked: see
  Caps). Opens count by access mode (`O_RDONLY` vs.
  `O_WRONLY`/`O_RDWR`; `creat` writes); the bytes are the reads and
  writes on the fd instance an open returned, until its close.
  `ops` are the file-system changes by kind (`creat`, `mkdir`,
  `unlink`, `rmdir`, `rename -> NEW` / `renamed from OLD`, link,
  symlink, truncate, chmod, chown, utime, xattr changes) once each,
  ` (ERRNO)` when the call failed — a failed open is the op
  `open (ENOENT)`. An `open` with `O_CREAT` is a write open, not an
  op: the record does not say whether the file existed before.
  Metadata reads (stat, readlink, getxattr) are left out.
- **Methods** are causing methods by the goldens' rule
  (`normalize.CausingMethod`: the innermost frame of `java.stack`
  outside the JDK's packages), resolved through the session's
  methods tables; a stack the table cannot resolve names none.
- **Connections** are one entry per end: a process holding both
  ends of a connection (calling its own server over loopback) or of
  a pipe gets two, told apart by `fd`, each the other's peer. They
  carry the merged conn id (what `query -conn`
  takes), the role from `connect`/`accept`, the addresses this end
  saw, the server's label from the service database (its name,
  else `hostname:port`, else the address the client dialled), and
  the other end's processes when the rollup holds them
  (`peer_state` `traced`), else `untraced`.
- **Threads** keep their first-seen order; `calls` are the
  thread's http events in order, `in` when its process served the
  request, `out` when it made it. A served request's http event is
  written when its response completes, so the calls the server
  made for it come before it on the thread's line. A carrier
  thread's virtual threads' I/O (`java.carrier`) is counted in its
  `events` but also in `carried`, and such a call has
  `"carrier": true` (both omitted when false; task 0f4): the text
  says so, never letting it read as the carrier's own work.
- **Bytes** of a connection (`sent_bytes`, `recv_bytes`), a
  thread and a method are the bytes moved by the stream, not the
  bytes copied (task cl4): a `MSG_PEEK` receive (`args.peek`)
  counts as an event but adds no bytes, so a peek followed by the
  read that consumes the same bytes counts them once — on the
  connection, the thread and the method alike. A file's
  `read_bytes`/`write_bytes` never see a peek (files cannot be
  peeked).
- **Caps**: 256 processes, and per process 32 files, connections
  and threads, 32 calls per thread and requests per connection,
  10 methods (by events, then bytes), 8 ops per file; each cut is
  counted in its `more_*` field. Past 32 files the kept ones are
  ranked, not first-seen: files written or changed (a write open,
  written bytes, a file-system change that succeeded), then files
  read from, then files only opened, then files only failed on —
  first-seen first within a rank, listed in first-seen order (task
  vg4). A file lists its first 4 causing
  methods and does not count the rest. While reading, the collector
  tracks at most 2²⁰ entries at once; an open file fd holds one
  only until its close (task 2i4), so the cap bounds the state
  held, not the opens a long trace makes. An event that found no
  room for some part of it is counted once in `untracked`, and the
  text starts with a `partial` line, as do the processes views of
  `show processes`, the TUI and the site.
- Like the trace, the rollup is metadata only — paths, sizes,
  redacted request lines — and deterministic: the same store,
  database and methods tables give the same bytes.
