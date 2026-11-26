# M4 acceptance on two real hosts (task lb4)

Part of [Testing strategy](../testing.md).

The rig above proves M4's criteria on network namespaces; the
acceptance itself (milestones.md M4, "Acceptance") also needs one run
on two real hosts. **Status (2026-11-21): accepted** (milestones.md
M4, an owner-delegated advisor decision): run six times on two
Rocky 9.9 VMs the owner provided; the sixth (qf4, the build of
35c7de7) printed every criterion PASS, and so did a seventh on
a HEAD build (task 3f4, the build of 5eedf77). Both VMs share
one physical host; the two kf4 runs before the sixth passed 14
of 15 each, the fourth failing the clock bound (before jf4),
the fifth the spread M4-9 (skipped runs, qf4) (see "Run on
Rocky 9.9" below). The M5 and M6 re-checks on the same VMs are
"M5 on Rocky 9.9" and "M6 on Rocky 9.9" below. A rerun at
9bca1b2 (task 3k4, 2026-11-26) passed 14 of 15: the runner's
M4-8 check missed the `@host` loopback labels (task 7m4). With
7m4's fix, a rerun at c10b0a3 (task 4k4, 2026-11-26) passed all
15: **M4 is accepted on el9** (see "Guide walk on Rocky 9.9"
below).

`mage m4Acceptance HOST1 HOST2` (`magefile_m4acceptance.go`,
`internal/acceptance`) runs the whole sequence and checks each
criterion itself. It contacts exactly the two hosts named
(any other name is refused in code), and refuses to start
without both, with the same host twice, with two names for
one machine (same `/etc/machine-id`), or with the controller
itself as a host. iormeshctl runs with
`HOME` set to a private work directory, so its registry holds
only the two hosts and the operator's own registry, store and
service database are never touched. On the servers the tracer
is started, stopped, upgraded and uninstalled only by
iormeshctl; the runner itself runs the workloads (each under
`timeout 300`, killed with `pkill` when its scenario fails),
read-only probes, one fill file, and one privileged command —
the read-only `sudo -n /opt/iormesh/bin/iormesh status -json`,
through the same sudoers rule. The operator's own sudo is never
used on a server. HOST1 gets the clients, the single-host
scenarios and the microservices; HOST2 the servers and the
watchdog project.

## Parts

Each part is its own file; open only the one you need.

- [Prerequisites and running it](m4-acceptance/setup.md) —
  prerequisites (incl. the manual bootstrap), running it, what it checks.
- [Local, el9 and agent runs](m4-acceptance/first-runs.md) — local
  verification, the Rocky 9.9 run, the run with the agent (kf4).
- [Root suites on Rocky 9.9 (task nf4,
  2026-11-21)](m4-acceptance/root-suites.md) — the root suites on el9
  (nf4).
- [M5 and M6 on Rocky 9.9](m4-acceptance/m5-m6.md) — M5 and M6 on el9
  (3f4), M6 on three hosts (wg4).
- [Reruns and the guide walk](m4-acceptance/reruns.md) — M4/M6 rerun at
  9bca1b2 (3k4), the guide walk (4k4).
