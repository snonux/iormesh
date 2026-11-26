# Decided — runtime control, explorer and the Java agent

Part of [Decided](../decided.md), in [Decisions](../../decisions.md).

- **#19 Runtime control** — the transient systemd unit (M4):
  graceful stop via SIGTERM, flag changes via stop-and-restart.
  No control file, no socket. Built by task 9b4 (`start`,
  `stop`, `status`, `uninstall`); `state.json` is a status
  record the run writes, read-only to the control commands —
  not a control file.
- **#11 Explorer form factor** — all three, on one view model:
  a generated static site (HTML/JS/SVG, no server, opens from
  `file://`), a Bubble Tea terminal UI, and plain text. A
  local web server was rejected: it adds a listening socket
  for nothing the static site cannot do at these sizes. Design
  in docs/visualization.md.

- **#2 Java agent attach strategy** — `-javaagent` at launch
  is primary; live attach is supported (decided by S1,
  2026-11-18). Live attach works on JDK 25 and 27-ea and prints
  JEP 451's warning on the target's stderr;
  `-XX:+EnableDynamicAgentLoading` silences it,
  `-XX:-EnableDynamicAgentLoading` (and a future default) makes
  the load fail, which the tracer reports instead of retrying.
  A root attacher reaches a JVM of another uid; the handshake is
  a client connection to the target's own attach socket, so the
  tracer still listens on nothing. Documented caveats: the
  warning, the retransformation of loaded classes, and that a
  live-attached agent misses all I/O before the attach. The k8s
  flavour is #14. Built and tested end to end by task ge4
  (`run -agent`, `iormesh attach`; 21, 25 and 27-ea warn and
  refuse under `-EnableDynamicAgentLoading` alike, 17 attaches
  silently).
- **#6 Java stack source** — the agent captures the stack at
  the I/O boundary with `StackWalker` (decided by S1,
  2026-11-18); blanket method entry/exit instrumentation is a
  non-goal; a plain BPF stack walk gave only unsymbolized Java
  frames, and a HotSpot-aware eBPF unwinder is not needed for
  v1. No async-profiler-style sampling between I/O events
  in v1. The walk is the agent's largest cost (≈8 µs per I/O
  call, S1), so M3 must reduce it and measure it against #25.
- **#27 Agent → tracer transport** — a memory-mapped ring per
  JVM (decided by S1, 2026-11-18): a file in the tracer-owned
  spool directory (reached through `/proc/<pid>/root` in k8s),
  mapped shared by agent and tracer; lock-free reservation,
  records published with a release store, zero syscalls per
  record. Back-pressure: when the reader is a ring behind, the
  agent drops the record and counts it in the ring header; the
  tracer turns the counters into the trace's loss accounting.
  The agent's own syscalls are its ring setup and one
  `/proc/thread-self` lookup per thread, tagged by path and
  never shown as the target's I/O, plus the sleeps of its
  watermark thread, which names itself in the ring. An
  append-only file was rejected: every flush is a target-thread
  syscall to filter, and it grows unbounded when the tracer
  lags. Built in M3 (task 1b4): one shared ring with a CAS on
  `head`, not a ring per thread — the thread count is unknown at
  setup, and the stack walk, not the CAS, bounds a bracket's
  cost; format versioned in the header, reader strict
  (`internal/agentspool`). The spool is untrusted input (task
  yi4): the tracer reads a ring only once its process is traced
  — an event of that pid and incarnation (its attach or fork
  event, at the latest) has reached the Java join; a ring found
  earlier is held unread, never drained, emitted or probed for,
  and a ring of a process never traced stays unread, as does
  one whose process's start time is unknown when it is found
  (only the ring's writer-chosen stamp would be left to match);
  only the newest waiting ring per pid stays mapped. A ring's
  capacity is at most 64 MiB (the agent refuses a larger
  `ringsize=`, the tracer a larger header) and a record other
  than PAD at most 64 KiB, four times the agent's own 16 KiB
  cap: a longer one is skipped as malformed, undecoded, so one
  ring puts at most about its capacity on the tracer's heap.
  Readers come and go while the JVM runs on (task 5j4): the
  ring is consume-once, and the agent writes its dictionary
  records (HOOK, THREAD, STACK, CLASS) once, so a later session
  would read brackets it can never attribute. Each reader
  therefore adds one to a reader-epoch word in the header (offset
  40) when it opens the ring; once the epoch passes 1 (or moves
  after an older tracer drained the ring: the tail had moved),
  the agent's watermark thread resends the hook table, closed by
  a WATERMARK, and bumps a generation that makes each thread,
  stack and class be written again at its next sighting. No
  watermark is written until the whole table landed, so the
  tracer never takes a partly dropped resend for the table. At
  most one resync per second; app threads pay only what their
  first bracket cost. A repeated agent load asks for the same
  resync instead of doing nothing. The word was zero padding:
  an older agent never reads it and an older tracer never bumps
  it, so the format version stays and either mix behaves as
  before (a later session on an older agent stays attribution
  none). Between sessions the agent goes dormant (task ej4): when
  the tail stood still for 10 s while the ring kept dropping and
  the epoch did not move, the watermark thread sets a flag the
  hooks read first among their work (one volatile read when
  awake); a dormant hook walks no stack and runs no probe, and
  counts its record as dropped in the header, as the full ring
  would. A moving tail or epoch, or a repeated load, wakes it
  through the same resync as a new reader. A ring lives as long
  as its JVM, not as long as a session (task 2j4): the agent
  unlinks its own ring in its shutdown hook after the last
  watermark (a tracer reading it has it mapped); the Java join
  retires a JVM once its group-dead exit was handled and nothing
  can ask for its ring (the fuse window plus the join timeout
  past the exit, none of its events held): a last drain, then
  close and unlink; and the spool scan unlinks a ring whose
  process is gone (SIGKILL, or a JVM that died while no session
  ran). Every unlink goes
  through the directory's descriptor and only if the name still
  names the regular, single-link inode meant — the one mapped,
  or the one listed — so a reused pid's new ring survives. A
  live JVM's ring is never removed: a later session reads it. A
  virtual thread's entry in the join goes with its last bracket
  (Retain past the watermark); its agent rewrites its THREAD
  record after 5 s without one, so the thread is known again
  before its next bracket.
