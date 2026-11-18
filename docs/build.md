# Build & code generation

iormesh follows ior's BPF/C generation pattern wholesale, so
nothing below gets invented twice. Companion to
[PLAN.md](../PLAN.md) ("BPF/C code generation — follow ior" and
"Container builds").

- Tracepoint discovery from the build host's kernel → generated C
  enter/exit handlers, generated Go tracepoint tables and
  C-derived Go types — all from a unit-tested `internal/generate`
  package, driven by Mage targets.
- Generated artifacts are committed, with a committed result file as
  the diff gate pinning the kernel tracepoint set: regenerating on
  a different kernel shows up as a diff and stops the build.
- `vmlinux.h` from BTF, clang CO-RE compile, the `.bpf.o` embedded
  into the static binary via `go:embed` (env override to load an
  external object for testing).
- Container builds with Docker **or Podman**: the container pins
  the build environment; the static binary is
  copyable to any BTF host, and tracepoints missing there are
  skipped at runtime. The tracer builds for linux/amd64 + arm64
  from its container; the cgo-free controller cross-compiles
  darwin/linux for both arches from the same pipeline.

Where ior already solved a problem — fd→path resolution in BPF,
syscall enter/exit pairing and restart edge cases, thread/TID
handling, ring buffer consumption, safe libbpf link teardown —
iormesh reuses that approach and code rather than re-deriving it.
Every copied file records the ior commit it came from, so later
ior fixes can be ported deliberately.

Reuse has an edge. ior reads no socket peer addresses, follows no
child processes, and captures no stacks — so 4-tuples (from the
socket's state transitions), process-tree following with
exec-time pattern matching, stack capture and uprobes on the
JDK's native I/O functions are new work in iormesh. See PLAN.md,
"What ior does not give us".

## Tech choices

- Go + libbpfgo + eBPF (CO-RE) — the same stack as ior, adopted
  along with its code-generation pattern (see above).
- Java agent: bytecode instrumentation at the JDK's I/O
  boundaries only, stacks captured there; joined to syscalls
  by thread id, most likely through eBPF uprobes on the JDK's
  native I/O functions (design gated by spike S1 — see
  PLAN.md). Platform threads only in v1.
- Controller (`iormeshctl`): pure Go, no cgo — cross-compiled
  for Linux and macOS; tracer: linux/amd64 + linux/arm64 with
  the embedded CO-RE object.
- Storage: in-memory ring buffer → parquet trace files on
  servers (minute-rotated segments, parquet-internal zstd;
  ior's parquet pattern); graph + summary exports, rebuilt on
  the controller at every build — no server-side database.
