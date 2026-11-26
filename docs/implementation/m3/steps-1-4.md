# M3 steps 1–4: build, install, hook table, ring

Part of [M3 — Java agent](../m3.md), in [Implementation
guide](../../implementation.md).

1. Build: `mage agentBuild` → `bin/iormesh-agent.jar` with the bridge
   jar nested as a resource; ASM shaded under
   `iormesh.agent.shaded.asm` (the S1 prototype used
   `java.lang.classfile`, final only in JDK 24 — M3 must run on
   21 and, per #29, 17). Manifest: `Premain-Class`,
   `Agent-Class`, `Can-Retransform-Classes`. Done (task 0b4):
   JDK-only like the workload, `--release 17`. ASM is vendored —
   the released `asm-9.11.1.jar` with its BSD-3 license and a
   PROVENANCE file (URL, SHA-1 matching Maven Central, SHA-256)
   — rather than downloaded by mage: build and tests stay offline,
   and mage checks the pinned SHA-256 before use. Shading needs
   no Maven: `agent/tools/Relocate.java` rewrites the constant
   pool's Utf8 entries (`org/objectweb/asm/` →
   `iormesh/agent/shaded/asm/`) and copies the rest of each class
   file unchanged; the core imports the relocated package
   directly. 9.11.1 reads class files up to Java 28; a JDK newer
   than ASM leaves the class unrewritten (stderr note, no
   attribution), never a broken target.
