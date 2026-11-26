# Files and the parquet mapping

Part of [Trace schema (v1, frozen)](../schema.md).

## Files

`iormesh run -out DIR` writes a trace directory:

| File | Format | Writer |
|------|--------|--------|
| `events.ndjson` | one event per line (NDJSON) | `schema.NDJSONSink`, as events complete |
| `graph.json` | one graph object, indented JSON | `schema.WriteGraphFile`, at exit |
| `stacks.ndjson` | the native stack table, one stack per line; only with `-native-stacks` | `schema.StackWriter`, one line per new stack |
| `methods.ndjson` | the Java method table; only with `-spool` (M3) | `schema.TableWriter[Method]`, one line per new frame |
| `threads.ndjson` | the Java thread table; only with `-spool` (M3) | `schema.TableWriter[Thread]`, one line per agent THREAD record |
| `classes.ndjson` | the Java class table (class-load map); only with `-spool` (M3) | `schema.TableWriter[Class]`, one line per new class a method row names |


`iormesh normalize -out DIR` writes the same two files for the
normalized trace.

`iormesh run -trace-dir DIR` (M4; the default, under
`/opt/iormesh/traces`, when `-out` is not given) writes the same
events and tables as parquet segments instead — or as well, with
both flags — under `DIR/<project>/`; see
[Parquet mapping](#parquet-mapping). The graph is `-out`'s only:
on a server the controller rebuilds it from the events (decision
#3), which carry everything it rests on — so a single-host run's
rebuilt graph is its `graph.json` (see [Graph](graph.md#graph)).

**NDJSON framing**: UTF-8, one JSON object per line, `\n`
terminated, no blank lines, keys in the order of the table below
(byte-stable for the same event). Events appear in `seq` order.
The reader also accepts a final line without `\n` and a `\r\n`
ending, and rejects a line of 1 MiB or more. Linux paths are raw
bytes: a path that is not valid UTF-8 is written with U+FFFD in
place of the invalid bytes — lossy, and a known v1 limit. Each
invalid byte becomes the six-byte escape `\ufffd`, whichever Go
built the writer (decision #33); a valid U+FFFD stays raw.

**The graph export** is written atomically by `WriteGraphFile`
(synced temp file, mode 0644, renamed into place) with
nodes sorted by `(type, id)` and edges by `(type, from, to)`, so
the same graph is the same bytes.

**Readers are strict** (`schema.DecodeEvent`, `ReadNDJSON`,
`DecodeGraph`): an unknown or mis-cased key, a missing or `null`
required field, another `schema_version`, an unknown event kind or
trailing data rejects the record — never half-read. Writers
validate before writing, so a producer bug fails loudly; the error
names the rejected event (kind, pid, tid, fd, fd_gen, ret), which is
not written.


## Parquet mapping

The M4 segment writer (`internal/segment`, task 8b4) writes the
same records as the NDJSON files, one parquet file per record
type and segment, through the `parquet:` tags in
`internal/schema`:

- **Columns**: one column per envelope field, named as its JSON
  key; `args`, `session`, `java`, `http`, `otel` and `dns` are
  groups with one column per field. Every `args` field and every
  optional field is a nullable column — absent in NDJSON is null
  in parquet — and lists (`args.argv`, `java.stack`,
  `http.req_headers`, `http.resp_headers`, `dns.addrs`, a
  stack's `frames`) are parquet LIST columns. Low-cardinality
  strings (`project`, `host_id`, `comm`, `event`, `err`,
  `attribution`, method and class names, a frame's module and
  build id) are dictionary-encoded. Read back, a file gives
  the same records the NDJSON reader does: an absent list and
  an empty one are the same record.
- **Pages**: zstd-compressed inside the file (decision #16;
  never a whole-file `.zst`), 8192 rows per row group — each file
  is directly queryable (DuckDB, pandas, `parquet-tools`). The
  tables are written by `parquet-go` (256 KiB pages, page
  statistics on). The events file has the tracer's own columnar
  encoder (task ee4, decision #25's CPU item): one data page v2
  per column chunk (a dictionary page before it for the
  dictionary columns), the dictionary columns `RLE_DICTIONARY`,
  `seq`, `boot_ns`, `wall_ns` and `latency_ns`
  `DELTA_BINARY_PACKED`, the rest PLAIN; its page and chunk
  statistics carry null counts only, and it has no column or
  offset index — a reader filtering pages by min/max reads the
  whole chunk instead. Its footer schema is the one `parquet-go`
  derives from `schema.Event`, so both kinds of file read back
  the same way; pyarrow and DuckDB read it value for value, nulls
  included (`TestIndependentReaders`, opt-in with
  `IORMESH_PARQUET_CHECK=<venv with pyarrow and duckdb>`).
- **Files**: in `<trace-dir>/<project>/ready/`, per segment
  `<fqdn>-<session>-<start-ts>-<seq>.parquet` (events) and
  `<same base>.methods.parquet`, `.threads`, `.classes`,
  `.stacks` for the tables that got rows while it was active.
  `<start-ts>` is `YYYYMMDDTHHMMSSZ` (UTC), `<seq>` six or more
  digits from `000000`, `<session>` 8 hex digits; the FQDN may
  contain dashes, so a name parses from the right. FQDN and
  session hold only letters, digits, `-`, `.`, `_` and `:`: the
  writer refuses to start with any other, and a reader refuses a
  file whose name holds one (it is ignored as a stray), because
  the server names its files and those parts reach the
  controller's shell commands and terminals (task 3i4).
- **Footer metadata**: `iormesh.schema_version`,
  `iormesh.project`, `iormesh.fqdn`, `iormesh.session`,
  `iormesh.seq`, `iormesh.start` (RFC 3339, nanoseconds) and
  `iormesh.content` (`events` or the table name), so a file
  copied loose still says where it came from.
- **Order and completeness**: events keep `seq` order within
  and across a session's segments; the session header is in
  segment 0, the end event in the last. A table row lands in the
  segment open when it was written: Java table rows always in or
  before the segment of the first event naming them, a native
  stack possibly one segment later (the stack table writes from
  its own worker). A reader takes the union of a session's
  table files.
