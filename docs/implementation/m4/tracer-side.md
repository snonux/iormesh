# Tracer side

Part of [M4 — controller](../m4.md), in [Implementation
guide](../../implementation.md).

Subcommands of `/opt/iormesh/bin/iormesh` (the only thing the
sudoers rule names):

| Subcommand  | Does |
|-------------|------|
| `run`       | the foreground tracer (also standalone mode) |
| `start`     | launches `run` as the transient unit `iormesh-<project>.service` |
| `stop`      | `systemctl stop` of that unit: SIGTERM, last segment, end |
| `status`    | the unit's state plus `<trace-dir>/<project>/state.json` |
| `upgrade F` | verifies staged binary `F`, replaces itself (#1, #28) |
| `uninstall` | stops every unit, removes `/opt/iormesh`, then bootstrap's sudoers rule (marker line only) |

Built by task 9b4 (`internal/cli`: `start.go`, `control.go`,
`systemd.go`, `state.go`):

```go
// unitArgs is systemd-run's argv. --service-type=exec makes systemd-run
// return only once the binary is exec'd; --collect lets a failed unit
// vanish, its state.json and journal keeping what happened.
func (cfg startConfig) unitArgs(self string) []string {
    return append([]string{
        "--unit=" + cfg.unit, // iormesh-<project>.service
        "--description=iormesh tracer, project " + cfg.run.project,
        "--collect", "--quiet", "--service-type=exec",
        "--property=RuntimeMaxSec=" + seconds(cfg.run.duration+runtimeGrace),
        "--property=CPUQuota=" + cfg.cpuQuota,   // 100%: one core (#25)
        "--property=MemoryMax=" + cfg.memoryMax, // 1G
        "--property=KillSignal=SIGTERM",
        "--property=TimeoutStopSec=" + seconds(stopTimeout), // 30s
        "--", self, "run",
    }, cfg.runArgs...)
}
```

- `start` takes `run`'s flags plus `-cpu-quota`, `-memory-max`
  and `-wait` (default 2m). It validates them as `run` would,
  so a bad flag fails the start, not the journal, and forwards
  the ones set as single `-name=value` argv elements, paths made
  absolute (the unit runs in `/`). Without `-ready-owner` it
  passes `SUDO_UID:SUDO_GID` (none under root's own sudo).
  Through the sudoers rule (`SUDO_UID` set, binary under
  `/opt/iormesh` or the install a symlink) `Main` runs only
  `run`, `start`, `stop`, `status` and `uninstall`; any other
  subcommand is refused (`sudoAllowed`, fail closed).
  Its run is root, so start is confined (`confine.go`, also
  applied to the installed binary's `run` under sudo): no
  `-out`, `-cpuprofile` or `-ready`, `-trace-dir` only
  `/opt/iormesh/traces` (where `state.json` lives too), `-spool`
  only `/run/iormesh/spool/<project>`, `-ready-owner` only the
  sudo caller, no `$` in a forwarded value
  ([security.md](../../security.md)). A live unit for the project is
  refused — flags change by stop and start (#19) — and a failed
  one reset.
- Every systemd call is an argv through the `commander`
  interface; the unit tests replace it with a fake, so they
  never start, stop or list a host unit. Unit names are built
  only from labels `internal/project` accepts, with the
  `.service` suffix spelled out (a label may contain dots).
- A failed systemd-run is diagnosed (`startdiag.go`, task rf4):
  the unit's journal since the launch, or `systemctl status
  --lines=0` (the unit's state only: a unit status still finds
  is likely a racing start's, whose log is not ours — task
  8g4), each command under a 5 s timeout, bounded and through
  `termsafe` (lines cut after the quoting), plus an SELinux hint when
  the binary's label is not `bin_t`/`usr_t` or the log shows a
  denial ([testing.md](../../testing.md), "Root suites on Rocky 9.9").
- `state.json` is written by the run (atomic rename, 0644), not
  by `start`: `running` once the probes are attached, with pid
  and incarnation, `$INVOCATION_ID`, targets, start and end-by
  times, and the run's own tracer `version` and `argv` (task
  ph4: upgrade replaces the binary, not a running session, so
  only the run can say which build it is; a state from an older
  tracer lacks both and reads as version unknown) and
  `operator_stop: false` (task zh4); `ended` with
  the end reason before the segment writer
  closes (after that its lock is free and the next run of the
  project owns the file). `start` polls `systemctl show` and
  the file until the state carries the unit's invocation id —
  the run attached — or the unit is gone (error, see
  `journalctl -u`) or `-wait` passed (error, the unit keeps
  running). `status` reports a `running` state whose process
  is gone as `dead`: its active minute was never published.
- `stop -project P` refuses a unit that is not live, stops it
  (systemctl waits for the drain, the end event and the last
  publish) and prints the recorded end — and fails when
  `state.json` does not say `ended` (the run was killed after
  the stop timeout, or never attached: its last minute is
  unpublished; task eb4). Once the unit is stopped it sets
  `operator_stop: true` in `state.json` (task zh4: a reboot's
  SIGTERM ends a run as `stop` too, and `iormeshctl ensure`
  must leave only the operator's stops alone) — only then,
  since the mark is an unlocked read-modify-write and a live
  run's start or end record landing in between would be rolled
  back; a run SIGKILLed after the timeout is marked too. A
  stop that fails marks nothing. Each writer of `state.json`
  uses its own temporary name (pid), removed again when the
  write fails. A deploy restart whose start fails after its
  stop leaves the project marked stopped, so ensure will not
  bring it back; deploy reports it. Through the
  sudoers rule the mark is written only under the confined
  trace root, never into a `-trace-dir` the caller chose.
  `status [-project P]
  [-json]` covers one project or every one with a loaded unit
  or a `state.json` under `-trace-dir`, with the count of
  events segments waiting in `ready/`; an unknown project or a
  corrupt or foreign state is an error. `uninstall` takes no
  path — it runs under the sudoers rule — stops every
  `iormesh-<project>.service` first and keeps `/opt/iormesh`
  if one will not stop.
- Tests: argv construction, flag forwarding and the sudo owner,
  rejected flags with no systemd call, the live-unit refusal,
  the wait (attach, early exit, early end, timeout), stop,
  status (running, dead, ended, corrupt, unknown, foreign) and
  uninstall, all against the fake; `TestSystemdUnit` runs the
  lifecycle on the real systemd as root under a unique project
  name, stopping and resetting its unit in `t.Cleanup`.

Segment writer (`internal/segment`, built by task 8b4):

```
internal/segment/writer.go     # Writer: New, Event, rows, Tick, Close
internal/segment/rotate.go     # rotate, publish (tables first), abort
internal/segment/stream.go     # one parquet file: footer, sync, rename
internal/segment/dir.go        # names, ready/ owner, stale-active sweep
internal/segment/watchdog.go   # statfs: percent + free-bytes floor
internal/segment/zstdcodec.go  # ior's one-encoder zstd codec
internal/cli/segment.go        # -trace-dir flags, tee, tick, endReason
```

```go
// rotate publishes the active segment and opens the next. The disk is
// checked first; over the limit nothing rotates and the tracer ends
// the trace. A segment without a row is not published.
func (w *Writer) rotate(now time.Time) error {
    if err := w.checkDisk(); err != nil {
        return err
    }
    if w.empty() {
        w.start, w.rotateAt = now, now.Add(w.cfg.Interval)
        return nil
    }
    if err := w.publish(); err != nil { // tables, then events, fsync ready/
        return err
    }
    w.seq++
    return w.openSegment(now)
}
```

- A segment's files are written in the project directory as
  `.active-<session>-<what>.parquet` and published by `os.Rename` into
  `ready/` (same filesystem: atomic) after the footer is written
  and the file fsync'd; `ready/` is fsync'd after the table
  renames and again after the events rename.
  Names: `<fqdn>-<session>-<start-ts>-<seq>.parquet`, tables
  `<same base>.<table>.parquet`, published before the events
  ([schema.md](../../schema/files-parquet.md#parquet-mapping)).
- Rotation runs in `Tick`, which the run loop calls on its
  200 ms ticker — never on the per-event path, which only
  validates the event and appends its fields to the open row
  group's column buffers (`segment/eventcols.go`, task ee4: the
  hand-written mapping replaced `parquet-go`'s reflection, which
  cost ~3× the NDJSON sink per event; `newEventColumns` checks it
  against the library's schema for `schema.Event`, and a test
  holds it to the library's `Deconstruct`). A minute (from the
  segment's opening) or 64 MiB (bytes written plus the open row
  group's buffers) rotates. The tables keep `parquet-go`'s
  `GenericWriter`: their rows are rare.
- Compression: ior's `zstdCodec` (one encoder, 1 MiB window)
  passed to the tables' writers as `parquet.Compression`, not the
  library's pooled `parquet.Zstd`; the events file's encoder calls
  its `Encode` directly, page by page.
- `ready/` is created 0755 and `Lchown`ed to `-ready-owner
  UID:GID` (`start` passes `SUDO_UID`/`SUDO_GID` on to `run`);
  a `ready` that is not a real directory fails the start, as does
  a trace or project directory not owned by the euid or writable
  by group or others (docs/security.md). The directories it
  creates are set 0755 whatever the umask; an existing one is
  never chmod'ed (`segment.MakeDirs`). `New` takes an exclusive
  `flock` on `<project>/.lock` (one run per project directory)
  before sweeping stale `.active-<session>-*` files, and holds it
  until `Close` has published.
  Published files are 0644 root-owned: the owner of `ready/`
  can read and unlink them, which is all rsync
  `--remove-source-files` needs.
- `Watchdog.Check()`: `statfs` on `-trace-dir`; trips at
  `-disk-limit` percent used (df's used/(used+avail)) or under
  `-disk-min-free`. It runs in `preflight` before the BPF load
  (failing the run), in `segment.New`, every 5 s in `Tick` and
  before each rotation. A trip in the loop returns
  `ErrDiskLimit`; `endReason` turns it into end reason
  `disk_limit` with no error, `finish` writes the end event and
  closes the writer, which publishes the last segment — exit 0.
- On SIGTERM: the loop ends as `stop`, `finish` writes `end`
  and the writer's `Close` publishes the active segment.
- The writer is closed after the tables (`outputs.close`): the
  stack table's worker may still write rows after the event
  stream closed, so `segmentSink.Close` leaves the writer open.
- Tests: round trip of every nested shape (compared as NDJSON
  lines), zstd on every column chunk, the minute boundary, the
  size cap, idle segments, publish-is-a-rename (same inode,
  0644), tables beside their segment, stale active files, the
  `ready/` owner and symlink, the watchdog's limits (table),
  a trip that ends cleanly with an intact last segment, startup
  over the limit touching nothing; in `internal/cli` the loop's
  disk-limit end through `finish`, the real-partition startup
  check and the tee. The events encoder is held to
  `parquet-go`'s `Deconstruct` over random events and read back
  across several row groups (`eventcols_test.go`); its hand-written
  metadata is checked against the pages it describes, and its
  output for determinism (`eventmeta_test.go`); pyarrow and DuckDB
  read it back value for value with
  `IORMESH_PARQUET_CHECK=<venv>` (`indep_test.go`, a venv with
  `pip install pyarrow duckdb`).
  `BenchmarkWriterEvent` and `BenchmarkWriterJoinedEvent` are
  the per-event cost next to `schema`'s `BenchmarkNDJSONSink`
  (task ee4).
