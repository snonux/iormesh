# Decided

Part of [Decisions](../decisions.md).

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
  [docs/spikes/s2-cross-host.md](../spikes/s2-cross-host.md).
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
  [docs/spikes/s3-overhead.md](../spikes/s3-overhead.md)). The
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
     Java join (`-spool`, every event joined): 5.2 µs, over
     budget (2b4; td4's Java mark measured +0.7 µs per marked
     event on top, within the host's noise, s3-overhead.md
     "Follow-up (td4)"). Task ud4 cut the
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
     (s3-overhead.md, "Follow-up (ee4): owner decision").
     Measured as the tracer's own user+sys CPU time per event
     from its ready file to its exit (load and attach excluded),
     which host load moves less than wall time — not nothing: on
     a loaded SMT host one thread's CPU time still spreads 2–3×,
     hence medians of ≥ 5 runs. Task ak4 (2026-11-26, Rocky 9.9 on
     the 2-vCPU `iom1`, after the msghdr handlers, ti4, wi4 and
     lh4): 1.06 / 1.05 µs at 1 / 4 threads, the build before them
     1.02 / 1.00 — passes; a msghdr event on a netlink-heavy load
     costs 3.9 µs (not gated; task am4), 1.50 µs after am4's
     reflection-free decode (task 9m4, same VM);
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
     "Follow-up (9f4)"). On el9 (task ak4, `iom1`, 30 pairs):
     1.255× after the msghdr handlers and 1.245× before them, wall
     1.269× / 1.261× — the handlers change nothing, but the item
     is **over** by a hair on el9 in both builds, task 9m4
     (s3-overhead.md, "Re-measure on Rocky 9.9"). Task 9m4
     (2026-11-26, same VM): **1.252×** CPU, wall 1.265× (50
     pairs) — on the line. The trace adds ~0.32 µs to each
     bystander read or write, no more than on the laptop
     (0.28–0.37 µs); 70 % of it is the `raw_syscalls` tracepoint
     path with an empty program (1.18× alone), the rest the
     dispatcher's mirror lookup and scope test, which no cheap
     change removes. The ratio is higher because the el9 guest's
     syscall is cheap (1.26 µs on xfs, one CPU, against
     1.9–2.7 µs on the laptop's btrfs) and because the guest runs
     retpolines, PTI and MDS clearing (bhyve hides the N100's
     `ARCH_CAPABILITIES`): booted with `mitigations=off` the same
     VM measures 1.195× (wall 1.22×) (s3-overhead.md, "Follow-up
     (9m4)"). *Amended (owner, 2026-11-26):* keep ≤ 1.25× as
     the gate on a host whose CPU mitigations are the hardware's
     own (bare metal or a
     hypervisor that passes `ARCH_CAPABILITIES`), and accept the
     measured **1.25× (CPU) / 1.27× (wall)** as el9's figure in a
     guest running the full software mitigation set, with the
     added CPU per bystander read or write (≤ 0.35 µs there) as
     the figure that must not grow;
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
  ([docs/implementation.md](../implementation.md) M2 step 11,
  [docs/schema.md](../schema.md) "Folded reads and writes"):
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
  Where the bound is not a power of two,
  `iormesh_index_clamp(idx, max)` does the same with an inline
  asm `if idx <= max goto +1; idx = max` on the register it
  returns. Applied to every clamp-then-index site:
  `rwagg.c`'s slot and `javamark.c`'s frame indexes (task il4),
  `filter.c`'s histogram bucket (mask, 8 buckets) and
  `fdname.c`'s name length (clamp, at most 67; task ll4). The
  builders still check their clang against a recorded version
  (`CLANG_VERSION` in
  `scripts/container/tracer-*.Containerfile`, held to a
  recorded value by `TestBuilderClangPinned`): not to make the
  C load, but so a compiler change cannot reach a release
  without the el9 verifier check first (docs/testing.md,
  "Builder clang pin"). Rejected: a newer clang in the builder
  only — it hides the pattern until the next compiler change;
  a mask without the barrier — clang drops a mask it proves
  redundant and is back to the checked copy; a power-of-two
  mask for the name length — 127 does not keep the index
  inside the 68-byte field, 63 would cut names of 64–67
  bytes.

- **#32 Unsigned release archives; operators sign** — owner
  decision (2026-11-26, asked during task 7k4; built by task
  5m4), amending #28 for releases. `mage release` builds and
  packs the per-arch archives with a `SHA256SUMS` only: no
  `.minisig`, no `minisign.pub` in an archive, no
  `SHA256SUMS.minisig`; it needs no `IORMESH_MINISIGN_KEY` and
  asks no password. Each artifact must still carry the release
  version's stamp (`release.CheckStamp`), so a stale build is
  never packed. Deploy and bootstrap keep verifying every build
  against the operator's pinned key exactly as #28 says, so the
  operator signs the builds they deploy with their own key:
  `iormeshctl sign -key SECRET-KEY [-version V] DIR` signs the
  release artifacts in an unpacked archive (the tracer, the
  agent jar, the controllers; nothing else there) with no
  repository checkout, and `mage sign` stays for a `dist/`
  built from source. Both run the same `internal/release` code
  (`SignRelease`/`SignDir`, `ReadSecretKey`): the stamp check
  before any file is signed, the trusted comment
  `file:<name> version:<x.y.z>`, a secret key that is a
  regular file, not a symlink, the caller's own, with no group
  or other permission, its password from
  `$IORMESH_MINISIGN_PASSWORD` or read without echo from the
  terminal, never printed. Why: the project holds no release
  key that every operator would have to trust; each operator
  already pins their own key on the controller and their
  hosts, so a project signature would add a second trust
  anchor nobody checks against, and a leaked project key would
  reach every operator who pinned it. `SHA256SUMS` catches a
  corrupt download; what is trusted is the operator's own
  signature after they checked it. Rejected: signed archives
  plus re-signing by the operator — two keys, and the project
  key protects nothing deploy checks; shipping the project's
  public key for operators to pin — the trust anchor this
  decision removes.
- **#33 Invalid UTF-8 in NDJSON, and the supported Go range** —
  the NDJSON writer (`schema.AppendEvent`) writes each invalid
  UTF-8 byte of a string as the six-byte escape `\ufffd`, by
  the project's own rule, not by whichever Go builds it. That
  is the bytes every trace and golden written so far holds:
  the encoder copied Go 1.26's `json.Marshal`, and Go 1.27's
  `encoding/json` now writes the raw U+FFFD bytes instead (task
  8m4). Both decode to the same string, so readers on either
  Go see the same event; only the bytes would differ, and
  byte-stable output for the same event (docs/schema.md,
  "NDJSON framing") is the contract. The encoder's test takes
  `json.Marshal` as its reference for everything else and the
  fixed `\ufffd` escape for invalid UTF-8. Go: go.mod's
  `go 1.26.0` is the floor, CI builds with it, and Go 1.27 is
  supported too (the local gates pass on both); go.mod gets no
  `toolchain` line, which would force a toolchain download on
  every host with another Go. Source must be gofmt-clean under
  both versions: where their gofmt differ (1.27 aligns a map
  literal's keys differently when one key is far longer), the
  code takes a form both print alike. Rejected: following the
  installed Go's `json.Marshal` — a Go update would silently
  change every trace's bytes; a `toolchain go1.26.x` line —
  it hides the 1.27 difference instead of settling it and
  costs a download.
- **#34 The pod sandbox's container id is shown bare** — the
  pod sandbox's `pause` process sits in a container cgroup of
  its own (`cri-containerd-<sandbox id>.scope` on k3s on el9,
  task 6k4), so an in-pod tracer stamps its events with the
  sandbox's runtime id, like any other container's
  (`k8s.ContainerOf`). No container status lists that id —
  `containerStatuses`, `initContainerStatuses` and
  `ephemeralContainerStatuses` name containers only, and `kubectl
  get pod -o json` carries no sandbox id at all — so the pod's
  container map (task jk4) never names it and the build shows
  the bare id, as for any id the map lacks (task fm4). That is
  the decision, not a gap to fill: pause does no traced I/O, so
  the id is seen at most on a process line (pid 1 to a sidecar
  tracer with `shareProcessNamespace`). Rejected: recognising
  the sandbox by its `comm` `pause` — the infra image is the
  runtime's configuration (containerd's `sandbox_image`, CRI-O's
  `pause_image`, any binary name), CRI-O drops the infra
  container altogether unless the pod shares its PID namespace
  (`drop_infra_ctr`), and any app may name a process `pause`, so
  the name would be a guess an app can forge; labelling every
  id the map lacks as the sandbox — a container that restarted
  more than once between two collects is also missing (the
  status keeps only the last `lastState.terminated`), so an
  unknown id is not a sandbox; asking the runtime (CRI
  `PodSandboxStatus`) — the tracer's only host mount is tracefs
  (#13), no CRI socket, and the controller talks to the API
  server alone.
- **#35 Connections open before the session are seeded** (task
  cm4). A TCP socket registers its owner only at connect or
  accept (`sock_owner_map`), so a connection a target opened
  before the session — a keep-alive client, a connection pool
  — gave no state record, its reads and writes no conn id, and
  the build no edge until it reconnected (Rocky 9.9, task 4k4:
  726 events, 0 edges). Now the tracer seeds such sockets:
  right after the probes attach and the start targets are in
  (a pattern's start scan included), and again whenever it
  adopts a process that was already running (a `-listen-port`
  session's restarted owner), userspace runs two BPF task-file
  iterators once each (`iter/task_file`, Linux 5.8; el9 has
  it) by reading them to the end; nothing stays attached. The
  first, `iormesh_seed_listen`, notes the (local address,
  port) of every listening TCP socket of any task the tracer's
  PID namespace shows. The second, `iormesh_seed_sock`, takes
  every TCP socket of a traced target that is in no state of
  CLOSE or LISTEN and not registered yet, registers it as a
  connect would (pid, fd, socket inode; `BPF_NOEXIST`, so a
  socket a connect or accept registered first is not seeded,
  and a second seeding adds nothing; an accept whose exit runs
  after the walk seeded its socket still emits its accept
  snapshot — a second snapshot of one socket, harmless: the
  correlator binds the same fd to the same socket), and emits
  a `tcp_state` record
  whose old state is a marker beyond every TCP state:
  `TCP_STATE_INITIAL_SERVER` (258) when a listener holds the
  socket's local port, on its address or the wildcard (or the
  socket is in SYN_RECV), else `TCP_STATE_INITIAL_CLIENT`
  (257). It carries the 4-tuple and the four `tcp_sock` fields
  (#26), read like the accept snapshot's: re-read until two
  reads agree, else torn. The correlator binds the fd to the
  socket (as a connect's first record does), takes the socket
  as established, and writes a `tcp_state` event with
  `args.sock_snapshot` `initial_client` or `initial_server`.
  The merge reads that value as role evidence, keys the half
  from it as from an accept snapshot (only when no locked
  state change keys it), and takes the half's `Established`
  as an upper bound: such a half is a candidate for a partner
  established no later than its own time plus the window, two
  of them always — the ISN pair alone already tells reused
  tuples apart (S2); the window only guards the ISN clock's
  wrap. `report` and the service database take the role too.
  Why the listener test: the kernel keeps no "accepted" mark
  on an established socket, and the key's SYN correction
  (#26) needs the role; a local port some listener holds is
  how `ss`-based tools tell a server end. Why iterators: the
  owner map is keyed by `struct sock` address, which /proc
  and sock_diag never show; the iterator runs in the kernel
  over the tracer's own PID namespace, so a pod's tracer seeds
  its pod's processes only. What stays: a client whose local
  port a listener of another network namespace also holds, or
  one bound to a listening port on purpose, reads as a server
  — its key then misses by one and the half stays `no_match`,
  never a wrong fuse (likewise a server whose listener closed,
  read as a client); calls between the attach and the seeding
  walk (milliseconds) name no connection; bytes moved before
  the session are in no sum, so the stream check fails for
  such a direction as it must (the counter proof still
  holds); and an initial half gives no handshake clock bound.
  Rejected: inferring the role at the merge from the two
  ends' raw ISN pairs — it needs both ends traced, and a
  single host's report needs the role too; registering lazily
  on a socket's first read or write — an fd walk on every data
  call; duplicating the target's descriptor (`pidfd_getfd`)
  to snapshot it through a test run — the tracer would hold
  the target's socket, delaying its close; keeping it a
  documented limitation — pooled clients are the common case.
- **#36 Large docs are an index plus a folder of parts** — a
  topic doc in `docs/` that grows past about 600 lines keeps
  its path and becomes an index: its intro, then a "Parts"
  list with one summary line per part and the sections each
  part holds. The sections move to `docs/<topic>/<part>.md`
  (for a spike, `docs/spikes/<spike>/`), each part opening
  with a "Part of" line back to its index. A part that grows
  past the same size, and has sections of its own, becomes an
  index of its own the same way (`docs/<topic>/<part>.md` plus
  `docs/<topic>/<part>/`). Agents and readers load the index
  first and open only the part they need, instead of a
  1,500–6,500-line file; every citation of the form
  `docs/<topic>.md "Section"` or `docs/decisions.md #N` keeps
  resolving through the index, and links to a moved section
  point at its part. `docs/decisions.md` also lists every
  decision by number with the part that holds it, so the next
  free number stays one glance away. A section heading kept as
  a part's title keeps its anchor. Docs under the limit stay
  one file. Rejected: one file per `##` section everywhere —
  dozens of 15-line files for testing.md's task records, which
  are read together; splitting by line count — parts would
  start mid-section.
