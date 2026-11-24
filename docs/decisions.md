# Decisions

Every design decision iormesh has taken, numbered, each with its
reasons. A decision is recorded here first; the topic docs
([architecture](architecture.md), [trace scope](trace-scope.md),
[output contract](output-contract.md), [build](build.md),
[testing](testing.md), …) narrate it, and
[milestones.md](milestones.md) says where it was built. A new
decision takes the next free number.

Numbers are stable ids — references in the docs, the code and the
tests keep working — so the groups below are not in numeric
order. Four groups: decided, open (grouped by the milestone they
were raised for, each with a default so work was never stalled on
a missing answer; those milestones are accepted on the defaults,
which stand until the owner decides otherwise), provisional
(settled under the 2026-11-20 sequencing relaxation, each naming
what the owner must confirm and what would overturn it), and
deferred.

## Decided

- **#3 Graph storage** — none on the server. The tracer writes
  parquet segments; the controller rebuilds the merged graph
  from them offline, on every build. There is no long-lived
  collector whose state would need to survive a restart. (The
  controller's service database is separate — #24.)
- **#4 Go eBPF library** — ior's stack (`aquasecurity/libbpfgo`,
  cgo, Mage, Docker generation host), adopted with its
  generation pattern. Mechanics: copy/adapt ior's
  `internal/generate`, recording the ior commit each copied
  file came from, so later ior fixes can be ported
  deliberately. Extracting a shared package into ior is the
  alternative to revisit once the copies diverge.
- **#12 Controller transport** — SSH with the operator's key;
  collect syncs over rsync, which must be present on servers
  (`deploy` fails fast if it is missing); server registry in
  `~/.config/iormesh/servers.yaml`. The controller never
  trusts a host key on first use: ssh runs with
  `StrictHostKeyChecking=yes` and `BatchMode=yes`, so a server's
  key must already be in the operator's known_hosts, and no
  password prompt can hang a run; keepalives, rsync's I/O
  timeout and a per-host deadline keep a stalled host from
  hanging one either (M4 "Collect"). The k8s fetch path is
  provisionally settled in #18: an exec-streamed tar, no
  `kubectl cp`.
