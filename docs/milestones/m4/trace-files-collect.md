# M4 trace files, disk watchdog and collect

Part of [M4 — Controller (`iormeshctl`): push, collect, merge
locally](../m4.md), in [Milestones](../../milestones.md).

- **Trace files** land on the server as parquet under
  `/opt/iormesh/traces/<project>/` — every trace is always
  inside a project subfolder, so unrelated apps on one server
  never mix. Parquet is the bulk format behind the same schema
  as NDJSON (columnar, queryable, compact; ior already records
  parquet), compressed inside the file (zstd pages), so every
  segment stays directly queryable. Segments **rotate once per
  minute**: only the active segment is being written; on
  rotation it is closed and moved (atomic rename) into a
  `ready/` subfolder as
  `<fqdn>-<session>-<start-ts>-<seq>.parquet` (`<start-ts>` is
  when that segment was opened).
  `ready/` is the sync contract: files there are immutable and
  final, so iormeshctl can collect them while the session
  runs, and a crash or kill loses at most the current minute,
  never a synced segment. `ready/` belongs to the deploying
  user (`start` records who invoked sudo), so collection needs
  no privilege.
  A segment is its events file plus one file per table that
  got rows while it was active —
  `<…>-<seq>.methods.parquet`, `.threads`, `.classes`,
  `.stacks` — published tables first, so a collected events
  segment never names a Java table row that is not yet in
  `ready/`; the union of a session's segments is its tables.
  `<session>` is 8 random hex digits per run (names never
  collide, even in the same second); every file's footer
  repeats project, FQDN, session, seq and start. FQDN and
  session hold only `[A-Za-z0-9_.:-]`: the writer refuses
  others and the controller ignores a file named with them,
  since refs built from the name reach shell commands (task
  3i4). A segment
  without a single row is not published (an idle session
  leaves no empty files), and a crashed run's footer-less
  active file is removed by the next start.
  Status (2026-11-20, task 8b4): built — `internal/segment`
  and `iormesh run -trace-dir` (the default without `-out`;
  `-out` keeps writing NDJSON, both together tee). The
  parquet library is ior's, `parquet-go` v0.29.0, with ior's
  one-encoder zstd codec; per event the writer costs ≈ 3× the
  NDJSON sink (≈ 10 k vs 3.4 k instructions on the burst
  event, ≈ 4.6 B/event on disk for that burst) — ee4 measures
  what that does to decision #25's CPU item. Task ee4 replaced
  the events file's writer with the segment package's own
  columnar encoder (the tables keep `parquet-go`): ≈ 2.5 k
  instructions per burst event, below the NDJSON sink, the same
  records read back, files smaller; one data page v2 per column
  chunk, null-count statistics only, no column or offset index
  (docs/schema.md, "Parquet mapping").
- **Disk watchdog**: the trace directory is configurable
  (`-trace-dir`, default `/opt/iormesh/traces`), and the
  tracer continuously monitors the partition holding it
  (statfs on the configured dir — on a short check interval
  and before every segment rotation). When usage crosses the
  limit (default 90%, `-disk-limit`), it stops tracing, flushes
  the current parquet segment, records the exit reason, and
  exits. Together with the 4h bound, tracing is bounded by
  disk too — it never fills the partition. A startup check
  fails fast if the partition is already at the limit.
  As built (task 8b4, decision #17's default): both limits —
  `-disk-limit 90` (percent used, df's view, so root's
  reserved blocks are never eaten) and `-disk-min-free
  256MiB` (four segments at the size cap), either 0 for off;
  checked every 5 s from the run loop and before every
  rotation; a trip stops the trace with end reason
  `disk_limit`, publishes the last segment and exits 0; the
  startup check runs before the BPF object loads.
- **Three commands, two jobs**:
  - `iormeshctl collect` only collects: it syncs trace segments
    from the servers to the controller. It touches the network
    and nothing else.
  - `iormeshctl build` only builds: it merges what has been
    collected into the graph, updates the service database and
    renders the outputs. It is offline, repeatable, and never
    contacts a server — so a build can be rerun after naming
    services, with other projects, or on a plane.
  - `iormeshctl refresh` does both, collect then build — the
    everyday command.
- **Collect**: iterate the registry and sync the trace files.
  **Sync is streaming
  and rsync-based**: `collect` is repeatable during a live
  session and rsyncs each server's `ready/` folder over SSH
  with `--remove-source-files`, unprivileged, so a segment is
  deleted from the server as soon as it has transferred —
  server disk holds only unsynced segments plus the minute
  currently being written, and collection is never an
  end-of-run bulk download. The active segment never enters
  `ready/`, so rsync can never grab or delete a file mid-write.
  Server-side cleanup is delete-on-transfer, not by age: an
  uncollected segment older than a minute is still data the
  operator wants, so only a confirmed transfer frees it —
  confirmed meaning fsynced on the controller (rsync
  `--fsync`; the pod path syncs file and directory before its
  remove, task ii4); if
  `collect` never runs, the disk watchdog and the 4h bound cap
  what piles up.
  Synced segments land on the controller under
  `~/.local/share/iormesh/traces/<project>/` — that path on
  every platform, always — and stay there until the operator
  deletes them; nothing is auto-cleaned. Deleting is an
  explicit operator command (task yh4, owner-approved
  2026-11-22): `iormeshctl prune -older-than DUR` lists the
  store's own segment files whose time range ended more than
  DUR ago and deletes them only with `-apply` — never a file it
  cannot parse as a segment, a dot file or a symlink (see
  **Build** below); no command prunes on its own. What prune
  deletes, the service database forgets in the same run (task
  gk4, see **Build window and prune**).
  Collect is project-aware: one project at a time, or
  several together.
  Built (task eb4, `internal/ctl/collect.go`): `iormeshctl
  collect [-project P]... [HOST...]` — no HOST is the whole
  registry; every host and project is checked against the
  registry before any host is contacted. Per host an `ssh
  true` probe (an unreachable host fails, it is never "nothing
  to collect"), then per project `test -d` of its `ready/` (a
  project never started — exit status 1, nothing else — is
  skipped) and the Transport's Fetch (`rsync -rt
  --remove-source-files`, only the non-dot `*.parquet` files
  directly in `ready/`, no sudo). Each host
  lands in its own directory,
  `~/.local/share/iormesh/traces/<project>/<host>/` (the
  registry's host name): a server picks its own segment
  names, so a shared directory would let one host overwrite
  another's. The store and project directories are created
  0700 and refused when foreign-owned or writable by others;
  the host directory is forced to 0700 by every fetch. A
  failed host is reported, the others still collected, and
  the command exits 1 naming it. A hung host counts as
  failed: ssh gives up on a dead connection after about 45 s
  (`ServerAliveInterval=15`, `ServerAliveCountMax=3`), rsync
  on a transfer that moves no data for 60 s (`--timeout=60`),
  and each server host gets an hour in all (collect, deploy
  and rm alike) before the controller moves on — generous,
  since an interrupted collect loses nothing and a backlog may
  be large. Pod entries get the same hour (each entry in
  collect and rm, each pod in deploy); below it every kubectl
  call that answers and exits (get, debug, a short exec) has
  2 min, and the fetch stream an idle timeout of 2 min without
  a byte — silence, not length, so a long backlog still
  moves (task pi4). A Ctrl-C or SIGTERM cancels the command's
  calls, lets it clean up, and it reports "interrupted". The
  build reads every
  `*.parquet` under `<project>/*/` and skips dot files
  (rsync's in-flight temporaries).
