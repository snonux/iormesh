# Code generation — follow ior

Part of [Build & code generation](../build.md).

iormesh does not reinvent ior's generation machinery; it copies the
pattern (and, where sensible, the code) from `../ior`:

- **Generator package** (`internal/generate/` in ior): reads the
  build host's tracepoint set and emits
  - the C enter/exit handlers
    (`internal/c/generated_tracepoints.c`),
  - a Go tracepoint table
    (`internal/tracepoints/generated_tracepoints.go`),
  - C-derived Go types (`internal/types/generated_types.go`).
  Unit-tested, with harness tests exercising generated-handler
  behavior.
- **Diff gate**: the generation result is committed
  (`internal/c/generated_tracepoints_result.txt`); regenerating on a
  kernel whose tracepoint set differs shows up as a diff and fails
  the gate (force via env override, ior-style). Builds happen on a
  matching kernel — ior pins its generation host with
  `scripts/build-with-docker.sh`; iormesh does the same.
- **vmlinux.h** from the build host's BTF (`/sys/kernel/btf/vmlinux`),
  kept in the working tree (gitignored, as in ior) and
  deliberately preserved by clean targets.
- **Compile & embed**: hand-written BPF C (ior's helpers: fd name
  resolution, file identification, poll/recv handling…) plus the
  generated handlers, clang with CO-RE relocations → `.bpf.o` →
  `go:embed` into the static binary, with an env override
  (`IOR_BPF_OBJECT`-style) to load an external object for testing.
- **Runtime portability**: static binary, copyable to any BTF host;
  tracepoints missing on the target kernel are skipped — the same
  rule ior runs under.
- **Problems ior already solved** — reuse the approach and code
  rather than re-derive: fd→path resolution and file identification
  in BPF (`fdname.c`/`fileident.c`), syscall enter/exit pairing
  including restart edge cases, thread/TID and comm resolution,
  ring buffer consumption, and the libbpf link-wrapper seam that
  keeps teardown safe.
- **Build tooling**: Magefile with generate/build/test/vet/lint
  targets wired for cgo (libbpfgo paths), container builds that
  run on Docker **and Podman** (engine-agnostic scripts,
  ior's build-with-docker pattern with a podman alias), and
  generated-artifact drift checked in CI like ior does.
- **Provenance**: every file copied or adapted from ior records
  the ior commit it came from, so ior's later fixes in the same
  code can be ported on purpose instead of drifting apart
  unnoticed (decision #4).

## What ior does not give us

Checked against `../ior` on 2026-11-17 (re-check when picking
this up — ior moves). These are new work, not reuse, and M1/M2
are sized accordingly:

- **Peer addresses and 4-tuples**: ior traces the socket
  syscalls but identifies sockets only as descriptors
  (`socket:…`); it never reads local or remote addresses.
  iormesh adds the `sock:inet_sock_set_state` handler, the
  sock-pointer join back to pid/fd, and the join key (M1, S2).
- **Process-tree following**: ior's in-kernel filter accepts
  one TGID (or all); following children is a plan there
  (`docs/follow-forks-plan.md`), not code. iormesh needs
  fork/exec following, exec-time pattern matching and restart
  re-attach (M2).
- **Stack capture**: ior's BPF code captures no stacks. Native
  stacks are an opt-in addition (M2, done: `-native-stacks`);
  Java stacks come from the agent (M3).
- **Per-syscall cost**: ior documents what classic syscall
  tracepoints cost every task on the host, and prototypes
  cheaper dispatch (`docs/raw-tracepoint-design.md`). iormesh
  inherits the cost with the pattern — S3 measures it, and
  ior's findings there are the first place to look for relief.
  Task 8e4 left the pattern for correctness, not cost: the
  handlers run behind classic `raw_syscalls` dispatchers, which
  makes traced kinds slightly dearer and untraced kinds about
  twice as dear in kernel instructions ("Syscall handlers run
  behind `raw_syscalls` dispatchers" in
  [output-contract.md](../output-contract.md)).
