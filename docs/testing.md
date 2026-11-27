# Testing strategy

How iormesh is tested. The milestones whose acceptance it records
are in [milestones.md](milestones.md), the decisions it cites in
[decisions.md](decisions.md).

The test workload comes *first*, not last — and it comes first
per milestone:

- The workload framework (M0) lands before any tracer code. Every
  later milestone adds its own scenarios before its tracer code,
  so each one lands against a known target. milestones.md's scenario
  catalogue says which scenarios prove which milestone.
- A walking skeleton (M1) runs the whole chain once — probes,
  correlator, schema, normalizer, golden check, harness — on the
  smallest scenario, before the traced surface widens.
- The risky mechanisms are proven by spikes before milestones
  depend on them: the Java↔syscall join, the cross-host join,
  and the overhead budget.
- The app has client and server modes — and a microservices flavor
  where several service roles talk REST over plain HTTP and TLS —
  so cross-host fusion and mesh expansion can be rehearsed on
  loopback, between two test VMs/containers or across test hosts,
  without going near anything live. One scenario talks to a
  non-Java peer, so the degraded node shape is tested too.
- Only once a milestone passes against the workload is it considered
  ready for a live process.

## Parts

This file is the index: each part below is its own file. Read the summaries,
then open only the part you need.

- [Golden checks, race gate and CI](testing/golden-and-ci.md) — what a golden
  check asserts, beyond single-host, the race gate, the docs size gate,
  CI.
  - [What a golden check
    asserts](testing/golden-and-ci.md#what-a-golden-check-asserts)
  - [Beyond single-host](testing/golden-and-ci.md#beyond-single-host)
  - [Race detector gate (task
    ij4)](testing/golden-and-ci.md#race-detector-gate-task-ij4)
  - [Docs size gate (task
    sm4)](testing/golden-and-ci.md#docs-size-gate-task-sm4)
  - [CI](testing/golden-and-ci.md#ci)
- [Integration tests follow ior](testing/integration.md) — the integration
  suite, and the rig runs for M2–M6.
  - [The suite today](testing/integration/suite.md) — what the integration
    suite covers; M2 acceptance.
  - [M3 scenarios and live attach](testing/integration/m3.md) — M3 scenarios
    ahead of the agent; live attach (ge4).
  - [M4 on the rig](testing/integration/m4.md) — the microservices scenario
    (jb4), two-host and NAT'ed pairs (kb4).
  - [M5 and M6 on the rig](testing/integration/m5-m6.md) — M6 (tb4) and M5
    (rb4) on the rig.
- [M4 acceptance on two real hosts (task lb4)](testing/m4-acceptance.md) — the
  two-host acceptance (lb4): setup, runs on Rocky 9.9, reruns.
  - [Prerequisites and running it](testing/m4-acceptance/setup.md) —
    prerequisites (incl. the manual bootstrap), running it, what it checks.
  - [Local, el9 and agent runs](testing/m4-acceptance/first-runs.md) — local
    verification, the Rocky 9.9 run, the run with the agent (kf4).
  - [Root suites on Rocky 9.9 (task nf4,
    2026-11-21)](testing/m4-acceptance/root-suites.md) — the root suites on el9
    (nf4).
  - [M5 and M6 on Rocky 9.9](testing/m4-acceptance/m5-m6.md) — M5 and M6 on el9
    (3f4), M6 on three hosts (wg4).
  - [Reruns and the guide walk](testing/m4-acceptance/reruns.md) — M4/M6 rerun
    at 9bca1b2 (3k4), the guide walk (4k4).
- [Socket edge cases](testing/sockets.md) — msghdr calls, MSG_PEEK, datagram
  peers, the final socket snapshot, connections open before the session.
  - [msghdr data calls (task oh4,
    2026-11-22)](testing/sockets.md#msghdr-data-calls-task-oh4-2026-11-22)
  - [MSG_PEEK receives (task ok4,
    2026-11-24)](testing/sockets.md#msg_peek-receives-task-ok4-2026-11-24)
  - [The msghdr calls' datagram peer (task nk4,
    2026-11-24)](testing/sockets.md#the-msghdr-calls-datagram-peer-task-nk4-2026-11-24)
  - [Final socket snapshot (task sf4,
    2026-11-21)](testing/sockets.md#final-socket-snapshot-task-sf4-2026-11-21)
  - [Connections open before the session (task cm4,
    2026-11-26)](testing/sockets.md#connections-open-before-the-session-task-cm4-2026-11-26)
- [arm64, the clang pin and releases](testing/arm64-and-release.md) — arm64 on
  an emulated el9 kernel, the arm64 root gate, the builder clang pin, the
  release dry run, the reproducible agent jar.
  - [arm64 on an emulated el9 kernel (task 8k4,
    2026-11-24)](testing/arm64-and-release.md#arm64-on-an-emulated-el9-kernel-task-8k4-2026-11-24)
  - [The root BPF gate on arm64 (task jl4,
    2026-11-24)](testing/arm64-and-release.md#the-root-bpf-gate-on-arm64-task-jl4-2026-11-24)
  - [Builder clang pin (task ll4,
    2026-11-24)](testing/arm64-and-release.md#builder-clang-pin-task-ll4-2026-11-24)
  - [Release dry run v0.1.0 (task 7k4,
    2026-11-26)](testing/arm64-and-release.md#release-dry-run-v010-task-7k4-2026-11-26)
  - [Reproducible agent jar (task bm4,
    2026-11-26)](testing/arm64-and-release.md#reproducible-agent-jar-task-bm4-2026-11-26)
- [el9 runbook and record layout](testing/el9.md) — the el9 runbook and runs,
  the el9 record layout check (of4).
  - [el9 runbook and runs](testing/el9.md#el9-runbook-and-runs)
  - [el9 record layout (task of4)](testing/el9.md#el9-record-layout-task-of4)
- [Focused checks and baselines](testing/focused-checks.md) — comm renames, CLI
  help, the local site, the LLM introspection eval, performance baselines,
  independent parquet readers.
  - [Comm renames (task 0g4)](testing/focused-checks.md#comm-renames-task-0g4)
  - [Command-line help (task
    vh4)](testing/focused-checks.md#command-line-help-task-vh4)
  - [Local site (task nb4)](testing/focused-checks.md#local-site-task-nb4)
  - [LLM introspection eval (task
    qb4)](testing/focused-checks.md#llm-introspection-eval-task-qb4)
    - [First runs with real models (task 9k4,
      2026-11-26)](testing/focused-checks.md#first-runs-with-real-models-task-9k4-2026-11-26)
  - [Performance baselines](testing/focused-checks.md#performance-baselines)
  - [Independent parquet readers (task
    ee4)](testing/focused-checks.md#independent-parquet-readers-task-ee4)
- [Kubernetes: capabilities, PID namespaces, transport](testing/k8s-tracer.md)
  — the k8s capability set (ub4), PID namespaces (4f4), the k8s transport
  (wb4).
  - [K8s capability set (task
    ub4)](testing/k8s-tracer.md#k8s-capability-set-task-ub4)
  - [PID namespaces (task 4f4)](testing/k8s-tracer.md#pid-namespaces-task-4f4)
  - [K8s transport (task wb4)](testing/k8s-tracer.md#k8s-transport-task-wb4)
- [Kubernetes: controller, manifests, route map](testing/k8s-controller.md) —
  controller wiring (af4), manifests and pod identity (vb4), route map and
  ClusterIP join (xb4).
  - [K8s controller wiring (task
    af4)](testing/k8s-controller.md#k8s-controller-wiring-task-af4)
  - [K8s manifests and pod identity (task
    vb4)](testing/k8s-controller.md#k8s-manifests-and-pod-identity-task-vb4)
  - [Route map and ClusterIP join (task
    xb4)](testing/k8s-controller.md#route-map-and-clusterip-join-task-xb4)
- [M7 acceptance on kind (task yb4)](testing/m7-acceptance.md) — the kind
  acceptance (yb4), pods, an existing cluster on el9, SELinux reruns.
  - [Microservices as pods (task uh4)](testing/m7-acceptance/pods.md) — the
    microservices scenario as pods (uh4).
  - [M7 on an existing cluster](testing/m7-acceptance/existing-cluster.md) — an
    existing cluster (zg4), the Rocky 9.9 run, the load refusal hint (6h4).
  - [SELinux reruns](testing/m7-acceptance/selinux.md) — spc_t (5h4), app
    container restart (8h4), the iormesh_container_t type (7h4).
  - [kind reruns](testing/m7-acceptance/kind-reruns.md) — debug tracer
    replacement (ah4), rerun with pod fields (6k4).
- [The M8 agent trial harness](testing/agent-trial.md) — running
  `mage agentTrial`, the sandbox, the scripted user, preparation, budget
  and score, the tests (mm4), the first trial's four cells (nm4), the
  rerun (8n4).
  - [Running it (task mm4)](testing/agent-trial.md#running-it-task-mm4)
  - [The sandbox](testing/agent-trial.md#the-sandbox)
  - [The scripted user](testing/agent-trial.md#the-scripted-user)
  - [Preparation, budget and
    score](testing/agent-trial.md#preparation-budget-and-score)
  - [Tests](testing/agent-trial.md#tests)
  - [The first trial (task nm4,
    2026-11-26)](testing/agent-trial.md#the-first-trial-task-nm4-2026-11-26)
  - [The rerun (task 8n4,
    2026-11-27)](testing/agent-trial.md#the-rerun-task-8n4-2026-11-27)
