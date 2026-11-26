# M4 service database, cleanup, platform and acceptance

Part of [M4 — Controller (`iormeshctl`): push, collect, merge
locally](../m4.md), in [Milestones](../../milestones.md).

- **Service-interaction database**: every `build` also
  analyzes the merged traces and updates a controller-side
  database of app-to-app network interaction — per observed
  connection: client app (project, host, app identity),
  server endpoint (hostname as resolved via the agent's DNS
  hooks, IP, port), protocol hints (plain vs TLS, http
  host/path), counts and activity windows — keyed so
  rebuilding the same mesh updates rows instead of
  duplicating them. The database is **additive**: new sessions,
  hosts and projects fold in, and human input is never
  overwritten. Humans name services through the CLI, never by
  hand-editing files: `iormeshctl services` lists observed
  endpoints (unnamed ones first), and naming rules map
  hostname patterns + port numbers to service names
  (`orders*` + 8443 → `orders`); rules apply retroactively
  and to every future build, so the interaction view says
  "orders → inventory" instead of bare host:port — the
  who-talks-to-whom index of the end-goal picture (see
  Purpose); the in-order, thread-attributed story is M5's
  sequence view. Views (M5)
  and the summary rollup render the named services; storage
  format is decision #24.
  Built (task hb4, `internal/servicedb`, #24's default: one
  sqlite database via `modernc.org/sqlite`, glob rules): one
  row per observed TCP connection — fused, unresolved with the
  merge's reason, or a same-host pair — keyed by its anchor end
  (the client half's host-local conn id when the client was
  traced, else the server half's), so a rebuild upserts. The
  window, endpoint and verdict merge as MIN, MAX or
  keep-if-unknown, and a fused verdict never reverts. HTTP
  request counts (method, scheme, host, redacted path) are
  kept per source — the trace unit a build reads — and summed
  at read time, so they stay exact when a pooled connection's
  requests span segments and old segments were deleted; the
  client's and the server's view of the same requests are
  kept apart and the larger counts, never both (exact while
  one view's segments were all imported at some point, a lower
  bound otherwise). Re-import is
  idempotent and a build over fewer segments loses nothing;
  only `prune -apply` makes it forget the segments it deletes
  (task gk4, **Build window and prune**).
  The requests give the TLS and path hints; the hostname is
  the client's HTTP host when that is a name, else its DNS
  lookup of the endpoint's address (a best guess: one address
  may serve many names). An endpoint is hostname, IP and
  port, plus the anchor host for a loopback, link-local or
  unspecified IP (task ai4): that address is a different
  machine on every host, so two hosts' local services never
  fold into one.
  Rules match
  hostname or IP, case-insensitively; port-specific beats
  any-port, then the longer glob, then the older rule; a
  duplicate is refused, never overwritten. The file is 0600 in
  a 0700 directory, refused when open to others; reading
  commands never create it.
  `iormeshctl services` lists (unnamed first), shows the edges
  (`edges -from orders`: what orders talks to), and adds,
  lists and removes rules (`name`, `rules`, `unname`). The
  build calls `Observe` and `Import` (task ib4: one source per
  collected segment) and reads the projects it built back
  through `Filter.Projects`, so a cross-project connection is
  one edge, counted once.
- **Cleanup (`rm`)**: removes the host from the registry and
  tears down the deployment on it — `sudo iormesh uninstall`
  stops every session and removes `/opt/iormesh` (binary and
  traces), then the sudoers rule `iormeshctl bootstrap`
  installed (decision #1). Trace files are synced off first (rsync, same as
  collect), so `rm` never destroys uncollected traces unless
  the operator says so with `-force`; an unreachable host
  warns and only drops the host (or the `-project`) from the
  registry — its tracer and traces stay on it, and deploy's
  record keeps the dropped projects as tombstones so a later
  `rm` of the host still collects them.
  Built (task eb4, `internal/ctl/rm.go`): per removed project
  a live session is stopped first (`sudo iormesh stop`, which
  publishes its last segment and now fails when `state.json`
  does not say `ended` — a run SIGKILLed after the stop
  timeout left its minute unpublished), then collected.
  `uninstall` runs only when the host leaves the registry
  (`rm HOST`, or its last project), and first every other
  project directory under `/opt/iormesh/traces` that the
  registry or deploy's record (tombstones included) lists for
  the host is stopped and collected too. A directory neither
  lists (started by hand, or named by a compromised server)
  is not collected (task hi4): the server never picks the
  store's project.
  Then any segment file still in a `ready/` or active —
  an unlisted project's included — refuses the uninstall,
  naming it.
  The registry changes only after all of that succeeded — a
  failed or unclean stop, a failed sync, a remote check that
  fails other than with "no" (exit status 1; ssh's 255 is a
  failure), leftovers, or a failed uninstall leave it as it
  was. `-force` turns an unclean stop and leftovers into
  warnings and uninstalls, losing them. A host without the
  installed tracer is just unregistered.
