# Local, el9 and agent runs

Part of [M4 acceptance on two real hosts (task lb4)](../m4-acceptance.md),
in [Testing strategy](../../testing.md).

## Local verification (2026-11-20)

This machine has no second host and no sshd, so the target has
not run against real hosts. What ran here:

- `internal/acceptance`'s unit tests (`mage test`):
  `TestDryRun` drives every phase against fake hosts and a fake
  iormeshctl — each criterion the fakes satisfy passes, the
  single-host golden fails on the fakes' empty trace (the check
  is not vacuous), the microservices spread (M4-9, task 1f4)
  passes with orders' certificate copied to HOST1, and the
  verdict is "not accepted"; `TestDryRunSpreadNoCertificate`
  fails M4-9 when the certificate cannot be read and never runs
  the gateway; `TestDryRunTouchesOnlyTheTwoHosts` finds every
  remote command on the two hosts and no tracer lifecycle
  command outside iormeshctl; `TestPreflightRefusesBusyHost`
  stops at a host with someone else's traces before iormeshctl
  runs; the host gate, the config's refusals and the rsync
  version check have their own tests. Added in the review
  round: `TestInterruptStillTearsDown` (a ^C during the
  streaming rounds still runs `rm` on both hosts, `pkill`s the
  roles and removes the run directories),
  `TestInterruptOutlivesMageGrace` and
  `TestSecondInterruptKills` (a child process playing mage's
  generated main, its 5-second grace shortened: a teardown
  longer than the grace still finishes after one SIGINT, and a
  second SIGINT exits at once with 130),
  `TestFailedClientStopsServer` (a failed two-host client gets
  its server killed and M4-6 reports the workload's failure;
  the fakes also fail any role started without `timeout 300`),
  `TestPreflightRefusesController` and `TestNewExecCtlEnv`.
  Task kf4 added `TestDryRunMicroservicesWithAgent` (exactly the
  eight microservices projects are added with their spool and
  the installed jar, every microservices JVM starts with that
  jar as `-javaagent` into its own project's spool, no other one
  does, M4-8 and M4-9 pass) and
  `TestPreflightRefusesMissingAgentJar`; the deploy and upgrade
  halves are unit-tested in `internal/ctl`
  (`TestDeployShipsAgent`, `TestDeployRefusesBadAgent` — a
  tampered, unsigned, foreign-signed, renamed or other-version
  jar pushes nothing — `TestDeployAgentUpgradeRefused`) and
  `internal/cli` (`TestUpgradeInstallsAgentJar`,
  `TestUpgradeRefusesAgentJar`, `TestUpgradeRefusesAgentLayout`,
  the confined `-agent` in `TestRunConfinement`); 21 mutants of
  the new checks were each killed by a test. The review round
  added `TestStartForwardsResolvedAgent` (a symlink to the jar
  passes, the unit argv carries the resolved path, never the
  symlink), `TestStartForwardsCleanSpool`,
  `TestStartRefusesOtherAgent`, `TestConfinementSymlinkedAgent`,
  `TestUnitRunIsConfined`, `TestUpgradeTracerDropsAgent` and the
  0644 check; 7 more mutants, each killed.
- `mage m4Acceptance m4-host1.invalid m4-host2.invalid` (real
  SSH transport, a throwaway key and dist): the preflight fails
  on the first `ssh … pwd`, every other criterion is NOT RUN,
  nothing is added or deployed, exit status 1.
- `mage test`: every package passes (30). `mage build`, `vet`,
  `lint` (0 issues) and `fmtCheck` pass.