- **#16 Trace-file segmentation** — rotate once per minute,
  close-then-rename into `ready/`, collect-while-running via
  rsync with delete-after-sync (see M4). Compression is
  parquet-internal zstd — whole-file compression
  (`.parquet.zst`) would make segments unqueryable without a
  decompress step. Variable event arguments map to one
  nullable column per argument field (see "Trace output
  contract"). A size cap forces an early rotation for very hot
  sessions (settled by S3): a segment rotates at one minute or
  64 MB (≈ 3.5 minutes at the #25 reference rate of 18 MB per
  minute), whichever comes first, so a segment never outgrows
  what one `collect` moves comfortably.
- **#26 Cross-host join key** — the TCP initial-sequence-number
  pair (decided by S2, 2026-11-18): the client's `(iss, irs)`
  equals the server's `(irs, iss)`, which NAT leaves untouched.
  Each tracer records `snd_una`, `rcv_nxt`, `bytes_acked` and
  `bytes_received` with the socket's role on its TCP state
  records, at `ESTABLISHED` or later; the merge rebuilds the pair
  (connecting side `iss = snd_una − bytes_acked`, accepting side
  one less, both `irs = rcv_nxt − bytes_received − 1`, mod 2³²)
  and joins on it, inside a coarse time window that guards
  against ISN-clock wrap. The 4-tuple plus a time window breaks
  under NAT and port reuse and is not a fallback across hosts.
  Conntrack on the NAT host is optional — it needs a tracer or
  netlink access there and adds nothing the two ends lack. When
  no pair matches (sequence-randomizing firewalls, SYN proxies,
  an untraced far end), the edge stays unresolved, never a
  guess; L4/L7 proxies that terminate the connection stay the
  route map's job (#15). Details:
  [docs/spikes/s2-cross-host.md](spikes/s2-cross-host.md).
- **#5 Test workload scope** — same repo, `workload/`
  (adopted with M0, 2026-11-18). A plain Java 21 app built with
  the JDK alone (`javac` + `jar` via `mage workloadBuild`, no
  Maven or Gradle), so the thing being traced never has an
  excuse to behave differently from a real app. The OTEL
  flavor (M3, task za4) is the one dependency, kept outside the
  core jar: its own module and jar, run against the vendored
  OpenTelemetry API jars (`workload/otel/third_party`). The
  third-party HTTP libraries of decision #22 (task de4) follow
  the same pattern: `workload/libs`, `workload-libs.jar`,
  vendored under `workload/libs/third_party/<library>`.
  Decided (review of de4): the repo carries these jars — about
  13 MB, each SHA-256-pinned in the magefiles and documented in a
  PROVENANCE file — so the workload builds offline and
  reproducibly; the size is accepted as the price of that.
- **#25 Tracer overhead and volume budget** — settled by S3
  (2026-11-18; numbers and method in
  [docs/spikes/s3-overhead.md](spikes/s3-overhead.md)). The
  four numbers, as M1 measured them on `syscall-burst`, and the
  budget M2 onward is accepted against:
  1. *tracer CPU* — M1: 6.6–13 µs per event (medians). Budget:
     ≤ 2 µs per emitted event on `syscall-burst` (median) — one
     core then sustains 500 k events/s, five times M1, which
     together with aggregation covers the burst (task
     wc4 brought it to ~1.5–2 µs on a busy host; task md4
     measured 1.2 µs at 1 thread and 1.7 µs at 4, unfolded,
     medians of 5 — s3-overhead.md, "Follow-up (wc4)" and
     "Follow-up (md4)"; at md4's load, 4.6–9, the item passes).
     Task ae4 (after 8e4's dispatch, load 9.8–11.8): 3.0 µs at
     1 thread and 3.1 µs at 4, over budget; interleaved against
     the build before 8e4 at load 8–14, 2.18 / 3.55 µs before
     and 2.08 / 2.15 µs after — no 8e4 regression, but both
     builds over 2 µs at load ≥ 10 (s3-overhead.md, "Follow-up
     (ae4)"); task ee4 re-measures it on M4's segment writer
     (it gates M4's acceptance), the limit unchanged. With the
     Java join (`-spool`, every event joined): 5.2 µs, over budget (2b4; td4's Java mark
     measured +0.7 µs per marked event on top, within the host's
     noise, s3-overhead.md "Follow-up (td4)"). Task ud4 cut the
     consumer's per-event work (a per-event clock syscall, the
     mark's reflective decode, the join's map keys, the event's
     copies, integer and string encoding): instructions per event
     −34 % with `-spool` and −19 % without; at load 8–13 the
     `-spool` tracer still spends 3.8 µs at 1 thread and 5.6 µs at
     4 (from 7.6 / 9.2, task clock, medians of 5); the plain
     unfolded burst is under at 1 thread (1.7 µs) and over at 4
     (2.28 µs, 30–58 % of records lost), load 6.5–10 — `-spool` stays
     **over** budget, its next step being M4's output format
     (s3-overhead.md, "Follow-up (ud4)"); task ee4 re-measures
     on M4's segment writer, the limit unchanged. That writer
     (task 8b4) costs ≈ 3× the NDJSON sink per event (≈ 10.4 k
     vs ≈ 3.4 k instructions on the burst event), so the
     parquet path starts further over, not under;
     `DataPageStatistics(false)` (≈ −7 %) is the one known
     cheap lever. Task ee4 (2026-11-20) replaced the events
     file's `parquet-go` writer with a columnar encoder of its
     own (writer 11.2 k → 2.5 k instructions per event) and
     measured the segment path, interleaved against the build
     before it, medians of 5: plain unfolded **passes** —
     0.85 µs at 1 thread and 1.62 µs at 4 (from 2.68 / 3.14;
     5.8 k / 6.0 k instructions, 2.6 k / 3.1 k cycles per event,
     load 2.5–3.6); `-spool` with every event joined stays
     **over** — 2.89 µs at 1 thread (load 3–41) and 4.82 µs at 4
     (load 41–46), 11.5 k / 12.0 k instructions and 5.8 k /
     7.7 k cycles per event (from 4.43 / 5.82 µs). Owner
     decision (2026-11-20, delegated to an advisor): the
     original ≤ 2 µs covered `-spool` with every event joined and
     was not met (2.89 / 4.82 µs after ee4). The limit is hereby
     changed for that case only: ≤ 4 µs per emitted event and
     ≤ 12.5 k instructions per event, valid at load ≤ 10 with
     every event attributed and zero loss. This is a relaxation,
     argued by the extra agent records each joined event consumes
     (one core still sustains 250 k joined events/s) and by the
     default fold keeping JVM hot loops out of the join. Plain
     stays ≤ 2 µs.
     Re-measured by those rules after one more round of join cuts
     (task ee4: unboxed agent-ring drain, chunked bracket lists,
     no per-event java allocation), interleaved, medians of 5, load
     4.4–8.7, every `-spool` run attributed and lossless: `-spool`
     1.90 µs and 11.1 k instructions at 1 thread, 3.75 µs and
     11.8 k at 4 — **passes** the changed limit; plain 1.59 µs at
     1 thread and 1.95 µs at 4 (5.8 k instructions both, unchanged
     by the cuts) — **passes** 2 µs, though the unfolded plain
     4-thread runs lost 30–50 % of their records to a full ring
     (recorded, not gated). Both CPU-time passes are narrow: the
     medians sit inside the ~±20 % run-to-run noise (2 of 5 runs
     over the limit at 4 threads in each case), so the instruction
     counts — well under 12.5 k — are the robust part of the pass
     (s3-overhead.md, "Follow-up (ee4): owner decision"). Measured as the
     tracer's own user+sys CPU time per event from its ready
     file to its exit (load and attach excluded), which host
     load moves less than wall time — not nothing: on a loaded SMT host one thread's CPU
     time still spreads 2–3×, hence medians of ≥ 5 runs;
  2. *bystander cost* (host-wide, every untraced process) — M1:
     ~1.14× per traced-kind syscall at 4 and 16 threads. Budget: ≤ 1.25× on
     `syscall-burst` at 4 threads, paid only while a trace runs.
     Measured as the median of ≥ 10 interleaved traced/untraced
     pairs, pinned with taskset (tracer on its own core), on the
     bystander's user+sys CPU time; the wall-clock ratio is
     recorded alongside, and if its median exceeds 1.25× while
     CPU time passes, the item stays open and one run is taken
     on a host with its background load paused (2026-11-19: the
     quiet-host rule replaced, the limits unchanged). Task md4:
     1.19× and 1.15× (CPU time) in two sets of 10 pairs, wall
     clock 1.21× and 1.14×, so the guard did not trip. Task
     ae4, after 8e4's `raw_syscalls` dispatch: 1.16× and 1.14×
     (wall clock 1.16× and 1.19×) — passes; a bystander of a
     kind no family traces (a `getppid` loop) pays ~2.0×,
     recorded, not gated (s3-overhead.md, "Follow-up (ae4)").
     Task 4f4 (M7's PID-namespace targeting): host mode
     unchanged; for a tracer in a PID namespace the item was
     open — on a quiet host (load ~2) 1.25× / 1.30× CPU and
     1.28× / 1.27× wall clock at 1 / 4 threads, beside host mode's
     1.27× / 1.20× (wall 1.30× / 1.23×) in the same set
     (s3-overhead.md, "Follow-up (4f4)"). Task 9f4 cut the
     tracer's kernel cost per bystander syscall by 17 % in both
     modes (a target-hint bitmap before `target_map`, the scope
     test in the dispatcher): the namespace item **passes**, 1.23×
     / 1.23× CPU, wall 1.22× / 1.21× (20 pairs per thread count,
     load 2–9), beside host mode's 1.19× / 1.15× and the old
     build's namespace 1.23× / 1.23× (wall 1.24× / 1.25×) in the
     same set — a thin margin the burst barely resolves; the
     instruction counts are the firm part (s3-overhead.md,
     "Follow-up (9f4)");
  3. *loss* — M1: 16–42 % of ring records on a sustained burst,
     0 on a short burst and on the M0 scenarios. Budget: zero
     lost records on every M0/M2 scenario, and < 1 % on
     `syscall-burst` at 1 and 4 threads with aggregation on, both
     counted in lost records — skipped program runs are reported
     apart as possible loss, not in the budget (task vd4, "Trace
     output contract"). Any loss is an explicit `drop` event.
     Task vc4's aggregation lost 0 in 3 runs each at 1 and 4
     threads, on a busy host (s3-overhead.md, "Follow-up (vc4)");
  4. *volume* — M1: ~326 B/event NDJSON, zstd ×18. Budget:
     ≤ 30 B/event compressed (≈ 18 MB per minute at 10 k
     events/s, ≈ 4.3 GB per 4 h session), measured as zstd -3
     on the NDJSON until the M4 parquet writer exists.

  In-kernel aggregation (ior's model): repeated read/write on
  one fd instance by one thread are summed per flush into one
  event with a count; every event that changes fd, socket or
  process state stays individual, so goldens and attribution
  are unaffected. Implemented by task vc4
  ([docs/implementation.md](implementation.md) M2 step 11,
  [docs/schema.md](schema.md) "Folded reads and writes"):
  a call folds only once its key (thread, fd instance,
  direction) has sent four hot calls individually — hot meaning
  within 1 ms of the previous one and itself returning data
  within 1 ms — so declared single calls stay exact; the folded
  event carries `args.count` (≥ 2) and summed `bytes`; every
  other traced call of the thread flushes first; `iormesh run
  -aggregate=false` turns it off for debugging. Enforcement: the
  budget is checked by a documented `spikes/s3/measure.sh` run
  per milestone by the paired CPU-time method above, load
  average recorded, not gated (CI machines are too noisy for a
  perf gate); the zero-loss criterion is part of the integration
  suite.
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
  retires a JVM once its group-dead exit was handled and nothing can ask for its ring
  (the fuse window plus the join timeout past the exit, none of
  its events held): a last drain, then close and unlink; and the
  spool scan unlinks a ring whose process is gone (SIGKILL, or
  a JVM that died while no session ran). Every unlink goes
  through the directory's descriptor and only if the name still
  names the regular, single-link inode meant — the one mapped,
  or the one listed — so a reused pid's new ring survives. A
  live JVM's ring is never removed: a later session reads it. A
  virtual thread's entry in the join goes with its last bracket
  (Retain past the watermark); its agent rewrites its THREAD
  record after 5 s without one, so the thread is known again
  before its next bracket.

- **#30 Repository-root layout** — the root keeps only what
  the conventions pin. The Mage targets move into `magefiles/`
  (mage ≥ 1.17, the pinned version, reads the directory; all
  magefiles move together, keeping their `//go:build mage` tags
  and provenance headers, so plain `go vet ./...` and
  `go test ./...` keep skipping the mage `package main`). The
  build outputs go to `bin/` (gitignored; owner override, tasks
  rk4 and uk4, of the first cut that kept them at the root as
  in ior): the binaries `iormesh`, `iormeshctl`, `echopeer` and
  `edgepeer`, the four jars (`workload.jar`, `workload-otel.jar`,
  `workload-libs.jar`, `iormesh-agent.jar`) and the gates' test
  binaries (`integrationtests.test`, `iormesh-internal.test`).
  One directory for all of them keeps what had pinned the root
  working: the non-java-peer still finds `echopeer` beside the
  jar, the module jars' manifest `Class-Path` still names
  `workload.jar` beside them (the vendored jars as
  `../workload/…`), the integration harness's defaults become
  `../bin/iormesh`, `../bin/workload.jar` and `../bin/edgepeer`,
  and a scoped sudoers rule names
  `<repo>/bin/integrationtests.test`. `mage clean` removes
  `bin/` (and any root artifact of a tree built before the
  move); `dist/` stays the release output. The kind
  acceptance's state files
  (`.k8s-acceptance/`: the kubeconfig, the two `.state.json`s,
  `teardown.log`) group under one hidden directory.
  Rejected: the outputs at the root (the first cut, for ior's
  pattern) — they cluttered the top level; a `bin/java/` of its
  own for the jars — the jars and `echopeer` must share a
  directory, so a split buys nothing.

- **#31 Verifier-safe array indexes in the BPF C** — a variable
  index into a map value's array is bounded on the register
  that indexes, not only by a range check:
  `iormesh_index_mask(idx, n)` (`internal/c/maps.h`) hides
  idx's range from clang (`barrier_var`) and masks it with
  `n - 1` (n a power of two). A plain `if (idx >= n) return;
  a[idx]` leaves the compiler free to test a copy: Debian clang
  19.1.7 (the `tracer-arm64` builder) did, and both the el9
  5.14 and the 7.x verifiers refused `handle_sys_exit_read`
  (task il4). The source must load whatever reasonable clang
  builds it, so the fix is in the C, not a pinned compiler.
  Applied to `rwagg.c`'s slot and `javamark.c`'s frame
  indexes. Rejected: a newer clang in the builder only — it
  hides the pattern until the next compiler change; a mask
  without the barrier — clang drops a mask it proves redundant
  and is back to the checked copy.

## Open — raised for M2 (built on the default)

- **#7 Event families, default-on vs opt-in** — signals, epoll
  registrations, mmaps and renames all add volume; reuse ior's
  trace-family concept and pick defaults per measured cost
  (S3). *Default: file, socket and lifecycle on; the rest
  opt-in.* S3 supports the default: the consumer's per-event
  cost, not the probes, limits throughput, so every family
  that fires at syscall frequency stays opt-in.
  M2 step 7 (task sa4) implements the default as iormesh's own
  families (ior's concept, iormesh's grouping:
  `internal/tracepoints`): `file` and `socket` on — socket now
  includes bind and the datagram calls sendto/recvfrom, since
  unix sockets and UDP are sockets — and lifecycle always on
  (its probes decide what is traced); `ipc` (pipes,
  socketpairs), `signal` (kill/tgkill/tkill and
  signal_generate), `poll` (epoll, poll) and `zerocopy`
  (sendfile, splice, tee, copy_file_range) opt-in through
  `iormesh run -trace-families ipc,signal,…|all`. The flag only
  adds: file and socket cannot be turned off (the scenario
  markers are opens). Task oh4 grows socket by the msghdr data
  calls sendmsg, recvmsg, sendmmsg and recvmmsg — in socket,
  not a family of their own: an opt-in family would leave the
  clock proof's byte sums open to them by default (M4), and
  they are socket data calls like sendto/recvfrom. Their
  events reuse the schema's rule that a syscall kind is its
  syscall's name (`sendmsg`, …, appended to schema v1).
  Task nk4 names their datagram peer as sendto/recvfrom do,
  for the DNS resolvers and QUIC servers that send through
  them unconnected: the exit reads the msghdr's `msg_name`
  and `msg_namelen` (recvmsg's kernel writes the sender's
  length back there) and sends the same address record, IPv4,
  IPv6 or AF_UNIX, never for a stream socket. For sendmmsg
  and recvmmsg it is the first message's address only: one
  record per call, a fixed 16-byte read with no loop, so the
  el9 5.14 verifier sees a straight line next to mmsg.c's
  bounded sum (the exits grew by about 600 processed
  instructions, to 42.6k of the 1M limit). A batch to several
  peers is named by its first; the rest are not guessed. Its
  whole byte sum goes to that first peer's flow or connection:
  a QUIC server's recvmmsg of datagrams from several clients
  adds them all to the first client's flow in the report
  (task hl4 pins this with two senders). Renames and mmaps are not families: the
  renames are the file surface's, mmaps are not traced yet.
  ior's restart fold (one row for a call a signal interrupted
  and the kernel re-executed) is not part of the signal family
  and not lifted: a restarted call stays two rows, the first
  with its restart code (-512…-516). Its kernel half — two
  probes never attached, and enter/exit hooks emitting records
  nothing read — was removed (task pf4); lifting it needs both
  of its signal probes and a userspace consumer.
- **#23 Target-pattern mechanics** — match on exec (argv/exe)
  vs comm; glob vs regex; concurrent matches (newest wins vs
  trace all vs error); restart semantics (wait for
  re-appearance vs end the session); and the listening-port
  target expansion needs (resolve once at start vs follow the
  port across restarts). Kernel comm's 15-char limit makes
  argv matching the primary source for Java apps. *Default:
  glob on argv, trace all matches, wait for re-appearance.*
  M2 steps 1–3 (task pa4) implement the default: the glob runs
  over argv and the exe path, never the comm, and the
  exec-to-attach window is written as an `attach_gap` drop.
  Task mh4 settles the listening-port target: *follow the port
  across restarts, conservatively.* Every host `expand -apply`
  enrolls is bound by port, so ending at the first restart lost
  the host. When the last traced process of a `-listen-port`
  session is gone, the session re-resolves the port's owners
  about once a second for a grace period (`-listen-grace`,
  default 2m; 0 ends at once, the old behaviour). A holder is
  adopted only if it runs the same executable (" (deleted)"
  ignored, so a package update still matches) under the same
  effective uid as a process the session started with: the same
  port alone is not the same app. Task qj4 tightens both halves:
  only start holders running the executable of the
  earliest-started holder (the one that most likely bound the
  port) are remembered, so another program that merely inherited
  the listener fd (a root helper) does not widen what is
  accepted. Their uids are kept as they are: a
  privilege-separated server (nginx, httpd: a root master and
  workers of the same binary under their own uid, all holding the
  listener) is followed whole, since the restarted workers fork
  before the next re-resolve and fork-following never picks up a
  process that already exists. And a holder is adopted only
  while its pid still has the start time the port listing read
  with its fd table, so a pid reused in between is never
  mistaken for the holder. Task kk4 widens the executable half
  for an upgrade, conservatively: a restart onto a new path (a
  JDK or package upgrade into a versioned directory,
  `.../java-25-openjdk-25.0.5/bin/java` to `.../25.0.6/...`) is
  adopted if its executable has the same base name, its
  effective uid is the same, *and* it runs in the same cgroup
  (on systemd the service's unit, on cgroup v2 the `0::` path,
  else the `name=systemd` one) as a remembered holder. The base
  name alone is not enough — `java` names every JVM of the user
  — and the cgroup alone is not either: a unit may run helpers.
  No readable cgroup, or the root cgroup `/` (which says
  nothing), leaves only the exact path. A different program
  (another base name), another uid, and qj4's inherited-fd
  helper (not remembered) stay refused; the service manager is
  never a holder. A manual restart of an upgraded binary from
  another login session is not followed (another scope): start
  a new session. An adopted holder
  gets an `attach` event of its own incarnation and the session
  goes on; stderr names it. No match within the grace ends the
  session `targets_exited`, stderr saying no new owner appeared.
  A service manager — a process whose executable is named
  `systemd`, pid 1 or a user manager, holding a socket-activated
  listener — is never a target, at start or later: tracing it
  would follow every service it spawns. Task lk4 settles a port
  only the manager holds (an idle socket-activated service, also
  under `expand -apply`): *wait for the activated service,
  conservatively.* The session starts with no target, pins the
  listening sockets the manager holds (their inodes), and
  re-resolves about once a second within `-listen-grace` (0
  keeps the old refusal at start). With no original owner there
  is no owner key to compare against, so the socket itself and
  the manager's unit layout identify the service: a holder is
  adopted only if it (1) holds one of the pinned sockets — still
  listening on the port, the very socket systemd hands to the
  service it activates, not merely the port — (2) is not itself
  a service manager, and (3) runs in a `.service` unit cgroup
  (slices skipped; a delegated sub-cgroup of the unit counts)
  inside the holding manager's subtree: anywhere for pid 1, under
  `user@UID.service` for a user manager. Task el4 makes the user
  manager's subtree strict: the manager must run in the
  `init.scope` directly below `user@UID.service`, UID its own
  effective uid, or it names no subtree; and (4) a holder below a
  user manager must have that manager's effective uid — a
  user's services run as that user, so a setuid program or
  another user's process in the tree is not one. A login-session
  scope, a shell or another unit's process holding a copy of the
  fd is refused and named once. The parent pid is not checked: a
  wrapper script that forks its server is still the service. For
  pid 1 the uid is not checked (`User=`). The adopted holders
  are then remembered as start holders are (qj4's
  earliest-started executable), so later restarts and
  re-activations follow the ordinary rules above; if no adopted
  holder's owner key can be read, the pin stays and the next
  activation is waited for as at start (task el4). A service
  that activates between the start's resolve and the pin is
  caught by resolving the port once more (task el4). No
  activation within the grace ends the session `targets_exited`,
  stderr saying so. Not covered:
  `Accept=yes` sockets (each instance gets a connection, never
  the listener) and a socket unit restarted while waiting (new
  sockets, not the pinned ones). No unit name is read: that needs
  systemd's D-Bus, which the tracer does not use. A new owner that listens while the old one still
  runs (an overlapping restart) is adopted once the old one
  exits, within the grace.
- **#9 Capture policy for argv and env** — argv is in, but it
  is not harmless: command lines carry passwords and tokens
  (`-Dpassword=…`, `--token …`). Argv is recorded through the
  same redaction policy as HTTP (denylist patterns, values
  masked). Capturing the environment at exec
  (`/proc/<pid>/environ`) is useful for the model but a
  privacy call to make explicitly. *Default: argv redacted,
  env off.* M2 step 8 (task ta4) implements the default:
  `internal/redact` masks option and KEY=VALUE values by a
  denylist, program-scoped flags (mysql's `-p`), URL userinfo
  and query values, denied pairs of `&`/`;` runs (form bodies,
  scheme-less JDBC such as h2 files) and Oracle's thin
  `user/password@` (task zi4); every exec of a traced process
  becomes an `exec` event carrying its redacted argv, and the
  environment is never read. A process already running when the
  trace attaches (a `-pid` or port target, a pattern's start-scan
  match) has no exec; task nh4 records its argv and executable on
  its `attach` event instead, read from `/proc/<pid>/cmdline` and
  `/proc/<pid>/exe` at attach, checked against the start time
  seeded, and redacted by the same policy before the event
  exists. An unreadable `/proc` (process gone, hidepid, another
  pid namespace) leaves both absent and never fails the attach.
  Every `/proc` command line read for an event (this one, an
  exec's completion) is bounded at 64 KiB, its cut last element
  masked, so no event line nears the readers' 1 MiB cap.

## Open — raised for M3 (built on the default)

- **#29 JDK version support** — the plan says JDK 21, but
  fleets run 17 and older. Boundary hooks, stacks and HTTP
  events do not need 21 (only the deferred virtual-thread
  support would). S1 (from sources, no 21 runtime then) found
  the hook set's natives identical from 21 to 25 (27-ea changes
  two signatures outside the set), while 17 differs (no
  `UnixFileDispatcherImpl`/`UnixDispatcher`, RAF `readBytes`
  instead of `readBytes0`); the hooked *Java* methods differ
  more, which sc4 settled as three variants (below). M3 (task 0b4) builds the agent for 17 with
  shaded ASM and ships per-JDK table variants. A real JDK 21
  (task sc4) showed one hooked Java method S1's source reading
  missed: `Net.serverSocket` keeps its `boolean` stream flag
  through 24 and drops it in 25 — so three variants, `jdk17`
  (17–20), `jdk21` (21–24) and `jdk25` (25+). The agent runs and
  its hooks fire on 17, 21 (21.0.6, 21.0.12), 25 and 27-ea; 22
  and 24 were checked by `javap` of their class files only, 23
  and 18–20 are untested.
  *Default: agent runs on 17+; anything
  older is traced without Java attribution (the non-Java
  node shape).*
- **#22 HTTP capture mechanics** — agent-boundary hooks (v1
  set: JDK HttpClient, HttpURLConnection, the JDK's built-in
  HttpServer (the microservices workload's REST server),
  Servlet, Apache HttpClient, OkHttp, Netty?); redaction
  policy — denylist (drop Authorization/Cookie/… by default)
  vs allowlist (record only named headers), sensitive query
  params, and URIs: verbatim, path-only, or templated
  (`/orders/{id}`) — path segments carry ids and tokens too;
  and HTTP/2 request pairing — capture stream ids at the hooks
  vs leave HTTP/2 hops time-ordered in v1. *Default: denylist,
  query values masked, HTTP/1.1 pairing only.* Task 3b4
  implements the default for the JDK's HttpServer, HttpClient
  and HttpURLConnection: headers on internal/redact's denylist
  and every query value are masked in the agent (and again by
  the tracer), paths are verbatim, and `req_seq` is recorded
  over HTTP/1 only. Task de4 adds the third-party libraries in
  #22's order, rewritten in the app's class loaders: Servlet,
  Apache HttpClient 5, OkHttp and Netty's HTTP/1.x codec.
  Task 0j4 widens the header denylist to the argv globs
  (anywhere in a name: X-Forwarded-Authorization, Cookie2,
  X-Amz-Credential) plus named key globs (`*api*key*`,
  `*auth*key*`, `*access*key*`, `*private*key*`,
  `*subscription*key*`, `*functions*key*`, not `*key*`), and
  masks the forward-auth URI headers (X-Original-URI/-URL,
  X-Forwarded-URI, X-Rewrite-URL, X-Envoy-Original-Path) as
  URLs like Location; a test fails when the agent's copy and
  internal/redact's lists differ.
  *Decided (task xh4): templated at the controller's
  aggregations, raw in the events.* The trace keeps the path
  verbatim (no schema change, no data lost, `iormeshctl query`
  and report.json show what was requested); the build folds
  id-like segments where it counts per path — the service
  database's request keys, hence the services view's top
  paths. Built-in folding is conservative: a segment of only
  digits, a canonical UUID, or 16+ hex digits with a digit
  becomes `{id}`; words and versions (`v2`, `cafe`,
  `report-2024`) never fold. The operator's `path_templates`
  in routes.yaml (`/shops/{shop}/orders/{order}`: a `{name}`
  segment matches any one segment) win over it, first match,
  a literal template pinning a path verbatim. A source
  imported again replaces its rows on a connection, so a
  changed rule or the upgrade never counts requests twice;
  rows stored raw before it (marked by the database's schema
  version 3) fold by the built-in rules when read, stored
  templates never (internal/pathtmpl).
- **#8 Hostname & TLS visibility** — DNS hostname↔IP via Java
  agent hooks (InetAddress) is the clean path; TLS peer
  identity (trust-manager/SSLSocket hooks) is extra value at
  extra surface. Non-Java nodes get no hostnames this way.
  *Default: DNS hooks in v1, TLS identity later.* Task wd4
  implements the DNS half: the agent hooks
  `InetAddress.getAllByName(String)` (stable 17–27) and records
  each new host-to-addresses mapping of a JVM as a `dns` event
  (literals and failed lookups not); TLS identity stays later.
- **#20 OTEL integration mode** — agent-read span context
  (primary, socket-free) vs ingesting the app's OTLP stream (a
  local receiver the app dual-exports to, or the collector's
  file export) vs both; and whether the views render a
  distributed-trace overlay on the mesh. Ingested spans are
  the app's own exported telemetry — deliberately distinct
  from the no-payload-capture rule, worth stating in
  docs/schema.md. *Default: span-context tagging only.*
  Built so (task 4b4): span-context tagging, the `otel`
  envelope object; ingestion stays deferred (below).

## Open — raised for M4 (built on the default)

- **#1 Remote bootstrapping and sudo scope** — transport is
  settled: SSH, key-based, from the controller only. The sudo
  scope is one pinned sudoers rule for exactly
  `/opt/iormesh/bin/iormesh` (ior precedent: scoped sudoers
  rules). Raised: bootstrap and upgrade. The first install needs
  broader privilege once (the operator's own sudo, or
  configuration management laying down the binary and the
  rule), and the binary must be root-owned and not writable by
  the deploying user — otherwise the pinned rule is full sudo
  in disguise. Upgrades therefore go through a tracer
  subcommand that verifies the staged binary's signature
  against a root-owned public key (#28) before replacing
  itself. Built so (task bb4, the upgrade half):
  `iormesh upgrade FILE` runs through the rule and takes
  only the staged file and `FILE.minisig`. It reads both once
  into memory, verifies them against
  `/opt/iormesh/etc/minisign.pub`, requires the signed name to
  be this arch's tracer (`iormesh-linux-<arch>`) and the signed
  version not older than the installed binary's (checked under
  an exclusive lock on `bin/`; through the rule the installed
  binary must still be the running process's own file, and the
  same version with other bytes is refused, so every release
  bumps the version), then installs exactly the verified
  bytes: a new root-owned 0755 file in `bin/`, renamed over
  `/opt/iormesh/bin/iormesh`. It first
  checks the layout — `/opt`, `/opt/iormesh`, `bin/`, `etc/`
  root-owned directories nobody else may write, the installed
  binary and the key regular root-owned files, never a
  symlink — and refuses rather than repairs. A downgrade needs
  `-allow-downgrade`, which root may pass but the rule may not:
  an older signed build may carry a fixed hole.
  Built so (task qh4, the bootstrap half; owner-approved
  2026-11-22): the first install — binary, key and rule — is
  the operator's own sudo, driven by `iormeshctl bootstrap
  HOST` (or configuration management doing the same). Without
  `-apply` it contacts nothing and prints the exact steps,
  with the hashes of the build it verified, for the arch and
  SSH user `-arch` and `-user` name (default amd64 and the
  registry host's `user@` part, else the controller's own user
  name, ssh's default login — a `User` in `~/.ssh/config` is
  not seen); the rule's user must be one user, never `ALL` or
  an all-caps alias name (sudoers would widen it); with
  `-apply` it reads both from the host (`uname -m`, `id -un`;
  a flag that disagrees is refused) and runs the same steps
  over the Transport. Interactive sudo over ssh needs
  a TTY and the Transport has none (`BatchMode`, decision #12),
  so `-apply` runs every privileged step as `sudo -n`: it needs
  the SSH user's own sudo to work without a password
  (NOPASSWD, as on cloud images), and a host whose sudo asks for
  one is refused with a pointer to the printed steps, which the
  operator runs by hand on a terminal. Only registered hosts,
  and only hosts without the tracer (deploy upgrades the
  rest); an existing `/etc/sudoers.d/iormesh` is refused, never
  overwritten. The steps: the controller verifies
  `dist/iormesh-linux-<arch>` against its pinned key exactly as
  deploy does and stages those verified bytes, its signature,
  the pinned key and the rule in `~/.iormesh/staging`; root
  creates `/opt/iormesh`, `bin/` and `etc/` (root:root 0755),
  installs each file under a temporary name next to its place
  (sudo ignores a `.`-name in `sudoers.d`), checks the root-owned
  copy's SHA-256 against the controller's (the rule also with
  `visudo -cf`) and only then renames it into place — the
  binary never runs before that check; then, still under the
  operator's sudo and before the rule goes live, the installed
  tracer's own `upgrade` of the staged build re-verifies it
  against the root-owned key and checks the whole layout
  (same bytes: a no-op); last the rule is installed and a
  `sudo -n iormesh status` proves it. The rule is
  `USER ALL=(root) NOPASSWD: /opt/iormesh/bin/iormesh` for the
  SSH user, under a marker comment line, generated from
  `internal/hostlayout` — the one source the tracer's gate,
  deploy and the docs' tests use. `iormesh uninstall` (rm's
  last step) removes `/etc/sudoers.d/iormesh` when its first
  line is that marker, and leaves any other rule alone.
- **#28 Signature scheme** — which tool signs the tracer build
  (minisign, cosign, `ssh-keygen -Y`), where the public key is
  pinned (controller config; a root-owned copy on servers for
  upgrades), and who holds the private key (the release
  pipeline). *Default: minisign, key pinned in the controller
  config.* Built with the default (task bb4; the owner has not
  decided otherwise): minisign signatures made and checked with
  `aead.dev/minisign` (pinned in go.mod/go.sum; the files are
  plain minisign, so the `minisign` tool checks them too). The
  signed trusted comment carries `file:` and `version:`, so a
  renamed or older build cannot pass as the one wanted.
  `mage sign` signs `dist/` with the release pipeline's
  encrypted secret key from `$IORMESH_MINISIGN_KEY`, which must
  lie outside the repository; no private key is ever committed,
  tests generate throwaway keys. The controller pins the public
  key at `~/.config/iormesh/minisign.pub` (`iormeshctl verify`,
  which deploy will run); servers hold the root-owned copy at
  `/opt/iormesh/etc/minisign.pub`. Key rotation is replacing
  both files — the server's with the operator's own sudo, as
  in bootstrap.
  *Provisional (2026-11-21, owner-delegated default, task kf4):
  the Java agent jar is a release artifact signed and checked
  exactly like the tracer* — the plan had not said how the jar
  reaches a server, and M4's TLS label needs it there. `mage
  agentBuild` copies it to `dist/iormesh-agent.jar`, `mage
  sign` signs it at the release version (`file:
  iormesh-agent.jar`) — and refuses to, as it refuses a tracer
  or controller, when the artifact's own stamp (the jar's
  manifest `Implementation-Version`, a binary's embedded
  version) differs, so a stale build is never signed as the
  release (task uf4; the stamp proves the version, not the
  commit — task 8g4 left a vcs revision out, as the container
  builds see no `.git`, and docs/build.md has dist/ rebuilt
  from the release commit before signing) — `iormeshctl
  deploy` verifies it with the
  pinned key and ships it only to hosts with a project using
  `-spool` or `-agent`, and `iormesh upgrade` — no new
  subcommand, no wider sudoers rule — installs a file whose
  signed name is the jar's as `/opt/iormesh/lib/iormesh-agent.jar`
  (root's, 0644; `lib/` created root's 0755 by the first install,
  so the bootstrap is unchanged). The jar's signed version must
  equal the installed tracer's (one ring format, and an older
  signed jar may carry a fixed hole); through the rule the same
  version with other bytes is refused, as for the tracer. A
  confined session's `-agent` may name only that file, so root
  never loads a jar the release key did not sign: the tracer's
  "never run unverified code" rule covers the jar; `start`
  forwards the checked, resolved jar path to its unit, whose run
  re-checks it. The installed jar has no version of its own, so
  a tracer upgrade to another version removes it under the
  upgrade lock and the next deploy installs the matching one
  (until then `-agent` fails, closed). JVMs matched at exec get
  the same file by `run -agent`'s late attach (M3, task lh4),
  or through `-javaagent` pointing at it, which misses nothing
  before the attach (the M4 runner sets `JAVA_TOOL_OPTIONS`;
  the product sets nothing in a JVM's environment). Overturned if the owner
  wants the jar inside the tracer binary (one artifact, extracted
  at start) or a separate key for it.
- **#24 Service-interaction database** — controller-side:
  sqlite via a pure-Go driver (keeps iormeshctl cgo-free;
  additive merges and queries come natural) vs a plain YAML
  file (trivially diffable and inspectable, but incremental
  updates and bigger meshes get awkward); one database per
  project vs one with project scoping; glob vs regex for the
  naming rules. Lives under `~/.local/share/iormesh/`, next to
  the collected traces. *Default: sqlite, one database, glob
  rules.* Task hb4 built the default (`modernc.org/sqlite`,
  `~/.local/share/iormesh/services.db`, every row carrying its
  projects, `-project` filtering the listings, rules global);
  the owner has not decided otherwise.
- **#21 Project semantics** — for now a project is a manual
  label (`-project`, kept with the registry entry); later
  candidates: defaulting to the k8s namespace, or
  auto-derivation from the traced app. Cross-project
  collection: explicit project list vs merge-everything-found.
  *Default: manual label, explicit list.* The registry (task
  cb4) implements the default: each server entry lists its
  projects by label, each with its own target and flags. In k8s
  mode the project defaults to the namespace (M7): task vb4
  built the tracer side — `iormesh run` in a pod (the
  downward-API `POD_NAME`, `POD_NAMESPACE` and `POD_UID` all
  set) without `-project` takes the namespace; an explicit
  label still wins.
- **#17 Disk-watchdog semantics** — percent of partition vs an
  absolute free-bytes floor (or both); check interval; and
  what the controller does when a tracer exits on the limit
  (surface it, redeploy with a lower duration, or nothing).
  *Default: both limits, surface only.* The tracer side took the
  default (task 8b4): 90% and a 256 MiB floor, checked every
  5 s and before every rotation. The controller half surfaces
  it (task lf4): `build`'s summary and `show health` name a
  session that ended on `disk_limit`; nothing is redeployed.

## Provisional — M7 (2026-11-20)

Settled provisionally under the sequencing relaxation (see
Milestones) with the plan's own leanings, task ub4. Each names
what would overturn it and what the owner must confirm. When
settled none was proven on kind (the settling host ran no
Docker); M7's acceptance on kind and el9 (tasks yb4, cf4–ff4,
5h4) has since exercised them, but they stay provisional until
the owner confirms them. Fleet facts — kubectl contexts,
namespaces, registries, kernels — are the owner's and appear
here only as placeholders.

- **#13 K8s privilege profile** — *Provisional (2026-11-20):*
  the minimal capability set, `privileged: true` the documented
  fallback; the tracer container — never the pod — runs as
  SELinux type `spc_t` (task 5h4, below). The tracer container runs as uid 0 with
  `allowPrivilegeEscalation: false` in a pod with `hostUsers:
  true` (a user-namespaced pod's capabilities do not reach BPF
  or the host kernel's tracing), drops ALL and adds:
  - `BPF` and `PERFMON` — loading tracing programs (tracepoint,
    raw tracepoint, uprobe_multi), the ring buffer and stack map,
    and the `perf_event_open` that attaches each tracepoint;
  - `SYS_RESOURCE` — only because libbpfgo v0.9.2's
    `NewModuleFromBuffer` raises RLIMIT_MEMLOCK to 512 MiB
    before every load; the kernel (≥ 5.11) charges BPF memory to
    the memcg and needs no limit. A load path that skips the bump
    removes it (M7 code);
  - for Java attribution (`-spool`, `-agent`, `attach`):
    `SYS_PTRACE` (the JVM's `/proc/<pid>/root`, `cwd`, `maps`,
    `environ` across uids — also `-listen-port`'s fd scan and
    `-native-stacks`), `DAC_OVERRIDE` (the JVM's 0600 attach
    socket, its 0600 ring, its working directory), `KILL` (the
    one SIGQUIT to a JVM of another uid) and
    `CHECKPOINT_RESTORE` (`/proc/<pid>/map_files`, by which the
    Java mark opens the very `libjava.so` mapped). M7 may drop
    it: open the library through `/proc/<pid>/root/<path>`
    (`RESOLVE_IN_ROOT`, as for unmapped libraries today) and
    accept it only if its device and inode equal the mapping's
    in `/proc/<pid>/maps` — the same file, proved without
    `map_files`.
  No `SYS_ADMIN`: every attach type the tracer uses (classic
  tracepoint, raw tracepoint, uprobe_multi) checks
  `CAP_BPF`/`CAP_PERFMON`. Kernel floors: 5.8 for the two
  capabilities and the ring buffer, 5.9 for
  `CAP_CHECKPOINT_RESTORE`, and 6.10 (or a 6.6–6.9 kernel with
  commit 46ba0e49, internal/bpf/javamark.go) for Java
  attribution's uprobe_multi marks — upstream versions; Rocky
  9.9's el9 5.14 carries all of these as backports (task lb4,
  see "Target platform"). Measured on the host, not
  assumed: this set traces and attaches as root does, and each
  capability left out alone breaks a step it is listed for
  ([testing.md](testing.md#k8s-capability-set-task-ub4)).
  Also required — volumes the pod must already carry, since an
  ephemeral container can mount only the pod's own volumes:
  - tracefs from the host, read-only (hostPath
    `/sys/kernel/tracing`): libbpf reads each classic
    tracepoint's id there, a container's sysfs has none, and
    mounting it needs `SYS_ADMIN`. Without it nothing attaches
    (`errNothingAttached`). Raw or BTF tracepoints would need
    only BTF, but task 8e4 measured them skipping runs on 7.2
    (internal/c/dispatch.c), so the classic hooks — and tracefs
    — stay;
  - a size-bounded `emptyDir` (`sizeLimit`) for the trace dir,
    so segments never fill the container's writable layer
    unbounded.
  No third volume for the host id: the node's
  `/etc/machine-id` is not mounted — an in-pod tracer's host is
  the pod, named from the downward API (M7 "pod is the host",
  task 7f4).
  BTF comes from the container's own sysfs
  (`/sys/kernel/btf/vmlinux`). Seccomp: `RuntimeDefault` is the
  provisional default — the containerd and Moby default
  profiles allow `bpf` with `CAP_BPF` and `perf_event_open`
  with `CAP_PERFMON`, and `openat2`, `pidfd_open` and
  `pidfd_send_signal` outright (believed from containerd 1.6
  and CRI-O 1.23, whose profiles gained the two capabilities;
  pin the minimum on kind) — with a `Localhost` profile, then
  `Unconfined`, as fallbacks for older runtimes.
  Found in the code: the tracer targets *host* PIDs (target_map
  is keyed by the kernel's tgid, `/proc` read as the host's), so
  in a pod's PID namespace `-pid` would name nothing. M7 makes
  targeting namespace-aware — the tracer passes its PID
  namespace and the BPF side keys by the pid at that
  namespace's level, walked the way `ior_current_namespace_tgid`
  (internal/c/filter.c) walks levels, since
  `bpf_get_ns_current_pid_tgid` matches only a task's innermost
  namespace and a pod may nest one — which also scopes the
  tracer to the pod by construction (built by task 4f4,
  internal/c/pidns.c; checked on the host in `unshare --pid`
  namespaces, nested ones included); `hostPID` is the rejected
  shortcut (it hands the app containers the node's process
  table). Fallback to `privileged` (which must not set
  `allowPrivilegeEscalation: false`; the API refuses the pair):
  a kernel before 5.8; a runtime that rejects the newer
  capability names; an SELinux-enforcing node whose policy has
  no `spc_t` (or denies it what the tracer needs); an AppArmor or seccomp default that
  cannot be relaxed per container; a node-level debug pod.
  Privileged does not help under kernel lockdown
  (confidentiality): the tracer's kernel reads are refused
  there. Overturned if: the kind run (or a fleet node) needs a
  capability beyond this list, or namespace-aware targeting
  proves unworkable (then a node-level tracer with `hostPID`
  takes over the in-pod design). Owner to confirm: node kernels
  and LSMs, runtime and seccomp defaults, and the Pod Security
  level of target namespaces — `baseline` and `restricted`
  refuse this set and `privileged` alike, so the tracer needs a
  `privileged`-level namespace or an exemption.
  On kind (task yb4, containerd 2.2.0, `RuntimeDefault`
  seccomp, no LSM active): the set sufficed — the tracer loaded
  its programs, attached through the tracefs hostPath, read BTF
  from `/sys`, live-attached the agent across containers, and
  ran with exactly these seven capabilities (CapEff
  0x1c001080022). Older runtimes were not run.
  On el9 (task zg4, Rocky 9.9 k3s node, SELinux enforcing,
  container-selinux 2.250, k3s-selinux 1.6, containerd 2.2.10,
  `RuntimeDefault` seccomp): the SELinux trigger above fires.
  The set is not short — the container type is: `container_t` is
  denied `bpf { prog_load }` (no boolean allows it), and
  Unconfined seccomp or an added `SYS_ADMIN` changes nothing.
  Measured on probe pods: `seLinuxOptions.type: spc_t` with
  these seven capabilities and `RuntimeDefault` attaches, and so
  does `privileged`. In permissive and then enforcing runs the
  tracer needed, beyond what `container_t` has: `bpf {
  map_create map_read map_write prog_load prog_run }`,
  `perf_event { open cpu kernel read write tracepoint }`, and
  `tracefs_t` `dir { search read open getattr }`, `file { open
  read getattr }`; with exactly that granted to `container_t`
  the whole acceptance passed and no AVC was logged. BTF from
  the container's sysfs needed nothing. So on an
  SELinux-enforcing node the choice is between `privileged`,
  `spc_t` (unconfined in SELinux, yet keeping the capability
  set, seccomp and no privilege escalation), and an iormesh
  container type shipped as a policy module with just the
  permissions above.
  *Provisional (2026-11-21, task 5h4, owner-delegated advisor
  decision):* `spc_t`. The minimal profile's tracer container
  carries `securityContext.seLinuxOptions: {type: spc_t, level:
  s0}` — container level only, never the pod's securityContext, so the
  app containers stay `container_t` — beside the seven
  capabilities, `RuntimeDefault` and `allowPrivilegeEscalation:
  false`, all unchanged. It is in `kubectl debug --custom`'s
  spec and the sidecar patch (`deploy/`); the prepare patch and
  the privileged profile (already `spc_t`) are unchanged. On a
  node without SELinux the field is inert. `iormeshctl deploy
  -selinux-type`, or `selinux_type` in the registry's `k8s:` or
  a pod entry (#18's precedence), picks another type; an empty
  one omits the field; a value must match `[a-z0-9_]+`. The
  `iormesh_container_t` policy module (task 7h4, below) is the
  opt-in confined alternative; `privileged` stays the last
  fallback. The level is not optional: given a
  type alone, containerd gives the container a fresh MCS pair
  and relabels the pod's shared files (`/etc/hosts`,
  `resolv.conf`, the trace `emptyDir`) to it, and on el9 the
  gateway's JVM (`container_t` at the pod's pair) was then denied
  reading `/etc/hosts` (task 5h4's first run); at `s0`, which
  every pair dominates, the app keeps access, and those files
  lose the pod's MCS pair: every `container_t` process on the
  node dominates `s0`, so MCS no longer separates them from
  other pods — only mount namespaces do, and a hostPath over
  `/var/lib/kubelet/pods` or an escape reaches them; the label
  stays after a debug tracer exits. Advisor confirmed `s0`
  (2026-11-21). For owners who pin pod levels (task 8h4):
  `iormeshctl deploy -selinux-level`, or `selinux_level` in the
  registry's `k8s:` or a pod entry (#18's precedence), sets the
  level — the pod's own pair, which keeps the shared files at
  it and so their MCS isolation; default `s0`; a value must be
  `s0` or `s0:cN,cM` with distinct N, M ≤ 1023; empty omits the
  level (the type-alone behaviour above); an empty type omits
  the whole field, level included. The sidecar patch is static
  at `s0`: its header tells such an owner to edit the level to
  the pod's pair.
  *Opt-in (owner, 2026-11-21, task 7h4):* the dedicated type
  `iormesh_container_t`, shipped as the `iormesh-selinux` RPM
  (`packaging/selinux/`: `iormesh.te`, the spec, `mage
  selinuxRPM docker|podman` building on Rocky 9 in a container;
  `%post` `semodule -X 200 -i`, `%postun` `semodule -X 200 -r`
  on erase — priority 200, the distribution modules', so 400
  stays free for local overrides; requires container-selinux;
  stop every `iormesh_container_t` tracer before `dnf remove`,
  or its processes become `unlabeled_t`). `spc_t` stays the default; an
  owner whose nodes accept the RPM deploys with
  `-selinux-type iormesh_container_t -selinux-level s0:cN,cM`
  (or the registry's `selinux_type`/`selinux_level`). The type
  comes from container-selinux's `container_domain_template`,
  as `container_t` does (entrypoint and management of
  `container_file_t`, MCS-constrained, `system_r`), with
  `container_t`'s attributes `container_net_domain`,
  `container_user_domain`, `sandbox_net_domain`,
  `sandbox_caps_domain` and `svirt_sandbox_domain`, and exactly
  these allow rules: the tracer's seven capabilities (task gh4),
  `self:capability { dac_override kill sys_ptrace sys_resource }`
  and `self:capability2 { bpf perfmon checkpoint_restore }`; zg4's
  `self:bpf { map_create map_read map_write prog_load prog_run }`,
  `self:perf_event { open cpu kernel read write tracepoint }`,
  `tracefs_t:dir { search read open getattr }` and
  `tracefs_t:file { open read getattr }`; and, for the Java
  attach (#14), `container_t:dir { search getattr }`,
  `container_t:file { open read getattr }`,
  `container_t:lnk_file read`, `container_t:process signal` and
  `container_t:unix_stream_socket connectto`. The second group
  came from an audit2allow review of a permissive run (the type
  permissive, dontaudit rules off) — zg4's diagnostic module had
  granted `container_t` itself, which owns the JVM's `/proc`
  entries and socket, so it needed none; without them BPF
  tracing passes and the agent never joins, with no AVC logged
  (container-selinux dontaudits a container domain searching
  another domain's `/proc/<pid>`). No wildcard, no permissive,
  no MCS exemption: unit tests pin the rule text. The module
  grants the seven capabilities itself, so the type no longer
  depends on container-selinux's `virt_sandbox_use_all_caps`
  boolean, through which six of them (all but `KILL`) reach
  `container_t` and, before task gh4, reached this type too (with
  the boolean off its BPF load failed on `capability2 { bpf
  perfmon }`). On el9 the M7 acceptance passed with the boolean
  off and on (task gh4); the boolean still matters to the app
  containers, `container_t`: with it off the workload's JVM,
  running as root, was denied `capability dac_override` opening
  its jar, so that run gave `container_t` back `dac_override`
  alone through a temporary local module. MCS is why the level
  must be the pod's pair, but it
  does not gate every rule: it gates the signal, the `file` and
  `lnk_file` reads, `dir` search and read, and writes to
  filesystem sockets, which at the pod's pair reach the pod's
  own app containers and any `container_t` at plain `s0`, and
  no pod at another pair; the pod's shared files keep the
  pair. `unix_stream_socket connectto` is not MCS-constrained
  (an abstract socket is bounded by the pod's network
  namespace), nor are `file { open getattr }` and `dir
  getattr` (bounded by what the tracer's PID namespace shows).
  Where owners pin one pair per Deployment or namespace, shared
  by its replicas, MCS separates none of those pods and
  containment rests on the namespaces alone. A tracer at another
  pair than the app's is kept out: on el9 a tracer at
  `s0:c100,c200` beside an app container at `s0:c300,c400` loaded
  its BPF programs, but its pattern never matched the JVM and
  `iormesh attach` found no such process, with `avc: denied {
  search }` on `/proc/<pid>` (`tcontext=container_t:s0:c300,c400`,
  logged with dontaudit rules off; task gh4). At `s0` the tracer
  can neither read the app's files through `/proc/<pid>/root`
  nor signal or search its processes, so the Java attach is
  denied (BPF tracing still works; this follows from the same MCS
  constraint and was not run at `s0`). An MCS exemption
  (`mcs_constrained` left out, or `mcsreadall`) would make `s0`
  work but let the tracer read any pod's `container_file_t`
  files and reach any container's processes its namespaces
  show — rejected. A pod that does not pin its level gets a
  random pair, so the type suits owners who pin MCS levels; a
  debug tracer does not discover the pair itself. On el9 (one
  Rocky 9.9 k3s node, the versions of 5h4) the M7 acceptance
  passed with it under iptables and nftables, no AVC, app
  containers `container_t` at the pod's pair
  ([testing.md](testing.md#dedicated-type-iormesh_container_t-task-7h4-2026-11-21)),
  and `spc_t` still passed with the module absent; task gh4's
  RPM with the capability rules loaded and unloaded at priority
  200 and passed under iptables with the boolean off and on.
  Owner to confirm: `spc_t` and `s0`
  as the fleet default, no tenant hostPath to the kubelet
  directories, whether fleet pods pin MCS levels, where nodes
  accept the `iormesh-selinux` RPM (and so the opt-in type),
  OpenShift (its SCCs) out of scope.
- **#14 K8s Java attach** — *Provisional (2026-11-20):* live
  attach through the shared process namespace is the k8s
  default; `-javaagent` via `JAVA_TOOL_OPTIONS` stays the
  documented alternative for pods that restart anyway (with the
  sidecar: a debug tracer ends with its target container's
  restart, task ah4, see M7), for JVMs
  run with `-XX:-EnableDynamicAgentLoading`, and for a future
  JDK that disables dynamic loading by default (#2's caveats
  apply unchanged). The ephemeral container joins the target
  container's namespace (`kubectl debug --target`), so the JVM
  is never restarted for the attach — but the pod must already
  carry #13's two volumes (tracefs, the bounded trace dir), so
  a pod is prepared once by the deployment owner's patch (one
  rollout, no tracer container in it); a sidecar needs that
  patch plus `shareProcessNamespace: true`. The handshake is
  ge4's, unchanged: trigger file and
  socket reached through `/proc/<pid>/root` and `cwd`,
  `SO_PEERCRED` naming the target, as uid 0 (HotSpot accepts
  root or its own euid). M7 code: the JVM resolves the jar and
  spool paths in its own mount namespace, and an ephemeral
  container cannot add a volume to the app container, so the
  tracer stages both inside the JVM's root — a root-owned
  directory under its `/tmp`, created exclusively with
  `RESOLVE_IN_ROOT` and refused if it exists or is not root's,
  removed when the session ends — and reads the ring through
  `/proc/<pid>/root` (#27 foresaw this). That staging is
  bounded: the jar (≈280 KiB) and one
  ring per JVM of a fixed size (8 MiB by default, `ringsize=`
  at most 64 MiB; AgentArgs.java, Ring.java), charged to the app
  container's ephemeral storage — or its memory where `/tmp` is
  a tmpfs. Capabilities: #13's Java four. Overturned if: the
  owner's JVMs run with dynamic loading disabled or a
  read-only root without a writable `/tmp` (then `-javaagent`
  becomes the default), or the kind run shows the runtime's
  AppArmor profile blocks the cross-container `/proc` reads.
  Owner to confirm: JDK versions and flags in the fleet, and
  whether app containers keep a writable `/tmp`.
  Built (task cf4): `run -agent` stages for every JVM whose mount
  namespace is not the tracer's and whose PID namespace is (the
  agent names its ring by its own pid; a JVM in another PID
  namespace gets a note naming `-javaagent`). The JVM's `/` and
  `/tmp` must also be root's or the JVM user's and not writable
  by group or others unless sticky (the jar's trust rule, inside
  the JVM's root) — so an `emptyDir` mounted at `/tmp` (0777, not
  sticky) is refused too; a stage name already taken is skipped
  for a fresh random one, never adopted. Every check runs for
  every JVM; two JVMs share a stage only with the same euid and
  the same `/tmp` directory object (device and inode), not
  merely the same mount namespace (review of cf4). `iormesh attach` stages nothing: its spool belongs to
  a separate `run`, which would not read a stage it made. Proven
  in `mage integrationTest` with JVMs in private mount
  namespaces (`TestJavaAttachMountNS`). On kind (task yb4),
  before cf4: `run -spool` in the scratch image failed with a
  spool under `/tmp` (the image has none), and the jar path was
  the tracer's — so no in-pod tracer attributed Java. With the
  paths made to agree by hand (the app mounting
  the trace emptyDir, its image carrying the jar), live attach
  through the shared PID namespace worked: the agent joined and
  its http and dns events carried the gateway's FQDN. Neither
  AppArmor nor SELinux was active on that Fedora host, so the
  AppArmor question stays open.
- **#15 Route-map source of truth** — *Provisional
  (2026-11-20):* a static YAML map first, k8s API discovery
  second, Traefik's API and access logs not in v1. The map lives
  on the controller (`~/.config/iormesh/routes.yaml`, the
  registry's ownership and mode rules) and maps FQDN → ingress →
  service → deployment/statefulset; it works from outside any
  cluster. Discovery, once built, runs on the controller with
  the operator's kubectl (read-only `get` of Ingress,
  IngressRoute, Service, EndpointSlice and the owning workload),
  never from a tracer. Precedence: a static entry wins over a
  discovered one for the same FQDN; discovery fills only FQDNs
  the file lacks; observed http events (host + path from the
  agent) never change a route — a route they contradict is
  flagged in the build. Traefik's API needs a connection the
  controller does not otherwise make, and its logs need log
  access; both wait for a need. Overturned if: the owner's
  ingress routes change too often for a hand-kept file
  (discovery moves first), or the ingress is not Traefik.
  Owner to confirm: the ingress controller and CRDs in use, and
  a sample of real routes.
  Built (task xb4): the static map and discovery
  (docs/implementation.md M7 step 12); discovery writes a
  snapshot the build reads, so `build` itself never runs
  kubectl. Its `services:` section — ClusterIPs, ports,
  endpoints, the Service CIDR — is an addition the ClusterIP join
  needs (a route names a Service, not its address); a route's
  workload may be left out and is then the Service's (discovered
  routes leave it to the Service, so a static Service shadowing a
  discovered one keeps its workload). A route's optional
  `ingress_service` names the ingress controller's Service;
  discovery fills it from an Ingress's status addresses matching
  a discovered Service, else from `routes discover
  -ingress-service`. On kind (task yb4) Traefik's ClusterIP
  Service gave the Ingress no status address, so once Traefik's
  namespace was discovered too, only `-ingress-service` kept the
  gateway's edge the route's instead of a `route_contradicted`
  edge to Traefik's Service. A path-restricted ingress rule
  becomes a whole-host route: the map routes FQDNs, not paths. Scoping: the
  table applies to every flow of a build, whichever cluster or
  host it came from — a ClusterIP another traced cluster reuses
  would be named after this map's Service; the build refuses a
  snapshot whose context no registered cluster names, and one
  snapshot (the last discovered) serves the registry.
- **#18 K8s debug-container config in iormeshctl** —
  *Provisional — owner's examples pending (2026-11-20):* k8s
  targets live in the registry beside `servers:`, as a `k8s:`
  section of defaults and clusters plus a `pods:` list whose
  entries carry the same `projects:` as a server; flags
  override per run (`-context`, `-namespace`, `-image`,
  `-selinux-type`, task 5h4; `-selinux-level`, task 8h4). Order:
  flag, then pod entry, then cluster, then the built-in default
  image (ours, pinned by digest). Every kubectl call passes
  `--context` explicitly — the registry, never the kubeconfig's
  current context, picks the cluster, as it picks hosts for SSH.
  Mode `debug` (ephemeral container, the default) or `sidecar`
  (a patch the deployment owner applies); profile `minimal`
  (#13's set, via `kubectl debug --custom`) or `privileged`
  (`--profile=sysadmin`). Either mode needs a pod prepared with
  #13's volumes: before creating the ephemeral container,
  iormeshctl checks the pod spec for them and refuses an
  unprepared pod, naming the patch. Trace fetch (#12):
  `kubectl exec` of a tracer subcommand that takes no path — it
  streams only the configured trace dir's `ready/*.parquet`,
  each opened `O_NOFOLLOW` and sent only if a regular file — as
  a tar to stdout; the controller treats the stream as
  untrusted and unpacks it under collect's rules (only
  `*.parquet` directly in `ready/`, no links, no modes, sizes
  bounded), then removes what it took by a second exec — no
  `tar`, `kubectl cp` or rsync needed in the image. Schema
  sketch, placeholders only:
  [implementation.md](implementation.md#m7--kubernetes).
  *Wired (task af4, 2026-11-21, provisional like the rest):*
  commands name a pod entry `<cluster>/<namespace>/<pod>` or
  `<cluster>/<namespace>/<selector>` (the "/" sets it apart from
  a host); a project's name may be left out, and is then the
  namespace (#21). A `pod:` entry is one pod, refused when not
  running; a `selector:` (equality-based, `k=v[,k!=v…]`, at
  least one positive term — never empty) picks *every* running
  pod it matches, each its own session, at most 32 (more is
  refused as a selector broader than meant); one matching no
  running pod fails the command for that entry — a pod gone took
  its emptyDir, so it is never "nothing to collect". A debug-mode
  entry carries exactly one project (an ephemeral container runs
  one process); a sidecar's sessions are its patch's command
  line, and deploy only verifies it. Digest pinning holds in
  both modes: a tracer container already in the pod (the
  sidecar, or an earlier debug container, which is then left
  alone) must run the pinned digest by its status `imageID`;
  another digest, or an `imageID` without a registry digest, is
  refused — and an earlier debug container's spec image must carry
  the pinned digest too (iormeshctl always creates it so; the status
  is the node's word, the spec the API's). An image is needed to
  deploy, not to collect. A debug session runs `iormesh run
  -linger`: when its `-duration` ends it detaches and frees its
  BPF, publishes its last segment and then waits for SIGTERM
  instead of exiting, because an exited ephemeral container can
  never be exec'd into again and its last segments would be
  stranded in the emptyDir. A tracer container stopped anyway (an
  OOM kill, a crash, or an app container restart, which takes a
  debug tracer with it: task ah4) is reported by collect as
  "session ended", without an exec. A sidecar's segments are then
  stranded until the pod goes. In debug mode, deploy starts the
  next tracer (`iormesh-2` … `iormesh-8`), which serves them. Collect
  asks the API server which pods an entry is and whether each
  has a tracer container (none: nothing to collect, said), then
  fetches through `kubectl exec … iormesh ready`; a pod kubectl
  cannot reach fails, and a missing `ready/` is an empty stream —
  no `true`, `test` or `find` in the image — but a project with no
  trace dir at all in the container fails, so a project mismatch
  never reads as an empty collect. One fetch per local directory
  at a time (a flock, failing fast). Each pod's store directory is
  `pod-<uid>`, not its name (a StatefulSet pod recreated under its
  name is a new host), pinned in the build's host pins
  (`hosts.json`, beside the service database: collect's and
  refresh's `-db`, under a lock shared with build) to `host_id`
  `pod-<uid>` and the FQDN task 7f4 gives an in-pod tracer. Those
  pins grow with pod churn, one per pod incarnation ever
  collected, and will need pruning later. A selector pod that was
  replaced drops out of the selector's answer, and its emptyDir —
  whatever it had not yet been collected from — goes with it:
  collect often on churning workloads. `-context` may only restate
  the entry's cluster's context, and neither `-context` nor
  `-namespace` is taken by rm or by a collect of every target.
  rm collects one last time, then drops
  the entry; nothing in the pod is stopped (an ephemeral
  container cannot be removed or signalled without a shell), so
  the session runs until its `-duration` or the pod ends.
  Overturned by: the owner's examples. Owner to provide: real
  contexts, namespaces, the image registry, and how pods are
  picked (labels or names).

## Deferred — not in v1

- **#10 Java exception events** — JVMTI throw/catch hooks would
  enrich the model (error paths in source) but are hot.
  Out for v1; revisit as a sampled, opt-in family.
- **eBPF plaintext HTTP parsing for non-Java processes** (was
  part of #22) — conflicts with the no-wire-parsing rule; out.
- **OTLP span ingestion and the distributed-trace overlay**
  (the optional half of #20) — after span-context tagging has
  proven itself.
- **Virtual-thread attribution** — out of v1; the owner's apps
  do not use virtual threads yet. The problem: the kernel only
  sees the carrier thread, and a parked virtual thread may
  resume on another carrier, so thread id alone cannot name
  the virtual thread. The likely solution when it is needed:
  the agent's I/O-boundary hook records `Thread.currentThread()`
  — hooks sit at the innermost Java frame above the native
  call, where a virtual thread cannot park, so the carrier's
  thread id is stable for the length of one bracket. Fallback:
  track mount/unmount. Until then such I/O is attributed to
  the carrier and flagged (see M3). Needs JDK 21+, a
  `virtual-threads` scenario, and its own acceptance.