- **No management plane**: every tracer flag and feature is
  reachable through its own CLI, and `iormeshctl` composes
  those commands over plain SSH (later `kubectl exec` in the
  k8s phase) — the tracer never needs a network socket on the
  server. Tracer flags pass through on deploy; runtime changes
  are stop-and-restart with new flags (cheap under bounded 4h
  runs); status comes from the `status` subcommand reading its
  state files. The only way into a server is the operator's
  SSH key. Built (task ph4, owner-approved 2026-11-22):
  `iormeshctl status [-project P]... [HOST|POD...]` asks every
  registered target (or the named ones) — a host through the
  sudoers rule's `status -json`, which the rule already allows,
  a pod through `kubectl exec … iormesh ready -status` — and
  prints one row per target and project: state, end reason,
  ends-by, tracer version and the segments in `ready/` awaiting
  collection; a target that fails is a row, never an abort (exit
  1 after all were asked). `iormeshctl list` prints the registry
  (hosts, pod entries, projects, targets, flags) and contacts
  nothing. The run records its tracer version and argv in
  `state.json` so a skew between the deployed build and a
  running session is visible; a state from an older tracer
  lacks them and shows the version as unknown.
  Built (task zh4, owner-approved 2026-11-22): **tracing over
  days**. A session ends after its `-duration` and its
  transient unit has no `Restart=`, so a reboot, an OOM kill
  or a disk-limit trip ends it for good. `iormeshctl ensure
  [-project P]... [HOST...]` reads each host's `status -json`
  and starts again (deploy's start argv and start path) every
  registered project whose session ended on its own —
  `duration`, `targets_exited`, `error`, a `stop` that was not
  the operator's, a run that died — then collects the host.
  An explicit stop stays distinguishable from an end: `iormesh
  stop` sets `operator_stop: true` in `state.json` once the
  unit is dead (never while the run may write the file; the run
  writes `false` from its start), since a reboot's SIGTERM ends
  a run with the same `stop` reason; ensure leaves those
  stopped. A deploy restart whose start fails after its stop
  leaves the project marked stopped, so ensure does not recover
  it. A tracer from before the field (`operator_stop`
  absent) cannot tell, so its `stop` ends are left too. Both
  status texts say which ends ensure leaves (task 0l4): the
  `iormeshctl status` NOTE and the host's `iormesh status`
  name an operator's stop and a `stop` of unknown origin;
  `-json` carries the field itself. A
  `disk_limit` end is collected first and restarted only when
  the collect emptied its `ready/` (a second status read);
  should the partition still be over the limit, the new run's
  watchdog refuses at its start, so no restart loop fills the
  disk. ensure starts no project the host never ran and never
  upgrades — both are deploy's — and run again only collects,
  so a systemd user timer or a cron line drives it
  ([architecture.md](../../architecture.md#tracing-for-days); the
  timer in [the guide](../../guide/09-tracing-for-days.md#from-a-systemd-user-timer)).
  Decided (task pk4): **ensure covers pod entries** too, every
  registered one without a target named. A pod's tracer has no
  unit or `state.json` ensure can read once its container
  stopped (`kubectl exec` cannot enter it), so ensure decides a
  debug pod by its newest tracer container as the API server
  reports it, and restarts only through deploy's pod path (a
  fresh `iormesh-N`, at most `MaxDebugTracers`, then the attach
  wait): a container that terminated with a non-zero exit code
  — killed with its app container, OOM-killed, crashed — is
  restarted; one that exited 0 was ended on purpose (only a
  SIGTERM or SIGINT ends a `-linger` run cleanly; a session that
  had failed exits 1 on the signal and is restarted) and is
  left stopped, its collect skipped and reported rather than
  failed. A terminating pod (`deletionTimestamp` set, still
  phase Running) is left whatever its tracer: a selector never
  returns one, but a `pod:` entry's named pod is returned
  marked during its grace period (a collect there may still
  save segments), and a tracer started in it would die before
  attaching. A running tracer is asked `ready -status`: running or
  attaching is nothing to do; ended (its `-duration`,
  `disk_limit`) lingers in a container that cannot be restarted,
  so ensure reports that a new session needs a pod restart and
  leaves it. A pod without a tracer container (never deployed,
  or the pod restarted under the same name or selector) is
  deploy's, as a host's never-started project is; a `pod:`
  entry's pod that is gone, or a selector matching no running
  pod, fails the entry as collect does — nothing to restart or
  collect, deploy starts a tracer in its replacement. A sidecar
  is the kubelet's to restart: ensure only collects it. Every
  other pod of the entry is then collected as collect does.
- Target platform for the tracer: Rocky Linux 9 first (BTF
  present; ior's Rocky 9 build/Docker patterns apply), amd64
  and arm64. Deployed binaries come out of the container build
  pipeline (see build.md, "Container builds").
  Owner decision (2026-11-21): **Rocky Linux 9.9 is the main
  target**, and everything must work on its el9 kernel
  (5.14.0-741.22.1.el9_9 on the M4 test hosts), not only on
  the 7.x the suite was developed on. Kernel-version floors
  below are upstream numbers: RHEL backports BPF, and on 9.9
  `bpf_rdonly_cast`, uprobe_multi (the Java marks) and BTF are
  present (task lb4). An el9 load failure, verifier rejection
  or record-layout difference is a defect to fix behind a
  load-time probe or CO-RE, never a skip — as gf4 (verifier:
  ctx memcpy) and hf4 (12-byte `trace_entry`) were.
  Status (2026-11-21, task nf4): the root BPF suite (27 tests)
  passes on 9.9, the uprobe_multi pid filter (46ba0e49) and the
  `group_dead` fallback included; the integration suite passed
  50 of 51 on the final tree — `TestJavaLiveAttach` flakes
  there (about 1 run in 8, task tf4)
  ([testing.md](../../testing/m4-acceptance/root-suites.md#root-suites-on-rocky-99-task-nf4-2026-11-21)).
- Acceptance: decision #25's tracer-CPU item passes on the
  segment-writer path, plain and `-spool` (task ee4).
  Status (2026-11-20, task ee4): plain unfolded passes (0.85 µs
  at 1 thread, 1.62 µs at 4); `-spool` with every event joined
  missed 2 µs (2.89 / 4.82 µs). Owner decision (2026-11-20,
  delegated to an advisor): the original ≤ 2 µs covered `-spool`
  with every event joined and was not met (2.89 / 4.82 µs after
  ee4). The limit is hereby changed for that case only: ≤ 4 µs
  per emitted event and ≤ 12.5 k instructions per event, valid
  at load ≤ 10 with every event attributed and zero loss. This is
  a relaxation, argued by the extra agent records each joined
  event consumes (one core still sustains 250 k joined events/s)
  and by the default fold keeping JVM hot loops out of the join.
  Plain stays ≤ 2 µs. After one more round of join cuts, by those
  rules (load 4.4–8.7, every `-spool` run attributed with zero
  loss, medians of 5): `-spool` 1.90 µs / 11.1 k instructions at
  1 thread and 3.75 µs / 11.8 k at 4 — passes; plain 1.59 /
  1.95 µs — passes, with the plain 4-thread runs' 30–50 % ring
  loss recorded, not gated; both 4-thread CPU-time passes are
  narrow, inside the ~±20 % run-to-run noise (decision #25
  item 1; s3-overhead.md, "Follow-up (ee4): owner decision").
  The item is met. Two test
  hosts run M4's scenarios; from a
  controller, `add` → `deploy` → `refresh` → `rm` produces the
  merged graph export with zero manual steps on the servers,
  leaves nothing behind under `/opt/iormesh`, and the merged
  mesh equals the single-host result for single-host
  scenarios. Streaming collect is proven too: `collect` run
  repeatedly during a live session pulls each `ready/`
  segment over rsync and deletes it from the server, so
  between collections the server holds only unsynced segments
  plus the one being written. A deliberately low
  `-disk-limit` proves the watchdog: the tracer exits cleanly,
  the last parquet segment is intact, and the exit reason
  shows up in the summary.
  The M0 client and server run as two projects; building
  both together fuses their connection into a single cross-project
  edge. The microservices scenario spreads one service per
  host, each as its own project — a single `refresh` over the
  whole registry produces the complete service graph with
  every REST edge (plain and TLS) fused. The NAT'ed pair
  fuses into one edge although the two ends saw different
  4-tuples, and an injected clock skew (test-only flag) is
  recovered within the recorded bound. `build` rerun with the
  network down reproduces the same graph export byte for
  byte, and `collect` alone changes nothing but the local
  segment store. Naming two of the
  services via `iormeshctl services` shows them by name in
  the graph export — no manual file edits anywhere.
  Status (2026-11-21, task lb4): **accepted** — the sixth run
  on two Rocky 9.9 hosts (VMs iom1 and iom2, kernel
  5.14.0-741.22.1.el9_9, build of 35c7de7) printed every
  criterion M4-0 to M4-14 PASS. Limits of the evidence: both
  VMs share one physical host (one hardware clock, one
  physical network), and two runs passed all 15 (the sixth, and
  3f4's below); the five before the sixth each failed on defects
  since fixed (gf4–nf4, qf4, tf4).
  Owner-delegated advisor decision; the run was made by agents
  on hosts the owner provided for it. Task 3f4 re-ran the
  acceptance on a HEAD build (5eedf77, after pf4 changed the
  BPF object) on the same VMs: every criterion PASS again, so M4
  stays accepted. History: the first run passed 11 of 15;
  the microservices TLS label (kf4), one cross-host hop of the
  spread (if4), the clock bound (jf4) and the watchdog reason in
  the summary (lf4) fail on defects filed as tasks, none of them
  the kernel's. Two el9 kernel defects that stopped the tracer
  were fixed in the run (gf4, hf4). After if4, lf4 and kf4 (the
  agent jar shipped by deploy, #28), a fourth run passed 14 of
  15, M4-8 and M4-9 with their TLS labels; only the clock bound
  (jf4, fixed after that build) failed; a fifth, with jf4 and
  kf4's review fixes, passed M4-10 but lost two plain hops of the
  spread M4-9 to skipped runs (qf4: the merge keys a client from
  its `CLOSE`, the correlator splits a socket address reused after
  a lost `CLOSE`); a sixth, with qf4, printed every criterion
  PASS (build of 35c7de7). No criterion counts as
  accepted until one run passes all — the sixth did. `mage
  m4Acceptance HOST1 HOST2` (`internal/acceptance`) runs the
  whole sequence against exactly the two named, bootstrapped
  hosts and prints PASS,
  FAIL, SKIP or NOT RUN per criterion; the runbook, its
  prerequisites, the local results and the Rocky 9.9 runs are
  in
  [docs/testing.md](../../testing/m4-acceptance.md#m4-acceptance-on-two-real-hosts-task-lb4).
  On real hosts the clock criterion reports the real clocks'
  bound; the injected skew stays the rig's
  (`TestClockSkewRecovered`, task ye4). The microservices run
  covers both layouts: all four services on HOST1 (M4-8), and
  the spread (M4-9, task 1f4). In the spread, gateway and
  inventory run on HOST1 and orders and billing on HOST2, each
  as its own project, so gateway→orders (TLS) and
  orders→inventory (plain) cross hosts. The rig spreads them
  over three emulated hosts (`TestMicroservicesAcrossHosts`).
