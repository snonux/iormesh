# M2 steps 1–3: process tree, target spec, app identity

Part of [M2 — full trace surface](../m2.md), in [Implementation
guide](../../implementation.md).

1. **Process tree** (done, task pa4). ior's `internal/c/exec.c`
   handles `task:task_newtask`, `sched_process_exec` and
   `sched_process_exit` — but only to *report* tasks; its filter
   drops a forked child because the child has a new tgid. The
   new-task handler now inserts a traced creator's child into
   `target_map` before the child is first woken, so no record of
   it is lost (`iormesh_follow_child`):

   ```c
   // Runs in the creator's context inside copy_process(). A new
   // thread shares its creator's tgid and is covered already.
   if (clone_flags & IOR_CLONE_THREAD)
       return;
   if (!iormesh_pid_targeted(creator_pid))
       return;
   traced.state = IORMESH_TARGET_TRACED;
   if (bpf_map_update_elem(&target_map, &child_pid, &traced, BPF_ANY))
       ior_count_ringbuf_drop(); // a full map loses the child, counted
   ```

   ior's CO-RE "flavor" structs (`___ior` suffix) stay for the
   contexts. The exit handler removes a tgid once its whole group
   is dead (`iormesh_forget_target`). Userspace never re-inserts
   an entry the kernel may have removed: a pattern match is
   adopted only by updating its still-existing pending entry
   (`AdoptExec`, `BPF_EXIST`), and a match the start scan found is
   removed again if it exited before its insert — "exited" includes
   a task already in `do_exit` (`PF_EXITING` in `/proc/<pid>/stat`),
   whose exit handler has run while its state still reads R/S/D.
   So no entry outlives its process, and a reused PID is not
   traced. The three handlers attach as `bpf.ControlProbes`. The
   correlator registers a forked child at its `task_newtask`
   record (its incarnation is read from `/proc` then, and dropped
   to 0 if it postdates the record — see "Envelope") and drops a
   dead thread's pending enter at its exit.
   Root tests: `TestFollowForkExec` (a traced bash's forked and
   exec'd child is traced and leaves `target_map` once it exited)
   and `TestUntracedForkNotFollowed` (the negative, then the
   positive control). The `fork-exec` workload scenario is not
   written yet; `fd-lifecycle`, `pipe-ipc` and `signals` spawn
   child JVMs and exercise following once their families land.

2. **Target spec** (`internal/target`, done, task pa4):

   ```go
   // Spec says which processes a session traces; exactly one is set.
   type Spec struct {
       PIDs       []uint32
       Pattern    string // glob over the command line or the exe
       ListenPort uint16 // the owners of this listening socket
   }

   func (s Spec) Resolve(p ProcFS, self uint32) ([]uint32, error)
   func (s Spec) Matches(exe string, argv []string) bool
   ```

   `iormesh run` takes `-pid`, `-pattern` or `-listen-port`
   (exactly one). The glob's `*` crosses `/` and spaces, `?` is one
   byte, `\` escapes; a pattern without a literal character is
   refused (it would trace every process). `Matches` tries the
   argv joined by spaces, then the exe path — never the comm.
   `-listen-port` resolves the owners at start
   (`/proc/net/tcp{,6}` LISTEN inode → `/proc/*/fd`), never a
   service manager (an exe named `systemd` holding a
   socket-activated listener). Task mh4 follows the port across
   restarts (decision #23, `internal/cli/portfollow.go`): the
   session keeps each start owner's `target.Owner` (exe without
   " (deleted)", effective uid) whose exe is the earliest-started
   owner's (task qj4: a privilege-separated server's root master
   and its workers under another uid are all kept, a different
   program with an inherited fd is not); once its live set is
   empty, `session.ended` re-resolves the port every
   `portFollowEvery` (1s) for `-listen-grace` (default 2m) via
   `ListenHolders` (each holding pid with the start time read
   before a second read of its fd table), adopts a holder with a remembered key whose pid
   still has the listed start time (`AddTarget`, the liveness
   re-check of `addRunning`, `Correlator.Seed` → an `attach` of
   the new incarnation) and names refused holders once on
   stderr. No match within the grace ends the session
   `targets_exited` with a stderr line saying so; 0 ends at
   once. `TestListenPortFollowsRestart` (integration, root)
   kills and restarts an `edgepeer port-owner` on one port.
   Task lk4: a port only a manager holds is `target.ErrManagerOnly`;
   with a grace, `portActivation` pins it
   (`ProcFS.ManagerListener`: the manager-held LISTEN inodes and
   each manager's unit root, `/` for pid 1, the parent of a user
   manager's `init.scope`), the session starts with no target and
   `awaitActivation` stands in for the follow: each second
   `ProcFS.ActivatedHolders` lists holders of a pinned inode still
   listening on the port, adopting those in a `.service` unit
   below a root (slices skipped, `user@*.service` excluded) and
   naming the rest once. The first adopted holders are
   `remember`ed as start owners, the pin dropped. Unit tests use
   the fake procfs (`internal/target/activation_test.go`) and the
   fake port (`internal/cli/activation_test.go`).

   Pattern sessions load the object with `IORMESH_EXEC_WATCH`
   set: every exec of an untraced process is reported as an
   `exec_argv_event` (exe from the tracepoint, the first 1 KiB of
   the argument block; userspace re-reads `/proc/<pid>/cmdline`
   when it was cut) and marked PENDING in `target_map` first.
   Userspace adopts a match (`bpf.Tracer.AdoptExec`) or dismisses
   it (`IgnoreExec`); a match that exited before the verdict is
   not adopted and is no incarnation. Processes already running at
   start are found by a `/proc` scan after the probes attached,
   skipping the tracer, its ancestors and every other process
   running the tracer's executable, whose command lines name the
   pattern. Matching the whole command line is decision #23's
   default and it is broad: `grep RestartApp`, or an editor on a
   file of that name, matches `*RestartApp*` as well, so a pattern
   should name what only the app's command line carries. While PENDING, `filter()` counts every record the
   process loses: that is the window between exec and insert, and
   `AdoptExec` returns it as the gap, which the correlator writes
   as a `drop` naming the process with `args.drop_reason`
   `attach_gap`, ahead of its first traced event — never silently
   (a lower bound; 0 writes nothing). Children a process forks
   while it is still PENDING are not followed, and their records
   are not in the gap either: until the verdict nothing says the
   tree belongs to the session. A PENDING entry carries its
   exec time, so the dismissal of a posix_spawn child's first exec
   (`jspawnhelper`) cannot remove the entry of its second (the
   app): `IgnoreExec` removes only an entry a plain read shows as
   its exec's, takes it out atomically (lookup and delete in one
   syscall, hash maps since 5.14) and puts it back if a later exec
   replaced it after the read, removing it again if the process
   exited meanwhile; records in that window miss the gap, which
   stays a lower bound. A kernel before 5.14 lacks the atomic
   take-out; there the dismissal falls back to a plain delete and
   the race remains. A pattern session never ends by itself — it waits for
   re-appearance (decision #23) until `-duration` or a signal, and
   a stop drains the records in flight (at most 2 s). PID and port
   sessions end once every target and followed child is gone,
   judged by exit records and by `/proc` (start time, tgid, not a
   zombie; an unreadable start time counts as gone), so a reused
   PID cannot keep a run alive.
   Root test: `TestExecWatchAdoptAndIgnore`. Scenario: `restart`
   (landed ahead, task oa4): a launcher runs `RestartApp` as a
   child JVM, reaps it and runs it again with the identical argv
   (`java <flags> -cp <cp> iormesh.workload.scenarios.RestartApp
   <dir>`); the first incarnation finds no state file (a failed
   `openat`) and writes one, the second reads it and rewrites it.
   `TestRestartPattern` binds the session to the glob
   `*iormesh.workload.scenarios.RestartApp*`, which matches both
   incarnations and never the launcher (`-jar workload.jar
   restart`), so each incarnation emits its own `restart` markers
   and the per-process cut keeps it without fork-following. The
   JVM's own startup runs between exec and the begin marker, so an
   `attach_gap` drop lands before the bracket; the test fails on
   one inside it.

3. **App identity** (done, task pa4): all incarnations matched by
   one pattern share an `app:<project>/<pattern>` node
   (`schema.NodeApp`, `schema.AppNodeID`) with `incarnation` edges
   (`schema.EdgeIncarnation`, app → process), which
   `Graph.Validate` accepts and `correlate.Correlator.Incarnation`
   adds. `restart`'s golden declares them from one `$a1`; the
   normalizer passes `app:` ids through unchanged (they carry
   nothing run-specific). Its bracket prune keeps an `incarnation`
   edge, and so its app node, when the edge's process has events
   in the bracket — the app node never has events of its own
   (`TestRestartGoldenEndToEnd` runs the golden through
   `Normalize`; `TestRestartPattern` runs it against the real
   tracer).
