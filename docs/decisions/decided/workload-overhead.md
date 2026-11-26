# Decided — test workload and overhead budget

Part of [Decided](../decided.md), in [Decisions](../../decisions.md).

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
  [docs/spikes/s3-overhead.md](../../spikes/s3-overhead.md)). The
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
  ([docs/implementation.md](../../implementation.md) M2 step 11,
  [docs/schema.md](../../schema.md) "Folded reads and writes"):
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
