# Build window and prune (task yh4)

Part of [M4 — controller](../m4.md), in [Implementation
guide](../../implementation.md).

```
internal/federate/window.go     # Span, Window, StartRange
internal/ctl/window.go          # build -from/-to/-last, the readers' -windowed
internal/ctl/prune.go           # iormeshctl prune
```

```sh
iormeshctl build [...] [-from T] [-to T] [-last DUR]
iormeshctl show [VIEW] [...] -windowed
iormeshctl open|tui [...] -windowed
iormeshctl prune -older-than DUR [-project P]... [-store DIR] [-db FILE] [-apply]
```

- **A segment's range, from its name.** `federate.Span` is the
  named start minus a minute to the start plus the rotation
  interval (one minute) plus a minute: the late tick that
  rotates, the name's cut to the second and an event stamped
  just before a rotation all fit inside. Reading the events to
  place a segment would cost the decode a window is there to
  save, so the range errs wide: a window takes whole segments,
  about a minute or two past each edge. The range bounds when
  the segment was *written*, not its events' `wall_ns`: an
  event is stamped when its call began and written when it
  completed, so a read that blocked for minutes sits in a later
  segment, and a `-to` window can miss it.
- **Services and edges.** The build imports every segment it
  read into the service database, which holds every import ever
  made — pruned segments' too. So the windowed build reads the
  services and edges back through `servicedb.Filter{From, To}`
  (wall ns, the clock of `first_seen`/`last_seen`): a connection
  counts when its seen range overlaps the window, and its
  requests only from the per-segment rows whose range does.
  Without that, its summary would mix the window's tally with
  the database's whole history.
- **The window.** `T` is query's (`query.ParseTime`: RFC 3339,
  `YYYY-MM-DDTHH:MM:SS[.frac]` in UTC, or a bare UTC time of
  day); `-last DUR` is `-from now−DUR`, and not combined with
  `-from`/`-to`. An empty window is a usage error. The build
  echoes the resolved window (`window     FROM .. TO`, `(open)`
  for a side not given).
- **A time of day's date** (task 1l4, milestones.md M4). `windowSpec.day`
  is the one rule, for build and both kinds of query: it lists
  the projects' events segments (`federate.StartRange`, names
  only, readable or not) and places the time of day on their UTC
  date when every one starts on the same date. When they start
  on more than one it refuses (`usage: -from 12:00:00 is a time
  of day, but the store's segments start on D1 .. D2 (UTC):
  give its date, e.g. -from D2T12:00:00`). Anchoring on the
  earliest date (as before) let `prune` move a command to the
  next day once it deleted the oldest; the latest date moves
  with each collect — refusing leaves no date to drift. An
  instant never lists the store.
- **What it checks.** `LoadOptions.Window`: a pinned host
  directory's segments outside the window are not decoded; an
  unpinned directory's are, all of them, because the host
  vetting judges a directory on every session (two identities
  in one directory put all of it in doubt) — so the windowed
  build trusts, and pins, exactly what a full build would, and
  a damaged segment of an unpinned directory is reported though
  it lies outside the window. Each session's graph is rebuilt
  from its window alone, as from a store whose older segments
  were deleted.
- **Where it writes.** `<projects>[@NS]~window/` — `~` is in
  neither the project nor the namespace alphabet — one
  directory per project set and namespace, overwritten by each
  windowed build and read by `show`, `open` and `tui -windowed`
  (task hk4: `addReadBuildFlags` registers `-namespace` and
  `-windowed` for all three, which read the directory
  `buildFlags.key` names); the full build's directory is never
  touched. The model records the resolved window as
  `run.window` (the echoed text, omitted for a full build), so
  the site's header and the TUI's title name it.
- **prune.** Lists the store's own files whose range ended
  before now−DUR — exactly what `build -last DUR` would never
  take — and deletes them only with `-apply`, printing each.
  Its own means: directly in `<store>/<project>/<host>/` (both
  real directories with valid names, never a symlink), a
  regular non-dot file whose name `segment.ParseName` accepts.
  A table file goes only once no events segment of its session
  stays in the directory (a newer segment's events may name
  rows it holds). Deletes go through an `os.Root` on the store.
  Directories and host pins stay. Auto-cleaning stays out:
  prune runs only when the operator runs it.
- **The service database forgets** (task gk4). Without it a
  full build after `prune -apply` still showed the deleted
  segments' services, edges and counts: the import is additive,
  and a connection's row did not say which segments saw it.
  Schema version 4 adds `conn_source (host, conn_id, source,
  first_seen, last_seen)`: `Observe` records per connection
  every source whose records name it (`Observation.Sources`,
  either end's) and `Import` upserts them (MIN/MAX, so
  idempotent), plus each request's source; a fused takeover
  moves them like the requests, and a server half skipped
  because its connection is fused elsewhere still adds its
  sources to the fused row. The migration seeds the table from
  the request rows' sources. `DB.Forget(sources)` runs in one
  transaction: it deletes the sources' `http_request` and
  `conn_source` rows, then per touched connection drops it
  when no source is left (its requests cascade) or sets its
  window to the remaining sources' span. Verdict, ends,
  hostname and TLS hint stay; `naming_rule` is never touched.
  `prune -apply` calls it with every listed file *before*
  deleting any (`-db`, default the usual path; a missing
  database is skipped, not created): a failed update deletes
  nothing and exits 1, a failed delete afterwards leaves a file
  the database forgot, which the next build re-imports. Gaps:
  a pre-version-4 row without requests has no source and stays;
  a build running during prune can re-import what was just
  forgotten.
- **Tests.** `internal/federate/window_test.go` (range edges,
  `StartRange`); `internal/ctl/window_test.go` (`-from`, a time
  of day, `-last`, `-to` select the expected segments of the
  rotated fixture, the full build unchanged, services and edges
  seen only before the window left out of the windowed views,
  an unpinned directory checked whole and a pinned one only in
  its window, refusals); `internal/ctl/window_anchor_test.go`
  (task 1l4: a two-day store refuses a time of day in build and
  both queries and takes a dated one, the same command after a
  prune of the first day resolves on the remaining one; build
  and query echo the same window, and refuse alike for an
  unreadable segment's date);
  `internal/ctl/prune_test.go` (the listing deletes nothing,
  `-apply` exactly the listed files, a live session's table kept
  and an orphan's deleted, planted foreign entries — a stray
  name, a dot file, a symlink named like a segment, a directory
  named like one, a file above the host directories, a symlinked
  host directory — untouched, `-project`, refusals; task gk4:
  a peer seen only in a pruned segment is gone from the next
  full build's services, edges and `services list` while a
  kept segment's peer and the naming rules stay, no database
  is created by prune, and an unusable database deletes
  nothing); `internal/servicedb/forget_test.go` (connections
  only a forgotten source saw dropped, the others' requests and
  window narrowed, a repeat or unknown source a no-op, a
  re-import restores them, a later server segment keeps a fused
  row, the version 3 migration's seeded sources, unnamed source
  spans refused).
