# Changelog

All notable changes to iormesh are recorded here, in the style of
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions
follow `internal/version.Version`. [docs/milestones.md](docs/milestones.md)
and [docs/decisions.md](docs/decisions.md) hold the milestones and
decisions; this file only summarises.

## [Unreleased]

Nothing has been released yet. What exists: the milestones first,
then the features and changes of the latest round.

### Added

- M0: the traceable Java test workload and its scenarios.
- M1, M2: the eBPF tracer (`iormesh`): trace a PID or an exec
  pattern, socket and file I/O fused into edges, parquet trace
  segments.
- M3: the Java agent, joining JDK I/O to syscalls with code-flow
  stacks.
- M4: the controller (`iormeshctl`): deploy, collect and merge
  from servers over SSH, signed builds (minisign).
- M5: views, query and the LLM eval (accepted 2026-11-26: Claude
  Sonnet 5.5 1.00, GLM 5.3 0.90 against the pinned 0.8).
- M6: mesh expansion, following edges from the controller.
- M7: Kubernetes sidecar and debug-container deployment.
- `mage release`: reproducible per-arch archives with checksums,
  unsigned (docs/build.md, "Releases"; decisions.md #32).
- `iormeshctl sign -key KEY DIR`: operators sign an unpacked
  release with their own minisign key, the one deploy and
  bootstrap verify against.
- The MIT licence (`LICENSE`).
- The iormesh guide ([docs/guide/](docs/guide/README.md)):
  thirteen step-by-step chapters, for a human or an agent working
  with one; the quickstart is now a pointer into it.
- `iormeshctl bootstrap`: the one-time, verified tracer install
  ([guide, chapter 4](docs/guide/04-register-bootstrap.md),
  [security.md](docs/security.md)).
- `iormeshctl ensure`: restarts ended or dead sessions and
  collects ([architecture.md](docs/architecture.md)).
- `iormeshctl status` (sessions per host) and `list` (registry)
  ([guide, chapter 5](docs/guide/05-deploy-status.md)).
- `iormeshctl prune`: lists old segment files, deletes them with
  `-apply` ([guide, chapter 9](docs/guide/09-tracing-for-days.md)).
- `build -from`/`-to`/`-last` (windowed builds) and `-namespace`
  ([visualization.md](docs/visualization.md)).
- `open`, `show` and `tui` read namespace and windowed builds with
  `-namespace` and `-windowed`
  ([visualization.md](docs/visualization.md)).
- `iormeshctl query` filters, writing merged-trace slices
  ([schema.md](docs/schema.md), "Query slices").
- `query -window-segments`: a time window skips the segments
  outside it, trading exactness for speed
  ([implementation.md](docs/implementation.md), M5 "Query").
- Dated times of day (`2025-11-21T00:54:20`, UTC) in `build` and
  `query` windows; `query` echoes its resolved window on stderr
  ([troubleshooting.md](docs/troubleshooting.md)).
- URI path templates (`/orders/{id}`) in the service database's
  per-path counts ([output-contract.md](docs/output-contract.md)).
- `-listen-port` sessions follow the port to a restarted owner
  within `-listen-grace` ([trace-scope.md](docs/trace-scope.md)).
- Late Java attach: a JVM started after the session gets the
  agent ([java-attach.md](docs/java-attach.md)).
- `sendmsg`, `recvmsg`, `sendmmsg` and `recvmmsg` tracing
  ([schema.md](docs/schema.md)).
- Pod, namespace and container id on every event of an in-pod
  tracer ([schema.md](docs/schema.md), "Pod fields on events").
- The microservices-as-pods acceptance on kind
  ([testing.md](docs/testing.md)).
- Quickstart and troubleshooting guides
  ([quickstart.md](docs/quickstart.md),
  [troubleshooting.md](docs/troubleshooting.md)) and the
  Kubernetes guide ([kubernetes.md](docs/kubernetes.md)).
- GitHub Actions CI runs the unprivileged mage gates
  ([build.md](docs/build.md), "CI").
- Mage finds libbpfgo from a linked git worktree
  ([build.md](docs/build.md)).

### Changed

- `mage sign` refuses a secret key that is symlinked,
  someone else's, or open to group or other (`chmod 600`;
  decisions.md #32).
- A bare time of day in `build` or `query` is refused when the
  store's segments start on more than one UTC date, so a prune can
  no longer shift it to another day
  ([troubleshooting.md](docs/troubleshooting.md)).
- `iormesh stop` records `operator_stop`, and `ensure` leaves such
  a session stopped ([architecture.md](docs/architecture.md)).
- `iormeshctl status` says in its NOTE column which stopped
  sessions `ensure` leaves alone
  ([architecture.md](docs/architecture.md)).
- A `-listen-port` that only systemd holds (socket activation)
  waits within `-listen-grace` for the activated service instead
  of failing the start ([trace-scope.md](docs/trace-scope.md)).
- Port follow adopts an upgraded executable path in the same
  cgroup ([trace-scope.md](docs/trace-scope.md)).
- `iormesh start` appends the journal's tail when a run exits
  before attaching ([troubleshooting.md](docs/troubleshooting.md)).
- `prune -apply` makes the service database forget the pruned
  segments ([guide, chapter 9](docs/guide/09-tracing-for-days.md)).
- Build outputs go to the gitignored `bin/`, never the repository
  root ([build.md](docs/build.md)).
- The mage targets live in `magefiles/`, and `mage clean` sweeps
  the root build artifacts too ([build.md](docs/build.md)).
- `mage test` builds the jars and binaries its tests need
  ([build.md](docs/build.md)).

### Fixed

- Unix-socket accepts of a reused socket no longer fuse into one
  connection ([schema.md](docs/schema.md)).
- Start-time and late attach never send one JVM a second SIGQUIT
  ([java-attach.md](docs/java-attach.md)).
- The agent jar rebuilds to the same bytes from the same commit
  and JDK, so a release rebuilt from its tag matches the
  published archive ([build.md](docs/build.md#releases)).