2. Install, from `premain` and `agentmain` alike: append the
   bridge jar to the bootstrap search, then
   `Instrumentation.redefineModule(java.base, extraReads =
   {bridge's unnamed module})` — without it the rewritten JDK
   methods fail to link `Hooks`. Map the ring, warm up the hot
   path (thread state, `StackWalker`), then retransform. Expect
   the CDS warning on stderr ("Sharing is only supported for boot
   loader classes…") — the workload's stdout contract is intact.
   Done (task 0b4; the ring by task 1b4, step 4): the nested
   bridge jar is copied to a temp file (the bootstrap path takes
   files only) and deleted once the bridge is loaded and warm —
   the JVM keeps the jar open, so nothing is left behind even on
   SIGKILL; the bridge holds `Hooks` *and* `StackIds`, since
   rewritten java.base code cannot see the agent's loader; the
   transformer rewrites one class of its own before the first JDK
   class, so ASM's class loading never lands in a bracket. `Hooks`
   kept S1's `enter(id)`/`exit(id)` with a per-thread bracket
   stack (not the token sketched in S1's Experiment 2): nested
   hooks need the stack anyway. Neither ever throws into the
   target, and the depth stays balanced when the walk or the
   bookkeeping fails (a slot is marked skipped until the bracket
   is fully open; a throwing exit would run the catch-all's exit a
   second time and close the caller's bracket). A second
   `-javaagent` or attach finds the bridge claimed and installs
   nothing. A closed bracket goes to the ring (step 4); with the
   agent option `debug=<path>` it also bumps a per-hook counter,
   and the table is dumped with each row's status (`applied`,
   `missing` = this JDK lacks the method, `unloaded`) and count at
   exit. Any install failure prints `iormesh-agent: disabled: …`,
   removes the transformer and leaves the JVM running; a failure
   before the first rewrite also releases the claim and deletes
   the ring, so a later live attach can still install. `BoundaryTransformer`
   rewrites only bootstrap-loaded classes of the table and only
   their listed methods (exact descriptors); it writes the
   catch-all handler's stack map frame itself instead of
   `COMPUTE_FRAMES`, which would load classes from inside a
   transformer. `StackIds` drops the bridge's own frames by
   package, keeps 10, hashes them with 64-bit FNV-1a (a collision
   would merge two stacks, undetected), and stores a stack's
   frames (`class#method(file:line)`) the first time its id is
   seen, for the ring's STACK record.
3. `HookTable` is data, not code: a table of
   `(class, method, descriptor, hookId, native, allowed syscalls)`
   so decision #29's per-JDK differences become table variants and
   the tracer gets the same table for its uprobe set (S1's hook
   table above). Done (task 0b4): one TSV per variant (columns id,
   kind, class, method, descriptor, native, syscalls) — `jdk17`
   for 17–20, `jdk21` for 21–24, `jdk25` for 25+, chosen by
   `Runtime.version().feature()`, below 17 no attribution. One row
   per overload; kinds come from a fixed set (open, read, write,
   close, seek, socket, bind, connect, listen, accept, shutdown,
   unlink, rename, stat). A test walks each row against the
   running JDK's class bytes: the method exists, has a body, the
   named native is a native method, and the method reaches it
   within three same-class calls (JDK 24 and later route
   `RandomAccessFile.write` through a JFR `implWrite` that 21 does
   not have); deliberately wrong rows must fail it. Beyond S1's
   table, 17 sends socket writes and closes from
   `SocketDispatcher` to `FileDispatcherImpl`'s natives, and
   `FileDispatcherImpl.close` reaches the already hooked
   `FileDescriptor.close`.

   Tests (`mage agentTest`): the transformer rewrites exactly the
   listed methods of a fixture (unlisted ones and an overload stay
   instruction-for-instruction the same), the rewritten class
   verifies and closes one bracket per call on return, exception
   and inner-handler paths, all inside a hand-opened outer bracket
   that must close exactly once; an unknown class, a class without
   a listed method and a non-bootstrap loader are no-ops; `Hooks`
   nesting, busy skips, overflow past 16 levels, mismatched exits,
   a throwing walk and a throwing exit; table parsing and the
   running-JDK check; `StackIds` per call site. Smoke:
   `AgentSmoke` (file streams, RAF, `Files` stat/move/delete, TCP
   loopback with a shutdown) under `-javaagent` must close a
   bracket of all fourteen hook kinds with no `missing` row, and
   `workload.jar file-basic` under the agent keeps its
   stdout/stderr contract and fires its open/write/read hooks.
   Checked on 25, 27-ea and 17 (GraalVM CE 17.0.9, via
   `IORMESH_AGENT_JDKS`); the smoke runs with boot-class
   verification on (`-XX:+BytecodeVerificationLocal`). Live attach
   fired the hooks on 25 and a doubled `-javaagent` installed once
   (by hand, not in `agentTest`). Not run: 18–20, 22–24. JDK 21
   (21.0.6, 21.0.12; task sc4) passes everything, smoke included,
   after one fix: 21 has no `Net.serverSocket(ProtocolFamily)`,
   only `serverSocket(boolean)` and `serverSocket(ProtocolFamily,
   boolean)` — S1's reading of the 21 sources had missed it. The
   `boolean` survives through 24 (`javap` of 22.0.2's and 24.0.2's
   class files; every other hooked signature matches 21) and is
   gone in 25, hence `jdk21` (21–24) and `jdk25` (25+), which
   differ in that one row.
4. The ring (decision #27): one file per JVM,
   `<spool dir>/<pid>.ring` (pid as the JVM sees it; or
   `<pid>.<hex>.ring` when another user holds that name, task
   9j4), in the tracer-owned directory, mapped `MAP_SHARED` by
   agent and tracer. The agent never creates the directory: a
   missing one disables the agent (no tracer is listening). Agent
   options: `spool=<dir>`, `ringsize=<bytes>` (a power of two, 64
   KiB–64 MiB, default 8 MiB; task yi4 lowered the cap from 1 GiB, and
   the reader refuses a larger header), `watermark=<ms>` (default
   10, 0 = only at install and exit). Done (task 1b4). The
   header, little endian,
   version-checked by the reader:

   ```
   0    magic "IORMRING" (stored last, release: header complete)
   8    version u32 = 8     12  header size u32 = 4096
   16   capacity u64        24  pid u64     32  created mono ns
   40   reader epoch u64 (each reader adds 1 at open; task 5j4)
   64   head (writers' CAS, own cache line)
   128  tail (reader's release store, own cache line)
   192  dropped records per type u64[16]
   320  dropped bytes       328  abandoned records
   4096 data[capacity]
   ```

   Records are framed by one 8-byte word `len | type << 32` (len
   counts the word, a multiple of 8) published with a release
   store after the body; the reader copies a record, zeroes it and
   moves `tail`, so every lap starts from zeros, and a record that
   would straddle the end of the data area is preceded by a PAD.
   Writers reserve with a CAS on one shared `head` — not a ring
   per thread, which would need a file or a fixed slot table per
   thread (thread count unknown at setup) and a merge in the
   reader to keep the watermark promise; the stack walk (≈8 µs),
   not the CAS, bounds a bracket's cost. When the reader is a full
   ring behind the writer drops and counts per type — never
   blocks the target. A fill that throws (a `StackOverflowError`
   deep in the target) is published as a PAD and counted as
   abandoned, so the reader never waits on it. Record bodies:

   ```
   PAD       0  (skipped)
   BRACKET   1  hook i32, ktid i32, enter_mono, exit_mono,
                stack_id, thread_id u64        // 48 bytes
   STACK     2  stack_id u64, n u32, 0 u32, n × class_id u64,
                n × (u32 len, UTF-8)
   THREAD    3  thread_id u64, ktid i32, flags u32 (1 = virtual),
                (u32 len, name)
   WATERMARK 4  mono_ns u64    // every bracket that exited
                               // before this is in the ring
   HOOK      5  hook_id u64, 5 u32, 0 u32, kind, class, method,
                native, syscalls (comma-separated), as strings
      HTTP      6  exit_mono, stack_id, thread_id u64, ktid i32,
                status i32, n u32, nreq u32, flags u32 (1 = server),
                exchanges u32, method, scheme, host, path, version,
                local, remote, nreq request headers, response
                headers (step 7)
   CLASS     8  class_id u64, 3 u32, 0 u32, name, loader,
                code source, as strings (task fe4)
   ```

   Version 7 (task fe4) added CLASS, the STACK's class ids and
   the sixteen drop counters (CLASS is the ninth type, which
   moved dropped bytes and abandoned); 4–6 grew HTTP and added
   DNS (`internal/agentspool/format.go` lists each bump).
   Version 3 (task 3b4) added HTTP (step 7). Version 2 (task
   2b4) added HOOK and THREAD's flags: the agent
   writes its whole hook table into the still empty ring at
   install, so the joiner reads each hook's syscall allowlist
   from the ring — whatever variant or `table=` override the JVM
   chose — and the tracer keeps no copy of the TSVs. A virtual
   thread (JDK 21+, `Thread.isVirtual` found reflectively, the
   bridge being built for 17) is flagged in its THREAD, and its
   brackets carry the carrier tid read again at enter and exit
   (two `readlink`s per bracket, virtual threads only), 0 when it
   parked and moved carriers inside the bracket.

   STACK and THREAD are zero-padded to 8 and capped at 16 KiB
   (strings cut to 1024 chars; the reader skips any record but a
   PAD over 64 KiB as malformed, undecoded); BRACKET and
   WATERMARK allocate nothing. A thread's first bracket writes
   its THREAD, a stack's
   first sighting its STACK. Neither is guaranteed to precede the
   BRACKETs naming it: the stack id is visible to other threads
   before its STACK is published, so a second thread on the same
   call site may publish its BRACKET first, and a dropped THREAD or
   STACK is retried later (after a 1 ms back-off, so back-pressure
   costs no allocation per bracket; each failed attempt counts as
   a drop). The joiner resolves ids against everything it has
   read. Sightings of stacks past StackIds' 65 536-entry cap count
   as dropped STACKs. The method table is the tracer's (step 6).
   A thread renamed after its first bracket writes another THREAD
   at its next bracket (an identity compare of `getName()` with
   the name last written, task fe4). A later reader gets all of
   these again (task 5j4, decisions.md #27): its open bumps the
   reader epoch, and the watermark thread resends the hook table
   (closed by a WATERMARK) and bumps a generation, so each
   THREAD, STACK and CLASS is written once more at its next
   sighting; [java-attach.md](../../java-attach.md) "Second sessions".

   Brackets are written at exit, so a syscall inside a still-open
   bracket has no record yet; the agent's watermark thread writes
   `WATERMARK` (a memory store, no syscall) so the joiner knows
   when to stop waiting. The S1 prototype had no watermark — it
   drained after exit. The promise holds without locks: a closing
   bracket marks its thread with its enter stamp before stamping
   the exit and clears the mark after publishing, entering a
   small set of closing threads for that time; a watermark reads
   the clock, then scans that set — never every live thread, so
   parked threads cost it nothing (task dj4) — and lowers itself
   to any marked thread's enter stamp. Both sides put a full fence
   between their volatile access and their clock read: the JMM
   does not order `System.nanoTime()` against volatiles, and on
   AArch64 the closing mark could otherwise become visible only
   after the exit was stamped. Install and JVM exit (a shutdown
   hook) write one too.

   The agent's syscalls are the ring's setup in `install`, one
   `readlink("/proc/thread-self")` per thread, and the watermark
   thread's sleeps; the tracer tags them as the agent's (spool
   path; first such readlink per TID before that TID's THREAD
   record; the watermark thread names itself
   `iormesh-agent-watermark` in its THREAD record) — never the
   target's. The exit hook's thread (debug dump) is never
   bracketed.

   Reader: `internal/agentspool` (cgo-free: `syscall.Mmap`,
   `sync/atomic` on the mapped words). `Open` rejects a header it
   cannot trust (`ErrNotReady` before the magic, `ErrVersion`,
   `ErrTruncated`, `ErrCorrupt`); `Drain` hands out `Bracket`,
   `Stack`, `Thread` and `Watermark` records in ring order. A
   framing error (length unaligned, past the lap or past `head`)
   makes the ring `ErrCorrupt` for good; a body that does not
   decode (wrong length, hook ≤ 0, exit before enter, bad UTF-8,
   non-zero padding) is skipped and counted as `Malformed`, an
   unknown type as `Unknown`. `Losses` adds the agent's header
   counters, so delivered + lost = written, the same contract as
   `bpf.Losses`. Tests: round trip, wraparound with PADs, eight
   concurrent writers with a live reader, a lagging reader,
   negative tables for headers, framing and bodies; and from
   `mage agentTest` the Java-driven ones — `RingStress` (eight
   Java threads, a 64 KiB ring lapping under a live Go reader) and
   a `hooks` mode (eight threads closing brackets through `Hooks`
   on one call site while the watermark thread runs every
   millisecond: no BRACKET after a WATERMARK it exited before,
   every id resolves; without the closing mark it failed 6 of 6
   runs), and `workload.jar file-basic` under the agent, whose
   ring must resolve every bracket's THREAD and STACK, match the
   debug dump's counts and keep every watermark's promise. Checked on
   25 (the Go-driven runs), the Java unit tests also on 17
   (GraalVM CE 17.0.9) and 27-ea.