- `mage integrationTest` as root: all 47 tests pass, no skip,
  the rig's `TestTwoHostsTwoProjects`, `TestNATPair`,
  `TestClockSkewRecovered`, `TestClockSkewBeyondWindow`,
  `TestMicroservices`, `TestParquetSegments` and
  `TestSystemdUnit` included. A first run under load (≈ 5,
  other workers' suites beside it) failed `TestJavaLiveAttach`
  once on an attribution (`ambiguous 12`); the rerun passed,
  and this task changes nothing that test runs.


## Run on Rocky 9.9 (2026-11-21)

Two test VMs on the owner's homelab, `iom1` (10.20.0.140, HOST1)
and `iom2` (10.20.0.141, HOST2): Rocky Linux 9.9 minimal, 2 vCPUs,
2 GiB, kernel 5.14.0-741.22.1.el9_9.x86_64 with BTF, JDK 25.0.5,
rsync 3.2.7 (the controller's is 3.5.1). Bootstrapped as above
with `ops` = the SSH user and a test release key; ports 18202,
18203 and 18216 opened runtime-only on HOST2 and 18215 on
HOST1; the DNAT 18880 → 18203 on HOST2;
`IORMESH_M4_ADDR1/2` and `IORMESH_M4_NAT_PORT=18880` set.

The el9 kernel has every BPF feature the floors above ask of
6.2–6.10: `bpftool feature probe` and the BTF list
`bpf_rdonly_cast`, `bpf_cast_to_kern_ctx`, uprobe_multi and
kprobe_multi links, ringbuf, `bpf_get_current_task_btf`,
`bpf_ktime_get_boot_ns`; the 704 `syscalls` tracepoints and
`raw_syscalls` are there. Two el9 defects stopped the tracer and
were fixed in this task (each loaded per program on el9 — all 744
— and checked on 7.2 with `mage test`, `mage bpfRootTest` and
`mage integrationTest`, 51 passed, none skipped):

- **gf4** — the verifier refused `handle_inet_sock_set_state`
  ("dereference of modified ctx ptr R3 off=48"): clang lowered a
  16-byte memcpy from the tracepoint context to loads off an
  advanced ctx pointer. The addresses are now read with
  `bpf_probe_read_kernel`. It was the only program el9 refused.
- **hf4** — el9's `trace_entry` is 12 bytes (the rt-merge
  `preempt_lazy_count`), so `raw_syscalls` carries `id` at 16
  and `args`/`ret` at 24 while the per-syscall records keep
  `args` at 16. The dispatchers read `id` at a fixed 8 (nearly
  every syscall went to the read handler) and the handlers read
  `args`/`ret` through the per-syscall struct. Both now read the
  raw record through CO-RE flavors (dispatch.c, generator's
  `handlerCtxStruct`).

Beyond M4's run, on el9: the Java agent live-loaded into a traced
JVM and the uprobe_multi Java marks attached ("37 natives
probed", brackets and attributed calls), and `tcp_seq` is read
on both ends of a connection.

| Id | Run 1 (before gf4/hf4 were in) | Run 2 (after) |
|---|---|---|
| M4-0 | PASS | PASS |
| M4-1 | PASS | PASS |
| M4-2 | PASS | PASS (3 rounds, 20 files) |
| M4-3 | PASS | PASS |
| M4-4 | PASS | PASS |
| M4-5 | FAIL (no begin marker: syscalls lost, hf4) | PASS (1428 events) |
| M4-6 | FAIL (0 fused, hf4) | PASS |
| M4-7 | FAIL (hf4) | PASS (client :18880, server :18203) |
| M4-8 | FAIL (hf4) | FAIL: no TLS label — task kf4 |
| M4-9 | FAIL (hf4) | FAIL: 1 of 2 hops unresolved — task if4 |
| M4-10 | FAIL (hf4) | FAIL: one-sided bound — task jf4 |
| M4-11 | FAIL (no earlier build) | PASS (`unshare -rn`) |
| M4-12 | FAIL (hf4) | PASS |
| M4-13 | FAIL | FAIL: summary lacks the reason — task lf4 |
| M4-14 | PASS | PASS (15 sessions collected) |

Run 1 had the tracer fix gf4 but not hf4, so the traced
processes recorded little more than exec and exit. The four
run-2 failures are not the kernel's:

- **M4-8 (kf4)**: the TLS label comes only from the Java
  agent's http records, and the runner deploys the services
  without `-spool`/`-agent` (deploy has no way to ship the agent
  jar). The rig's `TestMicroservices` loads the agent. Fixed in
  kf4: deploy ships the signed jar to `/opt/iormesh/lib`
  (decisions.md #28, provisional), the microservices projects carry
  `-spool`/`-agent`, and their JVMs start with the installed jar
  as `-javaagent` (see "Run with the agent (kf4)" below).
- **M4-9 (if4)**: a skipped `inet_sock_set_state` run (a
  `skipped_runs` drop, many tracers on a 2-vCPU host) missed
  inventory's final CLOSE, its `sock_owner_map` entry stayed,
  and the reused `struct sock` of another process's socket was
  recorded under inventory's fd and conn id — whose half then
  had a connect and an accept and was dropped as a loopback
  pair. Fixed in if4: every state record now checks the entry
  against the socket's current life (a new-life transition, or
  the socket file's inode) and drops a stale one
  (`internal/sockreuse_root_test.go` reproduces the reuse, on
  7.2 and on el9). A third run with if4 fused orders→inventory
  (and M4-13 passed with lf4), but M4-9 still failed: a skipped
  run lost the gateway's `SYN_SENT → ESTABLISHED`, and the merge
  could not pair gateway→orders from its `CLOSE` alone — task
  qf4.
- **M4-10 (jf4)**: `tcp_seq` pairs exist in both directions,
  but any drop on a host — every pattern session has an
  `attach_gap` at the JVM's exec — disables the data messages
  of all its connections, leaving only the handshake.
- **M4-13 (lf4)**: the watchdog worked (`end_reason`
  `disk_limit`, `show health` prints it), but the plain
  `iormeshctl show` summary never named a recorded end reason:
  it listed only sessions with no end. Fixed in lf4 — the
  summary now names every session that ended abnormally
  (`ended disk_limit`, `ended error`), and the runner checks
  for `: ended disk_limit`.


## Run with the agent (kf4, 2026-11-21)

A fourth run, the build of 1080cfd (if4 and lf4 in, jf4 not
yet), both VMs re-bootstrapped with it, work directory
`~/.iormesh-m4-kf4-1`: deploy verified and installed
`dist/iormesh-agent.jar` on both hosts (the installed
`/opt/iormesh/lib/iormesh-agent.jar` byte-identical to the
signed one, root's 0644 in a root 0755 `lib/`), the
microservices' JVMs joined ("java agent of pid N joined, 37
natives probed"), and **14 of 15 criteria passed**:

- M4-8 PASS — `127.0.0.1:18213 tls x1, 127.0.0.1:18211 tls x1,
  127.0.0.1:18212 plain x2`, all fused;
- M4-9 PASS — the two cross-host hops fused across iom1 and
  iom2; `10.20.0.141:18216 tls x1, 10.20.0.141:18214 tls x1,
  10.20.0.140:18215 plain x2` (the qf4 loss did not recur in
  this run; qf4 stays open);
- M4-10 FAIL — `iom1~iom2: 1 windows (1 one-sided, 0 empty)`,
  jf4's defect, whose fix landed after this build;
- every other criterion PASS, M4-14 with 15 sessions collected
  and nothing left under `/opt/iormesh`.

The ports and the DNAT were removed after the run and both VMs
bootstrapped again with the same build.

A fifth run after kf4's review round (the build of 22c3ad9, jf4
in; `~/.iormesh-m4-kf4-2`) checked the hardened start on el9: the
units ran with `IORMESH_CONFINED_UNIT=1` and `-agent` forwarded as
`/opt/iormesh/lib/iormesh-agent.jar`, every confined unit run
started, and the agents joined. 14 of 15 passed — M4-8 (TLS
labels as above) and now M4-10 (`width 646µs`, two-sided) among
them. M4-9 failed: gateway→orders (TLS) and orders→billing (TLS)
fused, but orders→inventory and gateway→inventory stayed
unresolved on both ends (4 `skipped_runs` in the spread's
sessions) — the lost-transition class of if4/qf4, not the agent.
Ports, DNAT and bootstrap as after the fourth run.

Task qf4 fixed both spread losses on the controller and replayed
every stored run through the merge (no VM needed; the el9 rerun
waits for the VMs):

- **Third run** (`~/.iormesh-m4-if4-20261121123004`): the gateway
  half `3c19fcadb1b49857` had only `SYN_SENT` and `CLOSE`. The
  merge now keys a client from a `CLOSE` whose `bytes_acked`
  counts its SYN (implementation.md M4, "The merge"); replayed,
  that el9 `CLOSE` rebuilds orders' key, and the spread fuses
  4 of 4 with none unresolved (before: 3 fused, 2 unresolved).
  `TestSockSeqAbortClose` checks the `CLOSE` rebuild on 7.2 after
  RST aborts (client, client with data queued, server);
  `TestSockSeqFields` covers the FIN close.
- **Fifth run** (`~/.iormesh-m4-kf4-2`): all three unresolved
  halves had their keyed records. Inventory's server conn
  `b727ffb216e83ea3` held two connections: orders→inventory,
  whose final `CLOSE` a skipped run lost, then gateway→inventory,
  accepted at the same `struct sock` address, which the
  correlator kept under the old socket — a `key_conflict`, and
  both clients `no_match`. The correlator now starts a new socket
  when a record's tuple contradicts the socket's, both complete
  (`sockTable.observe`, `TestLostCloseThenAddressReuse`;
  `TestSockDualStackBoundConnect` keeps a dual-stack bound
  connect on one conn). The
  stored trace is already correlated, so only a rerun shows it.

The other stored runs replay unchanged.

A sixth run (qf4, the build of 35c7de7: both fixes plus the
review's — an unspecified address never contradicts a tuple, a
transition out of `CLOSE` or `LISTEN` starts a socket, a stale
record names no process; both VMs bootstrapped again with it,
work directory `~/.iormesh-m4-qf4-1`) **passed all 15
criteria**: M4-8 `127.0.0.1:18213 tls x1, 127.0.0.1:18211 tls
x1, 127.0.0.1:18212 plain x2`; M4-9 two conns fused across iom1
and iom2, `10.20.0.141:18216 tls x1, 10.20.0.141:18214 tls x1,
10.20.0.140:18215 plain x2`; M4-10 one window, two-sided, width
667µs; M4-2 3 rounds and 40 files; M4-14 15 sessions collected
and nothing under `/opt/iormesh`. Before it, on `iom1`,
`TestSockSeqAbortClose` (the three RST aborts), the new
`TestSockDualStackBoundConnect`, `TestSockSeqFields` and
`TestSockRefusedConnectNoSeq` passed as root on el9. Neither
kernel reported the unspecified address at `SYN_SENT` for that
loopback connect; the unit test covers the sequence. Ports and
DNAT were removed after the run, and both VMs bootstrapped again
with the same build.

Also found on el9, outside M4's sequence: a session dies with
"schema: invalid: fd_gen 1 without an fd" when the pattern also
matches a `su` that runs the JVM (task mf4).

A seventh run (task 3f4, the build of 5eedf77: pf4's BPF object
changes and uf4's stamped, signed artifacts; both VMs
bootstrapped again with it as root, work directory
`~/.iormesh-m4-3f4-1`) **passed all 15 criteria** on el9: M4-2
3 rounds and 44 files; M4-5 the golden on 1358 events; M4-8
`127.0.0.1:18213 tls x1, 127.0.0.1:18211 tls x1,
127.0.0.1:18212 plain x2`; M4-9 two conns fused across iom1
and iom2, `10.20.0.141:18216 tls x1, 10.20.0.141:18214 tls x1,
10.20.0.140:18215 plain x2`; M4-10 one window, two-sided, width
749µs; M4-14 15 sessions collected and nothing under
`/opt/iormesh`. Ports and DNAT as before, removed after the M6
rounds below.

Both VMs were bootstrapped again after the kf4 run with the build
of 1080cfd (the run's `rm` removes `/opt/iormesh`; the agent jar
needs no bootstrap, deploy installs it), so a rerun with that
build needs only the firewall ports, the DNAT and an empty work
directory; a rerun with a newer build of the same version needs
the tracer bootstrapped again, since `upgrade` through the rule
refuses other bytes under one version (run 2's work directory is
`~/.iormesh-m4`, run 1's `~/.iormesh-m4-run1-20261121`, kf4's
`~/.iormesh-m4-kf4-1` on the controller).
