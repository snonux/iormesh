# Milestones

The milestones iormesh was built in, in execution order: what each
one set out to land, how it was built, and what accepted it. When
this record was written (2026-11-24), M0, M1, M2, M3, M4, M6 and M7
were accepted; M5 was accepted on 2026-11-26, when its LLM eval
passed with real models (task 9k4). M8, added on 2026-11-26
(decision #37), is not accepted: its first trial (task nm4) and
its rerun (task 8n4) each passed two of four cells. Rocky Linux 9.9 (el9, kernel 5.14)
is the main target.

Decision numbers (`#N`) refer to [decisions.md](decisions.md);
what is traced and the trace format are in
[trace-scope.md](trace-scope.md) and
[output-contract.md](output-contract.md). This file and
decisions.md replaced the project plan in task tk4 (2026-11-24);
the text below is the plan's milestone section as it stood, with
only its headings and cross-references adapted.

Milestones are numbered in execution order. (Renumbered on
2026-11-17 — for readers of older commits: old M1 is now M1 + M2,
old M2 is M3, old M6 is M4, old M3 + M4 merged into M6; M0, M5
and M7 kept their numbers.)

| Milestone | What lands                                  | Gated by |
|-----------|---------------------------------------------|----------|
| M0        | Test workload framework                     | —        |
| M1        | Walking skeleton: one fused edge end to end | —        |
| M2        | Trace a PID: the full surface               | S3       |
| M3        | Java agent: code-flow attribution           | S1       |
| M4        | Controller: push, collect, merge            | S2       |
| M5        | Views, query and the LLM eval               | —        |
| M6        | Mesh expansion, controller-driven           | —        |
| M7        | Kubernetes                                  | —        |
| M8        | A smaller agent installs and uses iormesh   | —        |

Sequencing relaxation (2026-11-20, owner-delegated advisor
decision): M4's real-host run (task lb4) gates acceptance, not the
start of M5–M7. Their work proceeds on the simulated rig and kind;
a status may read "passes on the rig", never "accepted", until M4
is accepted and the real-host re-check (task 3f4) has run. A
real-host finding takes priority over any M5–M7 work. Decisions
settled meanwhile are provisional.

How to build each of these — files, order of work, code
sketches, tests — is in the
[implementation guide](implementation.md). The milestones
and [decisions](decisions.md) decide; the guide shows how.

## Parts

This file is the index: each part below is its own file. Read the summaries,
then open only the part you need.

- [Spikes — feasibility before commitment](milestones/spikes.md) — S1–S3 and
  their acceptance.
- [M0 — Traceable test workload (Java): framework first](milestones/m0.md) —
  the workload framework and the scenario catalogue.
- [M1 — Walking skeleton: one fused edge, end to end](milestones/m1.md) —
  probes → correlator → NDJSON → golden check on the smallest scenario.
- [M2 — Trace a PID: the full surface](milestones/m2.md) — files, processes,
  IPC, the target spec, redaction, native stacks, the budget.
- [M3 — Java agent: code-flow attribution](milestones/m3.md) — the agent, the
  Java↔syscall join, HTTP and OTEL capture, live attach.
- [M4 — Controller (`iormeshctl`): push, collect, merge
  locally](milestones/m4.md) — registry, deploy, the sudoers rule, collect,
  build, clock reconciliation, service database.
  - [M4 hosts, deploy and the sudoers rule](milestones/m4/hosts-deploy.md) —
    CLI conventions, registry, deploy, the systemd unit, the sudoers rule.
  - [M4 trace files, disk watchdog and
    collect](milestones/m4/trace-files-collect.md) — trace files, the watchdog,
    the three commands, collect.
  - [M4 build and clock reconciliation](milestones/m4/build-clocks.md) — the
    offline merge, clock reconciliation from the traffic.
  - [M4 service database, cleanup, platform and
    acceptance](milestones/m4/service-db-acceptance.md) — the service database,
    rm, no management plane, target platform, acceptance.
- [M5 — Views, query and the LLM eval](milestones/m5.md) — views, query, the
  eval and their acceptance.
- [M6 — Mesh expansion: follow the edges, from the
  controller](milestones/m6.md) — controller-driven expansion: allowlist, hop
  limit, enrolment.
- [M7 — Kubernetes: sidecar & debug-container deployment](milestones/m7.md) —
  debug containers and sidecars, the privilege profile, k8s transport, route
  map.
- [M8 — A smaller agent installs and uses iormesh](milestones/m8.md) — the
  iormesh skill, the trial harness on the iom VMs, and the four-cell
  acceptance (not accepted: two of four cells passed, nm4 and 8n4).
