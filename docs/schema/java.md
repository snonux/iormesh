# Java attribution (M3)

Part of [Trace schema (v1, frozen)](../schema.md).

`iormesh run -spool DIR` joins the I/O of JVMs started with
`-javaagent:iormesh-agent.jar=spool=DIR` to the Java code that
caused it (`internal/correlate`, `JavaJoin`). The tracer creates
`DIR` (mode `1733`: every user may create its ring, none may list
or delete another's); each agent maps one ring there,
`<pid>.ring` (decision #27, `internal/agentspool`; the agent
creates it `O_EXCL`, without following a symlink, mode `0600`). A
`<pid>.ring` the agent cannot delete — another user's, stale or
planted — makes it use `<pid>.<hex>.ring` instead, the tag 16
random hex digits (task 9j4), so a planted name cannot keep the
agent from installing. The tracer accepts both names, with the
same checks. It opens a ring without following a symlink and
checks on the descriptor, before mapping anything, that it is a
regular file with one link owned by the uid of the process its
name names; a file
that fails is reported on stderr and never mapped. After mapping,
the header must name the same pid. A JVM already gone at the
first scan after its ring appeared cannot be checked, so a JVM
that lives less than a scan interval (50 ms) after its agent
installed stays `none`. A ring truncated under the tracer's
mapping (its owner can) faults on access; the reader turns that
into a corrupt ring instead of a crash. The JVM must sit in the
tracer's pid namespace itself, not in one nested below it: the
agent names its ring and its brackets' threads by the pids its
own namespace shows, which match the tracer's only there (M7,
task 4f4).

How an event gets `attribution: java` — never by guessing:

- The agent's hooked JDK methods write a *bracket* (hook, kernel
  tid, enter and exit `System.nanoTime()`, stack id) when they
  return. The agent writes its hook table into the ring at install
  (HOOK records): each hook's native and that native's syscall
  allowlist (`stat`, `lstat` and `fstat` match the kinds
  `newstat`, `newlstat`, `newfstat`).
- The kernel marks the syscall (task td4): once a JVM's ring has
  delivered its whole hook table (a record after the last HOOK,
  the agent's first watermark, says so; task tf4), the tracer
  attaches uprobes to the hooks' natives in that JVM's
  `libjava`/`libnio`/`libnet` (scoped to its pid, each native's
  BPF cookie its id), which push and pop a per-thread mark. A syscall a thread enters from
  inside a hooked native carries that native and the native's
  entry time, stamped in CLOCK_MONOTONIC — `System.nanoTime()`'s
  clock — by BPF.
- The event joins the innermost bracket on its tid whose window
  contains the native's entry, no clock conversion; the bracket's
  hook must reach that very native and allow the event's kind,
  and the syscall's start (converted from `boot_ns` with a boot −
  monotonic offset sampled in user space as the midpoint of two
  monotonic reads around a boot read, S1 experiment 2) must not
  lie after the bracket's exit, which defuses a stale mark.
