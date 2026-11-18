# Trace schema (v1 draft)

The field-level reference of the trace output. The contract's
goals and guarantees are in [output-contract.md](output-contract.md)
and PLAN.md ("Trace output contract"); this file says what is on
disk. The Go form — and the authority when this file lags — is the
cgo-free package `internal/schema`.

Version 1 is a **draft** until spikes S1 (Java ↔ syscall join) and
S2 (cross-host join) report; the fields they own are marked
*provisional*. Fields are only ever appended, and appended fields
are optional, so an older reader never sees a changed meaning.

## Files

| File | Format | Written by |
|------|--------|------------|
| `events.ndjson` | one event per line | tracer (`schema.NDJSONSink`) |
| `graph.json` | one graph object, indented | tracer at exit (`schema.WriteGraphFile`) |
| `*.parquet` | the same events, columnar | tracer segments (M4, to come) |

**NDJSON framing**: UTF-8, one JSON object per line, `\n`
terminated, no blank lines, keys in the order of the table below
(byte-stable for the same event). Lines are at most 1 MiB.
Events appear in `seq` order.

**graph.json** is written atomically (temp file + rename) with
nodes sorted by `(type, id)` and edges by `(type, from, to)`, so
the same graph is the same bytes.

**Readers are strict** (`schema.DecodeEvent`, `ReadNDJSON`,
`DecodeGraph`): an unknown or mis-cased key, a missing or `null`
required field, another `schema_version`, an unknown event kind or
trailing data rejects the record — never half-read. Writers
validate before writing, so a producer bug fails loudly.

## Event envelope

Required fields are always present; a zero is a real zero.

| Key | Type | Meaning |
|-----|------|---------|
| `schema_version` | int | always `1` |
| `project` | string | the session's project label |
| `seq` | int | per-host sequence number from 1, gap-free except explicit `drop` events |
| `boot_ns` | int | boot-clock time of the syscall's start |
| `wall_ns` | int | the same instant on the wall clock (boot + offset measured once per session) |
| `host_id` | string | `/etc/machine-id` |
| `pid` | int | process id (tgid) |
| `pid_incarnation` | int | process start on the boot clock: a reused pid is another process |
| `tid` | int | thread id; `0` for records with no task context (`tcp_state`) |
| `comm` | string | process name |
| `event` | string | the event kind, below |
| `fd` | int | descriptor acted on — the argument, or the returned one for opens and accepts; `-1` when none |
| `fd_gen` | int | how often this fd number was (re)created in this incarnation; `0` = predates the trace or no fd |
| `ret` | int | the syscall's return value |
| `err` | string, optional | errno name (`ENOENT`), only when `ret < 0` |
| `latency_ns` | int | exit minus enter |
| `bytes` | int | bytes moved (read/write) |
| `args` | object | syscall-specific arguments, below |
| `conn_id` | string, optional | the connection a socket event belongs to (*provisional until S2*) |
| `attribution` | string | `java` or `none` — missing attribution is data |
| `java` | object, optional | `{thread, stack}`, set exactly when attribution is `java` (*provisional until S1*) |
| `http` | object, optional | request fields on http events (*provisional until S1/M3*) |

`(pid, pid_incarnation, fd, fd_gen)` names one fd instance, even
when a library's open and a later socket land on the same number.

### Args

Every argument is its own optional field — one nullable parquet
column each, never a blob.

| Key | Set on |
|-----|--------|
| `path` | opens (required) |
| `new_path` | rename/link family (M2) |
| `flags` | opens: the raw open flags |
| `offset` | positional I/O (M2) |
| `local_addr`, `local_port` | socket events, when known |
| `remote_addr`, `remote_port` | socket events, when known |
| `sock_state` | `tcp_state` (required): `ESTABLISHED`, `CLOSE`, … |
| `marker` | `marker` (required): `<scenario>/<edge>` |

IPv4 peers of dual-stack sockets are written unmapped
(`127.0.0.1`, not `::ffff:127.0.0.1`).

### Event kinds (M1)

| Kind | Source |
|------|--------|
| `open`, `openat`, `openat2` | syscall pair |
| `read`, `write`, `close` | syscall pair |
| `connect`, `accept`, `accept4` | syscall pair; args carry the 4-tuple |
| `tcp_state` | `sock:inet_sock_set_state`, joined to the owning pid/fd |
| `marker` | an open of `/iormesh-marker/<scenario>/<edge>` |
| `drop` | ring-buffer loss — gaps are explicit |
| `session` | the per-trace header |
| `end` | why the trace ended (duration, disk limit, stop, error) |

The set only grows; M2 appends the rest of the file and socket
surface.

## Graph

```json
{"schema_version": 1,
 "nodes": [{"type": "process", "id": "proc:<host>/<pid>@<inc>",
            "attrs": {"comm": "java"}}],
 "edges": [{"type": "connected", "from": "proc:…", "to": "conn:<id>",
            "state": "fused"}]}
```

| Node | Id |
|------|----|
| `host` | `host:<host_id>` |
| `process` | `proc:<host_id>/<pid>@<pid_incarnation>` |
| `file` | `file:<host_id>:<path>` — the path exactly as the open event reports it |
| `socket` | `conn:<conn_id>` — one node per connection, both ends |

| Edge | From → to | State |
|------|-----------|-------|
| `runs_on` | process → host | — |
| `opened` | process → file | — |
| `connected` | process → socket | `fused` (both ends traced and joined) or `unresolved` |

Node attributes stay minimal (`comm` on processes): raw pids or
ports in attributes would survive normalization and make two runs'
graphs differ.

## Parquet mapping

To come with the M4 segment writer: one column per envelope field
and per `args` field (the `parquet:` tags in `internal/schema`),
zstd page compression, minute-rotated segments (PLAN.md decision
#16).
