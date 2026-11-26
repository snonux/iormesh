# M3 steps 5–6: the join, method and thread tables

Part of [M3 — Java agent](../m3.md), in [Implementation
guide](../../implementation.md).

5. The Go side joins the agent bracket to the syscall. Done (task
   2b4) as `correlate.JavaJoin` (`javajoin.go`, `javajvm.go`,
   `javaattr.go`), a `schema.Sink` between the correlator and the
   events file, switched on by `iormesh run -spool DIR`
   (`internal/cli/java.go`; `agentspool.Dir` finds and vets the
   rings — see [schema.md](../../schema/java.md#java-attribution-m3)):

   ```go
   // innermost: per tid, brackets sorted by exit (ring order is
   // exit order per thread); the first that ends at or after t and
   // began at or before it is the innermost container.
   func (v *jvm) innermost(tid int32, t uint64) (b agentspool.Bracket, found, ambiguous bool)
   ```

   2b4 shipped the join without the BPF uprobe mark, the hook's
   allowlist its only guard against JVM work of an allowed kind
   inside a bracket. Task td4 built the mark
   (`internal/c/javamark.c`):

   - `iormesh_java_native_enter`/`_exit`, `uprobe.multi` and
     `uretprobe.multi` programs, push and pop a per-tid stack of
     `{cookie, entry ns (CLOCK_MONOTONIC), user sp}` (depth 4, LRU
     `java_mark_map`). The return pops every frame entered at or
     below its stack pointer and never an outer native's, so a
     return whose push was never seen pops nothing it does not own
     (the return probe is attached first for this reason), and a
     push whose return never runs is cleared by the next return
     at its depth or above; deeper than 4, the mark says "unknown"
     (cookie 0) until the thread leaves every native.
   - A generated hook in every enter handler,
     `iormesh_java_mark_on_enter` (`renderJavaMarkHook`, placed like
     the stack hook), sends a `java_mark_event` (cookie, entry) ahead
     of the enter record, stamped with the enter's own time. Load-
     time switch `IORMESH_JAVA_MARK` (`bpf.Config.JavaMarks`, on with
     `-spool`); off, the lookup is pruned, the map shrinks to one
     entry and the uprobe programs are not loaded (no uprobe_multi
     needed). The thread-exit handler deletes the tid's mark.
     `uretprobe(2)` (x86_64 nr 335) is not in the default families,
     and its kind is in no allowlist, so it never joins.
   - `bpf.Tracer.AttachJavaMarks(pid, natives)` finds the JDK's lib
     directory through the JVM's `libjava.so` mapping and opens
     `libjava`, `libnio` and `libnet` as the JVM sees them, never
     by plain path (`javalibs.go`): a mapped one through
     `/proc/<pid>/map_files`, an unmapped one (libnio and libnet
     load lazily) with `openat2(RESOLVE_IN_ROOT)` under
     `/proc/<pid>/root`, all non-blocking and kept only as regular
     files, so a planted FIFO or symlink cannot hang or redirect
     the tracer. It resolves each native's JNI symbol
     (`agentspool.Hook.Symbol`) to a file offset in the opened file
     and attaches the return and then the entry program by that
     descriptor (`/proc/self/fd/N`: the file probed is the file
     parsed), with the JVM's pid (uprobe_multi links: no other
     process traps; Linux ≥ 6.10 or a stable kernel with commit
     46ba0e49, which made the pid filter cover every thread) and
     one cookie per native. The joiner calls it once per ring,
     after the first drain that read the HOOK rows, numbering
     natives by symbol; `iormesh run` also polls the spool once
     before its ready file, so JVMs already running are probed
     before the tracer says it is live. `DetachJavaMarks` drops a
     JVM's links when its thread group dies (pattern sessions),
     and `Close` drops the rest — libbpfgo does not track
     uprobe_multi links.

   The join: the session hands every mark record to
   `JavaJoin.Mark`, keyed by (tid, enter time) = the event's
   (tid, `boot_ns`). An event joins the innermost bracket on its
   tid containing the mark's native entry — the agent's own clock,
   no conversion — whose hook's native has the mark's cookie and
   whose allowlist has the event's kind; the syscall's start,
   converted with the user-space midpoint offset (S1 experiment
   2), must not lie after that bracket's exit, which catches a
   stale mark. Never guessed: no mark (issued outside every hooked
   native: JVM work), another native, no bracket, another kind,
   brackets that overlap without nesting, a folded read/write, or
   a stack the agent did not deliver leave `attribution: none`,
   each counted in the summary line; a JVM whose natives could not
   be probed (an attach error, none of its natives found) keeps
   all its syscalls `none` and is reported and counted, and so is
   one whose threads close 64 brackets after the probe without
   sending one mark (a kernel whose pid filter matches one thread). A
   virtual thread's I/O gets `java.carrier` and the carrier's
   name.

   Release: a JVM's event waits until the ring's watermark is
   5 ms (`Grace`) past the syscall's end — the watermark only
   covers brackets that already exited, and a bracket exits a
   little after its native's last syscall — or 2 s (`Timeout`);
   a syscall that ended before the ring existed is decided at
   once; another process's event leaves once a spool scan after
   it found no ring (scans every 50 ms and on the run loop's
   tick). Brackets are kept 10 s past the watermark: the
   correlator holds socket events up to its 5 s fuse window.
   Tests: `javajoin_test.go` (fake rings: join, every `none`
   reason, nesting, carrier, waiting, timeout, stale ring of a
   reused pid, corrupt ring; td4's negative cases — a read inside a
   `FileInputStream.read` bracket with no mark, another native's
   mark, an unknown native or a stale mark stays `none` — the
   probe once per ring with every native's cookie, and mark
   pruning), `javamarkhook_test.go` (the hook's placement in every
   enter handler), the root test `TestJavaMarkNamesTheNative` (a
   real JVM: its FileInputStream open and read carry `open0`'s
   and `readBytes`' cookies with an entry stamp, while `info0`'s
   `/proc` reads carry none), the root test
   `TestJavaMarkOnHeldRead` (a 16-byte FileInputStream loop with
   the fold on: the EOF read the fold held still carries
   `readBytes`' mark, task ti4), `agentspool` dir tests (symlink,
   hard link, foreign owner, pid mismatch), and the integration
   test `TestJavaMethodFlow`: `method-flow` under the agent
   satisfies its `expect-pending.json`, every declared op on its
   declared thread and method.
6. Method table and thread table are emitted as their own NDJSON
   files, `methods.ndjson` and `threads.ndjson` (`schema.Method`,
   `schema.Thread`, `schema.TableWriter`;
   [schema.md](../../schema/java.md#java-attribution-m3)), keyed by id. Done (task 2b4) for NDJSON; the types carry
   `parquet:` tags; the M4 segment writer (task 8b4) writes them as
   segment tables with `-trace-dir`. A method id is one
   distinct frame (`class#method(file:line)`, parsed into class,
   method, file, line), keyed by the frame text and its class.

   The class-load map (task fe4; milestones.md M3 "Supporting
   mappings" has the decision) is a third table,
   `classes.ndjson` (`schema.Class`): one row per class a
   written method row names — class, loader label, code source —
   deduplicated across the trace's JVMs, and each method row
   carries its `class_id`. As built:

   ```
   agent/bridge/src/iormesh/bridge/ClassIds.java  # ClassValue ids, CLASS
   agent/bridge/src/iormesh/bridge/StackIds.java  # class id per frame
   internal/agentspool/record.go                  # Class, Stack.Classes
   internal/correlate/javaclass.go                # class rows, renames
   internal/schema/javatables.go                  # Class, CleanCodeSource
   ```

   - **Frame classes only.** `StackIds` walks with
     `RETAIN_CLASS_REFERENCE` (no measurable cost: the VM fills a
     frame's class either way) and, for a new stack only, asks
     `ClassIds` for each frame's class id. Every loaded class
     would cost ring space and agent work for classes no I/O
     stack ever names.
   - **No pinning, no locks.** The id lives in a `ClassValue`,
     whose `computeValue` runs without its lock; the CLASS record
     is written there, so a STACK naming the id lies behind it in
     the ring. Describing a class reads its loader, protection
     domain and module — nothing initializes a class.
   - **Code sources**: a `file:` location as a path, a boot
     loader class of the runtime image as `jrt:/<module>` (the
     platform loader's say jrt themselves), other URLs without
     userinfo, query or fragment — stripped in the agent and again
     by the tracer (`schema.CleanCodeSource`, the same algorithm
     as `ClassIds.clean`).
   - **Losses**: a dropped CLASS is retried when a later new
     stack names the class; a frame whose class never arrived
     gets a method row without `class_id`. The tracer resolves a
     stack once, when its first event joins, so a stack resolved
     before the retried CLASS arrived keeps no class for those
     frames even after it does (the drop is counted).
   - **SecurityManager** (JDK 17–23): the walker retaining
     classes and a class's protection domain need permissions
     the app's frames may lack, so the bridge (boot class path)
     asks for both in `doPrivileged`; a policy refusing even that
     gets the plain walker and stacks without class ids, never a
     failed bracket. `RingTest` runs a child JVM under a
     SecurityManager to check it (skipped on 24+, which has none).
   - **Renames**: a THREAD for a known Java thread is a rename;
     the thread's brackets read before it keep the earlier name
     (up to the latest exit among them still held), later ones
     carry the new name, and `threads.ndjson` gets a row per name
     change. An http or dns record still pending that completed
     after the thread's last held bracket but before the rename
     (any, when none is held) gets the new name.

   Tests: `RingTest` (CLASS ahead of the STACK, labels and code
   sources, rename), `ring_test.go` (round trip, malformed CLASS
   and STACK bodies, the counters' offsets), `javaclass_test.go`
   (dedup, another JAR, a lost CLASS, credentials, renames),
   `javatables_test.go`, `TestJavaAgentWorkload` (every stack's
   class ids resolve; the workload jar and java.base appear) and
   `TestJavaMethodFlow` (every method's class resolves; the
   scenario's frames map to the workload jar, the JDK's to jrt).