- So JVM work inside a bracket stays `none` whatever its kind: a
  syscall without a mark was issued outside every hooked native
  (class loading reading the runtime image or a JAR's bytes
  through no hooked native, the JVM's own `/proc` reads), and one
  marked with another native was issued by JVM work that called
  it. The allowlist stays as a second guard: another kind under
  the hook's own native is that native's JVM work (a class loaded
  inside `open0`).
- `none` also for: a syscall no bracket contains (JVM startup
  before the agent, JIT/GC threads, JNI, JVM-internal I/O), two
  brackets that overlap without nesting, a folded read/write
  (`args.count`: many calls, which one bracket cannot vouch for),
  a bracket whose stack the agent did not deliver (dropped STACK
  record, or a JVM run without stack walks) — `java` needs a
  stack — the syscalls a JVM issued before its natives were
  probed (a JVM that starts after the tracer: up to one spool scan,
  50 ms, past its agent's install; JVMs already running are probed
  before the tracer's ready file) — and every syscall of a JVM
  whose natives could not be probed (no uprobe_multi links, Linux
  < 6.6; a JDK whose libraries define none of the natives — no
  `Java_*` symbols; a JVM gone before the probe): reported on
  stderr and counted as an unmarked JVM. The mark needs Linux ≥
  6.10, or a 6.6–6.9 stable kernel carrying commit 46ba0e49 ("bpf:
  fix multi-uprobe PID filtering logic"): before it, a pid-scoped
  uprobe_multi link fired only for the thread whose tid equals the
  pid — in a JVM the launcher thread, not even Java's `main` — so
  every other thread's I/O would stay `none`. A JVM whose threads close 64
  brackets after the probe without one mark is reported on stderr
  and counted as an unmarked JVM too.
- **Virtual threads** (JDK 21+): the agent records the carrier tid
  at a bracket's enter and exit, 0 when they differ (such
  brackets are not indexed, so their own syscalls stay `none`).
  Equal tids do not prove the thread stayed: a hook that runs JDK
  code around a native of another class (`NioSocketImpl.accept` →
  `Net.accept`, `ServerSocketChannelImpl.implAccept`: the hook
  table's qualified natives) can park in the poller, let the
  carrier run other virtual threads and resume on the same
  carrier. Such a bracket's window may then hold another virtual
  thread's syscall whose own bracket was lost, so a parking
  hook's bracket of a virtual thread — or of a thread whose
  THREAD record is missing — joins nothing (counted ambiguous);
  hooks whose native is their own class's are pinned to the
  carrier for the native call and join as usual. A joined event
  gets the virtual thread's stack, `carrier: true`, and as
  `thread` the name of the platform thread with that tid — v1
  does not name virtual threads (milestones.md M3).

Events of a JVM with a ring wait (in seq order, holding the events
behind them) until the ring's WATERMARK is 5 ms past the
syscall's end — a bracket is written when its method returns —
or 2 s after it if the agent stops writing watermarks; an event
of a process without a ring leaves once a spool scan (every 50 ms
and on the run loop's tick) after its syscall found none (or 2 s
after it while the spool cannot be read: a failed scan is a
warning, retried at the next poll); a syscall that ended before
its JVM's ring existed is decided at once. At the end of the
trace everything is decided with what has been read. The summary line counts the rings (and the JVMs whose
natives could not be probed), brackets, joined events (and
carrier ones), why a JVM's other events stayed `none` (unmarked,
other native, unbracketed — of which stale: a containing bracket
closed before the syscall started on the converted clock — other
kind, ambiguous, no stack), the events released by the timeout
rather than a watermark, and the agents' own loss counters. The
boot − monotonic offset is the narrowest of eight user-space clock
triples per poll, so a triple preempted between its reads cannot
skew it (task se4). A suspend between
a syscall and its join shifts the boot − monotonic offset by the
time suspended; held events are then converted with the new
offset and most likely stay `none` (not handled specially).

`methods.ndjson` — one line per distinct frame, written before the
first event naming it:

```json
{"schema_version":1,"method_id":3,
 "frame":"iormesh.workload.scenarios.MethodFlow$Journal#append(MethodFlow.java:80)",
 "class":"iormesh.workload.scenarios.MethodFlow$Journal","method":"append",
 "file":"MethodFlow.java","line":80}
```

| Key | Type | Meaning |
|-----|------|---------|
| `method_id` | int | the id `java.stack` uses, from 1, unique in the trace |
| `frame` | string | the agent's frame text, `class#method(file:line)` |
| `class`, `method` | string | the frame's binary class name and method name |
| `file` | string, optional | the source file; absent when the class has no source attribute |
| `line` | int, optional | the line; absent when unknown (no line table, a native method) |
| `class_id` | int, optional | the frame's class in `classes.ndjson`; absent when the agent's description of it was lost |

A frame is a method *and* a line, so two call sites in one method
are two ids. Ids are keyed by the frame text and its class: the
same frame from two JARs (two classes) is two ids, from two JVMs
running the same JAR one.

`classes.ndjson` — the class-load map (task fe4): one line per
class some method row names, written before the first method row
naming it. The agent describes only classes on recorded stacks,
not every class the JVM loads (milestones.md M3, "Supporting
mappings"). Rows are deduplicated across the trace's JVMs by
class, loader and code source.

```json
{"schema_version":1,"class_id":2,
 "class":"iormesh.workload.scenarios.MethodFlow$Journal","loader":"app",
 "code_source":"/home/user/iormesh/workload.jar"}
```

| Key | Type | Meaning |
|-----|------|---------|
| `class_id` | int | the id `methods.ndjson` uses, from 1, unique in the trace |
| `class` | string | the binary class name, as the method row has it |
| `loader` | string | the defining loader: `bootstrap`, the loader's name (`app`, `platform`, a container's), else its class name |
| `code_source` | string, optional | the JAR or class directory path, `jrt:/<module>` for the runtime image, another URL without userinfo, query or fragment; absent when the class has none (generated classes) |

`threads.ndjson` — one line per agent THREAD record, the agent's
own `iormesh-agent-watermark` thread included. A thread renamed
after its first bracket gets another record (same
`java_thread_id`, the new name) at its next bracket; its events
from then on carry the new name:

| Key | Type | Meaning |
|-----|------|---------|
| `pid`, `pid_incarnation` | int | the JVM |
| `tid` | int | the kernel tid; `0` if the agent could not look it up; for a virtual thread the carrier of its first bracket only |
| `java_thread_id` | int | `Thread.getId()` |
| `name` | string | the thread's name at its first bracket, or after a rename |
| `virtual` | bool, optional | `true` for a virtual thread |

The tables carry `parquet:` tags for the M4 segment writer
(`methods`, `threads` and `classes` files, milestones.md M3); with
`-trace-dir` they are written as segment tables (task 8b4,
[Parquet mapping](files-parquet.md#parquet-mapping)).
