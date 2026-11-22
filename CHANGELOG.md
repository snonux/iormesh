# Changelog

All notable changes to iormesh are recorded here, in the style of
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions
follow `internal/version.Version`. [PLAN.md](PLAN.md) holds the
scope, milestones and decisions; this file only summarises.

## [Unreleased]

Nothing has been released yet. What exists, by milestone:

### Added

- M0: the traceable Java test workload and its scenarios.
- M1, M2: the eBPF tracer (`iormesh`): trace a PID or an exec
  pattern, socket and file I/O fused into edges, parquet trace
  segments.
- M3: the Java agent, joining JDK I/O to syscalls with code-flow
  stacks.
- M4: the controller (`iormeshctl`): deploy, collect and merge
  from servers over SSH, signed builds (minisign).
- M5 (built, not yet accepted): views, query and the LLM eval.
- M6: mesh expansion, following edges from the controller.
- M7: Kubernetes sidecar and debug-container deployment.
- `mage release`: signed, reproducible per-arch archives with
  checksums (docs/build.md, "Releases").
- The MIT licence (`LICENSE`).
