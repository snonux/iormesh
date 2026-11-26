# M2 steps 8–11: redaction, native stacks, non-Java peer, budget

Part of [M2 — full trace surface](../m2.md), in [Implementation
guide](../../implementation.md).

8. **Redaction** (`internal/redact`, cgo-free — M3 and M5 reuse
   it; done, task ta4):

   ```go
   // Policy masks secrets before anything is written: deny globs
   // ('*', '?') over keys folded to lower case without '-', '_'
   // and '.', so "*apikey*" covers --api-key, API_KEY, apiKey.
   type Policy struct {
       ArgvDeny   []string   // "*password*", "*token*", "*secret*", …
       FlagRules  []FlagRule // {Program: "mysql*", Flag: "-p"}, …
       HeaderDeny []string   // "*authorization*", "*cookie*", …
       URLHeaders []string   // "location", "xoriginaluri", …
       QueryDeny  []string   // "*": every query value (#22)
   }

   func Default() Policy
   func (p Policy) Argv(argv []string) []string
   func (p Policy) Header(name, value string) string
   func (p Policy) URL(raw string) string  // absolute or request target
   func (p Policy) Text(s string) string   // URLs embedded in text
   ```

   A masked value becomes `"***"` and the structure stays:
   `-Dpassword=***`, `--token ***` (a bare denied option masks the
   *next* element, whatever it looks like — `-Dname` without `=`
   excepted, it sets a property to ""), `DB_PASSWORD=***`,
   `https://user:***@host/?q=***` (userinfo always; a lone
   userinfo, a token, whole; an '@' past the first '/' still ends
   a userinfo that looks like `user:pass`). Options too generic to
   deny everywhere are scoped by program (`FlagRule`, matched
   against every program word before the option, so `sudo`,
   `env`, `timeout` and `docker login` are covered): `-p` is
   mysql's password but java's module path, so `mysql -psecret` →
   `-p***` and `java -p mods` stays. An element of several words
   (`"--token x"`, `sh -c "…"`) is redacted word by word, shell
   quoting respected; `Name: value` elements (`curl -H`,
   `--header=`) go through the header denylist, colon pairs
   (`--api_key:x`, `pass:x`) through both. The defaults over-mask
   rather than leak (`--password-stdin` masks its successor); a
   secret under an innocuous name is not recognized — a denylist
   is best effort, and the environment is never captured.
   There is no allowlist in v1 (#22's default). The Java agent
   (M3) cannot import Go: it either sends headers to the tracer
   for `Policy.Header` before the spool or mirrors the defaults —
   M3 decides, keeping `Default()` the one list.

   Capture: every exec of a traced process sends one more record
   after `process_exec_event`: `PROCESS_ARGV_EVENT`, the
   `exec_argv_event` struct of the exec watch (exe, the first
   1 KiB of argv) under its own type, so `internal/cli` records
   it instead of judging it. A cut block is completed from
   `/proc/<pid>/cmdline` only when that starts with the copied
   bytes (the process may have exec'd again, or its PID been
   reused); otherwise the cut argv stays, its partial last element
   masked (`ExecInfo.Cut`). Records are never logged: they carry
   the raw argv. The exec a
   pattern session adopts is recorded too, after its
   `attach_gap` drop. `correlate.Correlator.Exec` builds the
   `exec` event — `args.path` the exe, `args.argv` redacted
   there, before the event exists (`Config.Redaction`, nil =
   `Default()`). The normalizer rewrites argv elements under the
   temp dir like paths. A process already running at attach (a
   `-pid` or port target, the start scan's matches) has no exec;
   task nh4 puts its argv and exe on its `attach` event instead
   (`cli.attachIdentity` reads them with
   `target.ProcFS.Command`, which re-reads the stat to reject a
   reused PID; `Correlator.attach` redacts them). Graph node attrs
   stay `comm` only.
   Scenario: `argv-secret` — the workload starts a child JVM
   with `-Dpassword=S`, `--token S`, `--api-key=S` and a URL
   with userinfo and a query value; its golden declares the
   child's `exec` and lists `S` in `must_not_appear`.
   `TestArgvSecretRedacted` also checks the raw `events.ndjson`
   and `graph.json` and that each shape is in the exec's argv,
   masked. Unit tests: `internal/redact` (table, negatives,
   multi-word, header and wrapper cases, `FuzzArgv`,
   `FuzzArgvOneElement`), `TestExecRecordsRedactedArgv`,
   `TestExecArgvRecordedRedacted` (cli routing),
   `TestCutArgvCompletion` (/proc agrees, unreadable, exec'd
   again).

9. **Native stacks** (opt-in, `-native-stacks`; done, task
   ua4): `bpf_get_stackid(ctx, &stack_map, BPF_F_USER_STACK)`
   into a `BPF_MAP_TYPE_STACK_TRACE` (8192 buckets of 64
   frames, no `BPF_F_REUSE_STACKID`, so an id never changes
   meaning during a session), symbolized in userspace from
   `/proc/<pid>/maps`. Off by default; documented as not useful
   for JIT frames.
   - **Kernel** (`internal/c/stack.c`): every generated enter
     handler calls `iormesh_stack_on_enter(ctx, pid, tid, now)`
     after the hooks that may drop the enter (sampling, the
     read/write fold) and before its record
     (`renderStackHook`; `TestStackHookOnEveryEmittedEnter`
     pins the placement, `TestCommittedHandlersCallTheStackHook`
     the committed file). It sends a 40-byte `stack_event` (tid,
     the enter's own `now`, the id or the walk's negative errno,
     the image tag `mm->arg_start`) ahead of the enter record.
     A read or write the fold held and then sends alone gets its
     stack (and Java mark) from rwagg.c's owed enter at
     `sys_exit` instead (`TestHeldEnterCarriesMarkAndStack`).
     `IORMESH_NATIVE_STACKS` is a load-time constant, so with the
     flag off the verifier prunes the call and userspace shrinks
     `stack_map` to one entry (it is preallocated, ~4.3 MB). User stacks only: at
     `sys_enter` the kernel stack is the entry path.
   - **Correlator** (`internal/correlate/stacks.go`): a stack
     record is turned into a trace id by `Config.Stacks` and
     parked per tid; `base` hands it to the event whose enter has
     the same time, so a stack whose enter was lost pairs with
     nothing. Without the flag the map stays empty and `base`
     pays a length check. An exec tells the table a new image
     begins.
   - **Table** (`internal/stacks`, cgo-free): ids are per process
     image and kernel id — the same addresses in two images are
     two call sites — numbered from 1. The image is (pid,
     incarnation, tag), the tag being `mm->arg_start`, which the
     stack record carries and `/proc/<pid>/stat` shows (field
     48); a snapshot of `/proc/<pid>/maps` is believed only when
     stat shows the record's incarnation and tag before and after
     the read, so a late record of an exec'd, exited or reused
     process keeps bare addresses. A new stack is read from the
     map (`bpf.Tracer.StackTrace`), its image's maps are
     snapshotted and the files they map opened right away,
     through `/proc/<pid>/map_files/<range>` (the mapped file
     itself: no path under the traced process's control is
     resolved as root), `O_NONBLOCK`, regular files only, because
     a short-lived process takes both with it when it exits; the
     slow part — parsing ELF symbol tables, a 20 MB libjvm once —
     runs on a worker goroutine, behind a `recover` (debug/elf is
     not hardened against hostile files), that writes
     `stacks.ndjson`. Modules are cached by device, inode, size
     and mtime; uncovered addresses are remembered per image; a
     process's ids and snapshots are dropped at its exec and exit
     (and a thread's parked stack at its exit). The first version
     symbolized inline and resolved nothing for the
     non-java-peer's Go peer: the correlator was still parsing the
     JVM's libraries when the peer exited. Frames keep the raw
     address and add module, file offset, GNU build id and
     `.symtab`/`.dynsym` symbol where known; the run's summary
     line adds `native stacks N (inline I; uncaptured M:
     collisions C, faults F, other O)`.
   - **Collisions** (task kd4): with no `BPF_F_REUSE_STACKID` a
     stack hashing to a taken bucket got `-EEXIST`, and
     collisions grow with the square of the distinct stacks
     (2–7 of ~194 per non-java-peer run, enough to flake
     `TestNativeStacks`). Any `bpf_get_stackid` error but
     `-EFAULT` now falls back to `bpf_get_stack` into a 552-byte
     `stack_inline_event` carrying the frames; `Table.InlineID`
     gives such stacks ids by their frames per image. Ids still
     never change meaning.
   - **Schema**: `Event.StackID` (`stack_id`, appended, optional)
     and `schema.Stack`/`StackFrame` with a strict reader
     (`ReadStacks`); docs/schema.md "Native stacks".
   Tests: `internal/stacks` (maps lines, build-id notes, symbol
   lookup; with a fake /proc: opening through `map_files`, a FIFO
   there neither blocking nor parsed, another image or
   incarnation keeping bare addresses, a known miss not
   re-reading; garbage and corrupt ELF and `FuzzSafeReadModule`;
   id rules, a write error ending the trace), the
   correlator's pairing (`stacks_test.go`), schema round trip
   and strictness, `TestOpenStacks*` (a stale table removed when
   off); root `TestNativeStacksOnlyWhenAsked` (a one-entry map
   and no stack record without the flag; with it the full map
   and a stack record stamped with the open's own time and a
   non-zero image tag, readable back); integration
   `TestNativeStacks` (non-java-peer with
   `-native-stacks`: the golden still passes, every `stack_id`
   resolves to a stack of its own process, and the Go peer's
   `accept4` and log `openat` stacks reach `main.*` in the
   echopeer binary). Cost: [s3-overhead.md](../../spikes/s3-overhead.md),
   "Follow-up (ua4)".

10. **Non-Java peer** (done, task ua4): scenario
    `non-java-peer` (landed ahead, task oa4). Its peer is
    `workload/echopeer`, a cgo-free Go echo server that `mage
    workloadBuild` builds next to the jar — ior's `ioworkload`
    has no scenario that serves a TCP client. It uses raw
    `syscall` calls on a locked main thread (socket, bind,
    listen, `accept4`, read, write, close, then an
    `openat`/write/close of `$tmp/non-java-peer.log`), so its
    declared syscalls are exact and carry TID == PID; it
    brackets itself with the scenario's markers. The JVM spawns
    it, connects over AF_INET, then reads the log back. The
    golden declares `attribution: none` on every peer event;
    the tracer must never attribute a non-JVM process (no
    agent, no Java fields). `TestNonJavaPeer` passes it under
    the real tracer (the peer is followed as the JVM's forked
    child, found by its `exec` of `echopeer`), checks the
    degraded shape on the raw trace — every event `attribution:
    none`, no `java` or `http` payload — and that a default run
    has no `stack_id` and no `stacks.ndjson`; mutations (the
    peer attributed to Java, its connection unresolved) fail.
    In M2 no agent exists, so every process has this shape; M3
    must keep it for the peer once JVM events gain `java`.
    Seen while writing the test: a process's `comm` was what the
    correlator first read for its pid, so a child the JVM forked
    kept the JVM's comm after its exec. Fixed (task hd4): the exec
    record's post-exec comm renames the process for its `exec`
    event, every later event and its graph node; the test now
    also checks the peer's exec and accept4 carry `echopeer`.
11. **Budget (decision #25, from S3)**: in-kernel aggregation of
    hot read/write loops (done, task vc4), after ior's
    `syscall_aggregate_map` — repeated read/write on one fd
    instance by one thread summed per flush into one event with a
    count; every state-changing event stays individual.
    `internal/c/rwagg.c` keeps a per-thread `rw_agg_map` entry of
    four keys (enter trace id, fd, file identity). The generated
    read and write handlers (`internal/generate/rwagg.go`) call
    `ior_on_rw_syscall_enter` — the one enter hook that does not
    flush — then `iormesh_rw_on_enter`, which holds back the enter
    record of a *hot* call (within 1 ms of the key's previous exit)
    once the key has sent four hot calls individually; the exit
    hook `iormesh_rw_on_exit` (given the per-tid exit hook's
    verdict, and clearing the in-flight markers whatever it is)
    folds a held call that returned data within 1 ms. Flush points:
    every other enter hook (`ior_on_syscall_enter*` in filter.c
    flush the thread first); every read or write sent as a single
    event sends all the thread's pending sums first (a held call
    that turns out not hot: the sums, the enter it owes, its exit;
    the owed enter sends the native stack and Java mark records
    the held enter skipped, stamped with its enter time — the
    thread is still in the same native at `sys_exit`, task ti4);
    100 ms of span or the count cap, checked when a call folds; a
    fifth key (LRU); `signal_generate` for a signal the thread sent
    itself (never a kernel-raised one, which may fire in an
    interrupt inside a hook) and `task_newtask` in the creator's
    context; exec and thread exit (which also drop the entry). At
    the session end `traceLoop` sets `rw_agg_stop`
    (`bpf.Tracer.StopAggregation`) before its final drain — from
    then on no hook folds or flushes — and `finish` reads the
    frozen sums (`PendingAggregates`) into the correlator. A flush
    that finds the ring full counts one drop. The record is
    `rw_aggregate_event`; the correlator (`aggregate.go`) writes it
    as a read or write with summed `bytes`/`ret`/`latency_ns` and
    `args.count` (only when ≥ 2, so a single call is a plain
    event). The kernel keys by fd number (iormesh leaves
    `IOR_FILE_IDENT` off, so the identity is 0), so the correlator
    resolves the fd instance by time — `procState` keeps each
    instance's birth and the number's previous instance — and
    requires one instance live over the record's whole span; a
    record straddling another thread's close/dup2/reopen becomes
    an `unattributed` drop. The golden check matches a declaration
    without `args.count` only to a single call, and a lint
    (`TestGoldensDeclareFoldableRunsShort`) keeps goldens to at
    most four declared single reads or writes in a row on one fd.
    `iormesh run -aggregate=false` (rodata `IORMESH_RW_AGGREGATE`)
    turns it off. Tests: `rwagg_harness_test.go` compiles rwagg.c
    against a simulated map and ring and drives the fold, flush
    and stop rules, with mutations; correlator, schema, checker and
    parser unit tests; `TestSyscallBurstFoldsEveryCall`, which also
    requires zero unpaired exits on a run without counted loss.
    Review round 2 found the one silent loss left: a tracepoint
    program the kernel skips under its recursion guard (another BPF
    program running on that CPU) loses its record without a trace;
    `bpf.Losses.Missed` sums the attached programs'
    `recursion_misses` (`progmiss.go`) into `drop` events. Task vd4
    then moved it out of the lost records: the skip comes before
    the target filter, so the count is host-wide possible loss —
    its own `skipped_runs` drop and "skipped runs" summary figure;
    the integration harness still retries such a run. Task 8e4
    found why loaded hosts skipped in every run (Linux 7.2 runs
    per-syscall tracepoint programs preemptibly behind a
    per-program guard) and attaches the syscall handlers through
    two classic `raw_syscalls` dispatchers instead
    (`internal/c/dispatch.c`, `internal/bpf/dispatch.go`). Plus a
    cheaper consumer to reach
    ≤ 2 µs per event (done, task wc4: a hand-written encoder
    byte-identical to `encoding/json`, pooled records recycled,
    batched records read from the mapped ring; validation stays
    on the hot path, it costs ~15 ns once it stopped copying the
    event; numbers in
    [s3-overhead.md](../../spikes/s3-overhead.md), "Follow-up (wc4)";
    benchmark `go test ./internal/correlate -bench SyscallBurst`,
    profile `iormesh run -cpuprofile FILE`). Measured with
    `spikes/s3/measure.sh` against the #25 budget.

**Done when**: milestones.md's M2 acceptance holds, including the S3
budget (decision #25) checked by a documented
`spikes/s3/measure.sh` run by #25's paired CPU-time method — not
a CI gate, CI machines are too noisy. M2's two timing items moved
to task md4, which gated M3's acceptance; both pass
([s3-overhead.md](../../spikes/s3-overhead.md), "Follow-up (md4)").
