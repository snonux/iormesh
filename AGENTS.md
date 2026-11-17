# AGENTS.md — conventions for agents working in this repo

iormesh is in the planning phase: no implementation code exists
yet. [PLAN.md](PLAN.md) is the working plan and the source of
truth for scope, milestones (M0–M7), and open decisions. `ior`
(`../ior`) is the reference project for BPF/C code generation and
integration tests — consult it before inventing patterns.

## Documentation layout — keep it that way

- **README.md stays lean**: pitch, status, goals, deployment
  modes, a short security summary, and the docs index. It must
  not accumulate detail sections again.
- **Detailed documentation lives in `docs/`**, one topic per
  file (architecture, trace-scope, output-contract, testing,
  security, build), each linked from the README's Documentation
  section.
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
