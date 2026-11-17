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

## Tech choices

- Go + libbpfgo + eBPF (CO-RE) — the same stack as ior, adopted
  along with its code-generation pattern (see above).
- Java 21 agent; async-profiler-style frame walking.
- Controller (`iormeshctl`): pure Go, no cgo — cross-compiled
  for Linux and macOS; tracer: linux/amd64 + linux/arm64 with
  the embedded CO-RE object.
- Storage: in-memory ring buffer → parquet trace files on
  servers (minute-rotated, compressed segments; ior's parquet
  pattern); graph + summary exports.
