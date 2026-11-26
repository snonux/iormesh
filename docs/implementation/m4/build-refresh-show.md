# Build, refresh and show

Part of [M4 — controller](../m4.md), in [Implementation
guide](../../implementation.md).

Built by task ib4 (all cgo-free), hardened after its first review
against segments a compromised server sends:

```
internal/segment/reader.go      # ParseName, ReadEvents, CheckEvents, ReadTable
internal/federate/segments.go   # Segment (a Source), TableFile, LoadSegments
internal/federate/hosts.go      # vetHosts: one host identity per host dir
internal/federate/rebuild.go    # a session's graph from its events (#3)
internal/termsafe/              # trace text made safe for a terminal
internal/viewmodel/             # Model V1, Tally, New, the text views
internal/ctl/build.go           # iormeshctl build
internal/ctl/check.go           # the memory-capped check child, host pins
internal/ctl/show.go            # iormeshctl refresh, iormeshctl show
```

```sh
iormeshctl build [-project P]... [-store DIR] [-db FILE] [-builds DIR]
iormeshctl refresh [-project P]... [-registry FILE] [...] [HOST...]
iormeshctl show [summary|services|edges|health] [-project P]... [-json]
```

- **Hostile input.** A server may be compromised, and every
  collected byte is its choice. The reader refuses a file over
  `segment.MaxFileSize` (twice the writer's 64 MiB cap) and a
  footer over `MaxFooterSize` (8 MiB) or longer than the file
  before parquet-go allocates what the file declares (a footer
  length of 0xF0000000 would be 3.75 GiB), skips the page index
  and bloom filters, and turns the decoder's panics — in the
  footer's column loader as in the pages — into errors naming
  the file. Page sizes and zstd output inside the file are not
  bounded in process: parquet-go sizes them from the file and
  running out of memory is fatal to Go. So every segment is
  decoded first by `iormeshctl __check-segment SPACE PATH`, a
  child of the same executable whose address space is capped
  (`RLIMIT_AS`: what it mapped at start plus 1 GiB; the GC's soft
  limit at half; 2 minutes at most). A hostile file kills the
  child and fails that segment only. Decoding is deterministic,
  so a file the child decoded needs the same memory in the
  build's own process, where it is read again. On macOS
  `RLIMIT_AS` may not hold; the child still keeps a crash out of
  the build.
- **Reading a segment.** `segment.ReadEvents` decodes the events
  file 512 rows at a time into one reused batch; the decoder's
  buffers for a row group come on top. Each row is checked like
  an NDJSON line (`Event.Validate`; empty lists the decoder
  returns become absent ones first, the contract's equality);
  the footer, parsed once per read, must say `events`, this
  schema version and — the caller's `want` — the project,
  FQDN, session and seq of the file's name and directory.
  `CheckEvents` is the child's read: it also requires one host
  id in the file and at least one event. Table files read
  through `ReadTable`; the build reads the methods tables its
  conversations' frames need (task mb4, M5 "Conversations and
  exports").
- **One source per segment.** `LoadSegments` lists
  `<store>/<project>/<host>/*.parquet` (every project directory
  without `-project`; dot files skipped, other entries reported
  as ignored). Each events segment is a `federate.Segment`, named
  `<project>/<host>/<file>` — stable wherever the store lives —
  because the service database counts requests per source and
  only a segment is immutable and may later be deleted (task
  hb4).
- **Host identity** (`hosts.go`). A server names its own files
  and writes its own events, so it could plant
  `hostB.example.org-…` files with hostB's host id in its own
  `ready/`; the merge and the service database (keyed by host
  id, additive) would take them as hostB's. The store directory
  is the one thing a server cannot choose, so each host identity
  (host id, FQDN) is tied to one directory. The first build pins
  each directory to the identity it saw
  (`~/.local/share/iormesh/hosts.json`, 0600, never
  overwritten; a host reinstalled under the same registry name
  needs its entry removed by hand). Bad, then: a segment whose
  host id is not its session's; a session whose directory is
  pinned to another identity, or whose host id or FQDN is
  pinned to another directory — only the claimant, so a new
  server imitating a pinned host cannot get the genuine host's
  sessions dropped (ib4's second review,
  `TestPinnedHostKeptAgainstNewDir`); and, among the unpinned
  rest, every session of a directory holding two identities and
  every session of an identity found under two directories (the
  forgery cannot be told there). One machine registered under
  two names collides with this rule.
- **Clocks and errors.** The reader refuses an event whose
  `boot_ns` or `wall_ns` is past int64 or whose `wall_ns +
  latency_ns` overflows it: the service database stores int64
  and would reject the window, failing the whole build's import
  instead of the one segment (`TestBoundTimes`,
  `TestBuildHostileClockSegment`); servicedb also saturates the
  sum. Every error and warning `iormeshctl` prints passes
  through `termsafe` (`errText`): ssh's stderr, request paths
  and host names in an error come out quoted
  (`TestErrorsAreTerminalSafe`).
- **The graph is rebuilt** (decision #3, the server keeps none)
  once per session, segments in seq order, by the correlator's
  rules (`rebuild.go`, `rebuild_fds.go`): host, process (newest
  comm), runs_on; app and incarnation from an `attach` with
  `args.pattern`; forked from a `fork`, where the child also
  inherits a copy of the parent's fds; opened for successful
  opens and creat, and the new name of a rename that moved a
  file the process held open (opens, closes, dup, dup2, dup3,
  fcntl `F_DUPFD`/`F_DUPFD_CLOEXEC`/`F_SETFD`/`F_GETFD`, forks,
  the fd-creating accept, pipe, socketpair and epoll calls,
  unlinks, renames, and an exec closing the close-on-exec fds
  and the unknown ones above stdio, followed through the
  events); socket and connected for every conn_id, fused for a
  pipe or socketpair, a conn_id with both a connect and an
  accept on the host, or mirrored address pairs (a loopback UDP
  flow); signaled for a delivered signal from a traced sender.
  The `fork` and `attach` events and `F_SETFD`'s `args.flags`
  are task ze4's: before it a segment-built graph lacked app
  nodes, incarnation and forked edges, a child's inherited fds
  and `F_SETFD`'s close-on-exec. Now every judged integration
  run rebuilds its graph from its segments
  (`federate.RebuildGraph`) and must get its `graph.json`
  exactly (`assertRebuiltGraph`, M4's acceptance for
  single-host scenarios); `internal/correlate`
  `TestRebuiltGraphIsTheCorrelators` drives the correlator
  through forks, `F_SETFD`/`F_GETFD`, execs, pipes, attaches and
  a pid reused within the session, and holds the rebuild to its
  graph. The rebuild keeps one fd table per pid, as the
  correlator does: a fork or attach replaces the pid's table, so
  a dead incarnation's leaked fds never move with a later
  rename; a fork or exec touches only its own table. Still out
  of reach (rebuild.go's header): an open with an unreadable
  path (no event, yet a new fd in the correlator) and a rename
  by another process between an exec's record and its exec
  event. Every segment of
  a session returns the session's graph; the merge's union keeps
  one copy. `TestSegmentMergeGolden` writes
  `testdata/mesh.golden`'s traces as rotated segments and merges
  them to the same bytes as the NDJSON merge;
  `TestRebuildCorrelatorExport` rebuilds the correlator's own
  export golden (`internal/correlate/testdata`) to its graph.
- **Bad segments.** A segment the check fails, or whose host
  identity is in doubt, is left out before anything else reads
  it. The build goes on with the rest, names each — by its store
  name, never the absolute path — in the summary and in
  `model.json`, and exits 1 after writing the outputs; the file
  stays in the store. A read failing later (a file changed under
  the build) drops that segment and rebuilds its session's graph
  without it.
- **Passes.** Check (the child), graph, merge (the run's tally
  rides along: `talliedSegment`), `servicedb.Observe` (the
  conversations' http events ride along: `recordedSegment`) and,
  when a kept conversation has a server request, the
  conversations' file pass — five reads of every segment, none
  holding events. What grows with
  the input is the merge's per-connection state (keys, the data
  records' stream offsets for the clock bounds), the graph, the
  service database rows and one tally per session.
- **Outputs.** `<builds>/<project>[+<project>…]/graph.json`
  (`schema.WriteGraphFile`) and `model.json` (indented JSON),
  both written atomically; the builds directory is refused when
  someone else owns it or others can write it, and the build's
  own directory is forced to 0700. Many projects together get
  `projects-<hash>`. Build never contacts a server, and the
  merge is sorted, the import idempotent, the model free of
  clocks and local paths — so a rerun writes the same bytes.
- **The model** (`internal/viewmodel`, V1 of
  docs/visualization.md): `Run` (projects, hosts, sessions,
  segments, events, time range, fused and unresolved TCP
  halves, graph size), `Health` (lost, skipped runs,
  unattributed, attach gap, each session's end, a `Clock` per
  host pair, bad segments, ignored entries), `Services` and
  `Edges` from the service database after the import (filtered
  by `Filter.Projects`, so the whole history of the projects,
  not only the segments still in the store; an edge is `fused`,
  `unresolved` or `mixed`). `show` reads `model.json` alone.
  Every text view passes every string through
  `termsafe.String` first (a string with a control character is
  printed Go-quoted), so an escape sequence in a comm, hostname
  or file name never reaches the terminal raw; `iormeshctl
  services` uses the same function.
- **`refresh`** collects (the registry's hosts, or HOST...) and
  then builds every host directory of the projects; a host
  whose collect failed is reported, the store still built, and
  the command exits 1.
- Tests: `internal/segment/reader_test.go` (round trip through
  the writer at batch and row-group edges, tables, names;
  damaged files — empty, garbage, truncated, footer cut, pages
  zeroed; 3000 single-byte flips weighted to the footer and
  footer lengths past the file, none panicking; `FuzzReadEvents`
  for `go test -fuzz`); `internal/federate/segments_test.go`
  and `rebuild_test.go` (the golden mesh from segments, the
  rebuild against the mesh traces' and the correlator's export
  graphs and per rule, fd copies and exec, bad segments, a
  session of two host ids, a checker failure, host spoofing:
  an identity under two directories, two identities in one, a
  pinned directory); `internal/viewmodel/new_test.go` (tally,
  clocks, services and edges, determinism, escape sequences in
  every text view); `internal/ctl/build_test.go` and
  `check_test.go` — a two-host, two-project mesh written as
  rotated segments: summary, graph and model goldens
  (`testdata/build_*.golden`), byte-identical rebuilds, a naming
  rule shown by name, request counts exact over repeated builds
  and after deleting segments, a damaged segment, refusals,
  every `show` view, `refresh` over a fake transport, the check
  child (a good segment, garbage, and on Linux an address space
  too small to decode in), host pins, an open builds directory.
