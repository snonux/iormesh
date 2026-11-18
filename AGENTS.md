# AGENTS.md — conventions for agents working in this repo

iormesh is early in implementation: M0 (the Java test workload
under `workload/`) and M1's build and generator lifted from ior
(`internal/`, the `magefile_*.go` targets) are the first code;
everything else is still plan. [PLAN.md](PLAN.md) is the
working plan and the source of truth for scope, spikes (S1–S3),
milestones (M0–M7), and decisions. `ior` (`../ior`) is the
reference project for BPF/C code generation and integration
tests — consult it before inventing patterns.
Don't scaffold implementation ahead of the milestones — the
plan moves first.

Picking up a spike or milestone? Read its section in
[docs/implementation.md](docs/implementation.md) — files, order
of work, code sketches, tests — after PLAN.md. If a sketch there
disagrees with PLAN.md, the plan is right; fix the guide in the
same commit.

## Build and test

Run everything through Mage — `mage generate`, `mage build`,
`mage test`, `mage vet`, `mage lint`, `mage fmtCheck`,
`mage workloadTest`. Bare `go test`/`go vet` cannot build the
packages that import libbpfgo. Prerequisites (libbpfgo at
`../libbpfgo`, pinned tag, clang, bpftool, `sudo -n`) and the
generation-host rule are in [docs/build.md](docs/build.md);
ior's AGENTS.md ("Build/Test Commands", "Code Generation") is
the longer story. Files copied from ior carry a provenance
header (`// Adapted from ior <path> @ <commit>.` plus one
`Changes:` line); keep it current when you change such a file.
Never write `//nolint` — a test fails on it.

## Documentation layout — keep it that way

- **README.md stays lean**: pitch, status, goals, deployment
  modes, a short security summary, and the docs index. It must
  not accumulate detail sections again.
- **Detailed documentation lives in `docs/`**, one topic per
  file (architecture, trace-scope, output-contract, testing,
  security, build, implementation, visualization), each linked from the
  README's Documentation section.
- **Diagrams are generated**: `docs/diagrams/*.svg` come from
  `docs/diagrams/gen.py` (`python3 docs/diagrams/gen.py
  docs/diagrams`). Change the generator, not the SVGs.
- **PLAN.md decides, docs narrate**: decisions land in PLAN.md
  first; the docs expand and explain them. When the plan
  changes, update the matching docs file in the same commit —
  never leave docs contradicting PLAN.md.
- **New documentation goes into `docs/`** with a README index
  entry; new sections do not go into README.md.
- Don't duplicate PLAN.md verbatim in docs; link to it instead.

## Style

- Wrap prose at 79 columns (PLAN.md and docs/ use this).
- Commit messages: lowercase `area: summary` — e.g.
  `plan: …`, `docs: …`, `readme: …`. Commit and push after each
  change-set.
