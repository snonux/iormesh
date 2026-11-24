# AGENTS.md — conventions for agents working in this repo

iormesh is a proof of concept: M0, M1, M2, M3, M4, M6 and M7 are
accepted; M5 (views, query and the LLM eval) is built but not
accepted until its eval has run with a real model. Rocky Linux
9.9 (el9, kernel 5.14) is the main target. The code lives in
`cmd/` (`iormesh`, `iormeshctl`, `iormesheval`), `internal/`,
`agent/` (the Java agent), `workload/` (the Java test workload)
and the `magefiles/` targets (Magefile.go and the
`magefile_*.go` files, decision #30).
[docs/decisions.md](docs/decisions.md) holds every numbered
decision and is the source of truth;
[docs/milestones.md](docs/milestones.md) holds the spikes
(S1–S3) and milestones (M0–M7) with their acceptance. `ior`
(`../ior`) is the reference project for BPF/C code generation
and integration tests — consult it before inventing patterns.
Don't scaffold implementation ahead of a recorded decision —
the decision moves first.

Picking up a spike or milestone? Read its section in
[docs/implementation.md](docs/implementation.md) — files, order
of work, code sketches, tests — after docs/milestones.md. If a
sketch there disagrees with docs/decisions.md or
docs/milestones.md, they are right; fix the guide in the same
commit.

## Build and test

Run everything through Mage — `mage generate`, `mage build`,
`mage test`, `mage testRace`, `mage vet`, `mage lint`,
`mage fmtCheck`, `mage workloadTest`, `mage bpfRootTest` and
`mage integrationTest` (both root, `sudo -n`; see
[docs/testing.md](docs/testing.md)). Bare
`go test`/`go vet` cannot build the packages that import
libbpfgo. Build outputs (binaries, jars, test binaries) go to
the gitignored `bin/`, never the repository root (decision
#30). Prerequisites (libbpfgo at
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
  file (quickstart, architecture, decisions, milestones,
  trace-scope, output-contract, schema, testing, java-attach,
  kubernetes, security, build, implementation, visualization,
  troubleshooting; spike results under `docs/spikes/`), each
  linked from the README's Documentation section.
- **Diagrams are generated**: `docs/diagrams/*.svg` come from
  `docs/diagrams/gen.py` (`python3 docs/diagrams/gen.py
  docs/diagrams`). Change the generator, not the SVGs.
- **Decisions first, docs narrate**: a decision is recorded
  first in `docs/decisions.md` (the next free number, with its
  reasons); the topic docs expand and explain it. When a
  decision changes, update the matching topic doc in the same
  commit — never leave docs contradicting docs/decisions.md.
  A milestone's scope and acceptance change in
  `docs/milestones.md` the same way. New scope beyond M7 that
  needs its own acceptance gets a new milestone section (M8,
  …) in `docs/milestones.md`, its decisions recorded first; a
  change that fits an existing milestone or needs no
  acceptance of its own is a decision only.
- **New documentation goes into `docs/`** with a README index
  entry; new sections do not go into README.md.
- Don't duplicate a decision verbatim in the topic docs; cite
  its number (`docs/decisions.md #N`) instead.

## Style

- Wrap prose at 79 columns (docs/ uses this).
- Commit messages: lowercase `area: summary` — e.g.
  `docs: …`, `readme: …`. Commit and push after each
  change-set.
