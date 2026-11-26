# Follow-up (ee4): tracer CPU on M4's segment writer

Part of [S3 — overhead and volume budget](../s3-overhead.md).

Task ee4 re-measured decision #25's tracer-CPU item on M4's output
path — parquet segments under `-trace-dir`, no `-out` — and cut the
segment writer, which task 8b4 had measured at ≈ 3× the NDJSON sink
per event. Same host (i7-1185G7, 8 CPUs, kernel 7.2.15, JDK 25),
busy as ever. Samples, the A/B script and benchmark output:
`spikes/s3/results-ee4/`.

`measure.sh` gained `S3_SEGMENT=1`: the traced run writes segments to
a `-trace-dir` in its work dir with `-disk-limit 0` (the developer
laptop's root is 95 % used, over the watchdog's 90 % default; the
256 MiB floor stays), and the emitted events are counted from the
published segments' footers (`spikes/s3/rowcount.go`, run with `go
run`). `S3_PERF=1` now records cycles (user and kernel) beside the
instructions.

**Where the time went** (`-cpuprofile` and `perf stat`, the build
before ee4, `2d04f09`): on the plain unfolded burst the tracer spent
12.3 k instructions per event on the segment path against 6.6 k on
NDJSON, and lost 20–25 % of the records at 1 thread, which NDJSON
never did. `segment.Writer.Event` was three quarters of the per-event
work: `parquet-go`'s reflection over `schema.Event`'s 68 leaf columns
(each nil pointer — the args, the java, http, session, otel and dns
objects — walked row by row into ~50 null levels), then the page flush:
level histograms, page bounds computed twice, and zstd. Neither the
library's `WriteRows` (`parquet.Value` rows, 17.8 k instructions per
event) nor `DataPageStatistics(false)` (−7 %) came near.

**The cut**: the events file now has its own columnar encoder
(`internal/segment/column.go`, `eventcols.go`, `eventfile.go`; the
tables keep `parquet-go`, their rows are rare):

- a hand-written mapping appends each field to a typed column buffer
  — a definition level per slot of an optional leaf, a repetition
  level per list element, values only for set slots — with no
  reflection and no `parquet.Value`. It is parquet-go v0.29's mapping
  for the struct tags (a nil pointer and an empty list are null; an
  optional non-pointer field is always present, its zero value
  included), checked against the schema the library derives (path,
  physical type, levels) when a writer starts, and held to the
  library's own `Deconstruct`, value by value, over 3 000 random
  events by `TestEventColumnsMatchDeconstruct`;
- slot and null counts are derived from the levels once per row group,
  not counted per row: most of a syscall event's columns are nulls;
- per row group (8192 rows, or 4 MiB buffered) and column: a
  dictionary page for the six dict-tagged strings, one data page v2
  (levels RLE, values PLAIN, `RLE_DICTIONARY`, or `DELTA_BINARY_PACKED`
  for `seq`, `boot_ns`, `wall_ns` and `latency_ns`), zstd. Delta
  encoding mattered: as PLAIN int64 those four high-entropy columns
  cost zstd ~13 ns per byte, ~100 ns per event each, on a real
  `-spool` segment. The footer's schema, column orders and created_by
  are copied from an empty file `parquet-go` wrote, so the file
  describes itself as the library's did. Not written: min/max page
  statistics (null counts only), column and offset indexes;
- 8192 rows per row group, not more: 16 k and 32 k saved instructions
  but cost cycles once the open row group's buffers outgrew L2.

The output is the same records: the round-trip tests read the files
back with `parquet-go` and compare NDJSON lines, rich events and 16 k
random ones over several row groups included; and the files are
smaller (plain at 1 thread 8.1 → 5.2 B/event, `-spool` 10.2 → 7.0).

**Benchmarks** (`results-ee4/bench.txt`, `perf stat` per op as ud4's
`insn.sh`, pinned to CPU 3; `BenchmarkWriterJoinedEvent` is new, the
read attributed java with a four-frame stack):

| Benchmark (per op) | Instructions before → after | Cycles before → after |
|---|--:|--:|
| `WriterEvent` | 11 151 → 2 483 | 6 071 → 1 299 |
| `WriterJoinedEvent` | 12 619 → 2 777 | 7 244 → 2 148 |

For comparison, the NDJSON sink (`schema.BenchmarkNDJSONSink`) costs
3 369 instructions per event (ud4).

**End to end, by #25's method** (`results-ee4/ab.sh`: `measure.sh
S3_SEGMENT=1 S3_MODES=traced S3_TRACER_FLAGS=-aggregate=false
S3_OPS=500000 S3_PERF=1`, with and without `S3_AGENT=1`; five rounds,
the build before ee4 and ee4's interleaved, base first; tracer CPU
from its ready file to its exit per emitted event, medians of 5;
instructions and cycles user + kernel):

| Run | Threads | µs/event before → after | Instructions/event | Cycles/event | Lost before → after | Load |
|---|--:|--:|--:|--:|--:|--:|
| plain | 1 | 2.68 → **0.85** | 15 187 → 5 765 | 5 366 → 2 570 | 20–25 % → 0 | 2.8–3.4 |
| plain | 4 | 3.14 → **1.62** | 15 380 → 5 963 | 6 092 → 3 098 | 67–71 % → 27–46 % | 2.5–3.6 |
| `-spool`, agent | 1 | 4.43 → **2.89** | 22 836 → 11 503 | 9 583 → 5 834 | 0 → 0 | 3.1–41 |
| `-spool`, agent | 4 | 5.82 → **4.82** | 23 320 → 12 027 | 12 540 → 7 652 | 0 → 0 | 41–46 |

(The base's instruction counts look high against its cycles: the
library's reflection loops run at an IPC near 3.) The last six
`-spool` runs met another agent's test run at load 33–46; under that
load the join attributed only 67–89 % of the events in three base and
two ee4 rounds at 4 threads — the host, not a build.

**Verdict**: plain unfolded **passes** at 1 and 4 threads (0.85 and
1.62 µs; it now spends fewer instructions per event than the NDJSON
path did, 5.8 k against 6.6 k, and at 1 thread loses no record).
`-spool` with every event joined is **over**: 2.9 µs at 1 thread
(load 3–41) and 4.8 µs at 4 (load 41–46). In cycles it is 5.8 k and
7.7 k per event; this host's tracer core runs at an effective ~2 GHz
under load (cycles over CPU time), so 2 µs needs about 4 k cycles,
a further third off. The writer is no longer the problem — of the
joined event's cost the writer is ~2.8 k instructions; the rest is the
agent ring's drain, the join and the correlator, which ud4 already
cut. What is left there, from a `-spool` profile after this cut: the
agent ring's drain boxing every record into an interface (an
allocation per bracket), the bracket list's growth (`slices.Insert`
and `growslice` copying 10 s of 72-byte brackets, ~8 % of the loop),
one `schema.Java` allocation per joined event, the bracket search and
the per-tid map lookups — no single item above ~10 %. The limit was
left unchanged and the item went to the owner, whose decision follows
("Follow-up (ee4): owner decision").

## Follow-up (ee4): independent readers

`parquet-go`'s reader ignores most of the metadata the new encoder
writes by hand — a chunk's num_values, the null counts, the encodings
list, the sizes and offsets — while Arrow stops reading a chunk at its
num_values and DuckDB prunes `IS NULL` by null_count, and
docs/schema.md promises DuckDB and pandas can query the files. So the
files were read with two readers that share no code with
`parquet-go`: pyarrow 26.0.1 and DuckDB 2.0.0, from a Python venv
(`TestIndependentReaders`, `internal/segment/indep_test.go` with
`testdata/pqcheck.py`, opt-in with `IORMESH_PARQUET_CHECK=<venv>`,
docs/testing.md). Two files: the rich events plus 16 884 random ones
over several row groups (nested java stacks, every nullable arg
including the four tcp_state fields, http, otel and dns objects,
uint64 values past 2⁶³), and 8 202 burst reads whose args and
objects are all null in every row group. Both readers return every
row equal to the NDJSON of the same events, and DuckDB's `IS NULL`
on all 31 nullable columns and objects counts exactly the events
NDJSON omits them in. A planted wrong null count made DuckDB's
`IS NULL` answer 0 rows instead of 8 525, a chunk num_values one
short made DuckDB refuse the file — the check catches both.
`TestEventFileMetadata` checks the same metadata in-process: per
chunk, the pages laid end to end fill exactly its compressed size,
headers and bodies sum to its uncompressed size, the data pages'
num_values and num_nulls equal the chunk's and the slots and nulls
its decoded definition levels hold, and row groups tile the file up
to the footer. Two writes of the same events give the same bytes
(`TestEventFileDeterministic`).

## Follow-up (ee4): owner decision

Owner decision (2026-11-20, delegated to an advisor): the original
≤ 2 µs covered `-spool` with every event joined and was not met
(2.89 / 4.82 µs after ee4). The limit is hereby changed for that case
only: ≤ 4 µs per emitted event and ≤ 12.5 k instructions per event,
valid at load ≤ 10 with every event attributed and zero loss. This is
a relaxation, argued by the extra agent records each joined event
consumes (one core still sustains 250 k joined events/s) and by the
default fold keeping JVM hot loops out of the join. Plain stays
≤ 2 µs. The decision came with one bounded round of the join cuts
listed above first (docs/decisions.md, decision #25 item 1 and M4 status).

**The cuts** (commit `83b5c61`; output unchanged — the join, ring
and segment tests, the integration suite):

- the agent ring's drain hands brackets and watermarks to a typed
  visitor (`agentspool.Ring.DrainTo`) instead of boxing every record
  into a `Record` interface — an allocation per record;
- a thread's brackets live in 1024-bracket chunks, pruned a chunk
  at a time with one spare kept for reuse, instead of one slice
  regrown by copying all of Retain's brackets;
- the join lends one `schema.Java` and one `schema.Otel` per emitted
  event instead of allocating them (`schema.Sink` now says the
  objects an event points to are borrowed too).

| Benchmark (per op) | Instructions before → after | Cycles before → after |
|---|--:|--:|
| `JavaJoinJoined` | 3 944 → 3 369 | 2 427 → 1 850 |
| `DrainBrackets` (a bracket and a watermark) | 2 237 → 1 283 | 1 350 → 655 |

**Valid re-measure** (`results-ee4/ab-valid.sh`, `results-ee4/valid/`):
the build before the cuts (`a7194d6`) and after (`83b5c61`)
interleaved, five rounds, segment path, by the decision's rules — a
run counts only at load ≤ 10 (before it starts and as recorded after
it), with zero lost records and, with the agent, every burst event
attributed (≥ 2 × ops); none was rejected. The plain unfolded burst
at 4 threads overruns the ring by design, so its loss is recorded,
not gated. Medians of 5, instructions and cycles user + kernel:

| Run | Threads | µs/event before → after | Instructions/event | Cycles/event | Load | Budget | Verdict |
|---|--:|--:|--:|--:|--:|--|--|
| `-spool`, every event joined | 1 | 2.33 → **1.90** | 11 498 → 11 100 | 5 564 → 5 581 | 4.4–7.5 | ≤ 4 µs, ≤ 12.5 k | pass |
| `-spool`, every event joined | 4 | 3.52 → **3.75** | 12 240 → 11 839 | 8 522 → 7 735 | 5.2–8.7 | ≤ 4 µs, ≤ 12.5 k | pass |
| plain | 1 | 1.32 → **1.59** | 5 741 → 5 775 | 3 137 → 2 915 | 5.1–8.0 | ≤ 2 µs | pass |
| plain | 4 | 1.78 → **1.95** | 5 772 → 5 826 | 3 357 → 3 318 | 4.8–5.4 | ≤ 2 µs | pass |

Every `-spool` run attributed all 1 000 000 burst events (1 000 023
and 1 000 040–47 with the JVM's own I/O) and lost none; plain at 4
threads lost 30–50 % of records unfolded in both builds, as before.
In situ the cuts show less than in the benchmarks: −3 % instructions
with `-spool` and −9 % cycles at 4 threads; CPU time moved within its
spread (at 4 threads 2.8–5.8 µs per run, the median 3.75 µs close to
the limit).
The plain path did not regress: its instructions are unchanged
(+0.6–0.9 %) and its cycles slightly lower; its CPU-time medians
moved up within their spread (0.9–1.7 µs per run at 1 thread and
1.3–2.6 µs at 4, across both builds).

**The default fold** (`-spool`, `-aggregate` on, 4 threads, three
rounds each build, same rules less the every-event-attributed one, as
folded calls are never joined): the 1 000 000 burst calls leave
1 911–17 140 events, 1 343–13 492 of them attributed, and the tracer
spends 0.28–0.97 s for the whole burst after the cuts (0.46–0.77 s
before) — 0.3–1.0 µs per traced call, lost 0. Per emitted event that
is 57–152 µs, a figure the fold makes meaningless (a folded event
stands for hundreds of calls): what the fold shows is that a JVM's
hot loop sends the join a few thousand events, not a million.

**Verdict**: under the changed limit `-spool` with every event joined
passes at 1 and 4 threads (1.90 µs / 11.1 k and 3.75 µs / 11.8 k
instructions), plain passes 2 µs at 1 and 4 threads (1.59 / 1.95 µs).
Decision #25's tracer-CPU item is met on the segment path.
